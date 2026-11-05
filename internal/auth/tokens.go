// Package auth owns the read-only bearer tokens the A2A surface accepts: an
// env-driven list plus an optional 0600 token file that is re-read when it
// changes, so `agent tokens add|revoke` takes effect without a restart.
//
// Token values never leave this package except through Add (which prints a
// newly issued token once) and Lookup (which compares in constant time). Logs
// and errors name token ids and file paths only.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

const (
	// MinTokenBytes is the documented entropy floor for an issued token. The
	// generator uses exactly this many bytes, so a generated token is always
	// 43 URL-safe base64 characters.
	MinTokenBytes = 32
	// MinTokenLength is the shortest token accepted from configuration: 32
	// characters, i.e. at least 192 bits of base64 text. It rejects
	// hand-typed placeholders such as "changeme".
	MinTokenLength = 32
)

// ErrNoTokenFile reports that a token-file operation was requested but no path
// is configured.
var ErrNoTokenFile = errors.New("auth: no token file configured (set AGENT_A2A_TOKENS_FILE)")

// idPattern keeps token ids one predictable token: ids appear in audit lines
// and in the file/env syntax, so they cannot contain ':' or whitespace.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Token is one issued bearer credential. Value is the secret; ID is the stable
// label that appears in logs and audit records.
type Token struct {
	ID    string
	Value string
}

type tokenCtxKey struct{}

// WithToken attaches the authenticated token to a request context.
func WithToken(ctx context.Context, t Token) context.Context {
	return context.WithValue(ctx, tokenCtxKey{}, t)
}

// TokenFrom returns the authenticated token, if the request passed auth.
func TokenFrom(ctx context.Context) (Token, bool) {
	t, ok := ctx.Value(tokenCtxKey{}).(Token)
	return t, ok
}

// Store holds the token set. The env list is fixed at startup; the token file
// is stat-ed (and re-read when its mtime or size changes) on every lookup, so
// rotation is a file edit rather than a restart.
type Store struct {
	env      []Token
	filePath string

	mu      sync.Mutex
	file    []Token
	modTime int64
	size    int64
	loaded  bool
	lastErr string
}

// NewStore loads the env list and the current contents of the file. A missing
// file is not an error: that is the token-less dev deployment. An unreadable,
// malformed, or group/world-readable file is a startup error.
func NewStore(envList, filePath string) (*Store, error) {
	env, err := parseList(envList, "environment token list")
	if err != nil {
		return nil, err
	}
	s := &Store{env: env, filePath: strings.TrimSpace(filePath)}
	if s.filePath != "" {
		if err := s.reloadLocked(true); err != nil {
			return nil, err
		}
	}
	if err := checkUnique(env, s.file); err != nil {
		return nil, err
	}
	return s, nil
}

// Len reports how many distinct tokens are configured. Zero means auth is
// disabled (the loopback dev deployment).
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	return len(s.env) + len(s.file)
}

// IDs returns the configured token ids in sorted order, never their values.
func (s *Store) IDs() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.refreshLocked()
	ids := make([]string, 0, len(s.env)+len(s.file))
	for _, t := range s.env {
		ids = append(ids, t.ID)
	}
	for _, t := range s.file {
		ids = append(ids, t.ID)
	}
	s.mu.Unlock()
	sort.Strings(ids)
	return ids
}

// Lookup compares the presented value against every configured token in
// constant time and returns the matching id. It never short-circuits on a
// match, so timing does not reveal which token was right.
func (s *Store) Lookup(value string) (Token, bool) {
	if s == nil || value == "" {
		return Token{}, false
	}
	s.mu.Lock()
	s.refreshLocked()
	all := make([]Token, 0, len(s.env)+len(s.file))
	all = append(all, s.env...)
	all = append(all, s.file...)
	s.mu.Unlock()

	presented := sha256.Sum256([]byte(value))
	var matched Token
	found := 0
	for _, t := range all {
		want := sha256.Sum256([]byte(t.Value))
		if subtle.ConstantTimeCompare(presented[:], want[:]) == 1 {
			matched = t
			found = 1
		}
	}
	return matched, found == 1
}

