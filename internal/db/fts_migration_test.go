package db

// Migration 009 replaces notes_fts with a contentless-delete FTS5 table. Two
// things have to be true of it, and this file is what says so:
//
//   - on a fresh database the table is the new shape, so the sync path's
//     rowid-only delete is the one it was written against;
//   - on a database that already carries the tokens of text that has since been
//     edited away, the migration drops them. Re-indexing the current text and
//     nothing else is the only way to be sure: the old tokens cannot be
//     enumerated, which is the whole reason the old table could not remove them
//     (ticket 451).
//
// The production deploy path has no human in it — the image is pulled on
// :latest and started — so a migration that only works on an empty database is
// not a migration. The existing-database case below is the one that matters.

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ftsSchema returns the CREATE VIRTUAL TABLE statement of notes_fts.
func ftsSchema(t *testing.T, db *sql.DB) string {
	t.Helper()
	var stmt string
	require.NoError(t, db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'notes_fts'`).Scan(&stmt))
	return stmt
}

// ftsMatchCount counts the documents in notes_fts that match term, without
// joining notes: the index's own answer.
func ftsMatchCount(t *testing.T, db *sql.DB, term string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM notes_fts WHERE notes_fts MATCH ?`, term).Scan(&n))
	return n
}

func TestMigration009_FreshDatabaseUsesContentlessDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	pool, err := Open(path)
	require.NoError(t, err)
	defer pool.Close()

	version, maxSupported := pool.SchemaStatus()
	assert.Equal(t, 10, version)
	assert.Equal(t, 10, maxSupported)

	schema := ftsSchema(t, pool.ReadDB)
	assert.Contains(t, schema, "contentless_delete=1", "notes_fts must be a contentless-delete table")
	assert.Contains(t, schema, "content=''", "a contentless-delete table stores no copy of the note text")
	assert.NotContains(t, schema, "content=notes", "the external-content shape is what could not unindex a row")

	// The rowid-only delete the sync path relies on, on a table that was never
	// written to: a no-op, not an error.
	_, err = pool.WriteDB.Exec(`DELETE FROM notes_fts WHERE rowid = 1`)
	require.NoError(t, err, "deleting an unindexed rowid must be harmless")
}

func TestMigration009_ExistingDatabaseDropsStaleTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	// A database left behind by the previous release: schema 8, external-content
	// notes_fts.
	migrateScratchDBTo(t, path, 8)

	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	require.NoError(t, err)
	require.Contains(t, ftsSchema(t, raw), "content=notes", "precondition: the old shape")

	// The state ticket 451 describes, written by hand: the note says one thing,
	// the index holds the text it used to say.
	_, err = raw.Exec(`INSERT INTO users (username, password_hash, uuid) VALUES ('alice', 'hash', 'uuid-alice')`)
	require.NoError(t, err)
	_, err = raw.Exec(`
		INSERT INTO notes (id, user_id, title, slug, disk_path, content, content_hash, tags, fts_synced_at)
		VALUES (1, 1, 'Zebrafish Ledger', 'zebrafish-ledger', 'alice/zebrafish-ledger.md',
		        'the okapi wandered past the crossing', 'hash-1', '[]', datetime('now'))`)
	require.NoError(t, err)
	_, err = raw.Exec(`
		INSERT INTO notes_fts (rowid, title, content)
		VALUES (1, 'Zebrafish Ledger', 'the crossing was repainted last spring')`)
	require.NoError(t, err)

	require.Equal(t, 1, ftsMatchCount(t, raw, "repainted"), "precondition: the stale token is in the index")
	require.Equal(t, 0, ftsMatchCount(t, raw, "okapi"), "precondition: the current text is not indexed")
	require.NoError(t, raw.Close())

	// The upgrade, exactly as the container start performs it.
	pool, err := Open(path)
	require.NoError(t, err)
	defer pool.Close()

	version, _ := pool.SchemaStatus()
	assert.Equal(t, 10, version)

	assert.Equal(t, 0, ftsMatchCount(t, pool.ReadDB, "repainted"),
		"the migration must drop the tokens of text the note no longer contains")
	assert.Equal(t, 1, ftsMatchCount(t, pool.ReadDB, "okapi"),
		"the migration must index the note's current text")
	assert.Equal(t, 1, ftsMatchCount(t, pool.ReadDB, "zebrafish"),
		"the migration must index the title too")
}

// TestMigration009_RollbackRestoresExternalContent runs the down migration. It is
// the half of the pair that never gets exercised in production until the day
// somebody needs it, so it is exercised here: the table comes back in the 001
// shape, and the index is rebuilt from notes rather than left empty.
func TestMigration009_RollbackRestoresExternalContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	pool, err := Open(path)
	require.NoError(t, err)
	defer pool.Close()

	_, err = pool.WriteDB.Exec(`INSERT INTO users (username, password_hash, uuid) VALUES ('alice', 'hash', 'uuid-alice')`)
	require.NoError(t, err)
	_, err = pool.WriteDB.Exec(`
		INSERT INTO notes (id, user_id, title, slug, disk_path, content, content_hash, tags)
		VALUES (1, 1, 'Zebrafish Ledger', 'zebrafish-ledger', 'alice/zebrafish-ledger.md',
		        'the okapi wandered past the crossing', 'hash-1', '[]')`)
	require.NoError(t, err)
	_, err = pool.WriteDB.Exec(`INSERT INTO notes_fts(rowid, title, content) SELECT id, title, content FROM notes`)
	require.NoError(t, err)
	require.Equal(t, 1, ftsMatchCount(t, pool.ReadDB, "okapi"))

	src, err := iofs.New(migrationsFS, "migrations")
	require.NoError(t, err)
	driver, err := migratesqlite.WithInstance(pool.WriteDB, &migratesqlite.Config{})
	require.NoError(t, err)
	m, err := migrate.NewWithInstance("iofs", src, "sqlite", driver)
	require.NoError(t, err)

	// The database is at 010 by now, so reaching 001's shape takes two steps:
	// 010 first (the tokenizer, back to `porter ascii`), then 009.
	require.NoError(t, m.Steps(-1), "010's down migration must run")

	tokenizerSchema := ftsSchema(t, pool.ReadDB)
	assert.Contains(t, tokenizerSchema, "tokenize='porter ascii'",
		"010's rollback restores the pre-accent-folding tokenizer")
	assert.Contains(t, tokenizerSchema, "content=''",
		"010's rollback keeps the contentless-delete shape 009 introduced")
	require.Equal(t, 1, ftsMatchCount(t, pool.ReadDB, "okapi"),
		"010's rollback repopulates the index from notes")

	require.NoError(t, m.Steps(-1), "009's down migration must run")

	assert.Contains(t, ftsSchema(t, pool.ReadDB), "content=notes", "rollback restores the 001 shape")
	assert.Equal(t, 1, ftsMatchCount(t, pool.ReadDB, "okapi"), "rollback rebuilds the index from notes")
	assert.Equal(t, 0, ftsMatchCount(t, pool.ReadDB, "repainted"))
}
