package logging_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/rs/zerolog"
)

// setup installs a JSON logger into a buffer for the duration of one test.
func setup(t *testing.T, cfg logging.Config) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	if err := logging.Setup(cfg, &buf); err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Cleanup(logging.Reset)
	return &buf
}

// lines decodes the JSON log lines written to buf.
func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func TestSetupTagsEachModule(t *testing.T) {
	buf := setup(t, logging.Config{Level: "debug", Format: "json"})

	for _, m := range []struct {
		name string
		log  zerolog.Logger
	}{
		{"main", logging.Main()},
		{"http", logging.HTTP()},
		{"sources", logging.Sources()},
		{"db", logging.DB()},
	} {
		log := m.log
		log.Info().Msg("from " + m.name)
	}

	recs := lines(t, buf)
	if len(recs) != 4 {
		t.Fatalf("got %d log lines, want 4:\n%s", len(recs), buf.String())
	}
	want := []string{"main", "http", "sources", "db"}
	for i, rec := range recs {
		if got := rec["module"]; got != want[i] {
			t.Errorf("line %d module = %v, want %q", i, got, want[i])
		}
		if _, ok := rec["time"]; !ok {
			t.Errorf("line %d has no timestamp: %v", i, rec)
		}
	}
}

func TestLevelFiltersModules(t *testing.T) {
	buf := setup(t, logging.Config{Level: "warn", Format: "json"})

	log := logging.DB()
	log.Debug().Msg("debug drops")
	log.Info().Msg("info drops")
	log.Warn().Msg("warn keeps")
	log.Error().Msg("error keeps")

	recs := lines(t, buf)
	if len(recs) != 2 {
		t.Fatalf("got %d lines at warn level, want 2:\n%s", len(recs), buf.String())
	}
	if recs[0]["level"] != "warn" || recs[1]["level"] != "error" {
		t.Errorf("levels = %v, %v; want warn, error", recs[0]["level"], recs[1]["level"])
	}
}

func TestDisabledLevelSilencesEverything(t *testing.T) {
	buf := setup(t, logging.Config{Level: "disabled", Format: "json"})
	log := logging.Main()
	log.Error().Str("secret", "value").Msg("dropped")
	if buf.Len() != 0 {
		t.Errorf("disabled level wrote %q, want nothing", buf.String())
	}
}

func TestTextFormatWritesReadableLines(t *testing.T) {
	buf := setup(t, logging.Config{Level: "info", Format: "text"})
	log := logging.Sources()
	log.Info().Str("provider", "ghost").Msg("listed posts")

	out := buf.String()
	if strings.Contains(out, `"module"`) {
		t.Errorf("text format looks like JSON: %q", out)
	}
	for _, want := range []string{"INF", "sources", "provider=ghost", "listed posts"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
}

func TestLoggerIsNoopBeforeSetup(t *testing.T) {
	logging.Reset()
	// Setup was never run, so both loggers must be silent: anything written
	// through them has to disappear, not reach the process stderr.
	for _, module := range []string{"main", "nope"} {
		log := logging.Logger(module)
		if got := log.GetLevel(); got != zerolog.Disabled {
			t.Errorf("logger %q level = %v, want disabled", module, got)
		}
		log.Error().Msg("must not reach stderr")
	}
}

func TestSetupRejectsBadConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  logging.Config
		want string
	}{
		{"unknown level", logging.Config{Level: "loud", Format: "json"}, "log level"},
		{"unknown format", logging.Config{Level: "info", Format: "xml"}, "log format"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := logging.Setup(tc.cfg, &buf)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("setup error = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv(logging.EnvLevel, "debug")
	t.Setenv(logging.EnvFormat, "json")
	cfg := logging.ConfigFromEnv()
	if cfg.Level != "debug" || cfg.Format != "json" {
		t.Errorf("ConfigFromEnv = %+v, want debug/json", cfg)
	}

	t.Setenv(logging.EnvLevel, "")
	t.Setenv(logging.EnvFormat, "")
	cfg = logging.ConfigFromEnv()
	if cfg.Level != "info" || cfg.Format != "text" {
		t.Errorf("ConfigFromEnv defaults = %+v, want info/text", cfg)
	}
}

func TestRequestMiddlewareLogsOneLinePerRequest(t *testing.T) {
	buf := setup(t, logging.Config{Level: "info", Format: "json"})

	cases := []struct {
		name    string
		status  int
		wantLvl string
	}{
		{"ok", http.StatusOK, "info"},
		{"client error", http.StatusBadRequest, "warn"},
		{"server error", http.StatusInternalServerError, "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			h := logging.RequestMiddleware("/v1/ask", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(`{"ok":false}`))
			}))
			req := httptest.NewRequest(http.MethodPost, "/v1/ask?query=secret-task-text", nil)
			h.ServeHTTP(httptest.NewRecorder(), req)

			recs := lines(t, buf)
			if len(recs) != 1 {
				t.Fatalf("got %d lines, want 1:\n%s", len(recs), buf.String())
			}
			rec := recs[0]
			if rec["module"] != "http" {
				t.Errorf("module = %v, want http", rec["module"])
			}
			if rec["level"] != tc.wantLvl {
				t.Errorf("level = %v, want %v", rec["level"], tc.wantLvl)
			}
			if got := rec["status"]; got != float64(tc.status) {
				t.Errorf("status = %v, want %d", got, tc.status)
			}
			if rec["route"] != "/v1/ask" || rec["method"] != http.MethodPost || rec["path"] != "/v1/ask" {
				t.Errorf("unexpected request fields: %v", rec)
			}
			if got := rec["bytes"]; got != float64(len(`{"ok":false}`)) {
				t.Errorf("bytes = %v, want %d", got, len(`{"ok":false}`))
			}
			// Peer task text is data, never logged.
			if strings.Contains(buf.String(), "secret-task-text") {
				t.Errorf("middleware logged the query string:\n%s", buf.String())
			}
		})
	}
}

func TestRequestMiddlewareDefaultsTo200(t *testing.T) {
	buf := setup(t, logging.Config{Level: "info", Format: "json"})
	h := logging.RequestMiddleware("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	recs := lines(t, buf)
	if len(recs) != 1 || recs[0]["status"] != float64(http.StatusOK) {
		t.Fatalf("log lines = %v, want one with status 200", recs)
	}
}