// refreshLocked re-reads the file when it changed and logs a change in the
// failure reason once, so a persistently broken file does not spam the log on
// every request. The last good token set stays active meanwhile.
func (s *Store) refreshLocked() {
	if s.filePath == "" {
		return
	}
	err := s.reloadLocked(false)
	if err == nil {
		s.lastErr = ""
		return
	}
	msg := err.Error()
	if msg == s.lastErr {
		return
	}
	s.lastErr = msg
	log := logging.Main()
	log.Warn().Err(err).Str("file", s.filePath).Msg("token file reload failed; keeping the last good set")
}

// reloadLocked re-reads the file when it exists and changed. require, used at
// startup, makes a missing file an error too.
func (s *Store) reloadLocked(require bool) error {
	info, err := os.Stat(s.filePath)
	if errors.Is(err, os.ErrNotExist) {
		if require && s.loaded {
			return nil
		}
		s.file = nil
		s.modTime, s.size, s.loaded = 0, 0, true
		return nil
	}
	if err != nil {
		return fmt.Errorf("auth: stat token file: %w", err)
	}
	if s.loaded && info.ModTime().UnixNano() == s.modTime && info.Size() == s.size {
		return nil
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("auth: token file %s must not be group/world-accessible (mode %#o; use 0600)", s.filePath, perm)
	}
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return fmt.Errorf("auth: read token file: %w", err)
	}
	tokens, err := parseList(string(data), "token file "+s.filePath)
	if err != nil {
		return err
	}
	// A reload must not introduce an id that already exists in the
	// environment: the audit record has to name exactly one credential.
	for _, t := range tokens {
		for _, e := range s.env {
			if t.ID == e.ID {
				return fmt.Errorf("auth: token file %s: a token id is configured twice; ids must be unique across the environment and the token file", s.filePath)
			}
		}
	}
	s.file = tokens
	s.modTime, s.size, s.loaded = info.ModTime().UnixNano(), info.Size(), true
	return nil
}

// parseList parses `id:token` entries separated by commas (env) or newlines
// (file). '#' starts a comment in file content; blank entries are ignored.
func parseList(raw, source string) ([]Token, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	parts := strings.Split(raw, ",")
	if strings.Contains(raw, "\n") {
		parts = parts[:0]
		for _, line := range strings.Split(raw, "\n") {
			if i := strings.Index(line, "#"); i >= 0 {
				line = line[:i]
			}
			parts = append(parts, line)
		}
	}
	tokens := make([]Token, 0, len(parts))
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		where := fmt.Sprintf("%s entry %d", source, i+1)
		id, value, ok := strings.Cut(part, ":")
		if !ok {
			return nil, fmt.Errorf("auth: %s: entry must be id:token", where)
		}
		tok, err := validateToken(strings.TrimSpace(id), strings.TrimSpace(value), where)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, tok)
	}
	if err := checkUnique(tokens); err != nil {
		return nil, fmt.Errorf("auth: %s: %w", source, err)
	}
	return tokens, nil
}

// validateToken enforces the id shape and the entropy floor. Errors name the
// entry, never the configured id or value: a misplaced secret in either field
// must not reach a log or a terminal.
func validateToken(id, value, where string) (Token, error) {
	if !idPattern.MatchString(id) {
		return Token{}, fmt.Errorf("auth: %s: token id must match %s", where, idPattern)
	}
	if value == "" {
		return Token{}, fmt.Errorf("auth: %s: token has no value", where)
	}
	if len(value) < MinTokenLength {
		return Token{}, fmt.Errorf("auth: %s: token value is shorter than %d characters; generate one with `agent tokens add`", where, MinTokenLength)
	}
	for _, r := range value {
		if r <= 0x20 || r == 0x7f {
			return Token{}, fmt.Errorf("auth: %s: token value must not contain whitespace or control characters", where)
		}
	}
	return Token{ID: id, Value: value}, nil
}

