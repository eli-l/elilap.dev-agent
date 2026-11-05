package auth_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/auth"
)

const (
	tokenA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 43 chars
	tokenB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func writeTokenFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	// WriteFile is subject to umask; make the test's intent explicit.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod token file: %v", err)
	}
}

func TestStoreReadsEnvList(t *testing.T) {
	store, err := auth.NewStore("alice:"+tokenA+", bob:"+tokenB, "")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got := store.Len(); got != 2 {
		t.Fatalf("Len = %d, want 2", got)
	}
	tok, ok := store.Lookup(tokenA)
	if !ok || tok.ID != "alice" {
		t.Errorf("Lookup(alice) = %+v, %v", tok, ok)
	}
	if _, ok := store.Lookup("wrong"); ok {
		t.Error("Lookup(wrong) succeeded")
	}
	if ids := store.IDs(); strings.Join(ids, ",") != "alice,bob" {
		t.Errorf("IDs = %v", ids)
	}
}

func TestStoreRejectsBadConfiguration(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{name: "no colon", env: "alice", want: "must be id:token"},
		{name: "short token", env: "alice:short", want: "shorter than 32"},
		{name: "bad id", env: "al ice:" + tokenA, want: "token id"},
		{name: "duplicate id", env: "alice:" + tokenA + ",alice:" + tokenB, want: "configured twice"},
		{name: "duplicate value", env: "alice:" + tokenA + ",bob:" + tokenA, want: "configured twice"},
		{name: "whitespace in token", env: "alice:" + tokenA + " ", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := auth.NewStore(tc.env, "")
			if tc.want == "" {
				if err != nil {
					t.Fatalf("NewStore error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("NewStore = nil error, want one containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want %q", err, tc.want)
			}
			// The error must never echo a token value.
			if strings.Contains(err.Error(), tokenA) || strings.Contains(err.Error(), tokenB) {
				t.Errorf("error leaked a token: %q", err)
			}
		})
	}
}

func TestStoreReadsFileAndReloadsOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens")
	writeTokenFile(t, path, "# comment\nalice:"+tokenA+"\n", 0o600)

	store, err := auth.NewStore("", path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, ok := store.Lookup(tokenA); !ok {
		t.Fatal("file token was not loaded")
	}

	// Rotate the file: new value, explicit mtime bump so the change is
	// visible even on a coarse filesystem timestamp.
	writeTokenFile(t, path, "alice:"+tokenB+"\n", 0o600)
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if _, ok := store.Lookup(tokenA); ok {
		t.Error("old token still accepted after rotation")
	}
	if tok, ok := store.Lookup(tokenB); !ok || tok.ID != "alice" {
		t.Errorf("rotated token not accepted: %+v %v", tok, ok)
	}
}

func TestStoreRejectsInsecureFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens")
	writeTokenFile(t, path, "alice:"+tokenA+"\n", 0o644)
	_, err := auth.NewStore("", path)
	if err == nil {
		t.Fatal("NewStore accepted a group/world-readable token file")
	}
	if !strings.Contains(err.Error(), "0600") {
		t.Errorf("error = %q, want the 0600 guidance", err)
	}
}

func TestStoreMissingFileIsEmpty(t *testing.T) {
	store, err := auth.NewStore("", filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if store.Len() != 0 {
		t.Errorf("Len = %d, want 0", store.Len())
	}
}

func TestStoreRejectsDuplicateAcrossSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens")
	writeTokenFile(t, path, "alice:"+tokenB+"\n", 0o600)
	_, err := auth.NewStore("alice:"+tokenA, path)
	if err == nil || !strings.Contains(err.Error(), "configured twice") {
		t.Fatalf("error = %v, want a duplicate-id error", err)
	}
}

func TestAddAndRevokeToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens")
	tok, err := auth.AddToken(path, "alice")
	if err != nil {
		t.Fatalf("AddToken: %v", err)
	}
	if tok.ID != "alice" || len(tok.Value) < auth.MinTokenLength {
		t.Fatalf("token = %+v", tok)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %#o, want 0600", perm)
	}

	store, err := auth.NewStore("", path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got, ok := store.Lookup(tok.Value); !ok || got.ID != "alice" {
		t.Errorf("issued token does not authenticate: %+v %v", got, ok)
	}

	if _, err := auth.AddToken(path, "alice"); err == nil {
		t.Error("AddToken accepted a duplicate id")
	}

	if err := auth.RevokeToken(path, "alice"); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if err := auth.RevokeToken(path, "alice"); err == nil {
		t.Error("RevokeToken accepted an unknown id")
	}
	store, err = auth.NewStore("", path)
	if err != nil {
		t.Fatalf("NewStore after revoke: %v", err)
	}
	if store.Len() != 0 {
		t.Errorf("Len = %d after revoke, want 0", store.Len())
	}
	if ids, err := auth.FileIDs(path); err != nil || len(ids) != 0 {
		t.Errorf("FileIDs = %v, %v", ids, err)
	}
}

func TestNewTokenValueIsHighEntropy(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 32; i++ {
		v, err := auth.NewTokenValue()
		if err != nil {
			t.Fatalf("NewTokenValue: %v", err)
		}
		if len(v) < auth.MinTokenLength {
			t.Fatalf("token %q is shorter than %d", v, auth.MinTokenLength)
		}
		if seen[v] {
			t.Fatal("NewTokenValue repeated a value")
		}
		seen[v] = true
	}
}

func TestTokenContext(t *testing.T) {
	tok := auth.Token{ID: "alice", Value: tokenA}
	ctx := auth.WithToken(t.Context(), tok)
	got, ok := auth.TokenFrom(ctx)
	if !ok || got.ID != "alice" {
		t.Errorf("TokenFrom = %+v, %v", got, ok)
	}
	if _, ok := auth.TokenFrom(t.Context()); ok {
		t.Error("TokenFrom reported a token on a bare context")
	}
}
