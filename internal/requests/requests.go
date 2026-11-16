// Package requests is the durable side of the deferred-question workflow: a
// peer that receives the refusal string can explicitly submit the missing
// question, the service records it, the owner answers it by publishing, and
// the recorded answer is delivered back to the peer.
//
// It is the one intentionally writable store. The writing index is derived
// data that may be deleted and rebuilt at any time (AGENTS.md), so requests
// live in their own SQLite file beside it: `rm data/index.db*` must never lose
// a question a peer waited on. `agent serve` opens the index read-only and this
// file read-write; the owner CLI is the only other writer.
//
// Question text is stored deliberately and read back only by the owner CLI and
// by the peer that holds the request id. It never goes to the audit stream or
// an ordinary log line (docs/logging.md, docs/a2a.md).
package requests

import (
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3" // the "sqlite3" driver, as in internal/store

	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

//go:embed schema.sql
var schemaSQL string

// Request statuses. A request starts at StatusNew and ends at exactly one of
// resolved, declined, or canceled.
const (
	StatusNew      = "new"
	StatusReviewed = "reviewed"
	StatusResolved = "resolved"
	StatusDeclined = "declined"
	StatusCanceled = "canceled"
)

// Origins record why the peer submitted the question.
const (
	OriginInsufficientEvidence = "insufficient_evidence"
	OriginPartial              = "partial"
	OriginPeerRequest          = "peer_request"
)

// Limits. They bound what a peer can persist: a question is at most the loop's
// own question cap, and the free-text context is smaller than the question so
// a submission cannot be used as bulk storage.
const (
	// MaxQuestionBytes matches agentloop.MaxQuestionBytes (8 KiB); the A2A
	// handler enforces the loop's own limit before calling here.
	MaxQuestionBytes = 8 << 10
	// MaxReasonBytes bounds the one-line "why do you need this" field.
	MaxReasonBytes = 1 << 10
	// MaxContextBytes bounds the optional supporting context.
	MaxContextBytes = 4 << 10
	// MaxIDBytes bounds opaque identifiers echoed from the peer (the A2A
	// contextId). They are stored per row, and the inbox cap counts rows, not
	// bytes, so an unbounded id would be bulk storage.
	MaxIDBytes = 256
	// MaxNoteBytes bounds an owner triage note.
	MaxNoteBytes = 4 << 10
	// MaxAnswerBytes bounds a stored grounded answer.
	MaxAnswerBytes = 32 << 10
	// maxKeyLen bounds the idempotency key a peer can mint.
	maxKeyLen = 128
)

// Store errors callers translate into a CLI message or an A2A error.
var (
	// ErrNotFound reports that no request has the given id.
	ErrNotFound = errors.New("knowledge request not found")
	// ErrIdempotencyConflict reports that an idempotency key was already used
	// for a different question.
	ErrIdempotencyConflict = errors.New("idempotency key already used for a different question")
	// ErrNoPushConfig reports that a request exists but has no push
	// registration. It is distinct from ErrNotFound so a peer is never told
	// its task vanished when it merely has no webhook.
	ErrNoPushConfig = errors.New("no push notification config registered")
	// ErrNoStore reports that the store file does not exist, which is a
	// configuration/path problem, not a missing request. A read-only open
	// returns it rather than creating an empty database at a wrong path.
	ErrNoStore = errors.New("knowledge request store does not exist")
	// ErrInvalidTransition reports a status change the workflow does not allow.
	ErrInvalidTransition = errors.New("invalid knowledge request status transition")
	// ErrInvalid reports malformed submission data.
	ErrInvalid = errors.New("invalid knowledge request")
)

// timeLayout is the storage and comparison format: fixed-width UTC, so a
// lexicographic SQL comparison is also a chronological one.
const timeLayout = "2006-01-02T15:04:05Z"

// Request is one deferred question and everything the owner and the peer need
// about it. Push credentials are deliberately excluded from JSON: the token is
// a shared secret between the operator and the peer.
type Request struct {
	ID             string          `json:"id"`
	SubmittedAt    string          `json:"submitted_at"`
	UpdatedAt      string          `json:"updated_at"`
	Question       string          `json:"question"`
	Reason         string          `json:"reason,omitempty"`
	Context        string          `json:"context,omitempty"`
	Principal      string          `json:"principal"`
	TokenID        string          `json:"token_id,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	Origin         string          `json:"origin"`
	ContextID      string          `json:"context_id,omitempty"`
	AnswerOutcome  string          `json:"answer_outcome,omitempty"`
	CorpusRevision string          `json:"corpus_revision,omitempty"`
	Status         string          `json:"status"`
	Note           string          `json:"note,omitempty"`
	SourceURL      string          `json:"source_url,omitempty"`
	Answer         string          `json:"answer,omitempty"`
	Citations      json.RawMessage `json:"citations,omitempty"` // JSON array
	Sources        json.RawMessage `json:"sources,omitempty"`   // JSON array
	ReviewedAt     string          `json:"reviewed_at,omitempty"`
	ResolvedAt     string          `json:"resolved_at,omitempty"`
	PushURL        string          `json:"push_url,omitempty"`
	PushToken      string          `json:"-"`
	PushAttempts   int             `json:"push_attempts,omitempty"`
	PushLastError  string          `json:"push_last_error,omitempty"`
	PushDelivered  string          `json:"push_delivered_at,omitempty"`
}

// Open reports whether a request is still open for the owner (new or reviewed).
func (r Request) Open() bool { return r.Status == StatusNew || r.Status == StatusReviewed }

// CreateInput is one explicit submission.
type CreateInput struct {
	Question       string
	Reason         string
	Context        string
	Principal      string
	TokenID        string
	IdempotencyKey string
	Origin         string
	ContextID      string
	AnswerOutcome  string
	CorpusRevision string
	// ID is test-only; production leaves it empty and gets a random id.
	ID string
	// Now is test-only; production leaves it nil for the wall clock.
	Now func() time.Time
}

// Resolution is what the owner records when the published writing now answers
// the question.
type Resolution struct {
	Answer    string
	Citations string // JSON array
	Sources   string // JSON array
	SourceURL string
	Note      string
	Now       func() time.Time
}

// ListFilter selects a slice of the inbox.
type ListFilter struct {
	// Status restricts to one status; empty means every status.
	Status string
	// Limit caps the rows returned; zero means 50.
	Limit int
	// OldestFirst reverses the newest-first default.
	OldestFirst bool
}

// Store is a handle to the requests database.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (creating if necessary) the requests database at path and applies
// its embedded schema. The path's directory is created if missing, so the
// default beside the index needs no extra setup.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("requests: database path is empty")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create requests directory %s: %w", dir, err)
		}
	}
	// Pre-create the database with owner-only permissions *before* SQLite
	// opens it: the -wal/-shm sidecars inherit the main file's mode at
	// creation time, and applying the schema first would leave them
	// world-readable (0644 under the default umask) while question text is
	// already being written.
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600); err != nil {
		return nil, fmt.Errorf("create requests database %s: %w", path, err)
	} else if err := f.Close(); err != nil {
		return nil, fmt.Errorf("create requests database %s: %w", path, err)
	}
	// _txlock=immediate makes Begin take the write lock up front. The store is
	// written by both `agent serve` and the owner CLI, and a deferred
	// read-then-write transition can otherwise fail on a busy snapshot
	// upgrade (SQLITE_BUSY_SNAPSHOT), which _busy_timeout does not retry.
	dsn := "file:" + path + "?_fk=1&_busy_timeout=5000&_journal_mode=WAL&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open requests %s: %w", path, err)
	}
	// One writer: submissions, owner triage, and push bookkeeping all come
	// from one process in practice, and SQLite is happiest that way.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		log := logging.DB()
		log.Error().Err(err).Str("db", path).Msg("requests schema failed")
		return nil, fmt.Errorf("apply requests schema to %s: %w", path, err)
	}
	// The store holds peer-submitted text that must not be world-readable on a
	// shared host; the operator account is the only reader besides the service.
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("restrict permissions on %s: %w", path, err)
	}
	log := logging.DB()
	log.Debug().Str("db", path).Msg("requests database ready")
	return &Store{db: db, path: path}, nil
}

// OpenReadOnly opens an existing store without creating it, applying the
// schema, or changing permissions. It is the path for a snapshot source: the
// server may be writing, and a backup must not need a write transaction (or a
// chmod) to read.
func OpenReadOnly(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNoStore, path)
		}
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	dsn := "file:" + path + "?mode=ro&_fk=1&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open requests %s (read-only): %w", path, err)
	}
	db.SetMaxOpenConns(1)
	return &Store{db: db, path: path}, nil
}

// Path is the database file this store opened.
func (s *Store) Path() string { return s.path }

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Create records one explicit submission. The second result is false when an
// idempotent retry found the existing request instead of creating a new one.
// A retry with a different question is ErrIdempotencyConflict: silently
// keeping the old text would lose what the peer meant to ask.
func (s *Store) Create(in CreateInput) (Request, bool, error) {
	in.Question = strings.TrimSpace(in.Question)
	in.Reason = strings.TrimSpace(in.Reason)
	in.Context = strings.TrimSpace(in.Context)
	in.Principal = strings.TrimSpace(in.Principal)
	in.TokenID = strings.TrimSpace(in.TokenID)
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	in.CorpusRevision = strings.TrimSpace(in.CorpusRevision)
	if in.Origin == "" {
		in.Origin = OriginPeerRequest
	}
	switch {
	case in.Question == "":
		return Request{}, false, fmt.Errorf("%w: question is required", ErrInvalid)
	case len(in.Question) > MaxQuestionBytes:
		return Request{}, false, fmt.Errorf("%w: question exceeds %d bytes", ErrInvalid, MaxQuestionBytes)
	case len(in.Reason) > MaxReasonBytes:
		return Request{}, false, fmt.Errorf("%w: reason exceeds %d bytes", ErrInvalid, MaxReasonBytes)
	case len(in.Context) > MaxContextBytes:
		return Request{}, false, fmt.Errorf("%w: context exceeds %d bytes", ErrInvalid, MaxContextBytes)
	case len(strings.TrimSpace(in.ContextID)) > MaxIDBytes:
		return Request{}, false, fmt.Errorf("%w: contextId exceeds %d bytes", ErrInvalid, MaxIDBytes)
	case in.Principal == "":
		return Request{}, false, fmt.Errorf("%w: principal is required", ErrInvalid)
	case len(in.IdempotencyKey) > maxKeyLen:
		return Request{}, false, fmt.Errorf("%w: idempotencyKey exceeds %d characters", ErrInvalid, maxKeyLen)
	}
	if !validOrigin(in.Origin) {
		return Request{}, false, fmt.Errorf("%w: unknown origin %q", ErrInvalid, in.Origin)
	}

	if in.IdempotencyKey != "" {
		existing, err := s.byIdempotencyKey(in.Principal, in.IdempotencyKey)
		switch {
		case err == nil:
			if existing.Question != in.Question {
				return Request{}, false, ErrIdempotencyConflict
			}
			return existing, false, nil
		case !errors.Is(err, ErrNotFound):
			return Request{}, false, err
		}
	}

	id := strings.TrimSpace(in.ID)
	if id == "" {
		id = newID()
	}
	now := stamp(in.Now)
	req := Request{
		ID:             id,
		SubmittedAt:    now,
		UpdatedAt:      now,
		Question:       in.Question,
		Reason:         in.Reason,
		Context:        in.Context,
		Principal:      in.Principal,
		TokenID:        in.TokenID,
		IdempotencyKey: in.IdempotencyKey,
		Origin:         in.Origin,
		ContextID:      strings.TrimSpace(in.ContextID),
		AnswerOutcome:  in.AnswerOutcome,
		CorpusRevision: in.CorpusRevision,
		Status:         StatusNew,
		Citations:      json.RawMessage("[]"),
		Sources:        json.RawMessage("[]"),
	}
	_, err := s.db.Exec(`
		INSERT INTO knowledge_requests (
			id, submitted_at, updated_at, question, reason, context_text, principal,
			token_id, idempotency_key, origin, context_id, answer_outcome, corpus_revision, status
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		req.ID, req.SubmittedAt, req.UpdatedAt, req.Question, req.Reason, req.Context,
		req.Principal, req.TokenID, req.IdempotencyKey, req.Origin, req.ContextID,
		req.AnswerOutcome, req.CorpusRevision, req.Status)
	if err != nil {
		// A concurrent submission with the same key won the race: return it
		// instead of surfacing a unique-constraint error to the peer.
		if in.IdempotencyKey != "" {
			if existing, lookupErr := s.byIdempotencyKey(in.Principal, in.IdempotencyKey); lookupErr == nil {
				if existing.Question != in.Question {
					return Request{}, false, ErrIdempotencyConflict
				}
				return existing, false, nil
			}
		}
		return Request{}, false, fmt.Errorf("insert knowledge request: %w", err)
	}
	log := logging.DB()
	log.Info().Str("request_id", req.ID).Str("origin", req.Origin).Msg("knowledge request recorded")
	return req, true, nil
}

// Get returns one request by id.
func (s *Store) Get(id string) (Request, error) {
	return s.queryOne(`SELECT `+selectColumns+` FROM knowledge_requests WHERE id = ?`, strings.TrimSpace(id))
}

// FindByIdempotencyKey reports a requester's earlier submission for a key, so
// a caller can resolve a retry *before* applying limits that would otherwise
// refuse a request the peer already owns. found is false when nothing matches.
func (s *Store) FindByIdempotencyKey(principal, key string) (req Request, found bool, err error) {
	principal = strings.TrimSpace(principal)
	key = strings.TrimSpace(key)
	if principal == "" || key == "" {
		return Request{}, false, nil
	}
	req, err = s.byIdempotencyKey(principal, key)
	if errors.Is(err, ErrNotFound) {
		return Request{}, false, nil
	}
	if err != nil {
		return Request{}, false, err
	}
	return req, true, nil
}

// byIdempotencyKey finds a requester's earlier submission.
func (s *Store) byIdempotencyKey(principal, key string) (Request, error) {
	return s.queryOne(`SELECT `+selectColumns+` FROM knowledge_requests
		WHERE principal = ? AND idempotency_key = ?`, principal, key)
}

// List returns requests newest first by default.
func (s *Store) List(f ListFilter) ([]Request, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	order := "DESC"
	if f.OldestFirst {
		order = "ASC"
	}
	query := `SELECT ` + selectColumns + ` FROM knowledge_requests`
	args := []any{}
	if f.Status != "" {
		if !validStatus(f.Status) {
			return nil, fmt.Errorf("%w: unknown status %q", ErrInvalid, f.Status)
		}
		query += ` WHERE status = ?`
		args = append(args, f.Status)
	}
	query += ` ORDER BY submitted_at ` + order + `, id ` + order + ` LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list knowledge requests: %w", err)
	}
	defer rows.Close()
	out := make([]Request, 0, limit)
	for rows.Next() {
		req, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, rows.Err()
}

// Pending counts the requests still waiting for the owner.
func (s *Store) Pending() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM knowledge_requests WHERE status IN (?,?)`,
		StatusNew, StatusReviewed).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count pending knowledge requests: %w", err)
	}
	return n, nil
}

