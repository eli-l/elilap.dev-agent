package audit_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/audit"
	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

// TestAnswerLogsFingerprintAndNeverTheQuestion pins the audit contract: the
// record carries a SHA-256 fingerprint and byte length, never the peer text,
// the answer, or an excerpt.
func TestAnswerLogsFingerprintAndNeverTheQuestion(t *testing.T) {
	var buf bytes.Buffer
	if err := logging.Setup(logging.Config{Level: "info", Format: "json"}, &buf); err != nil {
		t.Fatalf("setup logging: %v", err)
	}
	t.Cleanup(logging.Reset)

	const question = "What did Eli write about the harness?"
	audit.Answer(audit.Entry{
		TokenID:        "alice",
		Method:         "a2a/message/send",
		Question:       question,
		ToolIterations: 2,
		Citations:      1,
		Refused:        false,
		Elapsed:        1500 * time.Millisecond,
		Outcome:        audit.OutcomeAnswered,
	})

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("decode audit line: %v (%s)", err, buf.String())
	}
	if rec["module"] != "audit" || rec["token_id"] != "alice" || rec["outcome"] != "answered" {
		t.Errorf("record = %v", rec)
	}
	if got, ok := rec["question_sha256"].(string); !ok || len(got) != 64 {
		t.Errorf("question_sha256 = %v, want a 64-char hex digest", rec["question_sha256"])
	}
	if rec["question_bytes"] != float64(len(question)) {
		t.Errorf("question_bytes = %v, want %d", rec["question_bytes"], len(question))
	}
	for _, forbidden := range []string{"question", "answer", "prompt", "body"} {
		if _, ok := rec[forbidden]; ok {
			t.Errorf("audit record carries forbidden field %q: %v", forbidden, rec)
		}
	}
	if strings.Contains(buf.String(), question) {
		t.Errorf("audit line leaks the question: %s", buf.String())
	}
	for _, outcome := range []string{audit.OutcomeAnswered, audit.OutcomeRefused, audit.OutcomeTimeout, audit.OutcomeCanceled, audit.OutcomeError} {
		if outcome == "" {
			t.Error("empty outcome constant")
		}
	}
}
