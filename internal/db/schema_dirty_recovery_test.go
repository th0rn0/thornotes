package db

// SQLite dirty-migration recovery, in parity with the MySQL leg (ticket 472,
// bullet 4).
//
// mysql_test.go's TestMySQL_DirtyState_RefusesLoudly pins what a MariaDB
// database left dirty does. schema_floor_test.go pins the same refusal for
// SQLite at the *newest* version. Neither covers what an interrupted migration
// partway through the sequence leaves behind — a database stamped at version N
// with dirty=1 while this build carries migrations up to 9 — which is the state
// a crash during 006..009 actually produces on SQLite, and the one an operator
// meets.
//
// The behaviour is asserted, not assumed: Thornotes refuses, names the version,
// changes nothing, and leaves the dirty flag for the operator to inspect. The
// documented recovery (docs/operations.md, "Schema dirty") is a direct write
// clearing the flag after the schema has been verified by hand; this test
// performs that repair and then proves the next start migrates the rest of the
// way and the database is usable. Recovery is a human decision — so the refusal
// half matters as much as the recovery half.

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openRawSQLite opens the file without going through Open (which would migrate
// it), for the direct reads and writes an operator would make.
func openRawSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	t.Cleanup(func() { raw.Close() })
	return raw
}

func TestOpen_DirtyMidSequence_RefusesThenDocumentedRepairRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "half-applied.db")

	// A database left at version 5: as if the process died while applying 006.
	migrateScratchDBTo(t, path, 5)

	raw := openRawSQLite(t, path)
	_, err := raw.Exec(`UPDATE schema_migrations SET dirty = 1`)
	require.NoError(t, err)
	before := snapshotDB(t, raw)
	require.Equal(t, 5, before.version)
	require.True(t, before.dirty, "precondition: the row is stamped dirty at 5")

	// ── 1. startup refuses, names the version, and changes nothing ───────────
	_, err = Open(path)
	var dirty *ErrSchemaDirty
	require.ErrorAs(t, err, &dirty, "a half-applied migration must produce ErrSchemaDirty")
	assert.Equal(t, 5, dirty.Version, "the refusal must name the version that was stamped")

	msg := err.Error()
	assert.Contains(t, msg, "schema version 5 is dirty")
	assert.Contains(t, msg, "inspect the database and verify the schema by hand")
	assert.Contains(t, msg, "force version 5")

	// Untouched: same version, still dirty, same schema, same rows. A refusal
	// that "repairs" by guessing is worse than a refusal that stops.
	assert.Equal(t, before, snapshotDB(t, openRawSQLite(t, path)))

	// ── 2. no later migration ran against the dirty database ────────────────
	// The refusal happens before Up(), so a schema object introduced after 005
	// must still be absent. note_links arrives in 008; its absence is the proof
	// that nothing was applied.
	var linksTables int
	require.NoError(t, openRawSQLite(t, path).QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'note_links'`).Scan(&linksTables))
	assert.Zero(t, linksTables, "a refused startup must not apply migrations")

	// ── 3. the documented repair, performed by hand ──────────────────────────
	// docs/operations.md: inspect the schema, then clear the flag with a direct
	// write to the bookkeeping table (Thornotes has no force command of its own).
	_, err = openRawSQLite(t, path).Exec(
		`UPDATE schema_migrations SET version = 5, dirty = 0`)
	require.NoError(t, err)

	// ── 4. the next start finishes the sequence and is usable ───────────────
	pool, err := Open(path)
	require.NoError(t, err, "a cleared dirty flag must let startup migrate the rest")
	version, maxSupported := pool.SchemaStatus()
	assert.Equal(t, 9, version, "startup must bring the schema to the newest migration")
	assert.Equal(t, maxSupported, version)

	// Usable, not merely open: a row can be written and the index 009 rebuilds
	// is there.
	_, err = pool.WriteDB.Exec(
		`INSERT INTO users (username, password_hash, uuid) VALUES ('recovered', 'hash', 'uuid-recovered')`)
	require.NoError(t, err, "the recovered database must accept writes")
	require.NoError(t, openRawSQLite(t, path).QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'note_links'`).Scan(&linksTables))
	assert.Equal(t, 1, linksTables, "the migrations skipped by the refusal must land now")

	var ftsRows int
	require.NoError(t, pool.ReadDB.QueryRow(
		`SELECT COUNT(*) FROM notes_fts`).Scan(&ftsRows),
		"009 must have rebuilt notes_fts as a usable contentless-delete table")

	after := snapshotDB(t, openRawSQLite(t, path))
	assert.Equal(t, 9, after.version)
	assert.False(t, after.dirty, "a completed migration must clear the dirty flag")
	assert.Equal(t, 1, after.users)

	// ── 5. and reopening is a no-op, not a second recovery ──────────────────
	pool.Close()
	again, err := Open(path)
	require.NoError(t, err, "re-opening a recovered database must succeed")
	defer again.Close()
	version, _ = again.SchemaStatus()
	assert.Equal(t, 9, version)
}
