#!/usr/bin/env python3
"""Schema smoke check for the elilap.dev-agent migrations.

What it checks
--------------
1. ``migrations/0001_init.sql`` and ``migrations/0002_state.sql`` apply
   cleanly to a scratch SQLite database.
2. The FTS5 join-back contract holds: ``chunks_fts`` is *self-contained*
   (not contentless), so a MATCH query returns the stored ``chunk_id`` and
   the row can be joined back to ``chunks``.
3. The ``chunks_vec`` virtual table is declared with the embedding
   dimension the plan requires (768).
4. ``migrations/0001_init.sql`` and the schema block in ``PLAN.md`` §3
   describe the same ``chunks`` table. AGENTS.md requires them to stay in
   sync; this script is what keeps that true.

The ``vec0`` virtual table needs the sqlite-vec extension, which plain
``sqlite3`` does not have - applying the whole file with the shell
``sqlite3`` command fails on that statement by design. This script applies
everything else and checks the ``vec0`` table by shape. Point
``SQLITE_VEC_PATH`` at a ``vec0`` shared object to apply it in full
instead.

Usage: python3 scripts/check-ddl.py [--plan PATH] [--migrations DIR]
Exit code 0 = all checks passed, 1 = at least one check failed.
"""

from __future__ import annotations

import argparse
import os
import re
import sqlite3
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
EXPECTED_EMBEDDING_DIM = 768
EXPECTED_CHUNK_COLUMNS = [
    "chunk_id",
    "source_kind",
    "source_id",
    "source_url",
    "platform",
    "author",
    "published_at",
    "ingested_at",
    "updated_at",
    "content_hash",
    "title",
    "section",
    "body",
    "body_tokens",
    "meta_json",
    "embedded_with",
    "embedded_at",
]


class CheckFailed(Exception):
    """A check failed; the message is the actionable reason."""


def fail(message: str) -> None:
    raise CheckFailed(message)


def strip_comments(sql: str) -> str:
    """Remove SQL line comments, preserving quoted strings."""
    out = []
    for line in sql.splitlines():
        in_single = in_double = False
        cut = len(line)
        for i, ch in enumerate(line):
            if ch == "'" and not in_double:
                in_single = not in_single
            elif ch == '"' and not in_single:
                in_double = not in_double
            elif ch == "-" and not in_single and not in_double:
                if line[i : i + 2] == "--":
                    cut = i
                    break
        out.append(line[:cut])
    return "\n".join(out)


def split_statements(sql: str) -> list[str]:
    """Split a script into statements on top-level semicolons."""
    statements = []
    buf = []
    in_single = in_double = False
    for ch in sql:
        if ch == "'" and not in_double:
            in_single = not in_single
        elif ch == '"' and not in_single:
            in_double = not in_double
        if ch == ";" and not in_single and not in_double:
            statement = "".join(buf).strip()
            if statement:
                statements.append(statement)
            buf = []
            continue
        buf.append(ch)
    tail = "".join(buf).strip()
    if tail:
        statements.append(tail)
    return statements


def is_vec_statement(statement: str) -> bool:
    return re.search(r"USING\s+vec0\b", statement, re.IGNORECASE) is not None


def try_load_vec(conn: sqlite3.Connection) -> str | None:
    """Load the sqlite-vec extension when SQLITE_VEC_PATH points at it."""
    path = os.environ.get("SQLITE_VEC_PATH")
    if not path:
        return None
    if not Path(path).is_file():
        fail(f"SQLITE_VEC_PATH points at {path!r}, which is not a file")
    try:
        conn.enable_load_extension(True)
        conn.load_extension(path)
    except sqlite3.Error as exc:  # pragma: no cover - depends on the host
        fail(f"could not load the sqlite-vec extension from {path!r}: {exc}")
    finally:
        conn.enable_load_extension(False)
    return path


def apply_migrations(conn: sqlite3.Connection, migrations: list[Path]) -> list[str]:
    """Apply every migration file; return the list of skipped statements."""
    skipped = []
    for path in migrations:
        sql = path.read_text(encoding="utf-8")
        for statement in split_statements(strip_comments(sql)):
            if is_vec_statement(statement) and not conn.execute(
                "SELECT count(*) FROM pragma_module_list WHERE name='vec0'"
            ).fetchone()[0]:
                skipped.append(statement)
                continue
            try:
                conn.execute(statement)
            except sqlite3.Error as exc:
                fail(f"{path.name}: statement failed: {exc}\n  {statement.splitlines()[0]}")
    return skipped


