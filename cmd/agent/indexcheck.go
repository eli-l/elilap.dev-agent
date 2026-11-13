package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/eli-l/elilap.dev-agent/internal/embed"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// verifyIndexModel checks that the index is usable by the configured models:
// its vectors were produced by that embedding model and text template, and
// they have the configured width. A query vector only means something in the
// space its model produced, and the embedded_with stamp is the index's own
// record of that space, so a mismatch fails before any request is served
// instead of returning plausible-looking noise.
func verifyIndexModel(st *store.Store, model string, dims int) error {
	if err := st.CheckDims(dims); err != nil {
		return err
	}
	tag := embed.Tag(model)
	tags, err := st.EmbeddingTags()
	if err != nil {
		return err
	}
	if len(tags) == 0 {
		return errors.New("index has no embeddings; run `agent embed` first")
	}
	if len(tags) != 1 || tags[0] != tag {
		return fmt.Errorf("index vectors are stamped %s, but EMBED_MODEL=%s expects %s; run `agent embed` first",
			strings.Join(tags, ", "), model, tag)
	}
	return nil
}
