// Package audit emits the read-only task record the operator keeps: who asked
// (token id), what was asked (the peer's question), and how the loop ended
// (answered/refused/timeout/error, iterations, citation count, elapsed).
//
// It deliberately excludes the answer text, prompts, and retrieved chunk
// bodies, so the audit stream can stay on at a level that would not be safe
// for the debug RAG trace (docs/logging.md, docs/a2a.md).
package audit

import (
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

// Outcome is the audit record's terminal state.
const (
	OutcomeAnswered = "answered"
	OutcomeRefused  = "refused"
	OutcomeTimeout  = "timeout"
	OutcomeError    = "error"
)

// Entry is one task record.
type Entry struct {
	// TokenID is the authenticated token id, or empty for the token-less
	// loopback dev surface.
	TokenID string
	// Method is the surface that handled the task, e.g. "a2a/message/send".
	Method string
	// Question is the peer's question. It is the only peer text recorded.
	Question string
	// ToolIterations is how many search_writing rounds the loop ran.
	ToolIterations int
	// Citations is the number of resolved citations.
	Citations int
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
	log := logging.Audit()
	log.Info().
		Str("token_id", e.TokenID).
		Str("method", e.Method).
		Str("question", e.Question).
		Str("outcome", e.Outcome).
		Int("tool_iterations", e.ToolIterations).
		Int("citations", e.Citations).
		Bool("refused", e.Refused).
		Int64("elapsed_ms", e.Elapsed.Milliseconds()).
		Msg("task")
}
