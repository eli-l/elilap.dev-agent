package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

// ErrUnknownItem reports that the ledger/index has no such item, so there
// is nothing to diff or delete.
var ErrUnknownItem = errors.New("item not in ledger")

// Chunk is one normalized, hash-addressed piece of an item, ready to be
// written into chunks + chunks_fts (and later embedded into chunks_vec).
type Chunk struct {
	ChunkID     string
	SourceKind  string // 'ghost_post' or 'postiz_post'
	SourceID    string // upstream item id (Ghost uuid / Postiz id)
	SourceURL   string
	Platform    string // empty for ghost
	Author      string
	PublishedAt time.Time
	IngestedAt  time.Time // zero: ReplaceItemChunks stamps time.Now
	UpdatedAt   time.Time // upstream last-modified time
	ContentHash string    // sha256 of normalized body text
	Title       string
	Section     string // heading path; empty for social
	Body        string
	BodyTokens  int
	Meta        map[string]any
}

// metaJSON serializes the chunk metadata map for the meta_json column.
func (c *Chunk) metaJSON() (string, error) {
	if c.Meta == nil {
		c.Meta = map[string]any{}
	}
	b, err := json.Marshal(c.Meta)
	if err != nil {
		return "", fmt.Errorf("marshal meta for chunk %s: %w", c.ChunkID, err)
	}
	return string(b), nil
}

