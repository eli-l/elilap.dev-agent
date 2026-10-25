package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// The ingest ledger: `source_state` holds the bookkeeping of one configured
// source, `source_items` holds one row per upstream item the source
// contributed. The two tables come from migrations/0002_state.sql; this file
// is the only code that reads and writes them.

// SourceState is the ledger row of one configured source.
//
// The ledger is what makes an incremental ingest incremental: it records
// what the last run of this source saw, with which configuration, so the
// next run can skip the work that was already done.
type SourceState struct {
	// SourceID is the config id of the source, the primary key of the row.
	SourceID string
	// Type is the provider type the source is configured with.
	Type string
	// ConfigHash is the hash of the resolved configuration the last run
	// used. A different hash means the source is configured differently
	// now, so the index may not describe it any more.
	ConfigHash string
	// LastRunAt is when the source was last imported, ISO-8601 UTC. It is
	// empty before the first run.
	LastRunAt string
	// LastFullAt is when the source was last imported in full mode,
	// ISO-8601 UTC. It is empty until a --full run.
	LastFullAt string
	// ItemCount is how many items the source has in the ledger.
	ItemCount int
}

// SourceItem is the ledger row of one upstream item of one source.
//
// The item id is the upstream id (Ghost uuid, Postiz id, the relative path
// of a markdown file), and it is the key together with the config id of the
// source - not with the source kind, which is a property of the item.
type SourceItem struct {
	// SourceID is the config id of the source the item belongs to.
	SourceID string
	// ItemID is the upstream item id.
	ItemID string
	// URL is the item's canonical URL, when the provider knows one.
	URL string
	// ContentHash is the hash of the item's normalized content. It is the
	// strong fingerprint of the item: two runs that see the same hash see
	// the same content.
	ContentHash string
	// ETag holds the item's cheap fingerprint: the upstream entity tag when
	// the provider offers one, and the provider's own cheap hint
	// otherwise. It is what lets a run skip an item without fetching it.
	ETag string
	// LastModified is the upstream modification time, ISO-8601 UTC.
	LastModified string
	// FirstSeenAt is when the item was first seen, ISO-8601 UTC. It is
	// written once and never overwritten.
	FirstSeenAt string
	// LastSeenAt is when the item was last seen, ISO-8601 UTC.
	LastSeenAt string
	// SeenRunID is the id of the run that last saw the item.
	SeenRunID string
}

// SourceState returns the ledger row of a source and whether it exists.
//
// A source that was never imported has no row: the returned state is the
// zero value and the boolean is false, with no error. That is the normal
// state of a fresh index, not a failure.
func (s *Store) SourceState(ctx context.Context, sourceID string) (SourceState, bool, error) {
	id := strings.TrimSpace(sourceID)
	if id == "" {
		return SourceState{}, false, errors.New("source id is empty: the ledger key is the config id of the source")
	}

	var st SourceState
	err := s.db.QueryRowContext(ctx, `
SELECT source_id, type, config_hash, COALESCE(last_run_at, ''), COALESCE(last_full_at, ''), COALESCE(item_count, 0)
FROM source_state
WHERE source_id = ?`, id,
	).Scan(&st.SourceID, &st.Type, &st.ConfigHash, &st.LastRunAt, &st.LastFullAt, &st.ItemCount)
	if errors.Is(err, sql.ErrNoRows) {
		return SourceState{}, false, nil
	}
	if err != nil {
		return SourceState{}, false, fmt.Errorf("read the state of source %q: %w", id, err)
	}
	return st, true, nil
}