// Review marks a request as being worked on. Re-reviewing is allowed so a note
// can be corrected before the question is answered.
func (s *Store) Review(id, note string, now func() time.Time) (Request, error) {
	return s.transition(id, []string{StatusNew, StatusReviewed}, func(tx *sql.Tx, req *Request) error {
		ts := stamp(now)
		req.Status = StatusReviewed
		req.ReviewedAt = ts
		req.Note = strings.TrimSpace(note)
		if len(req.Note) > MaxNoteBytes {
			return fmt.Errorf("%w: note exceeds %d bytes", ErrInvalid, MaxNoteBytes)
		}
		req.UpdatedAt = ts
		return nil
	})
}

// Resolve records the grounded answer that now exists because the owner
// published about it. The answer is evidence from the index, never the owner's
// private note: `agent requests resolve --ask` refuses to call a question
// resolved while the corpus still does not ground it.
func (s *Store) Resolve(id string, in Resolution) (Request, error) {
	return s.transition(id, []string{StatusNew, StatusReviewed}, func(tx *sql.Tx, req *Request) error {
		in.Answer = strings.TrimSpace(in.Answer)
		in.SourceURL = strings.TrimSpace(in.SourceURL)
		in.Note = strings.TrimSpace(in.Note)
		switch {
		case in.Answer == "":
			return fmt.Errorf("%w: a resolved request needs a grounded answer", ErrInvalid)
		case len(in.Answer) > MaxAnswerBytes:
			return fmt.Errorf("%w: answer exceeds %d bytes", ErrInvalid, MaxAnswerBytes)
		case len(in.Note) > MaxNoteBytes:
			return fmt.Errorf("%w: note exceeds %d bytes", ErrInvalid, MaxNoteBytes)
		}
		ts := stamp(in.Now)
		req.Status = StatusResolved
		req.Answer = in.Answer
		req.Citations = jsonOrEmpty(in.Citations)
		req.Sources = jsonOrEmpty(in.Sources)
		req.SourceURL = in.SourceURL
		if in.Note != "" {
			req.Note = in.Note
		}
		req.ResolvedAt = ts
		req.UpdatedAt = ts
		return nil
	})
}