def check_five_available(conn: sqlite3.Connection) -> None:
    try:
        conn.execute("CREATE VIRTUAL TABLE temp.__fts5_probe USING fts5(body)")
    except sqlite3.Error as exc:
        fail(f"this Python's SQLite has no FTS5 support: {exc}")


def check_tables(conn: sqlite3.Connection) -> None:
    rows = conn.execute(
        "SELECT name FROM sqlite_master WHERE type IN ('table','view')"
    ).fetchall()
    present = {row[0] for row in rows}
    for table in ("chunks", "chunks_fts", "source_state", "source_items"):
        if table not in present:
            fail(f"table {table!r} is missing after applying the migrations")
    print(f"ok   tables present: chunks, chunks_fts, source_state, source_items")


def check_chunk_columns(conn: sqlite3.Connection) -> None:
    cols = [row[1] for row in conn.execute("PRAGMA table_info(chunks)")]
    if cols != EXPECTED_CHUNK_COLUMNS:
        fail(
            "the chunks table does not match the expected column list\n"
            f"  expected: {EXPECTED_CHUNK_COLUMNS}\n"
            f"  found:    {cols}"
        )
    print(f"ok   chunks has the expected {len(cols)} columns")


def check_fts_join_back(conn: sqlite3.Connection) -> None:
    """Prove the FTS5 stored columns read back, so query-time join-back works."""
    cols = [row[1] for row in conn.execute("PRAGMA table_info(chunks_fts)")]
    if cols[:4] != ["chunk_id", "title", "body", "tags"]:
        fail(f"chunks_fts columns are {cols}, expected chunk_id, title, body, tags")

    conn.execute(
        "INSERT INTO chunks (chunk_id, source_kind, source_id, author, published_at,"
        " ingested_at, updated_at, content_hash, body, body_tokens)"
        " VALUES ('probe', 'ghost_post', 'probe', 'probe', '2026-01-01T00:00:00Z',"
        " '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'hash', 'burnout recovery', 3)"
    )
    conn.execute(
        "INSERT INTO chunks_fts (chunk_id, title, body, tags)"
        " VALUES ('probe', 'Probe', 'burnout recovery', 'work')"
    )
    rows = conn.execute(
        "SELECT chunk_id, title FROM chunks_fts WHERE chunks_fts MATCH 'burnout'"
    ).fetchall()
    if not rows:
        fail("the FTS5 MATCH query returned no row - the index is not searchable")
    if rows[0][0] != "probe":
        fail(f"the FTS5 join-back returned chunk_id {rows[0][0]!r}, expected 'probe'")
    if rows[0][1] != "Probe":
        fail("chunks_fts is contentless: the stored title did not read back")
    joined = conn.execute(
        "SELECT c.body FROM chunks c JOIN chunks_fts f ON f.chunk_id = c.chunk_id"
        " WHERE chunks_fts MATCH 'burnout'"
    ).fetchall()
    if not joined:
        fail("the join-back from chunks_fts to chunks returned no row")
    print("ok   chunks_fts stores its columns: MATCH + join-back on chunk_id works")


def check_vec_dimension(conn: sqlite3.Connection, skipped: list[str]) -> None:
    dimension = None
    if skipped:
        statement = skipped[0]
        match = re.search(r"float\s*\[\s*(\d+)\s*\]", statement, re.IGNORECASE)
        if not match:
            fail(f"cannot read the embedding dimension out of: {statement}")
        dimension = int(match.group(1))
    else:
        row = conn.execute(
            "SELECT sql FROM sqlite_master WHERE name='chunks_vec'"
        ).fetchone()
        if row is None:
            fail("the chunks_vec virtual table is missing")
        match = re.search(r"float\s*\[\s*(\d+)\s*\]", row[0], re.IGNORECASE)
        if not match:
            fail(f"cannot read the embedding dimension out of: {row[0]}")
        dimension = int(match.group(1))
    if dimension != EXPECTED_EMBEDDING_DIM:
        fail(
            f"chunks_vec declares float[{dimension}], but the plan requires "
            f"float[{EXPECTED_EMBEDDING_DIM}]"
        )
    if skipped:
        print(
            f"ok   chunks_vec declares float[{dimension}] (checked by shape: the "
            "sqlite-vec extension is not loaded)"
        )
    else:
        print(f"ok   chunks_vec declares float[{dimension}] and applied cleanly")


