// Package audit emits the read-only task record the operator keeps: who asked
// (token id), a fingerprint of what was asked, and how the loop ended
// (answered/refused/timeout/canceled/error, iterations, citation count,
// elapsed).
//
// It deliberately excludes the question text, the answer, the prompt, and
// retrieved chunk bodies. The audit stream therefore stays safe to keep on at
// a level that would not be safe for the debug RAG trace (docs/logging.md,
// docs/a2a.md): the question is correlated by SHA-256 and byte length only.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

// Outcome is the audit record's terminal state.
const (
	OutcomeAnswered = "answered"
	OutcomeRefused  = "refused"
	OutcomeTimeout  = "timeout"
	OutcomeCanceled = "canceled"
	OutcomeError    = "error"
)

// Entry is one task record. Question is hashed before it reaches the log; it
// is never serialized.
type Entry struct {
	// Principal is the identity the budget charged: agent:<id>, user:<id>,
	// token:<id>, or ip:<addr>. It is self-asserted on a token-less public
	// bind, so it labels the caller without proving anything about it.
	Principal string
	// TokenID is the authenticated token id, or empty when no token gated the
	// request (the token-less dev and public surfaces).
	TokenID string
	// Method is the surface that handled the task, e.g. "a2a/message/send".
	Method string
	// Question is the peer's question. Only its SHA-256 and byte length are
	// logged.
	Question string
	// ToolIterations is how many search_writing rounds the loop ran.
	ToolIterations int
	// Citations is the number of excerpts behind the answer.
	Citations int
	// Sources is the number of distinct documents those excerpts came from.
	Sources int
	// Refused says the loop returned the refusal string.
	Refused bool
	// Elapsed is the task wall-clock time.
	Elapsed time.Duration
	// Outcome is one of the Outcome* constants.
	Outcome string
}

// Answer writes one audit line. The module logger is pinned to info (unless
// logging is disabled), so lowering --log-level does not quietly drop the
// audit trail.
func Answer(e Entry) {
	sum := sha256.Sum256([]byte(e.Question))
	log := logging.Audit()
	log.Info().
		Str("principal", e.Principal).
		Str("token_id", e.TokenID).
		Str("method", e.Method).
		Str("question_sha256", hex.EncodeToString(sum[:])).
		Int("question_bytes", len(e.Question)).
		Str("outcome", e.Outcome).
		Int("tool_iterations", e.ToolIterations).
		Int("citations", e.Citations).
		Int("sources", e.Sources).
		Bool("refused", e.Refused).
		Int64("elapsed_ms", e.Elapsed.Milliseconds()).
		Msg("task")
}
