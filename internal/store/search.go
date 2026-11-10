package store

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
	"unicode"

	vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

// SearchHit is one chunk returned by a search path: the readable chunk row plus
// whichever path-native scores produced it. Vector hits carry Distance and the
// stored Vector; text hits carry BM25. A chunk found by both paths appears once
// per path and is merged by the caller.
type SearchHit struct {
	ChunkID     string
	SourceKind  string
	SourceID    string
	SourceURL   string
	Platform    string
	Author      string
	PublishedAt time.Time
	Title       string
	Section     string
	Body        string
	// Distance is the vec0 L2 distance (lower is closer); zero on text hits.
	Distance float64
	// BM25 is the FTS5 bm25 rank (lower is better); zero on vector hits.
	BM25 float64
	// Vector is the stored embedding, returned so the caller can compute a
	// cosine similarity that does not depend on the vec0 distance metric. Nil
	// on text hits.
	Vector []float32
}

// Filter narrows both search paths to matching chunks. Zero values mean "no
// restriction"; times are RFC3339 UTC strings compared lexicographically
// against chunks.published_at, which ingest always writes in that form.
type Filter struct {
	// SourceKinds restricts chunks.source_kind (ghost_post, postiz_post,
	// web_page).
	SourceKinds []string
	// Platforms restricts chunks.platform (lowercased, Postiz only).
	Platforms []string
	// After is an inclusive lower bound on published_at.
	After string
	// Before is an exclusive upper bound on published_at.
	Before string
	// ExcludeScheduled drops Postiz chunks whose state is QUEUE.
	ExcludeScheduled bool
}

// active reports whether the filter narrows the result set at all.
func (f Filter) active() bool {
	return len(f.SourceKinds) > 0 || len(f.Platforms) > 0 ||
		f.After != "" || f.Before != "" || f.ExcludeScheduled
}

// clauses renders the filter as SQL conditions over the aliased chunks table.
func (f Filter) clauses(alias string) ([]string, []any) {
	var conds []string
	var args []any
	if len(f.SourceKinds) > 0 {
		conds = append(conds, alias+".source_kind IN ("+placeholders(len(f.SourceKinds))+")")
		for _, kind := range f.SourceKinds {
			args = append(args, kind)
		}
	}
	if len(f.Platforms) > 0 {
		conds = append(conds, "LOWER("+alias+".platform) IN ("+placeholders(len(f.Platforms))+")")
		for _, p := range f.Platforms {
			args = append(args, strings.ToLower(p))
		}
	}
	if f.After != "" {
		conds = append(conds, alias+".published_at >= ?")
		args = append(args, f.After)
	}
	if f.Before != "" {
		conds = append(conds, alias+".published_at < ?")
		args = append(args, f.Before)
	}
	if f.ExcludeScheduled {
		// COALESCE keeps rows without a state (Ghost, older Postiz meta) from
		// falling into SQL's three-valued logic and being dropped by NOT.
		conds = append(conds, "NOT ("+alias+".source_kind = 'postiz_post' AND "+
			"COALESCE(json_extract("+alias+".meta_json, '$.state'), '') = 'QUEUE')")
	}
	return conds, args
}

// placeholders returns "?,?,?" for n bound values.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// maxVectorK is sqlite-vec's practical KNN cap. A filtered vector query asks
// for the whole corpus to keep filtering exact; the plan's ≤1,500-chunk
// ceiling keeps this well inside the cap.
const maxVectorK = 4096

// vectorSearchQuery is the KNN query behind retrieval. The vec0 KNN syntax
// forbids LIMIT next to `k = ?`; k itself bounds the row count. The filters sit
// in the outer query because vec0 applies them after the KNN, which is why a
// filtered call asks for the whole corpus instead of the per-path depth.
const vectorSearchQuery = `
SELECT c.chunk_id, c.source_kind, c.source_id, COALESCE(c.source_url, ''),
       COALESCE(c.platform, ''), c.author, c.published_at,
       COALESCE(c.title, ''), COALESCE(c.section, ''), c.body,
       v.distance, v.embedding
FROM (SELECT chunk_id, distance, embedding FROM chunks_vec WHERE embedding MATCH ? AND k = ?) v
JOIN chunks c ON c.chunk_id = v.chunk_id`