def plan_schema_block(plan: Path) -> str:
    """Extract the schema SQL block that PLAN.md §3 shows for 0001_init.sql."""
    text = plan.read_text(encoding="utf-8")
    anchor = "**Schema** (`migrations/0001_init.sql`"
    start = text.find(anchor)
    if start == -1:
        fail(f"could not find the schema block in {plan.name}")
    fence_start = text.find("```sql", start)
    if fence_start == -1:
        fail(f"the schema block in {plan.name} has no ```sql fence")
    body_start = fence_start + len("```sql")
    fence_end = text.find("```", body_start)
    if fence_end == -1:
        fail(f"the schema block in {plan.name} is not closed")
    return text[body_start:fence_end]


def columns_of_create_table(sql: str, table: str) -> list[str]:
    """Return the column names of a CREATE TABLE statement.

    The SQL is stripped of line comments first: the migrations carry
    explanatory comments that contain both parentheses and commas, and
    scanning those as if they were part of the column list would split
    columns in the wrong places.
    """
    sql = strip_comments(sql)
    match = re.search(
        r"CREATE\s+(?:VIRTUAL\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?"
        + re.escape(table)
        + r"\s*\(",
        sql,
        re.IGNORECASE,
    )
    if not match:
        fail(f"could not find the CREATE TABLE for {table!r}")

    body, end = extract_parenthesised(sql, match.end())
    parts = split_top_level(body)

    names = []
    for column in parts:
        column = column.strip()
        if not column:
            continue
        head = column.split()[0].upper()
        if head in {
            "PRIMARY",
            "UNIQUE",
            "CHECK",
            "FOREIGN",
            "CONSTRAINT",
        }:
            continue
        names.append(column.split()[0].strip('"`[]'))
    return names


def extract_parenthesised(sql: str, start: int) -> tuple[str, int]:
    """Return the text inside the parenthesis that starts at start."""
    depth = 0
    for i in range(start, len(sql)):
        ch = sql[i]
        if ch == "(":
            depth += 1
        elif ch == ")":
            if depth == 0:
                return sql[start:i], i
            depth -= 1
    fail("unbalanced parentheses in a CREATE TABLE statement")


def split_top_level(body: str) -> list[str]:
    """Split a column list on commas that are not inside parentheses."""
    parts: list[str] = []
    current: list[str] = []
    depth = 0
    for ch in body:
        if ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
        if ch == "," and depth == 0:
            parts.append("".join(current))
            current = []
            continue
        current.append(ch)
    parts.append("".join(current))
    return parts


def check_plan_sync(plan: Path, migrations_dir: Path) -> None:
    """The plan's schema block and the migration file must agree."""
    block = plan_schema_block(plan)
    plan_columns = columns_of_create_table(block, "chunks")
    migration_sql = (migrations_dir / "0001_init.sql").read_text(encoding="utf-8")
    migration_columns = columns_of_create_table(migration_sql, "chunks")
    if plan_columns != migration_columns:
        fail(
            "PLAN.md §3 and migrations/0001_init.sql describe different chunks tables\n"
            f"  PLAN.md:     {plan_columns}\n"
            f"  0001_init:   {migration_columns}"
        )
    print(
        f"ok   PLAN.md §3 and migrations/0001_init.sql agree on the chunks table "
        f"({len(plan_columns)} columns)"
    )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument(
        "--plan",
        type=Path,
        default=REPO_ROOT / "PLAN.md",
        help="path to PLAN.md (default: the repo's PLAN.md)",
    )
    parser.add_argument(
        "--migrations",
        type=Path,
        default=REPO_ROOT / "migrations",
        help="directory holding the migration files (default: the repo's migrations/)",
    )
    args = parser.parse_args()

    if not args.plan.is_file():
        print(f"check-ddl: {args.plan} does not exist", file=sys.stderr)
        return 1
    if not args.migrations.is_dir():
        print(f"check-ddl: {args.migrations} is not a directory", file=sys.stderr)
        return 1

    migrations = sorted(args.migrations.glob("*.sql"))
    if not migrations:
        print(f"check-ddl: no .sql files in {args.migrations}", file=sys.stderr)
        return 1

    conn = sqlite3.connect(":memory:")
    try:
        check_five_available(conn)
        vec_path = try_load_vec(conn)
        if vec_path:
            print(f"ok   sqlite-vec extension loaded from {vec_path}")
        skipped = apply_migrations(conn, migrations)
        check_tables(conn)
        check_chunk_columns(conn)
        check_fts_join_back(conn)
        check_vec_dimension(conn, skipped)
        check_plan_sync(args.plan, args.migrations)
    except CheckFailed as exc:
        print(f"FAIL {exc}", file=sys.stderr)
        return 1
    finally:
        conn.close()

    print("DDL + FTS5 stored-column join-back OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