// Decline closes a request without an answer: the owner will not publish about
// it, or the question is out of scope. The peer is told so and can stop waiting.
func (s *Store) Decline(id, note string, now func() time.Time) (Request, error) {
	return s.transition(id, []string{StatusNew, StatusReviewed}, func(tx *sql.Tx, req *Request) error {
		note = strings.TrimSpace(note)
		if len(note) > MaxNoteBytes {
			return fmt.Errorf("%w: note exceeds %d bytes", ErrInvalid, MaxNoteBytes)
		}
		ts := stamp(now)
		req.Status = StatusDeclined
		req.Note = note
		req.ResolvedAt = ts
		req.UpdatedAt = ts
		return nil
	})
}

// Cancel is the peer withdrawing its own request.
func (s *Store) Cancel(id, note string, now func() time.Time) (Request, error) {
	return s.transition(id, []string{StatusNew, StatusReviewed}, func(tx *sql.Tx, req *Request) error {
		ts := stamp(now)
		req.Status = StatusCanceled
		if note = strings.TrimSpace(note); note != "" {
			req.Note = note
		}
		req.ResolvedAt = ts
		req.UpdatedAt = ts
		return nil
	})
}

// Delete removes one request and its push registration. Used for the owner's
// privacy purge of a submission the peer withdrew or that was sent by mistake.
func (s *Store) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM knowledge_requests WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return fmt.Errorf("delete knowledge request: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete knowledge request: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Purge deletes closed requests submitted before cutoff. Open requests are
// never purged: the peer is still waiting on them.
func (s *Store) Purge(cutoff time.Time, statuses []string) (int64, error) {
	if len(statuses) == 0 {
		statuses = []string{StatusResolved, StatusDeclined, StatusCanceled}
	}
	placeholders := make([]string, 0, len(statuses))
	args := []any{cutoff.UTC().Format(timeLayout)}
	for _, status := range statuses {
		if !validStatus(status) {
			return 0, fmt.Errorf("%w: unknown status %q", ErrInvalid, status)
		}
		switch status {
		case StatusNew, StatusReviewed:
			// The peer is still waiting on an open request; deleting it would
			// silently drop that wait. Housekeeping is for closed requests.
			return 0, fmt.Errorf("%w: only closed requests can be purged, not %q", ErrInvalid, status)
		}
		placeholders = append(placeholders, "?")
		args = append(args, status)
	}
	res, err := s.db.Exec(`DELETE FROM knowledge_requests
		WHERE submitted_at < ? AND status IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return 0, fmt.Errorf("purge knowledge requests: %w", err)
	}
	return res.RowsAffected()
}

// SetPushConfig records the peer's A2A push target for one request. A request
// has at most one: a newer registration replaces the previous target.
func (s *Store) SetPushConfig(id, target, token string) (Request, error) {
	target = strings.TrimSpace(target)
	token = strings.TrimSpace(token)
	if target == "" {
		return Request{}, fmt.Errorf("%w: push url is required", ErrInvalid)
	}
	return s.transition(id, []string{StatusNew, StatusReviewed, StatusResolved, StatusDeclined},
		func(tx *sql.Tx, req *Request) error {
			req.PushURL = target
			req.PushToken = token
			req.PushAttempts = 0
			req.PushLastError = ""
			// A new target has never been exercised; keeping the old delivery
			// stamp would make the inbox report a delivery that did not happen.
			req.PushDelivered = ""
			req.UpdatedAt = stamp(nil)
			return nil
		})
}

// ClearPushConfig removes a push registration; polling `tasks/get` remains.
// It is idempotent: clearing an already-empty registration succeeds, and only
// a missing task is ErrNotFound.
func (s *Store) ClearPushConfig(id string) error {
	id = strings.TrimSpace(id)
	if _, err := s.Get(id); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE knowledge_requests
		SET push_url = '', push_token = '', push_attempts = 0, push_last_error = '',
		    push_delivered_at = NULL
		WHERE id = ?`, id); err != nil {
		return fmt.Errorf("clear push config: %w", err)
	}
	return nil
}

// PushTarget returns a request's registered push URL and its shared secret.
// A missing request is ErrNotFound; an existing request with no registration is
// ErrNoPushConfig, because those are different answers for a peer.
func (s *Store) PushTarget(id string) (target, token string, err error) {
	req, err := s.Get(id)
	if err != nil {
		return "", "", err
	}
	if req.PushURL == "" {
		return "", "", ErrNoPushConfig
	}
	return req.PushURL, req.PushToken, nil
}

// RecordPush stores the outcome of one delivery attempt sequence.
func (s *Store) RecordPush(id string, attempts int, deliveryErr error) error {
	if attempts <= 0 {
		attempts = 1
	}
	var (
		query string
		args  []any
	)
	if deliveryErr != nil {
		query = `UPDATE knowledge_requests
			SET push_attempts = push_attempts + ?, push_last_error = ?, updated_at = ?
			WHERE id = ?`
		args = []any{attempts, truncate(deliveryErr.Error(), 500), stamp(nil), strings.TrimSpace(id)}
	} else {
		query = `UPDATE knowledge_requests
			SET push_attempts = push_attempts + ?, push_last_error = '', push_delivered_at = ?, updated_at = ?
			WHERE id = ?`
		ts := stamp(nil)
		args = []any{attempts, ts, ts, strings.TrimSpace(id)}
	}
	if _, err := s.db.Exec(query, args...); err != nil {
		return fmt.Errorf("record push delivery: %w", err)
	}
	return nil
}

// Backup writes a consistent snapshot with SQLite's online VACUUM INTO. The
// destination must not already exist, exactly like the index backup.
func (s *Store) Backup(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("backup destination is empty")
	}
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return fmt.Errorf("backup destination is a directory: %s", path)
		}
		return fmt.Errorf("backup destination already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create backup directory %s: %w", dir, err)
		}
	}
	if _, err := s.db.Exec("VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("vacuum into %s: %w", path, err)
	}
	// VACUUM INTO creates the file with the process umask. The snapshot holds
	// the same peer text as the source, so it gets the same 0600.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict permissions on %s: %w", path, err)
	}
	return nil
}

