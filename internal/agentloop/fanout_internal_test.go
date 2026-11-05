package agentloop

import (
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/retrieval"
)

func queriesOf(variants []variant) []string {
	out := make([]string, 0, len(variants))
	for _, v := range variants {
		out = append(out, v.Query)
	}
	return out
}

func kindsOf(variants []variant) []string {
	out := make([]string, 0, len(variants))
	for _, v := range variants {
		out = append(out, v.Kind)
	}
	return out
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestParseVariantsReadsTheLabelledShape(t *testing.T) {
	content := `{"exact_terms":"sqlite fts5 build tag","corpus_vocabulary":"full text search sqlite tag","narrower":"fts5 cgo"}`
	got := parseVariants(content, "what about fts5?", DefaultVariantCount)
	if want := []string{VariantExactTerms, VariantVocabulary, VariantNarrower}; !equalStrings(kindsOf(got), want) {
		t.Fatalf("kinds = %v, want %v (the labelled shape must keep its order)", kindsOf(got), want)
	}
	if want := []string{"sqlite fts5 build tag", "full text search sqlite tag", "fts5 cgo"}; !equalStrings(queriesOf(got), want) {
		t.Fatalf("queries = %v, want %v", queriesOf(got), want)
	}
}

func TestParseVariantsSkipsMissingLabels(t *testing.T) {
	got := parseVariants(`{"exact_terms":"a b","narrower":"c d"}`, "q", DefaultVariantCount)
	if want := []string{VariantExactTerms, VariantNarrower}; !equalStrings(kindsOf(got), want) {
		t.Fatalf("kinds = %v, want %v", kindsOf(got), want)
	}
}

func TestParseVariantsToleratesLegacyShapes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		kinds   []string
		queries []string
	}{
		{
			name:    "array of strings inside an object",
			content: "```json\n{\"variants\": [\"one two\", \"three four\"]}\n```",
			kinds:   []string{"", ""},
			queries: []string{"one two", "three four"},
		},
		{
			name:    "array of kind/query objects",
			content: `{"variants":[{"kind":"exact_terms","query":"a b"},{"kind":"narrower","query":"c d"}]}`,
			kinds:   []string{VariantExactTerms, VariantNarrower},
			queries: []string{"a b", "c d"},
		},
		{
			name:    "bare array",
			content: `["one two","three four"]`,
			kinds:   []string{"", ""},
			queries: []string{"one two", "three four"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseVariants(tc.content, "q", DefaultVariantCount)
			if !equalStrings(kindsOf(got), tc.kinds) || !equalStrings(queriesOf(got), tc.queries) {
				t.Fatalf("parseVariants = %v, want kinds %v queries %v", got, tc.kinds, tc.queries)
			}
		})
	}
}

func TestParseVariantsDropsUnusableEntries(t *testing.T) {
	long := strings.Repeat("x", maxVariantRunes+1)
	content := `{"exact_terms":"  ","corpus_vocabulary":"what about fts5?","narrower":"` + long + `"}`
	if got := parseVariants(content, "what about fts5?", DefaultVariantCount); len(got) != 0 {
		t.Fatalf("parseVariants = %v, want nothing usable", got)
	}

	content = `{"variants":["","a b","A B","keep me"]}`
	got := parseVariants(content, "q", DefaultVariantCount)
	if want := []string{"a b", "keep me"}; !equalStrings(queriesOf(got), want) {
		t.Fatalf("queries = %v, want %v", queriesOf(got), want)
	}
}

func TestParseVariantsNotJSON(t *testing.T) {
	if got := parseVariants("I cannot help with that.", "q", DefaultVariantCount); len(got) != 0 {
		t.Fatalf("parseVariants = %v, want nothing", got)
	}
}

func TestParseVariantsRespectsLimit(t *testing.T) {
	content := `{"exact_terms":"a","corpus_vocabulary":"b","narrower":"c"}`
	if got := parseVariants(content, "q", 2); len(got) != 2 {
		t.Fatalf("parseVariants = %v, want the first 2", got)
	}
}

func TestFanoutQueriesPutsTheQuestionFirstAndDedupes(t *testing.T) {
	got := fanoutQueries("What about FTS5?", []variant{
		{Kind: VariantExactTerms, Query: "fts5 build tag"},
		{Kind: VariantVocabulary, Query: "What about FTS5?"},
		{Kind: VariantNarrower, Query: "FTS5 BUILD TAG"},
	})
	want := []string{"What about FTS5?", "fts5 build tag"}
	if !equalStrings(got, want) {
		t.Fatalf("fanoutQueries = %v, want %v", got, want)
	}
}

func TestMergeFanoutSumsScoresAndDiversifiesSources(t *testing.T) {
	h := func(id, url string, score float64) retrieval.Hit {
		return retrieval.Hit{ChunkID: id, SourceURL: url, Score: score}
	}
	// idB is found by two queries at a low rank, idA once at a high rank: the
	// reciprocal-rank sums make idB the consensus winner.
	groups := [][]retrieval.Hit{
		{h("b", "u1", 0.030), h("a", "u2", 0.040)},
		{h("b", "u1", 0.030), h("c", "u2", 0.001)},
		{h("d", "u2", 0.0005)},
	}
	got := mergeFanout(groups)
	if len(got) != 3 {
		t.Fatalf("merged %d hits, want 3 (idA, idB, and one more)", len(got))
	}
	if got[0].ChunkID != "b" || got[0].Score < 0.059 {
		t.Errorf("first = %s (%.3f), want the consensus hit b with summed score", got[0].ChunkID, got[0].Score)
	}
	// u2 supplied a, c, and d; only two of them may survive.
	u2 := 0
	for _, hit := range got {
		if hit.SourceURL == "u2" {
			u2++
		}
	}
	if u2 > maxChunksPerSource {
		t.Errorf("%d chunks from one source survived, want at most %d", u2, maxChunksPerSource)
	}
}

func TestMergeFanoutIsDeterministic(t *testing.T) {
	h := func(id string, score float64) retrieval.Hit {
		return retrieval.Hit{ChunkID: id, Score: score}
	}
	first := mergeFanout([][]retrieval.Hit{{h("b", 0.01), h("a", 0.01)}})
	second := mergeFanout([][]retrieval.Hit{{h("a", 0.01), h("b", 0.01)}})
	if len(first) != 2 || first[0].ChunkID != "a" || second[0].ChunkID != "a" {
		t.Errorf("equal scores are not ordered by chunk id: %v vs %v", first, second)
	}
}
