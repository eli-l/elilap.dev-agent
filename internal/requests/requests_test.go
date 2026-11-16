package requests_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/eli-l/elilap.dev-agent/internal/requests"
)

func open(t *testing.T) *requests.Store {
	t.Helper()
	s, err := requests.Open(filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCreateAndGet(t *testing.T) {
	s := open(t)
	req, created, err := s.Create(requests.CreateInput{
		Question:  "What is the position on monorepos?",
		Reason:    "for an architecture comparison",
		Context:   "the client is deciding between repos",
		Principal: "agent:peer-1",
		Origin:    requests.OriginInsufficientEvidence,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !created {
		t.Fatal("Create reported an existing request")
	}
	if !strings.HasPrefix(req.ID, "req_") || req.Status != requests.StatusNew {
		t.Errorf("request = %+v, want a req_ id in status new", req)
	}
	got, err := s.Get(req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Question != req.Question || got.Reason != req.Reason || got.Context != req.Context {
		t.Errorf("round trip lost fields: %+v", got)
	}
	if got.Principal != "agent:peer-1" || got.Origin != requests.OriginInsufficientEvidence {
		t.Errorf("principal/origin = %q/%q", got.Principal, got.Origin)
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	s := open(t)
	cases := map[string]requests.CreateInput{
		"empty question":   {Principal: "agent:a"},
		"long question":    {Principal: "agent:a", Question: strings.Repeat("x", requests.MaxQuestionBytes+1)},
		"long reason":      {Principal: "agent:a", Question: "q", Reason: strings.Repeat("x", requests.MaxReasonBytes+1)},
		"long context":     {Principal: "agent:a", Question: "q", Context: strings.Repeat("x", requests.MaxContextBytes+1)},
		"missing identity": {Question: "q"},
		"unknown origin":   {Principal: "agent:a", Question: "q", Origin: "because"},
		"long contextId":   {Principal: "agent:a", Question: "q", ContextID: strings.Repeat("c", requests.MaxIDBytes+1)},
	}
	for name, in := range cases {
		if _, _, err := s.Create(in); !errors.Is(err, requests.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

// TestIdempotency pins both halves: a retry returns the same request, and a
// retry with different text is a conflict instead of a silent overwrite.
func TestIdempotency(t *testing.T) {
	s := open(t)
	in := requests.CreateInput{
		Question:       "What about event sourcing?",
		Principal:      "agent:peer-1",
		IdempotencyKey: "sub-1",
	}
	first, created, err := s.Create(in)
	if err != nil || !created {
		t.Fatalf("Create: %v created=%v", err, created)
	}
	second, created, err := s.Create(in)
	if err != nil {
		t.Fatalf("retry Create: %v", err)
	}
	if created || second.ID != first.ID {
		t.Errorf("retry created=%v id=%q, want the original %q", created, second.ID, first.ID)
	}
	if n, err := s.Pending(); err != nil || n != 1 {
		t.Errorf("Pending = %d, %v; want 1 row", n, err)
	}

	in.Question = "What about CQRS?"
	if _, _, err := s.Create(in); !errors.Is(err, requests.ErrIdempotencyConflict) {
		t.Errorf("err = %v, want ErrIdempotencyConflict", err)
	}
	// Another principal may use the same key.
	in.Principal = "agent:peer-2"
	if _, created, err := s.Create(in); err != nil || !created {
		t.Errorf("second principal: err=%v created=%v, want a new request", err, created)
	}
}

func TestTransitions(t *testing.T) {
	s := open(t)
	req, _, err := s.Create(requests.CreateInput{Question: "q", Principal: "agent:a"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	reviewed, err := s.Review(req.ID, "reading", nil)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if reviewed.Status != requests.StatusReviewed || reviewed.Note != "reading" || reviewed.ReviewedAt == "" {
		t.Errorf("reviewed = %+v", reviewed)
	}

	resolved, err := s.Resolve(req.ID, requests.Resolution{
		Answer:    "Eli wrote about it here.",
		Citations: `[{"title":"x"}]`,
		Sources:   `[{"title":"x"}]`,
		SourceURL: "https://read.elilap.dev/post/",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Status != requests.StatusResolved || resolved.Answer == "" ||
		resolved.SourceURL == "" || resolved.ResolvedAt == "" {
		t.Errorf("resolved = %+v", resolved)
	}
	if string(resolved.Citations) != `[{"title":"x"}]` {
		t.Errorf("citations = %s", resolved.Citations)
	}
	// Terminal states cannot move again.
	if _, err := s.Decline(req.ID, "", nil); !errors.Is(err, requests.ErrInvalidTransition) {
		t.Errorf("Decline after Resolve: err = %v, want ErrInvalidTransition", err)
	}
	if _, err := s.Resolve(req.ID, requests.Resolution{Answer: "x"}); !errors.Is(err, requests.ErrInvalidTransition) {
		t.Errorf("Resolve twice: err = %v, want ErrInvalidTransition", err)
	}
	if _, err := s.Resolve("req_missing", requests.Resolution{Answer: "x"}); !errors.Is(err, requests.ErrNotFound) {
		t.Errorf("Resolve unknown: err = %v, want ErrNotFound", err)
	}
}

func TestResolveNeedsAnAnswer(t *testing.T) {
	s := open(t)
	req, _, err := s.Create(requests.CreateInput{Question: "q", Principal: "agent:a"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Resolve(req.ID, requests.Resolution{SourceURL: "https://x"}); !errors.Is(err, requests.ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid for an empty answer", err)
	}
}

func TestListAndPending(t *testing.T) {
	s := open(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	for i, question := range []string{"first", "second", "third"} {
		_, _, err := s.Create(requests.CreateInput{
			Question:  question,
			Principal: "agent:a",
			Now:       func() time.Time { return now.Add(time.Duration(i) * time.Minute) },
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	rows, err := s.List(requests.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 3 || rows[0].Question != "third" {
		t.Errorf("rows = %+v, want newest first", rows)
	}
	oldest, err := s.List(requests.ListFilter{OldestFirst: true, Limit: 1})
	if err != nil {
		t.Fatalf("List oldest: %v", err)
	}
	if len(oldest) != 1 || oldest[0].Question != "first" {
		t.Errorf("oldest = %+v", oldest)
	}
	if _, err := s.List(requests.ListFilter{Status: "nope"}); !errors.Is(err, requests.ErrInvalid) {
		t.Errorf("bad status: err = %v, want ErrInvalid", err)
	}
}

func TestPurgeLeavesOpenRequests(t *testing.T) {
	s := open(t)
	old := time.Now().Add(-200 * 24 * time.Hour)
	closed, _, err := s.Create(requests.CreateInput{
		Question: "old and answered", Principal: "agent:a",
		Now: func() time.Time { return old },
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Resolve(closed.ID, requests.Resolution{Answer: "yes"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, _, err := s.Create(requests.CreateInput{
		Question: "old and still open", Principal: "agent:a",
		Now: func() time.Time { return old },
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	deleted, err := s.Purge(time.Now().Add(-30*24*time.Hour), nil)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if deleted != 1 {
		t.Errorf("purged %d, want 1 (the open request must survive)", deleted)
	}
	if _, err := s.Get(closed.ID); !errors.Is(err, requests.ErrNotFound) {
		t.Errorf("resolved request survived the purge")
	}
}

// TestReRegisteringPushClearsTheDeliveryStamp keeps the inbox honest: a new
// target has never been exercised, so an old "delivered" stamp must not survive.
func TestReRegisteringPushClearsTheDeliveryStamp(t *testing.T) {
	s := open(t)
	req, _, err := s.Create(requests.CreateInput{Question: "q", Principal: "agent:a"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.SetPushConfig(req.ID, "https://peer.example/one", ""); err != nil {
		t.Fatalf("SetPushConfig: %v", err)
	}
	if err := s.RecordPush(req.ID, 1, nil); err != nil {
		t.Fatalf("RecordPush: %v", err)
	}
	if _, err := s.SetPushConfig(req.ID, "https://peer.example/two", ""); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	got, err := s.Get(req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PushDelivered != "" || got.PushAttempts != 0 {
		t.Errorf("re-registration kept delivery state: %+v", got)
	}
	if _, err := s.SetPushConfig(req.ID, "https://peer.example/three", ""); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if err := s.RecordPush(req.ID, 1, nil); err != nil {
		t.Fatalf("RecordPush: %v", err)
	}
	if err := s.ClearPushConfig(req.ID); err != nil {
		t.Fatalf("ClearPushConfig: %v", err)
	}
	got, _ = s.Get(req.ID)
	if got.PushDelivered != "" {
		t.Errorf("clear kept the delivery stamp: %+v", got)
	}
}

// TestOpenReadOnlyNeverCreates pins the backup source contract: a missing store
// is an error, not an empty database, and an existing one is not migrated.
func TestOpenReadOnlyNeverCreates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.db")
	if _, err := requests.OpenReadOnly(path); !errors.Is(err, requests.ErrNoStore) {
		t.Fatalf("OpenReadOnly(missing) err = %v, want ErrNoStore", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("OpenReadOnly created %s", path)
	}

	s := open(t)
	req, _, err := s.Create(requests.CreateInput{Question: "q", Principal: "agent:a"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ro, err := requests.OpenReadOnly(s.Path())
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()
	if got, err := ro.Get(req.ID); err != nil || got.ID != req.ID {
		t.Errorf("read-only Get = %+v, %v", got, err)
	}
	if _, _, err := ro.Create(requests.CreateInput{Question: "q2", Principal: "agent:a"}); err == nil {
		t.Error("read-only store accepted a write")
	}
}

func TestPushConfigRoundTrip(t *testing.T) {
	s := open(t)
	req, _, err := s.Create(requests.CreateInput{Question: "q", Principal: "agent:a"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.SetPushConfig(req.ID, "https://peer.example/hook", "secret"); err != nil {
		t.Fatalf("SetPushConfig: %v", err)
	}
	target, token, err := s.PushTarget(req.ID)
	if err != nil {
		t.Fatalf("PushTarget: %v", err)
	}
	if target != "https://peer.example/hook" || token != "secret" {
		t.Errorf("target/token = %q/%q", target, token)
	}
	if err := s.RecordPush(req.ID, 2, errors.New("HTTP 500")); err != nil {
		t.Fatalf("RecordPush: %v", err)
	}
	got, err := s.Get(req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PushAttempts != 2 || got.PushLastError != "HTTP 500" || got.PushDelivered != "" {
		t.Errorf("push state = %+v", got)
	}
	if err := s.RecordPush(req.ID, 1, nil); err != nil {
		t.Fatalf("RecordPush: %v", err)
	}
	got, _ = s.Get(req.ID)
	if got.PushDelivered == "" {
		t.Errorf("delivered timestamp not recorded: %+v", got)
	}
	if err := s.ClearPushConfig(req.ID); err != nil {
		t.Fatalf("ClearPushConfig: %v", err)
	}
	if _, _, err := s.PushTarget(req.ID); !errors.Is(err, requests.ErrNoPushConfig) {
		t.Errorf("PushTarget after clear: err = %v, want ErrNoPushConfig", err)
	}
	// Clearing an already-clear registration is a no-op, not an error.
	if err := s.ClearPushConfig(req.ID); err != nil {
		t.Errorf("second ClearPushConfig: %v, want nil", err)
	}
	// A missing task is still ErrNotFound, which is a different answer.
	if _, _, err := s.PushTarget("req_missing"); !errors.Is(err, requests.ErrNotFound) {
		t.Errorf("PushTarget(unknown): err = %v, want ErrNotFound", err)
	}
	if err := s.ClearPushConfig("req_missing"); !errors.Is(err, requests.ErrNotFound) {
		t.Errorf("ClearPushConfig(unknown): err = %v, want ErrNotFound", err)
	}
}

// TestPurgeRejectsOpenRequests pins the contract the store documents: a purge
// may only delete closed requests, so a peer's wait is never dropped by
// housekeeping. The CLI passes --status through, so the store enforces it.
func TestPurgeRejectsOpenRequests(t *testing.T) {
	s := open(t)
	for _, status := range []string{requests.StatusNew, requests.StatusReviewed} {
		if _, err := s.Purge(time.Now().Add(24*time.Hour), []string{status}); !errors.Is(err, requests.ErrInvalid) {
			t.Errorf("Purge(%s) err = %v, want ErrInvalid", status, err)
		}
	}
}

// TestFindByIdempotencyKey covers the retry lookup a submission uses before
// applying the inbox cap: a found key is the peer's existing request.
func TestFindByIdempotencyKey(t *testing.T) {
	s := open(t)
	created, _, err := s.Create(requests.CreateInput{
		Question: "q", Principal: "agent:a", IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, found, err := s.FindByIdempotencyKey("agent:a", "k1")
	if err != nil || !found {
		t.Fatalf("FindByIdempotencyKey = %+v, %v, %v; want found", got, found, err)
	}
	if got.ID != created.ID {
		t.Errorf("found id = %q, want %q", got.ID, created.ID)
	}
	if _, found, err := s.FindByIdempotencyKey("agent:a", "missing"); err != nil || found {
		t.Errorf("unknown key = found %v, err %v; want not found", found, err)
	}
	if _, found, err := s.FindByIdempotencyKey("agent:b", "k1"); err != nil || found {
		t.Errorf("another principal = found %v, err %v; want not found", found, err)
	}
}

// TestDatabaseIsOwnerOnly pins the permission model: the store carries peer
// text, and the WAL/SHM sidecars inherit the main file's mode, so the file is
// created 0600 before SQLite opens it.
func TestDatabaseIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.db")
	s, err := requests.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if _, _, err := s.Create(requests.CreateInput{Question: "q", Principal: "agent:a"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(name)
		if errors.Is(err, os.ErrNotExist) && name != path {
			continue // a build without WAL sidecars is still fine
		}
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s mode = %o, want 600", name, mode)
		}
	}
}

// TestRecordPushTruncatesOnARuneBoundary keeps the stored error valid UTF-8:
// the owner CLI prints it, and a byte slice would split a multi-byte rune.
func TestRecordPushTruncatesOnARuneBoundary(t *testing.T) {
	s := open(t)
	req, _, err := s.Create(requests.CreateInput{Question: "q", Principal: "agent:a"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	long := strings.Repeat("ż", 600)
	if err := s.RecordPush(req.ID, 1, errors.New(long)); err != nil {
		t.Fatalf("RecordPush: %v", err)
	}
	got, err := s.Get(req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !utf8.ValidString(got.PushLastError) {
		t.Errorf("push_last_error is not valid UTF-8: %q", got.PushLastError)
	}
	if len([]rune(got.PushLastError)) != 500 {
		t.Errorf("push_last_error rune length = %d, want 500", len([]rune(got.PushLastError)))
	}
}

func TestBackupSnapshotIsReadable(t *testing.T) {
	s := open(t)
	if _, _, err := s.Create(requests.CreateInput{Question: "q", Principal: "agent:a"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	out := filepath.Join(t.TempDir(), "requests-backup.db")
	if err := s.Backup(out); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if err := s.Backup(out); err == nil {
		t.Fatal("Backup overwrote an existing snapshot")
	}
	if info, err := os.Stat(out); err != nil {
		t.Fatalf("stat snapshot: %v", err)
	} else if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("snapshot mode = %o, want 600 (it holds peer text)", mode)
	}
	copyStore, err := requests.Open(out)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer copyStore.Close()
	if n, err := copyStore.Pending(); err != nil || n != 1 {
		t.Errorf("backup Pending = %d, %v; want 1", n, err)
	}
}
