package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cappedMigrations returns an in-memory copy of the SQLite migration set with
// every version above max removed — the source set of a binary built before
// those migrations landed, which is exactly the image that used to crash-loop.
func cappedMigrations(t *testing.T, maxVersion int) fs.FS {
	t.Helper()
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	require.NoError(t, err)

	capped := fstest.MapFS{}
	kept := 0
	for _, entry := range entries {
		version, _, ok := parseMigrationFilename(entry.Name())
		if !ok || version > maxVersion {
			continue
		}
		data, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		require.NoError(t, err)
		capped["migrations/"+entry.Name()] = &fstest.MapFile{Data: data}
		kept++
	}
	require.NotZero(t, kept, "capped migration set must not be empty")
	return capped
}

// dbSnapshot is everything "untouched" has to mean: the stamped version, the
// dirty flag, the schema itself, and the user data.
type dbSnapshot struct {
	version   int
	dirty     bool
	tables    string
	users     int
	notes     int
	folders   int
	apiTokens int
}

func snapshotDB(t *testing.T, db *sql.DB) dbSnapshot {
	t.Helper()
	var s dbSnapshot
	require.NoError(t, db.QueryRow(
		`SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(&s.version, &s.dirty))
	require.NoError(t, db.QueryRow(
		`SELECT COALESCE(group_concat(name, ','), '') FROM (SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name)`).Scan(&s.tables))
	for _, c := range []struct {
		table string
		dest  *int
	}{
		{"users", &s.users},
		{"notes", &s.notes},
		{"folders", &s.folders},
		{"api_tokens", &s.apiTokens},
	} {
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM `+c.table).Scan(c.dest))
	}
	return s
}

// migrateScratchDBTo migrates a fresh SQLite file to exactly version, using a
// migration set capped at that version — the state a database is left in by a
// binary that predates later migrations.
func migrateScratchDBTo(t *testing.T, path string, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	src, err := iofs.New(cappedMigrations(t, version), "migrations")
	require.NoError(t, err)
	driver, err := migratesqlite.WithInstance(db, &migratesqlite.Config{})
	require.NoError(t, err)
	m, err := migrate.NewWithInstance("iofs", src, "sqlite", driver)
	require.NoError(t, err)
	require.NoError(t, m.Up())
}

// latestSQLiteVersion and latestMySQLVersion return the newest migration this
// build embeds, walked from the source driver. Assertions elsewhere use them
// instead of a literal, so adding a migration does not require editing a dozen
// unrelated tests — the number that matters is "the newest", not "12".
func latestSQLiteVersion(t *testing.T) int {
	t.Helper()
	src, err := iofs.New(migrationsFS, "migrations")
	require.NoError(t, err)
	v, err := maxSupportedVersion(src)
	require.NoError(t, err)
	return v
}

// ─── A. the preflight ────────────────────────────────────────────────────────

// TestPreflight_SchemaTooNew_RefusesWithoutTouchingDB is the regression test for
// the 2026-11-03 crash loop: a database at version N seen by a binary that only
// carries 1..N-1 must be refused with both numbers, and must be left exactly as
// it was found.
func TestPreflight_SchemaTooNew_RefusesWithoutTouchingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	// Migrate a scratch database to the newest version with the current code.
	pool, err := Open(path)
	require.NoError(t, err)
	_, err = pool.WriteDB.Exec(
		`INSERT INTO users (username, password_hash, uuid) VALUES ('alice', 'hash', 'uuid-alice')`)
	require.NoError(t, err)

	before := snapshotDB(t, pool.WriteDB)
	require.Equal(t, latestSQLiteVersion(t), before.version,
		"this test assumes the fixture database is at the newest migration")
	require.Equal(t, 1, before.users)

	// The same database, opened by a binary whose source stops one version short.
	src, err := iofs.New(cappedMigrations(t, before.version-1), "migrations")
	require.NoError(t, err)
	maxSupported, err := maxSupportedVersion(src)
	require.NoError(t, err)
	require.Equal(t, before.version-1, maxSupported)

	driver, err := migratesqlite.WithInstance(pool.WriteDB, &migratesqlite.Config{})
	require.NoError(t, err)
	m, err := migrate.NewWithInstance("iofs", src, "sqlite", driver)
	require.NoError(t, err)

	dbVersion, err := preflightSchema(m, maxSupported)
	assert.Equal(t, before.version, dbVersion, "preflight must report the version stamped in the database")

	var tooNew *ErrSchemaTooNew
	require.ErrorAs(t, err, &tooNew, "a newer database must produce ErrSchemaTooNew")
	assert.Equal(t, before.version, tooNew.DBVersion)
	assert.Equal(t, before.version-1, tooNew.MaxSupported)

	// Both numbers and the remedy, in the message an operator will see.
	msg := err.Error()
	assert.Contains(t, msg, fmt.Sprintf("schema version %d is newer than this build supports (%d)",
		before.version, before.version-1))
	assert.Contains(t, msg, fmt.Sprintf("deploy a build with migration ≥ %d", before.version))
	assert.Contains(t, msg, fmt.Sprintf("restore the database backup taken before migration %d", before.version))

	// Untouched: same version, not dirty, same schema, same rows.
	assert.Equal(t, before, snapshotDB(t, pool.WriteDB))

	pool.Close()
}