// ReplaceItemChunks writes one item's chunks atomically: every row for
// (source_kind, source_id) is deleted and re-inserted, each chunk row and
// its FTS mirror inside the same transaction (AGENTS.md convention).
//
// Replacing whole items rather than individual chunks keeps chunk_seq
// stable: a mid-item edit that changes the chunk count renumbers every
// chunk_id in the item together.
func (s *Store) ReplaceItemChunks(item ItemKey, chunks []Chunk) (added, updated int, err error) {
	log := logging.DB().With().
		Str("source_kind", item.Kind).
		Str("source_id", item.ID).
		Logger()
	if item.Kind == "" {
		return 0, 0, errors.New("replace item chunks: source_kind is required")
	}
	if item.ID == "" {
		return 0, 0, errors.New("replace item chunks: source_id is required")
	}
	for _, c := range chunks {
		if c.SourceKind != item.Kind || c.SourceID != item.ID {
			return 0, 0, fmt.Errorf("chunk %s: belongs to item %s/%s, not %s/%s",
				c.ChunkID, item.Kind, item.ID, c.SourceKind, c.SourceID)
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, fmt.Errorf("begin replace %s/%s: %w", item.Kind, item.ID, err)
	}
	defer tx.Rollback()

	// Snapshot what is stored for this item before touching anything: the
	// chunk id set plus content hashes decide whether the incoming chunk set
	// is a no-op, a content-preserving re-ingest, or new content.
	type oldRow struct {
		hash     string
		ingested time.Time
	}
	old := make(map[string]oldRow)
	rows, err := tx.Query(`SELECT chunk_id, content_hash, ingested_at
FROM chunks WHERE source_kind = ? AND source_id = ?`, item.Kind, item.ID)
	if err != nil {
		return 0, 0, fmt.Errorf("snapshot chunks of %s/%s: %w", item.Kind, item.ID, err)
	}
	for rows.Next() {
		var id, hash, ingested string
		if err := rows.Scan(&id, &hash, &ingested); err != nil {
			rows.Close()
			return 0, 0, err
		}
		ts, err := time.Parse(time.RFC3339, ingested)
		if err != nil {
			ts = time.Time{}
		}
		old[id] = oldRow{hash: hash, ingested: ts}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, err
	}
	rows.Close()

	unchanged := len(old) > 0 && len(old) == len(chunks)
	for _, c := range chunks {
		o, ok := old[c.ChunkID]
		if !ok || o.hash != c.ContentHash {
			unchanged = false
			break
		}
	}
	if unchanged {
		// Same chunk ids, same content: nothing to write. ingested_at keeps
		// its original stamp and embedded rows stay valid.
		log.Debug().Int("chunks", len(chunks)).Msg("chunks unchanged, skipped write")
		return 0, len(chunks), nil
	}

	for id := range old {
		if _, err := tx.Exec(`DELETE FROM chunks_fts WHERE chunk_id = ?`, id); err != nil {
			return 0, 0, fmt.Errorf("delete stale fts row: %w", err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM chunks WHERE source_kind = ? AND source_id = ?`,
		item.Kind, item.ID); err != nil {
		return 0, 0, fmt.Errorf("delete stale chunk rows: %w", err)
	}

	now := time.Now().UTC()
	for i := range chunks {
		c := &chunks[i]
		meta, err := c.metaJSON()
		if err != nil {
			return 0, 0, err
		}
		// A chunk id that survives a re-chunk keeps its original ingested_at;
		// only genuinely new ids get the fresh stamp.
		created := now
		if o, ok := old[c.ChunkID]; ok && !o.ingested.IsZero() {
			created = o.ingested
		}
		if _, err := tx.Exec(`INSERT INTO chunks (
  chunk_id, source_kind, source_id, source_url, platform, author,
  published_at, ingested_at, updated_at, content_hash, title, section,
  body, body_tokens, meta_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			c.ChunkID, c.SourceKind, c.SourceID, c.SourceURL, c.Platform, c.Author,
			c.PublishedAt.Format(time.RFC3339), created.Format(time.RFC3339),
			c.UpdatedAt.Format(time.RFC3339), c.ContentHash, c.Title, c.Section,
			c.Body, c.BodyTokens, meta); err != nil {
			return 0, 0, fmt.Errorf("insert chunk %s: %w", c.ChunkID, err)
		}
		if _, err := tx.Exec(`INSERT INTO chunks_fts (chunk_id, title, body, tags)
VALUES (?,?,?,?)`,
			c.ChunkID, c.Title, c.Body, tagsString(c.Meta)); err != nil {
			return 0, 0, fmt.Errorf("insert fts row for %s: %w", c.ChunkID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		log.Error().Err(err).Msg("commit chunk replace failed")
		return 0, 0, fmt.Errorf("commit replace %s/%s: %w", item.Kind, item.ID, err)
	}
	for _, c := range chunks {
		// `added` counts chunks whose stored row is new or whose content
		// changed (both need a fresh embed); `updated` counts ids whose
		// content survived the re-ingest untouched.
		if o, ok := old[c.ChunkID]; ok && o.hash == c.ContentHash {
			updated++
		} else {
			added++
		}
	}
	log.Info().
		Int("added", added).
		Int("updated", updated).
		Int("removed", len(old)-updated).
		Msg("replaced item chunks")
	return added, updated, nil
}

// selectItemChunkIDs lists the chunk_ids an item currently owns.
func selectItemChunkIDs(tx *sql.Tx, kind, id string) ([]string, error) {
	rows, err := tx.Query(`SELECT chunk_id FROM chunks WHERE source_kind = ? AND source_id = ?`, kind, id)
	if err != nil {
		return nil, fmt.Errorf("list chunks of %s/%s: %w", kind, id, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			return nil, err
		}
		ids = append(ids, cid)
	}
	return ids, rows.Err()
}

// DeleteItem removes every chunk (and FTS row) of one item. Drift detection
// uses it for items the ledger knows but this run did not see. Unknown items
// return ErrUnknownItem and write nothing.
func (s *Store) DeleteItem(item ItemKey) error {
	log := logging.DB().With().Str("source_kind", item.Kind).Str("source_id", item.ID).Logger()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin delete %s/%s: %w", item.Kind, item.ID, err)
	}
	defer tx.Rollback()

	ids, err := selectItemChunkIDs(tx, item.Kind, item.ID)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		log.Debug().Msg("delete skipped: item not indexed")
		return ErrUnknownItem
	}
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM chunks_fts WHERE chunk_id = ?`, id); err != nil {
			return fmt.Errorf("delete fts row %s: %w", id, err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM chunks WHERE source_kind = ? AND source_id = ?`,
		item.Kind, item.ID); err != nil {
		return fmt.Errorf("delete chunks of %s/%s: %w", item.Kind, item.ID, err)
	}
	if err := tx.Commit(); err != nil {
		log.Error().Err(err).Msg("commit item delete failed")
		return err
	}
	log.Debug().Int("chunks", len(ids)).Msg("deleted item chunks")
	return nil
}

// tagsString flattens the chunk's tags into the FTS tags column.
func tagsString(meta map[string]any) string {
	raw, _ := meta["tags"].([]any)
	parts := make([]string, 0, len(raw))
	for _, t := range raw {
		if s, ok := t.(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

// Counts is the index-wide statistic behind `agent stats`.
type Counts struct {
	Chunks   int
	FTSRows  int
	VecRows  int
	Embedded int
	Items    int
	LastRun  string // latest source_state.last_run_at, empty before first run
}

// Stats reports chunk, FTS, and vector-row counts plus ledger size. A vec0
// failure is returned, not silently zeroed: it usually means sqlite-vec
// failed to load.
func (s *Store) Stats() (Counts, error) {
	var c Counts
	counters := []struct {
		q string
		d *int
	}{
		{`SELECT COUNT(*) FROM chunks`, &c.Chunks},
		{`SELECT COUNT(*) FROM chunks_fts`, &c.FTSRows},
		{`SELECT COUNT(*) FROM chunks_vec`, &c.VecRows},
		{`SELECT COUNT(*) FROM chunks WHERE embedded_with IS NOT NULL`, &c.Embedded},
	}
	for _, e := range counters {
		if err := s.db.QueryRow(e.q).Scan(e.d); err != nil {
			return c, fmt.Errorf("stats (%s): %w", e.q, err)
		}
	}
	var items sql.NullInt64
	var lastRun sql.NullString
	if err := s.db.QueryRow(`SELECT COUNT(*), MAX(last_run_at) FROM source_state`).Scan(&items, &lastRun); err != nil {
		return c, fmt.Errorf("stats (source_state): %w", err)
	}
	c.Items = int(items.Int64)
	c.LastRun = lastRun.String
	return c, nil
}