// transition reads, checks, and writes one status change inside a transaction,
// so two concurrent triage commands cannot both move the same request.
func (s *Store) transition(id string, from []string, apply func(*sql.Tx, *Request) error) (Request, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Request{}, fmt.Errorf("%w: id is required", ErrInvalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Request{}, fmt.Errorf("begin request transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRow(`SELECT `+selectColumns+` FROM knowledge_requests WHERE id = ?`, id)
	req, err := scanRequest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, err
	}
	ok := false
	for _, allowed := range from {
		if req.Status == allowed {
			ok = true
			break
		}
	}
	if !ok {
		return Request{}, fmt.Errorf("%w: a %s request cannot move to the requested state", ErrInvalidTransition, req.Status)
	}
	if err := apply(tx, &req); err != nil {
		return Request{}, err
	}
	if _, err := tx.Exec(`UPDATE knowledge_requests SET
			updated_at = ?, status = ?, note = ?, source_url = ?, answer = ?,
			citations = ?, sources = ?, reviewed_at = ?, resolved_at = ?,
			push_url = ?, push_token = ?, push_attempts = ?, push_last_error = ?,
			push_delivered_at = ?
		WHERE id = ?`,
		req.UpdatedAt, req.Status, req.Note, req.SourceURL, req.Answer,
		req.Citations, req.Sources, nullable(req.ReviewedAt), nullable(req.ResolvedAt),
		req.PushURL, req.PushToken, req.PushAttempts, req.PushLastError,
		nullable(req.PushDelivered), req.ID); err != nil {
		return Request{}, fmt.Errorf("update knowledge request: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Request{}, fmt.Errorf("commit request transition: %w", err)
	}
	log := logging.DB()
	log.Info().
		Str("request_id", req.ID).
		Str("status", req.Status).
		Msg("knowledge request updated")
	return req, nil
}

// selectColumns is the one column list the scan helper understands.
const selectColumns = `id, submitted_at, updated_at, question, reason, context_text,
	principal, token_id, idempotency_key, origin, context_id, answer_outcome, corpus_revision,
	status, note, source_url, answer, citations, sources, reviewed_at, resolved_at,
	push_url, push_token, push_attempts, push_last_error, push_delivered_at`

// scanner is the shared shape of *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// scanRequest reads one row of selectColumns.
func scanRequest(row scanner) (Request, error) {
	var (
		req                           Request
		reviewed, resolved, delivered sql.NullString
		citations, sources            []byte
	)
	err := row.Scan(
		&req.ID, &req.SubmittedAt, &req.UpdatedAt, &req.Question, &req.Reason, &req.Context,
		&req.Principal, &req.TokenID, &req.IdempotencyKey, &req.Origin, &req.ContextID,
		&req.AnswerOutcome, &req.CorpusRevision, &req.Status, &req.Note, &req.SourceURL, &req.Answer,
		&citations, &sources, &reviewed, &resolved,
		&req.PushURL, &req.PushToken, &req.PushAttempts, &req.PushLastError, &delivered,
	)
	if err != nil {
		return Request{}, err
	}
	req.ReviewedAt = reviewed.String
	req.ResolvedAt = resolved.String
	req.PushDelivered = delivered.String
	req.Citations = jsonOrEmpty(string(citations))
	req.Sources = jsonOrEmpty(string(sources))
	return req, nil
}

// queryOne runs a one-row query and maps the empty case to ErrNotFound.
func (s *Store) queryOne(query string, args ...any) (Request, error) {
	req, err := scanRequest(s.db.QueryRow(query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, fmt.Errorf("read knowledge request: %w", err)
	}
	return req, nil
}

// stamp formats a UTC timestamp, defaulting to now.
func stamp(now func() time.Time) string {
	t := time.Now()
	if now != nil {
		t = now()
	}
	return t.UTC().Format(timeLayout)
}

// nullable turns an empty timestamp into NULL so "never happened" stays
// distinguishable from the zero time.
func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// jsonOrEmpty keeps the two evidence columns valid JSON: the A2A response
// embeds them verbatim, so an empty string would break a peer's parser.
func jsonOrEmpty(raw string) json.RawMessage {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return json.RawMessage("[]")
	}
	return json.RawMessage(raw)
}

// truncate caps a stored error message; a delivery failure should not grow a
// row without bound. It counts runes so the stored text is always valid UTF-8
// (the owner CLI prints it), matching the CLI's truncation rule.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// newID returns a request id: `req_` plus 128 random bits. The id is the
// peer's only handle on its question, so it must be unguessable.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("requests: crypto/rand: " + err.Error())
	}
	return "req_" + hex.EncodeToString(b[:])
}

func validStatus(s string) bool {
	switch s {
	case StatusNew, StatusReviewed, StatusResolved, StatusDeclined, StatusCanceled:
		return true
	}
	return false
}

func validOrigin(s string) bool {
	switch s {
	case OriginInsufficientEvidence, OriginPartial, OriginPeerRequest:
		return true
	}
	return false
}
