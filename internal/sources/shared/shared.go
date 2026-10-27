// Package shared holds the pieces every provider reuses: the token-estimate
// chunker, sha256-based chunk ids and content hashes, and the run-id
// convention. Provider clients turn Documents into store.Chunk values with
// these helpers so id and hash rules live in exactly one place.
package shared

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// avgCharsPerToken is the coarse prose heuristic used to size chunks; PLAN.md
// section 3 sizes chunks in tokens but the chunker works on characters.
const avgCharsPerToken = 4

// EstimateTokens estimates the token count of s at roughly 4 characters per
// token. It is deliberately cheap: exact tokenization is the embedder's job.
func EstimateTokens(s string) int {
	return (len(s) + avgCharsPerToken - 1) / avgCharsPerToken
}

// ChunkID derives the stable chunk primary key: sha256(source_kind|source_id|
// chunk_seq), hex. seq is the chunk's ordinal within the item across all its
// chunks, starting at 0.
func ChunkID(sourceKind, sourceID string, seq int) string {
	h := sha256.Sum256([]byte(sourceKind + "|" + sourceID + "|" + fmt.Sprint(seq)))
	return hex.EncodeToString(h[:])
}

// ContentHash hashes normalized chunk text: whitespace runs collapse to one
// space and surrounding space disappears, so reflowed text keeps its hash.
func ContentHash(body string) string {
	norm := strings.Join(strings.Fields(body), " ")
	h := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(h[:])
}

// NewRunID derives the per-run ledger stamp: sha256(source_id|unix-seconds),
// hex. It only needs to distinguish concurrent runs of one source.
func NewRunID(sourceID string, at time.Time) string {
	h := sha256.Sum256([]byte(sourceID + "|" + fmt.Sprint(at.UTC().Unix())))
	return hex.EncodeToString(h[:16])
}
