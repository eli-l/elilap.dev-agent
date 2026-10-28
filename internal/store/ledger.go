package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

// ItemKey identifies one upstream item to the store. Kind is the provider's
// kind ('ghost_post' / 'postiz_post') and ID is the upstream item id (Ghost
// uuid / Postiz id). The ledger's key is this pair scoped by the config id.
type ItemKey struct {
	Kind string
	ID   string
}

// SetSourceState upserts one provider's run bookkeeping after a run. The
// last_full_at column only moves forward on a --full run; incremental runs
// preserve the previous value.
func (s *Store) SetSourceState(sourceID, typeName, configHash string, full bool, itemCount int, at time.Time) error {
	q := `INSERT INTO source_state (
  source_id, type, config_hash, last_run_at, last_full_at, item_count
) VALUES (?,?,?,?,?,?)
ON CONFLICT(source_id) DO UPDATE SET
  type=excluded.type,
  config_hash=excluded.config_hash,
  last_run_at=excluded.last_run_at,
  item_count=excluded.item_count,
  last_full_at=COALESCE(?, source_state.last_full_at)`
	stamp := at.UTC().Format(time.RFC3339)
	var fullAt any
	if full {
		fullAt = stamp
	}
	_, err := s.db.Exec(q, sourceID, typeName, configHash, stamp, fullAt, itemCount, fullAt)
	log := logging.DB()
	if err != nil {
		log.Error().Err(err).Str("source_id", sourceID).Msg("update source_state failed")
		return fmt.Errorf("update source_state %s: %w", sourceID, err)
	}
	log.Info().
		Str("source_id", sourceID).
		Str("type", typeName).
		Bool("full", full).
		Int("items", itemCount).
		Time("at", at).
		Msg("recorded source run state")
	return nil
}

// ErrNoSourceState reports that a provider has never run, so there is no
// watermark to resume from.
var ErrNoSourceState = errors.New("source has no prior run")

// LastRunAt returns the provider's last_run_at watermark. It is the List
// overlap anchor: incremental ingest lists from watermark - overlap.
func (s *Store) LastRunAt(sourceID string) (time.Time, error) {
	var stamp sql.NullString
	err := s.db.QueryRow(`SELECT last_run_at FROM source_state WHERE source_id = ?`, sourceID).Scan(&stamp)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNoSourceState
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("read source_state %s: %w", sourceID, err)
	}
	if !stamp.Valid || stamp.String == "" {
		return time.Time{}, ErrNoSourceState
	}
	t, err := time.Parse(time.RFC3339, stamp.String)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse last_run_at for %s: %w", sourceID, err)
	}
	return t, nil
}

// LedgerItem is one row of source_items: the per-item fingerprint used to
// skip unchanged items without fetching them.
type LedgerItem struct {
	ItemID       string // upstream item id
	URL          string
	ContentHash  string // hash of the normalized text
	ETag         string
	LastModified string // upstream updated_at, ISO-8601 UTC
}

// UpsertItem records that this run saw the item, with its current
// fingerprint. first_seen_at is set on insert and never touched again.
func (s *Store) UpsertItem(sourceID string, it LedgerItem, runID string, at time.Time) error {
	_, err := s.db.Exec(`INSERT INTO source_items (
  source_id, item_id, url, content_hash, etag, last_modified,
  first_seen_at, last_seen_at, seen_run_id
) VALUES (?,?,?,?,?,?,?,?,?)
ON CONFLICT(source_id, item_id) DO UPDATE SET
  url=excluded.url,
  content_hash=excluded.content_hash,
  etag=excluded.etag,
  last_modified=excluded.last_modified,
  last_seen_at=excluded.last_seen_at,
  seen_run_id=excluded.seen_run_id`,
		sourceID, it.ItemID, it.URL, it.ContentHash, it.ETag, it.LastModified,
		at.UTC().Format(time.RFC3339), at.UTC().Format(time.RFC3339), runID)
	log := logging.DB()
	if err != nil {
		return fmt.Errorf("upsert source_item %s/%s: %w", sourceID, it.ItemID, err)
	}
	log.Debug().
		Str("source_id", sourceID).
		Str("item_id", it.ItemID).
		Str("run_id", runID).
		Msg("upserted ledger item")
	return nil
}

