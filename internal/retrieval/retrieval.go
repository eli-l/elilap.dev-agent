// Package retrieval fuses the two ways this corpus is searched — lexical
// (SQLite FTS5 BM25) and semantic (sqlite-vec cosine) — into one ranked
// list.
//
// The two sides are reached through the small interfaces below rather than
// through the store package directly, so the retriever has no dependency on
// how the index is built and can be tested with plain fakes.
package retrieval

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// rrfK is the Reciprocal Rank Fusion constant. 60 is the standard value:
// it damps the head of each list enough that one searcher's opinion of its
// own top hit cannot dominate the fusion.
const rrfK = 60

// BM25Searcher returns the lexical hits for a query, best first.
type BM25Searcher interface {
	SearchBM25(ctx context.Context, query string, limit int) ([]Hit, error)
}

// VectorSearcher returns the semantic hits for a query vector, best first.
type VectorSearcher interface {
	SearchVector(ctx context.Context, q []float32, limit int) ([]Hit, error)
}

// Hit is one retrieved chunk with the metadata a caller needs to quote it.
type Hit struct {
	// ChunkID identifies the chunk in the index.
	ChunkID string
	// Title is the source document's title, empty when untitled.
	Title string
	// SourceURL is the canonical URL of the source document.
	SourceURL string
	// PublishedAt is the publication timestamp in ISO-8601 UTC.
	PublishedAt string
	// Score is the fused RRF score on the returned hits. It is a rank
	// score, not a similarity: it is not comparable across queries.
	Score float64
}

// Retrieve runs both searches and returns the best hits, fused, up to limit.
//
// Both sides are optional individually: a nil searcher contributes nothing,
// and a side is skipped when it has nothing to work with (an empty text
// query means no lexical search, an empty vector means no semantic one).
// Asking for neither is an error — there is nothing to retrieve.
//
// Why Reciprocal Rank Fusion and not a weighted sum of scores: BM25 scores
// are negative and unbounded, cosine similarity lives in 0..1, and the two
// scales have nothing to do with each other. RRF fuses the *rank* of a hit
// in each list (1/(60+rank)) and never looks at the raw scores, so the two
// scales never have to be reconciled and no score normalization is needed.
//
// Ties are broken by the order a hit was first seen — lexical first, then
// semantic — so the same query always returns the same order.
func Retrieve(ctx context.Context, bm25 BM25Searcher, vec VectorSearcher, q []float32, textQuery string, limit int) ([]Hit, error) {
	textQuery = strings.TrimSpace(textQuery)

	if len(q) == 0 && textQuery == "" {
		return nil, errors.New("retrieval: nothing to search for: the query text and the query vector are both empty")
	}
	if bm25 == nil && vec == nil {
		return nil, errors.New("retrieval: no searcher configured: the BM25 and the vector searcher are both nil")
	}
	if limit <= 0 {
		return []Hit{}, nil
	}

	var lexical, semantic []Hit
	if bm25 != nil && textQuery != "" {
		hits, err := bm25.SearchBM25(ctx, textQuery, limit)
		if err != nil {
			return nil, fmt.Errorf("retrieval: BM25 search failed: %w", err)
		}
		lexical = hits
	}
	if vec != nil && len(q) > 0 {
		hits, err := vec.SearchVector(ctx, q, limit)
		if err != nil {
			return nil, fmt.Errorf("retrieval: vector search failed: %w", err)
		}
		semantic = hits
	}

	return fuse(lexical, semantic, limit)
}

// fuse merges two ranked lists with RRF and returns the best limit hits.
//
// Each list contributes 1/(rrfK+rank) per hit, with rank starting at 1, so
// a hit that both lists like beats a hit that only one list loves. A hit
// that appears twice in the same list counts once, at its better rank.
func fuse(lexical, semantic []Hit, limit int) ([]Hit, error) {
	type scored struct {
		hit   Hit
		score float64
	}

	entries := make([]scored, 0, len(lexical)+len(semantic))
	index := make(map[string]int, len(lexical)+len(semantic))

	add := func(side string, list []Hit) error {
		seen := make(map[string]bool, len(list))
		for i, hit := range list {
			if hit.ChunkID == "" {
				return fmt.Errorf("retrieval: %s searcher returned a hit with an empty chunk id", side)
			}
			if seen[hit.ChunkID] {
				continue
			}
			seen[hit.ChunkID] = true

			contribution := 1.0 / float64(rrfK+i+1)
			if pos, ok := index[hit.ChunkID]; ok {
				entries[pos].score += contribution
				continue
			}
			index[hit.ChunkID] = len(entries)
			entries = append(entries, scored{hit: hit, score: contribution})
		}
		return nil
	}

	if err := add("BM25", lexical); err != nil {
		return nil, err
	}
	if err := add("vector", semantic); err != nil {
		return nil, err
	}

	// A stable sort keeps the first-seen order for equal scores.
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].score > entries[j].score
	})

	out := make([]Hit, 0, min(limit, len(entries)))
	for i, entry := range entries {
		if i >= limit {
			break
		}
		hit := entry.hit
		hit.Score = entry.score
		out = append(out, hit)
	}
	return out, nil
}