// TestOpen_SchemaTooNew_ReturnsTypedError drives the refusal through the real
// Open path: a database stamped beyond anything this build carries (as an
// already-rolled-back image would find) must fail with the typed error rather
// than golang-migrate's "no migration found for version N" message.
func TestOpen_SchemaTooNew_ReturnsTypedError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	pool, err := Open(path)
	require.NoError(t, err)
	_, err = pool.WriteDB.Exec(`UPDATE schema_migrations SET version = 99`)
	require.NoError(t, err)
	before := snapshotDB(t, pool.WriteDB)
	pool.Close()

	_, err = Open(path)
	var tooNew *ErrSchemaTooNew
	require.ErrorAs(t, err, &tooNew)
	assert.Equal(t, 99, tooNew.DBVersion)
	latest := latestSQLiteVersion(t)
	assert.Equal(t, latest, tooNew.MaxSupported)
	assert.Contains(t, err.Error(), fmt.Sprintf("schema version 99 is newer than this build supports (%d)", latest))

	// The failed open left the database where it was.
	raw, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	defer raw.Close()
	assert.Equal(t, before, snapshotDB(t, raw))
}

// TestOpen_DirtySchema_RefusesAndNamesVersion covers the sibling state: a
// migration interrupted halfway leaves the database dirty, and startup must name
// the version and the recovery path instead of failing on migrate's own message.
func TestOpen_DirtySchema_RefusesAndNamesVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	pool, err := Open(path)
	require.NoError(t, err)
	_, err = pool.WriteDB.Exec(`UPDATE schema_migrations SET dirty = 1`)
	require.NoError(t, err)
	before := snapshotDB(t, pool.WriteDB)
	pool.Close()

	_, err = Open(path)
	var dirty *ErrSchemaDirty
	require.ErrorAs(t, err, &dirty, "a dirty database must produce ErrSchemaDirty")
	latest := latestSQLiteVersion(t)
	assert.Equal(t, latest, dirty.Version)

	msg := err.Error()
	assert.Contains(t, msg, fmt.Sprintf("schema version %d is dirty", latest))
	assert.Contains(t, msg, fmt.Sprintf("force version %d", latest))
	assert.Contains(t, msg, fmt.Sprintf("restore the database backup taken before migration %d", latest))

	// Refusing must not "repair" anything: the dirty flag stays put for the
	// operator to inspect, and no migration is attempted.
	raw, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	defer raw.Close()
	assert.Equal(t, before, snapshotDB(t, raw))
}

// TestOpen_DatabaseAtMax_IsNoOp and TestOpen_DatabaseBehind_StillMigrates pin
// the normal path: at or behind the floor, startup behaves exactly as before.
func TestOpen_DatabaseAtMax_IsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	pool, err := Open(path)
	require.NoError(t, err)
	pool.Close()

	pool, err = Open(path)
	require.NoError(t, err, "re-opening an up-to-date database must succeed")
	defer pool.Close()

	version, maxSupported := pool.SchemaStatus()
	assert.Equal(t, latestSQLiteVersion(t), version)
	assert.Equal(t, version, maxSupported)
}

func TestOpen_DatabaseBehind_StillMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	migrateScratchDBTo(t, path, 5)

	pool, err := Open(path)
	require.NoError(t, err, "a database behind the floor must migrate as before")
	defer pool.Close()

	version, maxSupported := pool.SchemaStatus()
	assert.Equal(t, latestSQLiteVersion(t), version, "Open must bring the database up to the newest migration")
	assert.Equal(t, version, maxSupported)
}

// TestMaxSupportedVersion_ComesFromSourceDriver pins the trap the ticket calls
// out: the two dialect sequences are numbered differently, so the floor has to
// be walked per dialect, never counted from a directory.
//
// The two literals are the contract's named numbers (SQLite 012_folder_shares,
// MySQL 009_folder_shares). They are asserted here rather than derived, because
// this is the one place that should notice if a migration lands in one sequence
// and not the other.
func TestMaxSupportedVersion_ComesFromSourceDriver(t *testing.T) {
	sqliteSrc, err := iofs.New(migrationsFS, "migrations")
	require.NoError(t, err)
	sqliteMax, err := maxSupportedVersion(sqliteSrc)
	require.NoError(t, err)

	mysqlSrc, err := iofs.New(mysqlMigrationsFS, "mysql_migrations")
	require.NoError(t, err)
	mysqlMax, err := maxSupportedVersion(mysqlSrc)
	require.NoError(t, err)

	assert.Equal(t, 12, sqliteMax, "SQLite carries 012_folder_shares")
	assert.Equal(t, 9, mysqlMax, "MySQL carries 009_folder_shares: its sequence has no 004_api_tokens_hash")
	assert.NotEqual(t, sqliteMax, mysqlMax, "the two dialects must not share a floor")
}

// TestMaxSupportedVersion_EmptySourceIsAnError guards the walk's error path.
func TestMaxSupportedVersion_EmptySourceIsAnError(t *testing.T) {
	src, err := iofs.New(fstest.MapFS{"migrations/.keep": &fstest.MapFile{}}, "migrations")
	require.NoError(t, err)

	_, err = maxSupportedVersion(src)
	require.Error(t, err)
	assert.True(t, errors.Is(err, fs.ErrNotExist), "empty migration set must report ErrNotExist, got %v", err)
}
