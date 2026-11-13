// Package embed fills chunks_vec: it pages over the chunks that still need a
// vector, embeds them in batches through an OpenAI-compatible backend, and
// writes each batch with its bookkeeping in one transaction.
//
// Embedding is a separate pass from ingest (PLAN.md Decision 14 and the
// Phase 1 task list). That keeps ingest runnable without the model host, lets
// a failed batch be retried without re-fetching upstream, and makes a full
// re-embed an explicit, resumable operation instead of a side effect.
//
// The pass is idempotent: a chunk is pending only when its embedded_with tag
// differs from the current one or its vec row is missing, so re-running after
// a partial run resumes exactly where it stopped.
package embed

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// TextVersion identifies the document text template used for embeddings. It
// is part of the stored tag: changing EmbedText without bumping this would
// leave old vectors looking current, so bump it in the same change and run
// `agent embed --full`. v1 = title, section, body, blank-line separated.
const TextVersion = "v1"

// DefaultBatchSize is how many chunks one embeddings request carries. 32 keeps
// a batch comfortably inside a single HTTP request and short enough that a
// retry after a transient failure is cheap.
const DefaultBatchSize = 32

// Tag is the embedded_with marker for this model and text template, for
// example "localai:embeddinggemma-300m/text-v1". A change to either value
// makes every chunk pending again on the next run, which is the intended
// invalidation signal.
func Tag(model string) string {
	return "localai:" + model + "/text-" + TextVersion
}

// Embedder is the one method the runner needs; *llm.Client implements it.
type Embedder interface {
	Embed(ctx context.Context, model string, inputs []string) ([][]float32, error)
}

// Progress is one observation of an embed run. It is reported through
// Options.Progress so the CLI can render it and tests can assert batching
// without a terminal.
type Progress struct {
	// Total is how many chunks this run intends to process.
	Total int
	// Done is how many chunks were embedded or skipped so far.
	Done int
	// InFlight is the size of the batch currently being embedded (0 between
	// batches).
	InFlight int
	// Batches is how many embedding requests completed.
	Batches int
	// Elapsed is the wall-clock time since the run started.
	Elapsed time.Duration
}

// Runner holds the dependencies of one embed invocation.
type Runner struct {
	Store    *store.Store
	Embedder Embedder
}

// Options controls one embed invocation.
type Options struct {
	// Model is the embeddings model id (EMBED_MODEL).
	Model string
	// BatchSize is the number of chunks per request; zero means
	// DefaultBatchSize.
	BatchSize int
	// Limit caps how many chunks this run embeds; zero means all pending.
	Limit int
	// Full re-embeds every chunk, ignoring the current tag and vec rows. It
	// is the recovery path after a model or text-template change that the tag
	// cannot see, and after manual corruption of chunks_vec.
	Full bool
	// Progress, when non-nil, is called at the start, before each batch is
	// embedded (with InFlight set), and after each batch is written. It runs
	// on the Run goroutine, so it must not block.
	Progress func(Progress)
	// Now is overridable in tests; nil means time.Now.
	Now func() time.Time
}

// Result summarizes one embed run.
type Result struct {
	// Pending is how many chunks needed an embedding when the run started.
	Pending int
	// Embedded is how many vectors were written.
	Embedded int
	// Skipped is how many selected chunks vanished mid-run (a concurrent
	// ingest rewrote them), so no vector was written.
	Skipped int
	// Remaining is the pending count after the run; non-zero means --limit
	// stopped early or another writer added chunks meanwhile.
	Remaining int
	// Batches is the number of embedding requests made.
	Batches int
}

