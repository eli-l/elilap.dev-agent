package store_test

// The SQLite schema smoke check. It replaces the never-written
// scripts/check-ddl.py (PLAN.md section 3) with a test that exercises the real
// build: the same driver, the sqlite_fts5 build tag, and sqlite-vec registered
// by this package's init, so the vec0 DDL applies exactly as it does in the
// binary. It asserts the three contracts migrations/0001_init.sql carries:
//
//   - the index tables exist after the migration runner applies them;
//   - chunks_fts stores its columns and joins back to chunks on chunk_id
//     (a contentless table would return NULL here - the trap in AGENTS.md);
//   - chunks_vec is a 768-dimension vec0 table that round-trips a KNN query
//     and rejects a vector of the wrong width.
//
//	go test -tags sqlite_fts5 ./internal/store -run TestSchemaDDL -v
//
// The command used to live in AGENTS.md as `python3 scripts/check-ddl.py`;
// keeping it here puts the check on the same driver the binary ships.

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3" // registers the "sqlite3" driver

	"github.com/eli-l/elilap.dev-agent/internal/store"
)

func TestSchemaDDL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")

	// Open through the real path: it registers sqlite-vec, creates the file,
	// and applies migrations/0001_init.sql + 0002_state.sql.
	s, err := store.Open(path, "../../migrations")
	if err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Seed one chunk the way ingest does, so chunks and chunks_fts are written
	// in the one transaction the schema expects.
	chunk := testChunk("ddl-chunk-1")
	chunk.Title = "Porter tokenizer notes"
	chunk.Body = "sqlitevec knn roundtrip probe"
	chunk.Meta = map[string]any{"tags": []any{"sqlite", "fts5"}}
	if _, _, err := s.ReplaceItemChunks(store.ItemKey{Kind: "ghost_post", ID: "uuid-1"},
		[]store.Chunk{chunk}); err != nil {
		t.Fatalf("seed chunk: %v", err)
	}

	// Every source kind the schema allows must be writable: ghost_post and
	// postiz_post from 0001_init.sql, web_page added by 0003_web_kind.sql.
	// This is what proves the migration's table rebuild widened the CHECK
	// instead of leaving the constraint behind.
	webChunk := testChunk("ddl-web-chunk-1")
	webChunk.SourceKind = "web_page"
	webChunk.SourceID = "elilap.dev/services"
	webChunk.Title = "Services page"
	webChunk.Body = "web provider chunk"
	if _, _, err := s.ReplaceItemChunks(store.ItemKey{Kind: "web_page", ID: "elilap.dev/services"},
		[]store.Chunk{webChunk}); err != nil {
		t.Fatalf("seed web_page chunk (0003 migration not applied?): %v", err)
	}

	// A second connection to the same file inspects the schema; the store's
	// pool stays private. It is writable so the dimension check can attempt a
	// bad insert at the vec0 layer rather than relying on the Go guard.
	db, err := sql.Open("sqlite3", "file:"+path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open raw connection: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	t.Run("tables", func(t *testing.T) {
		got := make(map[string]bool)
		rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type IN ('table','view')`)
		if err != nil {
			t.Fatalf("list tables: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("scan table name: %v", err)
			}
			got[name] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("list tables: %v", err)
		}
		// schema_migrations is the runner's bookkeeping; the rest is the
		// corpus, its search mirrors, and the ingest ledger.
		for _, want := range []string{
			"schema_migrations", "chunks", "chunks_fts", "chunks_vec",
			"source_state", "source_items",
		} {
			if !got[want] {
				t.Errorf("migration did not create table %q", want)
			}
		}
	})

	t.Run("fts5 stored columns and join-back", func(t *testing.T) {
		// Contentless FTS5 stores nothing: this read returns NULL or an error.
		var title, body, tags string
		err := db.QueryRow(`SELECT title, body, tags FROM chunks_fts WHERE chunk_id = ?`,
			"ddl-chunk-1").Scan(&title, &body, &tags)
		if err != nil {
			t.Fatalf("chunks_fts stored columns must read back (contentless tables cannot): %v", err)
		}
		if title == "" || body == "" || tags == "" {
			t.Errorf("chunks_fts stored columns came back empty: title=%q body=%q tags=%q",
				title, body, tags)
		}

		// The retrieval path is a MATCH plus a join to chunks on chunk_id.
		var id string
		err = db.QueryRow(`SELECT c.chunk_id FROM chunks_fts f
JOIN chunks c ON c.chunk_id = f.chunk_id
WHERE chunks_fts MATCH ?`, "sqlitevec").Scan(&id)
		if err != nil {
			t.Fatalf("chunks_fts MATCH must join back to chunks.chunk_id: %v", err)
		}
		if id != "ddl-chunk-1" {
			t.Errorf("join-back returned %q, want %q", id, "ddl-chunk-1")
		}
	})

	t.Run("vec0 width and knn round-trip", func(t *testing.T) {
		var ddl string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'chunks_vec'`).
			Scan(&ddl); err != nil {
			t.Fatalf("read chunks_vec DDL: %v", err)
		}
		if want := fmt.Sprintf("float[%d]", store.EmbeddingDims); !strings.Contains(ddl, want) {
			t.Errorf("chunks_vec DDL does not declare %s: %s", want, ddl)
		}

		vector := make([]float32, store.EmbeddingDims)
		vector[0] = 1
		written, skipped, err := s.WriteEmbeddings([]store.ChunkEmbedding{{
			ChunkID:     "ddl-chunk-1",
			ContentHash: "hash-ddl-chunk-1",
			Vector:      vector,
		}}, "ddl-test/text-v1", time.Now())
		if err != nil {
			t.Fatalf("write embeddings: %v", err)
		}
		if written != 1 || skipped != 0 {
			t.Fatalf("write embeddings = %d written, %d skipped; want 1, 0", written, skipped)
		}

		hits, err := s.SearchVectors(vector, 1, store.Filter{})
		if err != nil {
			t.Fatalf("knn search: %v", err)
		}
		if len(hits) != 1 || hits[0].ChunkID != "ddl-chunk-1" {
			t.Fatalf("knn search returned %+v, want the seeded chunk", hits)
		}

		// The vec0 table, not just the Go writer, must enforce the declared
		// width: a shorter vector is how a model switch would corrupt the
		// index silently.
		blob, err := vec.SerializeFloat32(make([]float32, store.EmbeddingDims-1))
		if err != nil {
			t.Fatalf("serialize short vector: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO chunks_vec (chunk_id, embedding) VALUES (?, ?)`,
			"wrong-width", blob); err == nil {
			t.Fatal("chunks_vec accepted a short vector; the DDL width is not enforced")
		}
	})
}
