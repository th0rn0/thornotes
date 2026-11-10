package db

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

// TestMigration008_NoteLinks_UpDownRoundTrip verifies migration 008 applies and
// reverts cleanly: note_links table and notes.title_key column appear on up and
// disappear on down, and the migration can be re-applied.
func TestMigration008_NoteLinks_UpDownRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	migDB, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	require.NoError(t, err)
	t.Cleanup(func() { migDB.Close() })

	src, err := iofs.New(migrationsFS, "migrations")
	require.NoError(t, err)
	driver, err := migratesqlite.WithInstance(migDB, &migratesqlite.Config{})
	require.NoError(t, err)
	m, err := migrate.NewWithInstance("iofs", src, "sqlite", driver)
	require.NoError(t, err)

	// Up to latest.
	require.NoError(t, m.Up())
	require.True(t, tableExists(t, migDB, "note_links"), "note_links should exist after up")
	require.True(t, columnExists(t, migDB, "title_key"), "title_key should exist after up")

	// Down to 007 — whichever migrations sit above it. This test is about 008's
	// down half, so "one step" stopped meaning "008 -> 007" as soon as 009
	// existed, and a fixed -1 would have tested the newest migration instead.
	applied, _, err := m.Version()
	require.NoError(t, err)
	require.GreaterOrEqual(t, int(applied), 8, "008 must be applied for its down half to be exercised")
	require.NoError(t, m.Steps(7-int(applied)))
	require.False(t, tableExists(t, migDB, "note_links"), "note_links should be dropped after down")
	require.False(t, columnExists(t, migDB, "title_key"), "title_key should be dropped after down")

	// Up again, back to the newest migration.
	require.NoError(t, m.Steps(int(applied)-7))
	require.True(t, tableExists(t, migDB, "note_links"), "note_links should be recreated")
	require.True(t, columnExists(t, migDB, "title_key"), "title_key should be recreated")
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var c int
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&c)
	require.NoError(t, err)
	return c > 0
}

func columnExists(t *testing.T, db *sql.DB, column string) bool {
	t.Helper()
	var c int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('notes') WHERE name = ?`, column).Scan(&c)
	require.NoError(t, err)
	return c > 0
}

// TestMigration007_MySQL_NoteLinks_SQL verifies the MySQL migration SQL is
// structurally complete (live MariaDB execution is unexercised — no DSN in this
// environment). The MySQL and SQLite link repositories share identical SQL
// (both use `?` placeholders), and the SQLite impl is fully exercised by the
// notes integration tests.
func TestMigration007_MySQL_NoteLinks_SQL(t *testing.T) {
	up, err := mysqlMigrationsFS.ReadFile("mysql_migrations/007_note_links.up.sql")
	require.NoError(t, err)
	down, err := mysqlMigrationsFS.ReadFile("mysql_migrations/007_note_links.down.sql")
	require.NoError(t, err)

	upSQL := string(up)
	for _, want := range []string{
		"ALTER TABLE notes ADD COLUMN title_key",
		"CREATE INDEX idx_notes_title_key ON notes(user_id, title_key)",
		"CREATE TABLE IF NOT EXISTS note_links",
		"target_hash  CHAR(64)     NOT NULL",
		"UNIQUE KEY uq_note_links_edge (from_note_id, target_hash)",
		"CONSTRAINT fk_note_links_from FOREIGN KEY (from_note_id) REFERENCES notes(id) ON DELETE CASCADE",
		"CONSTRAINT fk_note_links_to   FOREIGN KEY (to_note_id)   REFERENCES notes(id) ON DELETE SET NULL",
	} {
		assert.Contains(t, upSQL, want)
	}

	downSQL := string(down)
	assert.Contains(t, downSQL, "DROP TABLE IF EXISTS note_links")
	assert.Contains(t, downSQL, "DROP COLUMN title_key")
}