// textSearchSelect is the FTS5 half of hybrid retrieval: bm25 ranks the
// matching chunks, and the same filters the vector path uses apply here too.
const textSearchSelect = `
SELECT c.chunk_id, c.source_kind, c.source_id, COALESCE(c.source_url, ''),
       COALESCE(c.platform, ''), c.author, c.published_at,
       COALESCE(c.title, ''), COALESCE(c.section, ''), c.body,
       bm25(chunks_fts)
FROM chunks_fts
JOIN chunks c ON c.chunk_id = chunks_fts.chunk_id`

// SearchVectors returns the k chunks nearest to vector, closest first. The
// vector must have exactly EmbeddingDims values; k must be positive.
//
// A non-empty filter widens k to the whole corpus before filtering: vec0
// applies outer WHERE conditions after the KNN, so asking for only k rows
// would silently drop matches that rank behind non-matching chunks.
func (s *Store) SearchVectors(vector []float32, k int, filter Filter) ([]SearchHit, error) {
	if len(vector) != EmbeddingDims {
		return nil, fmt.Errorf("vector search: query has %d dimensions, schema wants %d",
			len(vector), EmbeddingDims)
	}
	if k <= 0 {
		return nil, fmt.Errorf("vector search: k must be positive, got %d", k)
	}
	blob, err := vec.SerializeFloat32(vector)
	if err != nil {
		return nil, fmt.Errorf("vector search: serialize query: %w", err)
	}

	// limit is what the caller asked for; k is how many rows the KNN must
	// consider, which widens to the whole corpus when a filter is present.
	limit := k
	if filter.active() {
		count, err := s.countVectors()
		if err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, nil
		}
		// Ask for the whole corpus: the filter runs after the KNN, so a
		// per-depth k would silently drop matches ranked behind non-matching
		// chunks. Above the vec0 cap, refuse rather than truncate the ranked
		// set without saying so.
		if count > maxVectorK {
			return nil, fmt.Errorf("vector search: %d vectors exceed the filtered KNN cap of %d; refusing to truncate the ranked set", count, maxVectorK)
		}
		k = count
	}

	q := vectorSearchQuery
	args := []any{blob, k}
	if conds, fargs := filter.clauses("c"); len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
		args = append(args, fargs...)
	}
	// The outer LIMIT is legal (MATCH lives in the subquery) and keeps a
	// filtered search from hydrating the whole corpus.
	q += " ORDER BY v.distance, c.chunk_id LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	defer rows.Close()

	hits := make([]SearchHit, 0, limit)
	for rows.Next() {
		var h SearchHit
		var published string
		var embedding []byte
		if err := rows.Scan(&h.ChunkID, &h.SourceKind, &h.SourceID, &h.SourceURL,
			&h.Platform, &h.Author, &published, &h.Title, &h.Section, &h.Body,
			&h.Distance, &embedding); err != nil {
			return nil, fmt.Errorf("vector search: scan hit: %w", err)
		}
		if h.PublishedAt, err = parsePublished(h.ChunkID, published); err != nil {
			return nil, err
		}
		if h.Vector, err = deserializeVector(embedding); err != nil {
			return nil, fmt.Errorf("vector search: chunk %s embedding: %w", h.ChunkID, err)
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	return hits, nil
}

// SearchText returns the k chunks that best match the FTS5 query, best first.
// ftsQuery must already be a safe FTS5 expression (see FTSQuery); callers pass
// free text through that builder rather than interpolating it here.
func (s *Store) SearchText(ftsQuery string, k int, filter Filter) ([]SearchHit, error) {
	if strings.TrimSpace(ftsQuery) == "" {
		return nil, fmt.Errorf("text search: query is required")
	}
	if k <= 0 {
		return nil, fmt.Errorf("text search: k must be positive, got %d", k)
	}

	q := textSearchSelect + " WHERE chunks_fts MATCH ?"
	args := []any{ftsQuery}
	if conds, fargs := filter.clauses("c"); len(conds) > 0 {
		q += " AND " + strings.Join(conds, " AND ")
		args = append(args, fargs...)
	}
	q += " ORDER BY bm25(chunks_fts), c.chunk_id LIMIT ?"
	args = append(args, k)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("text search: %w", err)
	}
	defer rows.Close()

	hits := make([]SearchHit, 0, k)
	for rows.Next() {
		var h SearchHit
		var published string
		if err := rows.Scan(&h.ChunkID, &h.SourceKind, &h.SourceID, &h.SourceURL,
			&h.Platform, &h.Author, &published, &h.Title, &h.Section, &h.Body,
			&h.BM25); err != nil {
			return nil, fmt.Errorf("text search: scan hit: %w", err)
		}
		if h.PublishedAt, err = parsePublished(h.ChunkID, published); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("text search: %w", err)
	}
	return hits, nil
}