// Run drains the pending queue in batches until it is empty or Limit is hit.
// The first error aborts the run; committed batches stay committed, so a
// rerun resumes from the first unembedded chunk.
func (r *Runner) Run(ctx context.Context, opts Options) (Result, error) {
	if r.Store == nil {
		return Result{}, errors.New("embed: store is required")
	}
	if r.Embedder == nil {
		return Result{}, errors.New("embed: embedder is required")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return Result{}, errors.New("embed: model is required")
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	if opts.Limit < 0 {
		return Result{}, errors.New("embed: limit must not be negative")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	tag := Tag(opts.Model)
	log := logging.Main().With().
		Str("model", opts.Model).
		Str("tag", tag).
		Bool("full", opts.Full).
		Logger()

	pending, err := r.Store.CountPendingEmbeddings(tag, opts.Full)
	if err != nil {
		return Result{}, err
	}
	if opts.Limit > 0 && pending > opts.Limit {
		pending = opts.Limit
	}
	start := opts.Now()
	done := 0
	res := Result{Pending: pending}
	report := func(inFlight int) {
		if opts.Progress == nil {
			return
		}
		opts.Progress(Progress{
			Total:    pending,
			Done:     done,
			InFlight: inFlight,
			Batches:  res.Batches,
			Elapsed:  time.Since(start),
		})
	}
	report(0)
	if pending == 0 {
		log.Info().Msg("embed: nothing pending")
		return res, nil
	}
	// fail fills Remaining best-effort before returning an error: the CLI
	// prints the summary on the error path too, and remaining 0 alongside a
	// half-finished run would claim there is nothing left to do.
	fail := func(err error) (Result, error) {
		if n, cerr := r.Store.CountPendingEmbeddings(tag, false); cerr == nil {
			res.Remaining = n
		}
		return res, err
	}
	// The live progress line owns stderr while it is active; the start and
	// completion records would otherwise be appended to the unterminated line.
	if opts.Progress == nil {
		log.Info().Int("pending", pending).Int("batch_size", opts.BatchSize).Msg("embed: starting")
	}

	cursor := ""
	for res.Embedded+res.Skipped < pending {
		want := opts.BatchSize
		if left := pending - res.Embedded - res.Skipped; left < want {
			want = left
		}
		chunks, err := r.Store.PendingEmbeddings(tag, want, opts.Full, cursor)
		if err != nil {
			return fail(err)
		}
		if len(chunks) == 0 {
			break
		}
		cursor = chunks[len(chunks)-1].ChunkID

		inputs := make([]string, len(chunks))
		for i, c := range chunks {
			inputs[i] = EmbedText(c)
		}
		report(len(chunks))
		vectors, err := r.Embedder.Embed(ctx, opts.Model, inputs)
		if err != nil {
			return fail(fmt.Errorf("embed batch ending at %s: %w", cursor, err))
		}
		if len(vectors) != len(chunks) {
			return fail(fmt.Errorf("embed batch ending at %s: got %d vectors for %d chunks",
				cursor, len(vectors), len(chunks)))
		}
		rows := make([]store.ChunkEmbedding, len(chunks))
		for i, c := range chunks {
			if len(vectors[i]) != r.Store.Dims() {
				return fail(fmt.Errorf("embed chunk %s: %d dimensions, the index stores %d (EMBED_DIMS)",
					c.ChunkID, len(vectors[i]), r.Store.Dims()))
			}
			rows[i] = store.ChunkEmbedding{
				ChunkID:     c.ChunkID,
				ContentHash: c.ContentHash,
				Vector:      vectors[i],
			}
		}
		written, skipped, err := r.Store.WriteEmbeddings(rows, tag, opts.Now())
		if err != nil {
			return fail(err)
		}
		res.Batches++
		res.Embedded += written
		res.Skipped += skipped
		done = res.Embedded + res.Skipped
		report(0)
		if opts.Progress == nil {
			log.Debug().
				Int("batch", res.Batches).
				Int("written", written).
				Int("skipped", skipped).
				Int("done", res.Embedded+res.Skipped).
				Int("target", pending).
				Msg("embed batch written")
		}
	}

	// Remaining is always measured against the normal tag/vec condition, even
	// after a full run: once every chunk is stamped and vectored, nothing is
	// pending under the tag, so reporting "all chunks" here would contradict
	// the work the run just did.
	res.Remaining, err = r.Store.CountPendingEmbeddings(tag, false)
	if err != nil {
		return fail(err)
	}
	report(0)
	if opts.Progress == nil {
		log.Info().
			Int("embedded", res.Embedded).
			Int("skipped", res.Skipped).
			Int("remaining", res.Remaining).
			Int("batches", res.Batches).
			Msg("embed complete")
	}
	return res, nil
}

// EmbedText is the exact document string that is vectorized. Title and
// section lead the body so a chunk carries its own context even after
// splitting; the same template is what TextVersion names.
func EmbedText(c store.PendingChunk) string {
	parts := make([]string, 0, 3)
	if t := strings.TrimSpace(c.Title); t != "" {
		parts = append(parts, t)
	}
	if s := strings.TrimSpace(c.Section); s != "" {
		parts = append(parts, s)
	}
	if b := strings.TrimSpace(c.Body); b != "" {
		parts = append(parts, b)
	}
	return strings.Join(parts, "\n\n")
}
