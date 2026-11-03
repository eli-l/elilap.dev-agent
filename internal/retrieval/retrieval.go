// Package retrieval is the engine behind the `search_writing` tool: it embeds
// free-text queries, runs FTS5 BM25 and vec0 KNN in parallel, merges the two
// ranked lists with reciprocal-rank fusion, and applies the source, platform,
// date, and recency filters shared by the CLI and the HTTP surface.
package retrieval

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// Bounds on the result set. The default keeps a tool result small enough for a
// grounded prompt; the maximum keeps a client from asking SQLite to scan the
// whole corpus in one request.
const (
	// DefaultTopN is the result count when the caller does not ask for one.
	DefaultTopN = 5
	// MaxTopN is the largest accepted top_n.
	MaxTopN = 50
)

// Merge policy pinned in PLAN.md Phase 2.
const (
	// rrfK is the reciprocal-rank-fusion constant: contribution is
	// 1/(rrfK + rank) per path.
	rrfK = 60.0
	// Candidate lists are fetched at this multiple of top_n, bounded so a
	// small top_n still sees enough neighbours and a large one stays cheap.
	candidateFactor = 4
	minDepth        = 20
	maxDepth        = 200
)

// ErrInvalidQuery marks caller mistakes (empty text, bad filters, out-of-range
// top_n), which HTTP surfaces map to 400 and the CLI to a usage-style error.
var ErrInvalidQuery = errors.New("invalid retrieval query")

// Embedder is the one method retrieval needs; *llm.Client implements it.
type Embedder interface {
	Embed(ctx context.Context, model string, inputs []string) ([][]float32, error)
}

// Searcher ranks indexed chunks against a free-text query.
type Searcher struct {
	// Store is the read-only index handle.
	Store *store.Store
	// Embedder vectorizes the query with the same model the corpus was
	// embedded with.
	Embedder Embedder
	// Model is the embeddings model id (EMBED_MODEL). It must match the tag
	// the chunks were embedded under, or the query vector lives in a different
	// space than chunks_vec.
	Model string
	// Now is overridable in tests for recency weighting; nil means time.Now.
	Now func() time.Time
}

// Query is one retrieval request. The fields mirror the `search_writing` tool
// parameters, plus VectorOnly for the CLI's `--hybrid=false` debug mode.
type Query struct {
	// Text is the free-form query, for example a portion of a sentence.
	Text string
	// TopN is the maximum number of hits; zero means DefaultTopN.
	TopN int
	// Source is "all", "ghost", or "postiz"; empty means all.
	Source string
	// Platforms restricts Postiz cross-posts (for example "linkedin").
	Platforms []string
	// After is an inclusive lower bound: RFC3339 or YYYY-MM-DD.
	After string
	// Before is an exclusive upper bound; a date-only value covers that day.
	Before string
	// RecencyHalflifeDays multiplies the merged score by
	// 0.5^(age_days/halflife). Zero disables recency weighting.
	RecencyHalflifeDays float64
	// ExcludeScheduled drops Postiz chunks in the QUEUE state.
	ExcludeScheduled bool
	// VectorOnly skips the FTS5 path (CLI `--hybrid=false`). The HTTP surface
	// and the tool always run hybrid.
	VectorOnly bool
}

// Hit is one ranked chunk, shaped for JSON: the metadata the model needs to
// cite the excerpt plus the excerpt itself.
type Hit struct {
	ChunkID     string `json:"chunk_id"`
	SourceKind  string `json:"source_kind"`
	SourceID    string `json:"source_id"`
	SourceURL   string `json:"source_url,omitempty"`
	Platform    string `json:"platform,omitempty"`
	Author      string `json:"author"`
	PublishedAt string `json:"published_at"`
	Title       string `json:"title,omitempty"`
	Section     string `json:"section,omitempty"`
	Body        string `json:"body"`
	// Score is the ranking score: an RRF value in hybrid mode, cosine
	// similarity in vector-only mode. Higher is better in both.
	Score float64 `json:"score"`
	// Distance, BM25, and Cosine are the path-native scores a chunk earned;
	// each is present only when the chunk came back from that path.
	Distance *float64 `json:"distance,omitempty"`
	BM25     *float64 `json:"bm25,omitempty"`
	Cosine   *float64 `json:"cosine,omitempty"`
}

