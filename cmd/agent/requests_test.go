package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
	"github.com/eli-l/elilap.dev-agent/internal/requests"
)

// requestConfig points the CLI at a temporary store.
func requestConfig(t *testing.T) (envcfg.Config, *requests.Store) {
	t.Helper()
	dir := t.TempDir()
	cfg := envcfg.Config{
		DBPath:         filepath.Join(dir, "index.db"),
		RequestsDBPath: filepath.Join(dir, "requests.db"),
	}
	store, err := requests.Open(cfg.RequestsDBPath)
	if err != nil {
		t.Fatalf("open requests: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return cfg, store
}

func runCLI(t *testing.T, args []string, cfg envcfg.Config) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runRequests(args, cfg, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

// TestRequestsListShowsTheInbox pins the owner view: the question text is
// visible here, and the pending count is reported even for an empty filter.
func TestRequestsListShowsTheInbox(t *testing.T) {
	cfg, store := requestConfig(t)
	if _, _, err := store.Create(requests.CreateInput{
		Question:  "What does Eli think about monorepos?",
		Principal: "agent:peer-1",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	stdout, stderr, code := runCLI(t, []string{"list"}, cfg)
	if code != 0 {
		t.Fatalf("list exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "monorepos") || !strings.Contains(stdout, "1 pending") {
		t.Errorf("list output = %q", stdout)
	}

	stdout, _, code = runCLI(t, []string{"list", "--status", "resolved"}, cfg)
	if code != 0 || !strings.Contains(stdout, "no knowledge requests") {
		t.Errorf("filtered list = %q (exit %d)", stdout, code)
	}

	stdout, _, code = runCLI(t, []string{"list", "--json"}, cfg)
	if code != 0 || !strings.Contains(stdout, `"pending": 1`) {
		t.Errorf("json list = %q (exit %d)", stdout, code)
	}
}

// TestRequestsTriage walks review -> resolve and asserts the record, including
// the refusal to resolve without an answer.
func TestRequestsTriage(t *testing.T) {
	cfg, store := requestConfig(t)
	req, _, err := store.Create(requests.CreateInput{Question: "q", Principal: "agent:peer-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, _, code := runCLI(t, []string{"review", req.ID, "--note", "reading"}, cfg); code != 0 {
		t.Fatalf("review exit = %d", code)
	}
	if got, _ := store.Get(req.ID); got.Status != requests.StatusReviewed || got.Note != "reading" {
		t.Errorf("after review = %+v", got)
	}

	if _, stderr, code := runCLI(t, []string{"resolve", req.ID, "--no-ask"}, cfg); code != 2 {
		t.Errorf("resolve without an answer exit = %d (stderr %q), want 2", code, stderr)
	}

	stdout, stderr, code := runCLI(t, []string{"resolve", req.ID, "--no-ask",
		"--answer", "Recorded answer", "--source-url", "https://read.elilap.dev/x/"}, cfg)
	if code != 0 {
		t.Fatalf("resolve exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "status:      resolved") {
		t.Errorf("resolve output = %q", stdout)
	}
	got, err := store.Get(req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != requests.StatusResolved || got.Answer != "Recorded answer" || got.SourceURL == "" {
		t.Errorf("resolved = %+v", got)
	}
	// No push target was registered, so the peer is expected to poll.
	if !strings.Contains(stdout, "none registered") {
		t.Errorf("resolve output does not mention the polling path: %q", stdout)
	}
}

func TestRequestsDeclineAndDelete(t *testing.T) {
	cfg, store := requestConfig(t)
	req, _, err := store.Create(requests.CreateInput{Question: "q", Principal: "agent:a"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, _, code := runCLI(t, []string{"decline", req.ID, "--note", "out of scope"}, cfg); code != 0 {
		t.Fatalf("decline exit = %d", code)
	}
	if got, _ := store.Get(req.ID); got.Status != requests.StatusDeclined {
		t.Errorf("status = %q, want declined", got.Status)
	}
	if _, _, code := runCLI(t, []string{"delete", req.ID}, cfg); code != 0 {
		t.Fatalf("delete exit = %d", code)
	}
	if _, err := store.Get(req.ID); err == nil {
		t.Error("request survived delete")
	}
}

// TestListMissingStoreDoesNotCreateIt pins the wrong-path failure: a
// read-only command must report that there is no store, not create an empty
// one beside the real inbox.
func TestListMissingStoreDoesNotCreateIt(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "requests.db")
	cfg := envcfg.Config{DBPath: filepath.Join(dir, "index.db"), RequestsDBPath: missing}

	_, stderr, code := runCLI(t, []string{"list"}, cfg)
	if code != 1 {
		t.Fatalf("list exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "no knowledge-request store") || !strings.Contains(stderr, missing) {
		t.Errorf("stderr = %q, want the missing-store hint naming the path", stderr)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("list created %s", missing)
	}
}

// TestResolveClosedRequestFailsBeforeAnswering keeps a re-run from paying for a
// model call that can never be recorded.
func TestResolveClosedRequestFailsBeforeAnswering(t *testing.T) {
	cfg, store := requestConfig(t)
	req, _, err := store.Create(requests.CreateInput{Question: "q", Principal: "agent:a"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Resolve(req.ID, requests.Resolution{Answer: "done"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	_, stderr, code := runCLI(t, []string{"resolve", req.ID, "--answer", "again"}, cfg)
	if code != 1 {
		t.Fatalf("resolve on a closed request exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "not open") {
		t.Errorf("stderr = %q, want an is-not-open error", stderr)
	}
	if got, _ := store.Get(req.ID); got.Answer != "done" {
		t.Errorf("answer changed to %q", got.Answer)
	}
}

// TestDeleteAndPurgeRequireAnExistingStore keeps a typo'd --db from reporting
// success on a store the command just created.
func TestDeleteAndPurgeRequireAnExistingStore(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.db")
	cfg := envcfg.Config{DBPath: filepath.Join(dir, "index.db"), RequestsDBPath: missing}
	for _, args := range [][]string{
		{"delete", "req_abc"},
		{"purge", "--older-than", "1d"},
	} {
		_, stderr, code := runCLI(t, args, cfg)
		if code != 1 {
			t.Errorf("%v exit = %d, want 1 (stderr %q)", args, code, stderr)
		}
		if !strings.Contains(stderr, "no knowledge-request store") {
			t.Errorf("%v stderr = %q, want the missing-store hint", args, stderr)
		}
		if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%v created %s", args, missing)
		}
	}
}

// TestTakeRequestIDWithBooleanFlagBeforeID pins the ordering that a known-value
// heuristic broke: a boolean flag does not consume the id.
func TestTakeRequestIDWithBooleanFlagBeforeID(t *testing.T) {
	cfg, store := requestConfig(t)
	req, _, err := store.Create(requests.CreateInput{Question: "q", Principal: "agent:a"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, stderr, code := runCLI(t, []string{"resolve", "--no-ask", req.ID, "--answer", "x"}, cfg)
	if code != 0 {
		t.Fatalf("resolve with a boolean flag before the id exit = %d, stderr = %s", code, stderr)
	}
	if got, _ := store.Get(req.ID); got.Status != requests.StatusResolved {
		t.Errorf("status = %q, want resolved", got.Status)
	}
}

// TestResolveRejectsNonPositiveTimeouts mirrors `agent ask`, which refuses a
// zero or negative budget instead of producing an already-expired context.
func TestResolveRejectsNonPositiveTimeouts(t *testing.T) {
	cfg, store := requestConfig(t)
	req, _, err := store.Create(requests.CreateInput{Question: "q", Principal: "agent:a"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, flag := range []string{"--timeout=0s", "--timeout=-1s", "--push-timeout=0s"} {
		if _, _, code := runCLI(t, []string{"resolve", req.ID, "--no-ask", "--answer", "x", flag}, cfg); code != 2 {
			t.Errorf("resolve %s exit = %d, want 2", flag, code)
		}
	}
}

// TestPrintableStripsTerminalEscapes pins the terminal-injection guard: a peer
// can put ESC/OSC sequences and carriage returns in a question, and the owner
// prints it.
func TestPrintableStripsTerminalEscapes(t *testing.T) {
	in := "status: new\r\nquestion: real\x1b]52;c;Zm9v\x07\x1b[2J"
	got := printable(in)
	if strings.ContainsAny(got, "\x1b\x07\r") {
		t.Errorf("printable kept control bytes: %q", got)
	}
	if !strings.Contains(got, "status: new") || !strings.Contains(got, "question: real") {
		t.Errorf("printable dropped visible text: %q", got)
	}
	if !strings.Contains(got, "\n") {
		t.Errorf("printable dropped the deliberate newline: %q", got)
	}
}

func TestRequestsUsageAndBadArguments(t *testing.T) {
	cfg, _ := requestConfig(t)
	if _, _, code := runCLI(t, nil, cfg); code != 2 {
		t.Errorf("no subcommand exit = %d, want 2", code)
	}
	if _, _, code := runCLI(t, []string{"nope"}, cfg); code != 2 {
		t.Errorf("unknown subcommand exit = %d, want 2", code)
	}
	if _, _, code := runCLI(t, []string{"show"}, cfg); code != 2 {
		t.Errorf("show without id exit = %d, want 2", code)
	}
	if _, _, code := runCLI(t, []string{"purge"}, cfg); code != 2 {
		t.Errorf("purge without --older-than exit = %d, want 2", code)
	}
}

// TestPurgeRefusesOpenStatuses pins the CLI half of the purge contract: an
// open status reaches the store and is rejected, exit 1 (not a silent purge).
func TestPurgeRefusesOpenStatuses(t *testing.T) {
	cfg, store := requestConfig(t)
	if _, _, err := store.Create(requests.CreateInput{Question: "q", Principal: "agent:a"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, stderr, code := runCLI(t, []string{"purge", "--older-than", "1d", "--status", "new"}, cfg); code != 1 {
		t.Errorf("purge --status new exit = %d (stderr %q), want 1", code, stderr)
	}
	if n, _ := store.Pending(); n != 1 {
		t.Errorf("pending = %d, want the open request kept", n)
	}
}

// TestTakeRequestIDIgnoresFlagValues keeps a flag value that happens to start
// with req_ from being stolen as the positional id.
func TestTakeRequestIDIgnoresFlagValues(t *testing.T) {
	id, rest := takeRequestID([]string{"resolve", "--note", "req_1 is the id", "req_real"})
	if id != "req_real" {
		t.Errorf("id = %q, want req_real", id)
	}
	if len(rest) != 3 || rest[1] != "--note" || rest[2] != "req_1 is the id" {
		t.Errorf("rest = %#v, want the flag and its value intact", rest)
	}
	id, rest = takeRequestID([]string{"--note=req_1", "req_real"})
	if id != "req_real" || len(rest) != 1 {
		t.Errorf("inline flag value: id = %q, rest = %#v", id, rest)
	}
}

// TestTruncateCellKeepsUTF8Valid pins rune-boundary truncation for the inbox
// table: a byte slice would cut a multi-byte character in half.
func TestTruncateCellKeepsUTF8Valid(t *testing.T) {
	got := truncateCell("żółć-zażółć-gęślą-jaźń", 8)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateCell produced invalid UTF-8: %q", got)
	}
	if want := "żółć-za…"; got != want {
		t.Errorf("truncateCell = %q, want %q", got, want)
	}
	if got := truncateCell("short", 10); got != "short" {
		t.Errorf("truncateCell(short) = %q", got)
	}
}

func TestParseAge(t *testing.T) {
	if got, err := parseAge("180d"); err != nil || got != 180*24*time.Hour {
		t.Errorf("parseAge(180d) = %v, %v", got, err)
	}
	if got, err := parseAge("72h"); err != nil || got != 72*time.Hour {
		t.Errorf("parseAge(72h) = %v, %v", got, err)
	}
	// An overflowing day count must not wrap to a negative duration: the
	// cut-off would land in the future and purge every closed request.
	for _, bad := range []string{"", "0d", "-3d", "soon", "200000d", "106752d"} {
		if _, err := parseAge(bad); err == nil {
			t.Errorf("parseAge(%q) accepted a bad age", bad)
		}
	}
}
