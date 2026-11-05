package agentloop

import (
	"fmt"
	"strings"
)

// SearchMode selects how a question becomes retrievals. The two paths share the
// prompt rules, the budgets, and the citation-or-refusal finalizer; they differ
// only in who decides the search queries.
type SearchMode string

const (
	// ModeLoop is the shipped path: the model calls the search_writing tool
	// itself, extracts the keywords, may search again, and answers from the
	// tool results. Adaptive, but it depends on the model emitting tool calls
	// reliably - which the small 1.7b models do not, especially at the cheap
	// reasoning effort (see ReasoningTransform).
	ModeLoop SearchMode = "loop"
	// ModeFanout is the deterministic alternative under evaluation: one chat
	// call writes rephrasings of the question, the service runs retrieval for
	// the original plus every variant itself, fuses the result sets, and hands
	// the merged excerpts to one answer call. No tool-call dependency, and the
	// excerpt payload enters the context once instead of per tool round.
	ModeFanout SearchMode = "fanout"
)

// ParseSearchMode resolves a configured value. Empty means ModeLoop, so an
// unset variable and the documented default agree.
func ParseSearchMode(value string) (SearchMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(ModeLoop):
		return ModeLoop, nil
	case string(ModeFanout):
		return ModeFanout, nil
	default:
		return "", fmt.Errorf("agentloop: unknown search mode %q (want %q or %q)",
			value, ModeLoop, ModeFanout)
	}
}

// mode is the effective answer path: an unset Mode is the shipped one.
func (r *Runner) mode() SearchMode {
	if r.Mode == "" {
		return ModeLoop
	}
	return r.Mode
}
