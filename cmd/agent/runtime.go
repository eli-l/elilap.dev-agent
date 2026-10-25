package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/eli-l/elilap.dev-agent/internal/embed"
)

// wordPattern matches the words of a free-text query. It is the same
// notion of a word the chunker uses, so the terms of a query and the terms
// of a chunk are the same kind of thing.
var wordPattern = regexp.MustCompile(`[\p{L}\p{N}_]+`)

// indexPath returns the path of the SQLite index: the --db flag when it is
// given, else AGENT_DB_PATH, else ./index.db.
func indexPath(flagValue string) string {
	if trimmed := strings.TrimSpace(flagValue); trimmed != "" {
		return trimmed
	}
	return envOr("AGENT_DB_PATH", defaultDBPath)
}

// newEmbedClient builds the embedding client from the environment.
//
// The base URL, the model and the key are the three variables the model
// server contract defines (docs/localai.md). The key is only ever sent as
// an Authorization header: it is never printed and never logged.
func newEmbedClient() (*embed.Client, error) {
	base := envOr("LOCALAI_BASE_URL", defaultModelBaseURL)
	model := envOr("EMBED_MODEL", defaultEmbedModel)
	key := strings.TrimSpace(os.Getenv("LOCALAI_API_KEY"))
	return embed.New(base, key, model)
}

// chunkID is the identity of a chunk: sha256(source_kind|source_id|chunk_seq)
// in hex. The ingest pipeline owns chunk identity, so the store never
// guesses it.
func chunkID(sourceKind, sourceID string, seq int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", sourceKind, sourceID, seq)))
	return hex.EncodeToString(sum[:])
}

// contentHash is the hash of a chunk's text. It is what the ingest
// pipeline compares to decide whether an item changed since the last run.
func contentHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// ftsQuery turns free text into an FTS5 query expression.
//
// The words of the query are quoted and joined with OR. Two reasons: a
// question like "how does the agent index markdown" must not require every
// one of its words to sit in the same chunk, and the raw text must not be
// able to smuggle FTS5 operators into the query.
//
// An empty result means the query holds no word at all; the caller skips
// the full-text search then.
func ftsQuery(text string) string {
	words := wordPattern.FindAllString(text, -1)
	if len(words) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(words))
	for _, word := range words {
		quoted = append(quoted, `"`+strings.ReplaceAll(word, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " OR ")
}
