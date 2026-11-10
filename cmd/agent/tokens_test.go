package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
)

func TestTokensAddRejectsEnvID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens")
	var stdout, stderr bytes.Buffer
	code := runTokens([]string{"add", "--id", "alice", "--file", path},
		envcfg.Config{A2ATokens: "alice:" + testToken, A2ATokensFile: path}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "already configured") {
		t.Errorf("stderr = %q, want the env-collision message", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want no token", stdout.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("token file was created despite the rejection: %v", err)
	}
}

func TestTokensAddRevokeList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens")
	cfg := envcfg.Config{A2ATokensFile: path}

	var stdout, stderr bytes.Buffer
	if code := runTokens([]string{"add", "--id", "alice"}, cfg, &stdout, &stderr); code != 0 {
		t.Fatalf("add exit = %d: %s", code, stderr.String())
	}
	line := strings.TrimSpace(stdout.String())
	id, value, ok := strings.Cut(line, ":")
	if !ok || id != "alice" || len(value) < 32 {
		t.Fatalf("issued line = %q", line)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runTokens([]string{"list"}, cfg, &stdout, &stderr); code != 0 {
		t.Fatalf("list exit = %d: %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "alice" {
		t.Errorf("list = %q, want alice", got)
	}
	if strings.Contains(stdout.String(), value) {
		t.Error("list printed a token value")
	}

	stdout.Reset()
	if code := runTokens([]string{"revoke", "--id", "alice"}, cfg, &stdout, &stderr); code != 0 {
		t.Fatalf("revoke exit = %d: %s", code, stderr.String())
	}
	stdout.Reset()
	if code := runTokens([]string{"list"}, cfg, &stdout, &stderr); code != 0 {
		t.Fatalf("list after revoke exit = %d", code)
	}
	if got := strings.TrimSpace(stdout.String()); got != "no tokens configured" {
		t.Errorf("list after revoke = %q", got)
	}
}