// FTSQuery turns free text into a safe FTS5 OR expression. Each term is
// quoted, so punctuation and FTS operators in user text cannot change the
// query's meaning or raise a syntax error. ok is false when no term survives
// (for example a single punctuation character), in which case the caller
// simply skips the text path.
func FTSQuery(text string) (query string, ok bool) {
	terms := ftsTerms(text)
	if len(terms) == 0 {
		return "", false
	}
	quoted := make([]string, len(terms))
	for i, term := range terms {
		quoted[i] = `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " OR "), true
}

// ftsTerms splits text into lowercased word tokens, dropping one-character
// noise and duplicates while keeping the caller's order.
func ftsTerms(text string) []string {
	const maxTerms = 32
	seen := make(map[string]bool)
	terms := make([]string, 0, 8)
	var cur strings.Builder
	flush := func() {
		term := strings.Trim(cur.String(), "'")
		cur.Reset()
		if len([]rune(term)) < 2 {
			return
		}
		key := strings.ToLower(term)
		if seen[key] || len(terms) == maxTerms {
			return
		}
		seen[key] = true
		terms = append(terms, key)
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return terms
}

// countVectors reports how many vectors the KNN can cover, so a filtered
// search can ask for all of them.
func (s *Store) countVectors() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM chunks_vec`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count vectors: %w", err)
	}
	return n, nil
}

// parsePublished parses the RFC3339 stamp ingest writes, naming the chunk when
// a hand-edited index carries something else.
func parsePublished(chunkID, published string) (time.Time, error) {
	ts, err := time.Parse(time.RFC3339, published)
	if err != nil {
		return time.Time{}, fmt.Errorf("chunk %s published_at %q: %w", chunkID, published, err)
	}
	return ts, nil
}

// EmbeddingTags lists the distinct embedded_with tags stamped on chunks - the
// provenance record for the vectors. A server compares them with the tag of its
// configured model: comparing a query vector against vectors from another model
// produces plausible-looking noise, so a mismatch is a startup error, not a
// warning.
func (s *Store) EmbeddingTags() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT embedded_with FROM chunks
WHERE embedded_with IS NOT NULL AND embedded_with <> ''
ORDER BY embedded_with`)
	if err != nil {
		return nil, fmt.Errorf("list embedding tags: %w", err)
	}
	defer rows.Close()

	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, fmt.Errorf("list embedding tags: %w", err)
		}
		tags = append(tags, tag)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list embedding tags: %w", err)
	}
	return tags, nil
}

// deserializeVector decodes the little-endian float32 blob sqlite-vec stores.
// The bindings only expose serialization, so the inverse lives here.
func deserializeVector(blob []byte) ([]float32, error) {
	if len(blob)%4 != 0 {
		return nil, fmt.Errorf("%d bytes is not a whole number of float32 values", len(blob))
	}
	out := make([]float32, len(blob)/4)
	if err := binary.Read(bytes.NewReader(blob), binary.LittleEndian, out); err != nil {
		return nil, err
	}
	return out, nil
}
