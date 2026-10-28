package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

func TestTakeLoggingFlags(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantArgs   []string
		wantLevel  string
		wantFormat string
		wantErr    string
	}{
		{
			name:       "defaults from environment",
			args:       []string{"sources", "list"},
			wantArgs:   []string{"sources", "list"},
			wantLevel:  "info",
			wantFormat: "text",
		},
		{
			name:       "flags anywhere in the command line",
			args:       []string{"--log-level", "debug", "sources", "list", "--log-format=json"},
			wantArgs:   []string{"sources", "list"},
			wantLevel:  "debug",
			wantFormat: "json",
		},
		{
			name:       "single-dash form",
			args:       []string{"-log-level=warn", "stats"},
			wantArgs:   []string{"stats"},
			wantLevel:  "warn",
			wantFormat: "text",
		},
		{
			name:      "missing value",
			args:      []string{"sources", "--log-level"},
			wantErr:   "a value is required",
			wantLevel: "info",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args, cfg, err := takeLoggingFlags(tc.args, logging.Config{Level: "info", Format: "text"})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want one mentioning %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(args, " ") != strings.Join(tc.wantArgs, " ") {
				t.Errorf("remaining args = %v, want %v", args, tc.wantArgs)
			}
			if cfg.Level != tc.wantLevel || cfg.Format != tc.wantFormat {
				t.Errorf("config = %+v, want level %q format %q", cfg, tc.wantLevel, tc.wantFormat)
			}
		})
	}
}

// TestRunLogsToStderrOnly pins the stream split: diagnostics go to the stderr
// writer as log lines, command output stays parseable on stdout.
func TestRunLogsToStderrOnly(t *testing.T) {
	path := writeConfig(t, goodConfig)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--log-format", "json", "--log-level", "debug", "sources", "list", "--config", path},
		&stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}

	// Every stderr line is a JSON log record tagged with one of our modules.
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		if line == "" {
			continue
		}
		if !strings.Contains(line, `"module":`) {
			t.Errorf("stderr line is not a tagged log record: %q", line)
		}
	}
	if !strings.Contains(stderr.String(), `"module":"main"`) {
		t.Errorf("no main-module lifecycle lines in stderr:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), `"module":"sources"`) {
		t.Errorf("no sources-module lines in stderr:\n%s", stderr.String())
	}
	if strings.Contains(stdout.String(), `"module":`) {
		t.Errorf("log lines leaked onto stdout:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "id: ghost") {
		t.Errorf("stdout lost the command output:\n%s", stdout.String())
	}
}

// TestRunRejectsBadLogConfig makes a typo a usage error rather than silently
// changing verbosity.
func TestRunRejectsBadLogConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--log-level", "loud", "stats"}, &stdout, &stderr); code != 2 {
		t.Errorf("exit code = %d, want 2 (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "log level") {
		t.Errorf("stderr = %q, want a log level error", stderr.String())
	}
}

// TestRunQuietByDefault keeps the CLI usable in pipes: without --log-level
// there is no chatter on stderr for a successful, non-verbose command.
func TestRunQuietByDefault(t *testing.T) {
	path := writeConfig(t, goodConfig)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"sources", "list", "--config", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want silence at info level for sources list", stderr.String())
	}
}
