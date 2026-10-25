package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes a fixture config into a temp dir and returns its path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sources.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

const goodConfig = `version: 1

defaults:
  chunking: { target_tokens: 400, overlap_tokens: 60 }
  user_agent: "elilap-dev-agent/0.1 (+https://elilap.dev)"

sources:
  - id: ghost
    type: ghost
    enabled: true
    credentials:
      api_url: { env: AGENT_TEST_GHOST_URL }
      api_key: { env: AGENT_TEST_GHOST_KEY }
    options: { include_drafts: false, limit: 500 }

  - id: postiz
    type: postiz
    enabled: false
    credentials:
      api_url: { env: AGENT_TEST_POSTIZ_URL }
      api_key: { env: AGENT_TEST_POSTIZ_KEY }
    options: { platforms: [instagram, linkedin, x], include_scheduled: false }
`

func TestRunSourcesList(t *testing.T) {
	path := writeConfig(t, goodConfig)

	var stdout, stderr bytes.Buffer
	code := run([]string{"sources", "list", "--config", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}

	out := stdout.String()
	for _, want := range []string{
		path,
		"version 1",
		"id: ghost",
		"id: postiz",
		"state: enabled",
		"state: disabled",
		"limit: 500",
		"include_drafts: false",
		"instagram",
		"api_url: env AGENT_TEST_GHOST_URL",
		"api_key: env AGENT_TEST_GHOST_KEY",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("sources list output missing %q:\n%s", want, out)
		}
	}

	// Credential values must never appear: the fixture only names vars and
	// the test sets a value that would be recognisable in the output.
	const secret = "super-secret-value"
	t.Setenv("AGENT_TEST_GHOST_KEY", secret)
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"sources", "list", "--config", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	if strings.Contains(stdout.String(), secret) {
		t.Errorf("sources list leaked a credential value:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "api_key: env AGENT_TEST_GHOST_KEY (set)") {
		t.Errorf("sources list did not report the credential as set:\n%s", stdout.String())
	}
}

func TestRunSourcesValidate(t *testing.T) {
	tests := []struct {
		name       string
		config     string
		args       []string
		setEnv     []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "good file exits zero",
			config:     goodConfig,
			setEnv:     []string{"AGENT_TEST_GHOST_URL", "AGENT_TEST_GHOST_KEY", "AGENT_TEST_POSTIZ_URL", "AGENT_TEST_POSTIZ_KEY"},
			wantCode:   0,
			wantStdout: ": ok",
		},
		{
			name:       "good file without credentials warns and exits zero",
			config:     goodConfig,
			wantCode:   0,
			wantStderr: "warning",
		},
		{
			name:       "good file without credentials fails under strict",
			config:     goodConfig,
			args:       []string{"--strict"},
			wantCode:   1,
			wantStderr: "AGENT_TEST_GHOST_KEY",
		},
		{
			name:       "duplicate ids exit one",
			config:     "version: 1\ndefaults:\n  chunking: { target_tokens: 400, overlap_tokens: 60 }\nsources:\n  - id: ghost\n    type: ghost\n  - id: ghost\n    type: postiz\n",
			wantCode:   1,
			wantStderr: "duplicate source id",
		},
		{
			name:       "unknown top-level key exits one",
			config:     goodConfig + "\nbogus_key: true\n",
			wantCode:   1,
			wantStderr: "bogus_key",
		},
		{
			name:       "unknown provider type exits one",
			config:     "version: 1\ndefaults:\n  chunking: { target_tokens: 400, overlap_tokens: 60 }\nsources:\n  - id: site\n    type: website\n",
			wantCode:   1,
			wantStderr: "known types",
		},
		{
			name:       "invalid provider option exits one",
			config:     "version: 1\ndefaults:\n  chunking: { target_tokens: 400, overlap_tokens: 60 }\nsources:\n  - id: ghost\n    type: ghost\n    options: { limit: 0 }\n",
			wantCode:   1,
			wantStderr: "options.limit",
		},
		{
			name:       "unsupported version exits one",
			config:     strings.Replace(goodConfig, "version: 1", "version: 9", 1),
			wantCode:   1,
			wantStderr: "version 9",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.config)
			for _, name := range tc.setEnv {
				t.Setenv(name, "test-value")
			}

			args := append([]string{"sources", "validate", "--config", path}, tc.args...)
			var stdout, stderr bytes.Buffer
			code := run(args, &stdout, &stderr)

			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s",
					code, tc.wantCode, stdout.String(), stderr.String())
			}
			if tc.wantStdout != "" && !strings.Contains(stdout.String(), tc.wantStdout) {
				t.Errorf("stdout missing %q:\n%s", tc.wantStdout, stdout.String())
			}
			if tc.wantStderr != "" && !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Errorf("stderr missing %q:\n%s", tc.wantStderr, stderr.String())
			}
		})
	}
}

func TestRunDispatches(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "no args prints usage",
			wantCode:   2,
			wantStderr: "usage: agent",
		},
		{
			name:       "unknown subcommand prints usage",
			args:       []string{"frobnicate"},
			wantCode:   2,
			wantStderr: "unknown subcommand",
		},
		{
			name:       "sources without subcommand",
			args:       []string{"sources"},
			wantCode:   2,
			wantStderr: "expected a subcommand",
		},
		{
			name:       "sources unknown subcommand",
			args:       []string{"sources", "frobnicate"},
			wantCode:   2,
			wantStderr: "unknown subcommand",
		},
		{
			name:       "ingest without a config fails loudly",
			args:       []string{"ingest"},
			wantCode:   1,
			wantStderr: "read config",
		},
		{
			name:       "serve is still a placeholder and fails loudly",
			args:       []string{"serve"},
			wantCode:   1,
			wantStderr: "not implemented",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tc.args, &stdout, &stderr)

			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d", code, tc.wantCode)
			}
			if tc.wantStdout != "" && stdout.String() != tc.wantStdout {
				t.Errorf("stdout = %q, want %q", stdout.String(), tc.wantStdout)
			}
			if tc.wantStderr != "" && !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tc.wantStderr)
			}
		})
	}
}

func TestRunSourcesValidateMissingConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"sources", "validate", "--config", filepath.Join(t.TempDir(), "absent.yaml")}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "read config") {
		t.Errorf("stderr = %q, want a read error", stderr.String())
	}
}