// Search runs the query and returns up to q.TopN ranked chunks, best first.
// It never falls back to an unranked scan: an embedding failure is an error.
func (s *Searcher) Search(ctx context.Context, q Query) ([]Hit, error) {
	if s == nil || s.Store == nil {
		return nil, errors.New("retrieval: store is required")
	}
	if s.Embedder == nil {
		return nil, errors.New("retrieval: embedder is required")
	}
	if strings.TrimSpace(s.Model) == "" {
		return nil, errors.New("retrieval: embeddings model is required")
	}

	text := strings.TrimSpace(q.Text)
	if text == "" {
		return nil, fmt.Errorf("%w: query text must not be empty", ErrInvalidQuery)
	}
	topN, err := topNValue(q.TopN)
	if err != nil {
		return nil, err
	}
	filter, err := buildFilter(q)
	if err != nil {
		return nil, err
	}
	if math.IsNaN(q.RecencyHalflifeDays) || q.RecencyHalflifeDays < 0 {
		return nil, fmt.Errorf("%w: recency_halflife_days must not be negative", ErrInvalidQuery)
	}
	depth := candidateDepth(topN)

	// The debug stream is the retrieval half of the RAG trace: the normalized
	// query and filters, how many rows each path contributed, and every ranked
	// hit with its path-native scores. It only exists at debug level, so a
	// default run logs no query text or bodies (docs/logging.md).
	log := logging.DB()
	log.Debug().
		Str("query", text).
		Int("top_n", topN).
		Int("candidate_depth", depth).
		Str("embed_model", s.Model).
		Strs("source_kinds", filter.SourceKinds).
		Strs("platforms", filter.Platforms).
		Str("after", filter.After).
		Str("before", filter.Before).
		Bool("exclude_scheduled", filter.ExcludeScheduled).
		Float64("recency_halflife_days", q.RecencyHalflifeDays).
		Bool("vector_only", q.VectorOnly).
		Msg("retrieval: query")
	queryStart := time.Now()

	vectors, err := s.Embedder.Embed(ctx, s.Model, []string{text})
	if err != nil {
		return nil, fmt.Errorf("retrieval: embed query: %w", err)
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("retrieval: embeddings returned %d vectors for one query", len(vectors))
	}
	if len(vectors[0]) != store.EmbeddingDims {
		return nil, fmt.Errorf("retrieval: query vector has %d dimensions, schema wants %d",
			len(vectors[0]), store.EmbeddingDims)
	}

	vectorHits, err := s.Store.SearchVectors(vectors[0], depth, filter)
	if err != nil {
		return nil, fmt.Errorf("retrieval: %w", err)
	}

	var (
		textHits []store.SearchHit
		ftsQuery string
		ftsUsed  bool
	)
	if !q.VectorOnly {
		if ftsQuery, ftsUsed = store.FTSQuery(text); ftsUsed {
			textHits, err = s.Store.SearchText(ftsQuery, depth, filter)
			if err != nil {
				return nil, fmt.Errorf("retrieval: %w", err)
			}
		}
	}
	log.Debug().
		Bool("fts_used", ftsUsed).
		Str("fts_query", ftsQuery).
		Int("vector_candidates", len(vectorHits)).
		Int("text_candidates", len(textHits)).
		Msg("retrieval: candidates")

	hits := s.merge(q, vectors[0], vectorHits, textHits, topN)
	for i, h := range hits {
		ev := log.Debug().
			Int("rank", i+1).
			Str("chunk_id", h.ChunkID).
			Str("source_kind", h.SourceKind).
			Str("title", h.Title).
			Float64("score", h.Score).
			Int("body_words", len(strings.Fields(h.Body))).
			Str("preview", preview(h.Body))
		if h.Platform != "" {
			ev = ev.Str("platform", h.Platform)
		}
		if h.Distance != nil {
			ev = ev.Float64("distance", *h.Distance)
		}
		if h.BM25 != nil {
			ev = ev.Float64("bm25", *h.BM25)
		}
		if h.Cosine != nil {
			ev = ev.Float64("cosine", *h.Cosine)
		}
		ev.Msg("retrieval: hit")
	}
	log.Debug().
		Int("results", len(hits)).
		Dur("duration", time.Since(queryStart)).
		Msg("retrieval: done")
	return hits, nil
}

// preview is a bounded, single-line glimpse of an excerpt for debug lines: the
// full body belongs to the agent module's tool-result line, not to every hit.
func preview(body string) string {
	const limit = 200
	line := strings.Join(strings.Fields(body), " ")
	runes := []rune(line)
	if len(runes) <= limit {
		return line
	}
	return string(runes[:limit]) + "…"
}