// LedgerDiff is what changed for one provider between the ledger and the
// provider's current list.
type LedgerDiff struct {
	ToFetch  []LedgerItem // new or fingerprint-changed: needs Fetch + write
	ToRemove []LedgerItem // known to the ledger, absent upstream: drift
	Seen     int          // items listed this run (new+changed+unchanged)
}

// DiffLedger compares one provider's listed items against the ledger and
// classifies them. Unchanged items (same etag + last_modified) are skipped
// entirely - no fetch, no chunk, no embed.
func (s *Store) DiffLedger(sourceID string, items []LedgerItem) (*LedgerDiff, error) {
	diff := &LedgerDiff{Seen: len(items)}

	known := make(map[string]LedgerItem)
	rows, err := s.db.Query(`SELECT item_id, url, content_hash, etag, last_modified
FROM source_items WHERE source_id = ?`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("list source_items for %s: %w", sourceID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var it LedgerItem
		var url, hash, etag, modified sql.NullString
		if err := rows.Scan(&it.ItemID, &url, &hash, &etag, &modified); err != nil {
			return nil, err
		}
		it.URL, it.ContentHash, it.ETag, it.LastModified = url.String, hash.String, etag.String, modified.String
		known[it.ItemID] = it
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(items))
	for _, it := range items {
		seen[it.ItemID] = true
		prev, ok := known[it.ItemID]
		if !ok || prev.ETag != it.ETag || prev.LastModified != it.LastModified {
			diff.ToFetch = append(diff.ToFetch, it)
		}
	}
	for _, it := range known {
		if !seen[it.ItemID] {
			diff.ToRemove = append(diff.ToRemove, it)
		}
	}
	log := logging.DB()
	log.Debug().
		Str("source_id", sourceID).
		Int("known", len(known)).
		Int("seen", diff.Seen).
		Int("to_fetch", len(diff.ToFetch)).
		Int("to_remove", len(diff.ToRemove)).
		Msg("diffed ledger")
	return diff, nil
}

// RemoveItem drops one item's ledger row and index chunks in one
// transaction; the ledger row and its chunks live and die together so a
// re-list can never classify a half-deleted item as "unchanged".
func (s *Store) RemoveItem(sourceID string, it LedgerItem) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin remove %s/%s: %w", sourceID, it.ItemID, err)
	}
	defer tx.Rollback()

	var ids []string
	for _, kind := range []string{"ghost_post", "postiz_post"} {
		kindIDs, err := selectItemChunkIDs(tx, kind, it.ItemID)
		if err != nil {
			return err
		}
		ids = append(ids, kindIDs...)
	}
	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM chunks_fts WHERE chunk_id = ?`, id); err != nil {
			return fmt.Errorf("delete fts row %s: %w", id, err)
		}
	}
	for _, kind := range []string{"ghost_post", "postiz_post"} {
		if _, err := tx.Exec(`DELETE FROM chunks WHERE source_kind = ? AND source_id = ?`,
			kind, it.ItemID); err != nil {
			return fmt.Errorf("delete %s chunks for %s: %w", kind, it.ItemID, err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM source_items WHERE source_id = ? AND item_id = ?`,
		sourceID, it.ItemID); err != nil {
		return fmt.Errorf("delete source_item %s/%s: %w", sourceID, it.ItemID, err)
	}
	log := logging.DB()
	if err := tx.Commit(); err != nil {
		log.Error().Err(err).
			Str("source_id", sourceID).Str("item_id", it.ItemID).
			Msg("commit item removal failed")
		return err
	}
	log.Info().
		Str("source_id", sourceID).
		Str("item_id", it.ItemID).
		Int("chunks", len(ids)).
		Msg("removed drifted item")
	return nil
}