// PutSourceState writes the ledger row of a source, replacing the row that
// is there.
//
// The whole row is written, so the caller decides what stays: a run that
// was not a full run keeps the previous last_full_at by passing it in.
func (s *Store) PutSourceState(ctx context.Context, st SourceState) error {
	if strings.TrimSpace(st.SourceID) == "" {
		return errors.New("source id is empty: the ledger key is the config id of the source")
	}
	if strings.TrimSpace(st.Type) == "" {
		return fmt.Errorf("source %q: the type is empty: it is the provider type the source is configured with", st.SourceID)
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO source_state (source_id, type, config_hash, last_run_at, last_full_at, item_count)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(source_id) DO UPDATE SET
  type         = excluded.type,
  config_hash  = excluded.config_hash,
  last_run_at  = excluded.last_run_at,
  last_full_at = excluded.last_full_at,
  item_count   = excluded.item_count`,
		st.SourceID, st.Type, st.ConfigHash, st.LastRunAt, st.LastFullAt, st.ItemCount,
	)
	if err != nil {
		return fmt.Errorf("write the state of source %q: %w", st.SourceID, err)
	}
	return nil
}

// SourceItems returns the ledger rows of one source, ordered by item id.
//
// A source the ledger does not know yet has no rows: the result is an
// empty slice and no error.
func (s *Store) SourceItems(ctx context.Context, sourceID string) ([]SourceItem, error) {
	id := strings.TrimSpace(sourceID)
	if id == "" {
		return nil, errors.New("source id is empty: the ledger key is the config id of the source")
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT source_id, item_id, COALESCE(url, ''), COALESCE(content_hash, ''), COALESCE(etag, ''),
       COALESCE(last_modified, ''), COALESCE(first_seen_at, ''), COALESCE(last_seen_at, ''),
       COALESCE(seen_run_id, '')
FROM source_items
WHERE source_id = ?
ORDER BY item_id`, id,
	)
	if err != nil {
		return nil, fmt.Errorf("read the ledger items of source %q: %w", id, err)
	}
	defer rows.Close()

	var items []SourceItem
	for rows.Next() {
		var it SourceItem
		if err := rows.Scan(
			&it.SourceID, &it.ItemID, &it.URL, &it.ContentHash, &it.ETag,
			&it.LastModified, &it.FirstSeenAt, &it.LastSeenAt, &it.SeenRunID,
		); err != nil {
			return nil, fmt.Errorf("read a ledger item of source %q: %w", id, err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the ledger items of source %q: %w", id, err)
	}
	return items, nil
}

// PutSourceItem writes the ledger row of one item, replacing the row that
// is there.
//
// The first_seen_at of an item is written once and never overwritten: an
// item the ledger already knows keeps the timestamp of the run that first
// saw it, so the ledger carries the age of the item and not the age of the
// last run.
func (s *Store) PutSourceItem(ctx context.Context, it SourceItem) error {
	if strings.TrimSpace(it.SourceID) == "" {
		return errors.New("source id is empty: the ledger key is the config id of the source")
	}
	if strings.TrimSpace(it.ItemID) == "" {
		return fmt.Errorf("source %q: the item id is empty: it is the upstream item id", it.SourceID)
	}
	if strings.TrimSpace(it.SeenRunID) == "" {
		return fmt.Errorf("source %q: item %q: the seen run id is empty: it names the run that saw the item",
			it.SourceID, it.ItemID)
	}

	firstSeen := strings.TrimSpace(it.FirstSeenAt)
	if firstSeen == "" {
		firstSeen = nowUTC()
	}
	lastSeen := strings.TrimSpace(it.LastSeenAt)
	if lastSeen == "" {
		lastSeen = firstSeen
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO source_items (source_id, item_id, url, content_hash, etag, last_modified, first_seen_at, last_seen_at, seen_run_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(source_id, item_id) DO UPDATE SET
  url           = excluded.url,
  content_hash  = excluded.content_hash,
  etag          = excluded.etag,
  last_modified = excluded.last_modified,
  last_seen_at  = excluded.last_seen_at,
  seen_run_id   = excluded.seen_run_id`,
		it.SourceID, it.ItemID, it.URL, it.ContentHash, it.ETag, it.LastModified,
		firstSeen, lastSeen, it.SeenRunID,
	)
	if err != nil {
		return fmt.Errorf("write the ledger item %q of source %q: %w", it.ItemID, it.SourceID, err)
	}
	return nil
}

// DeleteSourceItems deletes the ledger rows of a source whose item id is not
// in keep, and returns how many rows were deleted.
//
// An empty keep list deletes every row of the source: the caller is saying
// that the source has no items any more.
func (s *Store) DeleteSourceItems(ctx context.Context, sourceID string, keep []string) (int, error) {
	id := strings.TrimSpace(sourceID)
	if id == "" {
		return 0, errors.New("source id is empty: the ledger key is the config id of the source")
	}

	kept := cleanIDs(keep)
	query := `DELETE FROM source_items WHERE source_id = ?`
	args := []any{id}
	if len(kept) > 0 {
		query += ` AND item_id NOT IN (` + placeholders(len(kept)) + `)`
		for _, k := range kept {
			args = append(args, k)
		}
	}

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("delete the ledger items of source %q: %w", id, err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count the deleted ledger items of source %q: %w", id, err)
	}
	return int(deleted), nil
}

// DeleteChunksForItem deletes the chunks of one item whose chunk id is not
// in keep, and returns how many chunks were deleted.
//
// The deletion spans three tables and happens in one transaction: the chunk
// rows, their full-text rows and their vectors. The vectors go with the
// chunk - the foreign key cascades - but the full-text rows do not: an FTS5
// table has no foreign key to the chunks, so its rows are deleted here,
// explicitly. A full-text row that outlived its chunk would keep matching
// queries with a body that is no longer in the index.
//
// An empty keep list deletes every chunk of the item: the caller is saying
// that the item no longer produces any chunk.
func (s *Store) DeleteChunksForItem(ctx context.Context, sourceKind, sourceID string, keep []string) (int, error) {
	if err := validateSourceKind(sourceKind); err != nil {
		return 0, err
	}
	if strings.TrimSpace(sourceID) == "" {
		return 0, errors.New("source id is empty: the chunks of an item are found by their source id")
	}

	kept := cleanIDs(keep)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin the deletion of the chunks of %s %q: %w", sourceKind, sourceID, err)
	}
	defer tx.Rollback()

	selectQuery := `SELECT chunk_id FROM chunks WHERE source_kind = ? AND source_id = ?`
	args := []any{sourceKind, sourceID}
	if len(kept) > 0 {
		selectQuery += ` AND chunk_id NOT IN (` + placeholders(len(kept)) + `)`
		for _, k := range kept {
			args = append(args, k)
		}
	}

	rows, err := tx.QueryContext(ctx, selectQuery, args...)
	if err != nil {
		return 0, fmt.Errorf("read the chunks of %s %q: %w", sourceKind, sourceID, err)
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("read a chunk id of %s %q: %w", sourceKind, sourceID, err)
		}
		stale = append(stale, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("read the chunks of %s %q: %w", sourceKind, sourceID, err)
	}
	rows.Close()

	if len(stale) == 0 {
		return 0, nil
	}

	marks := placeholders(len(stale))
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM chunks_fts WHERE chunk_id IN (`+marks+`)`, toArgs(stale)...,
	); err != nil {
		return 0, fmt.Errorf("delete the full-text rows of %s %q: %w", sourceKind, sourceID, err)
	}

	result, err := tx.ExecContext(ctx,
		`DELETE FROM chunks WHERE chunk_id IN (`+marks+`)`, toArgs(stale)...,
	)
	if err != nil {
		return 0, fmt.Errorf("delete the chunks of %s %q: %w", sourceKind, sourceID, err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count the deleted chunks of %s %q: %w", sourceKind, sourceID, err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit the deletion of the chunks of %s %q: %w", sourceKind, sourceID, err)
	}
	return int(deleted), nil
}

// cleanIDs returns the non-empty ids of a keep list, without duplicates.
func cleanIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

// placeholders returns the SQL placeholder list of n parameters.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// toArgs turns a list of ids into the arguments of a query.
func toArgs(ids []string) []any {
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	return args
}