// merge fuses the two ranked lists, applies the recency multiplier, and
// projects the result into the JSON hit shape.
func (s *Searcher) merge(q Query, queryVector []float32,
	vectorHits, textHits []store.SearchHit, topN int) []Hit {

	type accumulated struct {
		hit      store.SearchHit
		score    float64
		distance *float64
		bm25     *float64
		cosine   *float64
	}

	order := make([]string, 0, len(vectorHits)+len(textHits))
	merged := make(map[string]*accumulated, len(vectorHits)+len(textHits))
	get := func(h store.SearchHit) *accumulated {
		acc, ok := merged[h.ChunkID]
		if !ok {
			acc = &accumulated{hit: h}
			merged[h.ChunkID] = acc
			order = append(order, h.ChunkID)
		}
		return acc
	}

	for i, h := range vectorHits {
		acc := get(h)
		distance := h.Distance
		cosine := cosine(queryVector, h.Vector)
		acc.distance = &distance
		acc.cosine = &cosine
		if !q.VectorOnly {
			acc.score += rrfContribution(i + 1)
		}
	}
	for i, h := range textHits {
		acc := get(h)
		bm25 := h.BM25
		acc.bm25 = &bm25
		acc.score += rrfContribution(i + 1)
	}

	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	halflife := q.RecencyHalflifeDays

	hits := make([]Hit, 0, len(order))
	for _, id := range order {
		acc := merged[id]
		// In vector-only mode the score is the cosine similarity, the number
		// the single path actually produced; hybrid mode keeps the RRF sum.
		score := acc.score
		if q.VectorOnly && acc.cosine != nil {
			score = *acc.cosine
		}
		if halflife > 0 {
			ageDays := now().Sub(acc.hit.PublishedAt).Hours() / 24
			if ageDays < 0 {
				ageDays = 0
			}
			score *= math.Pow(0.5, ageDays/halflife)
		}
		hits = append(hits, Hit{
			ChunkID:     acc.hit.ChunkID,
			SourceKind:  acc.hit.SourceKind,
			SourceID:    acc.hit.SourceID,
			SourceURL:   acc.hit.SourceURL,
			Platform:    acc.hit.Platform,
			Author:      acc.hit.Author,
			PublishedAt: acc.hit.PublishedAt.UTC().Format(time.RFC3339),
			Title:       acc.hit.Title,
			Section:     acc.hit.Section,
			Body:        acc.hit.Body,
			Score:       score,
			Distance:    acc.distance,
			BM25:        acc.bm25,
			Cosine:      acc.cosine,
		})
	}

	// Deterministic order: score first, chunk_id as the tie-break, so repeated
	// runs and the CLI/HTTP pair cannot disagree on equal scores.
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ChunkID < hits[j].ChunkID
	})
	if len(hits) > topN {
		hits = hits[:topN]
	}
	return hits
}

// rrfContribution is one path's reciprocal-rank-fusion term for a 1-based rank.
func rrfContribution(rank int) float64 {
	return 1 / (rrfK + float64(rank))
}

// candidateDepth is how many rows each path contributes before the merge.
func candidateDepth(topN int) int {
	depth := topN * candidateFactor
	if depth < minDepth {
		depth = minDepth
	}
	if depth > maxDepth {
		depth = maxDepth
	}
	return depth
}

// topNValue applies the default and bounds to the requested result count.
func topNValue(topN int) (int, error) {
	switch {
	case topN == 0:
		return DefaultTopN, nil
	case topN < 0:
		return 0, fmt.Errorf("%w: top_n must not be negative", ErrInvalidQuery)
	case topN > MaxTopN:
		return 0, fmt.Errorf("%w: top_n %d exceeds the maximum of %d", ErrInvalidQuery, topN, MaxTopN)
	}
	return topN, nil
}

// buildFilter validates the filter fields and turns them into the store's
// bound-value form. Date-only inputs cover a whole day: `after` starts at
// 00:00Z and `before` advances one day so the named day is included.
func buildFilter(q Query) (store.Filter, error) {
	filter := store.Filter{ExcludeScheduled: q.ExcludeScheduled}

	switch source := strings.ToLower(strings.TrimSpace(q.Source)); source {
	case "", "all":
	case "ghost", "ghost_post":
		filter.SourceKinds = []string{"ghost_post"}
	case "postiz", "postiz_post":
		filter.SourceKinds = []string{"postiz_post"}
	default:
		return filter, fmt.Errorf("%w: source must be ghost, postiz, or all, got %q",
			ErrInvalidQuery, q.Source)
	}

	for _, p := range q.Platforms {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			filter.Platforms = append(filter.Platforms, p)
		}
	}

	if strings.TrimSpace(q.After) != "" {
		after, err := parseBound(q.After, false)
		if err != nil {
			return filter, fmt.Errorf("%w: after: %v", ErrInvalidQuery, err)
		}
		filter.After = after.Format(time.RFC3339)
	}
	var before time.Time
	if strings.TrimSpace(q.Before) != "" {
		var err error
		if before, err = parseBound(q.Before, true); err != nil {
			return filter, fmt.Errorf("%w: before: %v", ErrInvalidQuery, err)
		}
		filter.Before = before.Format(time.RFC3339)
	}
	if filter.After != "" && filter.Before != "" {
		after, _ := time.Parse(time.RFC3339, filter.After)
		if !after.Before(before) {
			return filter, fmt.Errorf("%w: after must be earlier than before", ErrInvalidQuery)
		}
	}
	return filter, nil
}

// parseBound accepts RFC3339 or a plain date. dateOnlyBounds controls what a
// date means: a lower bound starts the day, an upper bound ends after it.
func parseBound(value string, upper bool) (time.Time, error) {
	value = strings.TrimSpace(value)
	if ts, err := time.Parse(time.RFC3339, value); err == nil {
		return ts.UTC(), nil
	}
	day, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, fmt.Errorf("want RFC3339 or YYYY-MM-DD, got %q", value)
	}
	if upper {
		day = day.AddDate(0, 0, 1)
	}
	return day.UTC(), nil
}

// cosine is the cosine similarity of a and b. A zero-norm vector (never
// produced by the configured model, but possible in a damaged index) scores 0
// instead of dividing by zero.
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		av, bv := float64(a[i]), float64(b[i])
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