// checkUnique rejects duplicate ids across the two token sources: an audit
// line naming an id must identify exactly one credential.
func checkUnique(lists ...[]Token) error {
	seenIDs := make(map[string]bool)
	seenValues := make(map[[32]byte]bool)
	for _, list := range lists {
		for _, t := range list {
			// Neither the id nor the value is echoed: a misconfigured file can
			// put a secret in either field, and these errors are user-facing.
			if seenIDs[t.ID] {
				return errors.New("a token id is configured twice; ids must be unique")
			}
			value := sha256.Sum256([]byte(t.Value))
			if seenValues[value] {
				return errors.New("a token value is configured twice; each token must be distinct")
			}
			seenIDs[t.ID] = true
			seenValues[value] = true
		}
	}
	return nil
}

// NewTokenValue returns a fresh high-entropy token (43 URL-safe base64 chars).
func NewTokenValue() (string, error) {
	var b [MinTokenBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("auth: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// AddToken appends a new `id:token` entry to the token file and returns the
// token value so the caller can print it exactly once. The file is created
// 0600 and rewritten atomically.
func AddToken(path, id string) (Token, error) {
	if strings.TrimSpace(path) == "" {
		return Token{}, ErrNoTokenFile
	}
	path = strings.TrimSpace(path)
	if err := ensureFileMode(path); err != nil {
		return Token{}, err
	}
	tokens, err := readFileTokens(path)
	if err != nil {
		return Token{}, err
	}
	if err := checkUnique(tokens); err != nil {
		return Token{}, err
	}
	for _, t := range tokens {
		if t.ID == id {
			return Token{}, fmt.Errorf("auth: token id %q already exists in %s", id, path)
		}
	}
	value, err := NewTokenValue()
	if err != nil {
		return Token{}, err
	}
	tok, err := validateToken(id, value, "new token")
	if err != nil {
		return Token{}, err
	}
	tokens = append(tokens, tok)
	if err := writeFileTokens(path, tokens); err != nil {
		return Token{}, err
	}
	return tok, nil
}

// RevokeToken removes one id from the token file.
func RevokeToken(path, id string) error {
	if strings.TrimSpace(path) == "" {
		return ErrNoTokenFile
	}
	path = strings.TrimSpace(path)
	if err := ensureFileMode(path); err != nil {
		return err
	}
	tokens, err := readFileTokens(path)
	if err != nil {
		return err
	}
	kept := tokens[:0]
	found := false
	for _, t := range tokens {
		if t.ID == id {
			found = true
			continue
		}
		kept = append(kept, t)
	}
	if !found {
		return fmt.Errorf("auth: token id %q is not in %s", id, path)
	}
	return writeFileTokens(path, kept)
}

// FileIDs lists the ids in the token file, sorted. It is used by
// `agent tokens list`; values are never returned.
func FileIDs(path string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrNoTokenFile
	}
	tokens, err := readFileTokens(strings.TrimSpace(path))
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(tokens))
	for _, t := range tokens {
		ids = append(ids, t.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

// readFileTokens reads and parses the file; a missing file is empty.
func readFileTokens(path string) ([]Token, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("auth: stat token file: %w", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("auth: token file %s must not be group/world-accessible (mode %#o; use 0600)", path, perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("auth: read token file: %w", err)
	}
	return parseList(string(data), "token file "+path)
}

// ensureFileMode rejects an existing file with loose permissions before it is
// modified. A missing file is fine; writeFileTokens creates it 0600.
func ensureFileMode(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("auth: stat token file: %w", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("auth: token file %s must not be group/world-accessible (mode %#o; chmod 600 it first)", path, perm)
	}
	return nil
}

// writeFileTokens rewrites the file atomically with a 0600 mode.
func writeFileTokens(path string, tokens []Token) error {
	var b strings.Builder
	b.WriteString("# elilap.dev-agent A2A tokens: one id:token per line, 0600, never commit.\n")
	for _, t := range tokens {
		b.WriteString(t.ID)
		b.WriteString(":")
		b.WriteString(t.Value)
		b.WriteString("\n")
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".a2a-tokens-*")
	if err != nil {
		return fmt.Errorf("auth: write token file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("auth: write token file: %w", err)
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return fmt.Errorf("auth: write token file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("auth: write token file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("auth: write token file: %w", err)
	}
	return nil
}
