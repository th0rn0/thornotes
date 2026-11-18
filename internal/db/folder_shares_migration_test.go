package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/repository/sqlite"
)

// Migration 012 (SQLite) / 009 (MySQL) — the folder_shares table (ticket 616).
//
// The failure these exist for is the one this repo has already shipped once: a
// migration only ever exercised on a fresh install. A fresh database reaches the
// new version with no rows, so nothing proves that the rows an upgrading instance
// already has survive — and here the rows that matter are the note shares that
// keep resolving through /s/<token>, which this migration must not touch at all.

// seedFolderShareFixture inserts a user, a folder and one shared note at the
// pre-012 schema, and returns their ids and the note's share token.
func seedFolderShareFixture(t *testing.T, conn *sql.DB, diskPrefix string) (userID, folderID, noteID int64, token string) {
	t.Helper()

	res, err := conn.Exec(
		`INSERT INTO users (username, password_hash) VALUES (?, 'x')`, "upgrader_"+diskPrefix)
	require.NoError(t, err)
	userID, err = res.LastInsertId()
	require.NoError(t, err)

	res, err = conn.Exec(
		`INSERT INTO folders (user_id, parent_id, name, disk_path) VALUES (?, NULL, 'work', ?)`,
		userID, diskPrefix+"/work")
	require.NoError(t, err)
	folderID, err = res.LastInsertId()
	require.NoError(t, err)

	token = strings.Repeat("d", 32)
	res, err = conn.Exec(`
		INSERT INTO notes (user_id, folder_id, title, slug, disk_path, content, content_hash, tags, share_token)
		VALUES (?, ?, 'Surviving share', 'surviving-share', ?, '', '', '[]', ?)`,
		userID, folderID, diskPrefix+"/work/surviving-share.md", token)
	require.NoError(t, err)
	noteID, err = res.LastInsertId()
	require.NoError(t, err)

	return userID, folderID, noteID, token
}

// TestMigration012_FolderShares_PopulatedUpgrade is the acceptance test for this
// ticket's migration: a populated database at version 011 upgrades, the note
// share it already had still resolves through the query /s/<token> runs, and the
// new table is usable by the repository that will read it.
func TestMigration012_FolderShares_PopulatedUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "folder-shares.db")
	m, conn := sqliteMigrator(t, path)

	// Build the database at the PREVIOUS version and populate it. A fresh
	// database at the new version would prove nothing about existing rows.
	require.NoError(t, m.Migrate(11))
	version, _, err := m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(11), version, "the fixture must sit at the version before 012")
	require.False(t, tableExists(t, conn, "folder_shares"))

	userID, folderID, noteID, token := seedFolderShareFixture(t, conn, "upgrader-root")
	ctx := context.Background()

	// Upgrade, exactly as the app does on boot.
	require.NoError(t, m.Up())
	version, _, err = m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(12), version)
	require.True(t, tableExists(t, conn, "folder_shares"), "012 must create the table")

	// The property the ticket is about: an existing NOTE share still resolves,
	// through the query the app actually runs, and its row was not rewritten.
	noteRepo := sqlite.NewNoteRepo(conn, conn)
	note, err := noteRepo.GetByShareToken(ctx, token)
	require.NoError(t, err, "an existing note share must still resolve after the upgrade")
	assert.Equal(t, "Surviving share", note.Title)
	assert.Equal(t, noteID, note.ID)

	var (
		title      string
		shareToken sql.NullString
	)
	require.NoError(t, conn.QueryRow(
		`SELECT title, share_token FROM notes WHERE id = ?`, noteID).Scan(&title, &shareToken))
	assert.Equal(t, "Surviving share", title, "the upgrade must not touch existing note rows")
	require.True(t, shareToken.Valid)
	assert.Equal(t, token, shareToken.String, "the upgrade must not rotate or clear a note token")

	// The new table is usable by the repository that reads it — a migration that
	// created an unusable table would pass a tableExists check.
	shareRepo := sqlite.NewFolderShareRepo(conn, conn)
	folderToken := strings.Repeat("e", 32)
	require.NoError(t, shareRepo.UpsertToken(ctx, userID, folderID, folderToken))

	share, err := shareRepo.GetByToken(ctx, folderToken)
	require.NoError(t, err)
	assert.Equal(t, userID, share.UserID)
	assert.Equal(t, folderID, share.FolderID)
	assert.Nil(t, share.ExpiresAt, "a fresh folder share has no deadline (expiry is slice 2)")
	assert.Nil(t, share.LastViewedAt)

	// One live share per folder: re-sharing rotates the row rather than adding a
	// second live token, which is what makes "one row per folder" structural.
	rotated := strings.Repeat("f", 32)
	require.NoError(t, shareRepo.UpsertToken(ctx, userID, folderID, rotated))
	var shareRows int
	require.NoError(t, conn.QueryRow(
		`SELECT COUNT(*) FROM folder_shares WHERE folder_id = ?`, folderID).Scan(&shareRows))
	assert.Equal(t, 1, shareRows, "re-sharing must rotate in place, not accumulate tokens")
	_, err = shareRepo.GetByToken(ctx, folderToken)
	assert.Error(t, err, "the rotated token must be gone, not stored beside the new one")

	// Revoking is a DELETE, so a revoked link and a never-shared folder are the
	// same state (no row) — the property the anonymous 404 relies on.
	require.NoError(t, shareRepo.Clear(ctx, userID, folderID))
	require.NoError(t, conn.QueryRow(
		`SELECT COUNT(*) FROM folder_shares WHERE folder_id = ?`, folderID).Scan(&shareRows))
	assert.Zero(t, shareRows)
}

// TestMigration012_FolderShares_Down verifies the down half is present and drops
// only the new table. The app never runs this automatically (see schema.go: it
// refuses a database newer than the build), so it is exercised here and nowhere
// else.
func TestMigration012_FolderShares_Down(t *testing.T) {
	path := filepath.Join(t.TempDir(), "folder-shares-down.db")
	m, conn := sqliteMigrator(t, path)

	require.NoError(t, m.Migrate(11))
	_, _, noteID, token := seedFolderShareFixture(t, conn, "down-root")

	require.NoError(t, m.Up())
	require.True(t, tableExists(t, conn, "folder_shares"))

	// One step down: 012 -> 011.
	require.NoError(t, m.Steps(-1))
	version, _, err := m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(11), version)
	assert.False(t, tableExists(t, conn, "folder_shares"), "down must drop folder_shares")

	// The notes and their tokens are untouched: the down half drops a table, it
	// does not revoke links.
	var (
		title      string
		shareToken sql.NullString
	)
	require.NoError(t, conn.QueryRow(
		`SELECT title, share_token FROM notes WHERE id = ?`, noteID).Scan(&title, &shareToken))
	assert.Equal(t, "Surviving share", title)
	require.True(t, shareToken.Valid)
	assert.Equal(t, token, shareToken.String)

	// And it is re-appliable.
	require.NoError(t, m.Up())
	assert.True(t, tableExists(t, conn, "folder_shares"))
}

// TestMigration012_FolderShares_SQL pins the statements themselves, so a rewrite
// that drops a column, or reintroduces the two the contract refuses, is caught
// even if the round trips above happen to still pass.
func TestMigration012_FolderShares_SQL(t *testing.T) {
	up, err := migrationsFS.ReadFile("migrations/012_folder_shares.up.sql")
	require.NoError(t, err)
	down, err := migrationsFS.ReadFile("migrations/012_folder_shares.down.sql")
	require.NoError(t, err)

	upSQL := string(up)
	// Comments are stripped first: the migration's own explanation says "there is
	// no revoked_at, revoking is a DELETE", and the assertion is about the
	// columns, not about the prose that explains why they are absent.
	var ddl strings.Builder
	for _, line := range strings.Split(upSQL, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		ddl.WriteString(line)
		ddl.WriteString("\n")
	}
	declarations := ddl.String()

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS folder_shares",
		"token          TEXT     NOT NULL UNIQUE",
		"expires_at     DATETIME",
		"last_viewed_at DATETIME",
		"created_at     DATETIME NOT NULL DEFAULT (datetime('now'))",
		"REFERENCES folders(id) ON DELETE CASCADE",
		"REFERENCES users(id)   ON DELETE CASCADE",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_folder_shares_folder_id ON folder_shares(folder_id)",
	} {
		assert.Contains(t, declarations, want, "the SQLite up migration must carry %q", want)
	}

	// The two columns the contract removed, and why they must not come back:
	// revoking is a DELETE (so a revoked link has no row), and the view stamp is
	// spelled last_viewed_at on both surfaces.
	assert.NotContains(t, declarations, "revoked_at")
	assert.NotContains(t, declarations, "last_used_at")

	assert.Contains(t, string(down), "DROP TABLE IF EXISTS folder_shares;")
}

// TestMigration009_MySQL_FolderShares_SQL pins the MySQL halves structurally. The
// live upgrade is asserted separately, below, against the real MariaDB.
func TestMigration009_MySQL_FolderShares_SQL(t *testing.T) {
	up, err := mysqlMigrationsFS.ReadFile("mysql_migrations/009_folder_shares.up.sql")
	require.NoError(t, err)
	down, err := mysqlMigrationsFS.ReadFile("mysql_migrations/009_folder_shares.down.sql")
	require.NoError(t, err)

	upSQL := string(up)
	var ddl strings.Builder
	for _, line := range strings.Split(upSQL, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		ddl.WriteString(line)
		ddl.WriteString("\n")
	}
	declarations := ddl.String()

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS folder_shares",
		"token          VARCHAR(64)  NOT NULL",
		"expires_at     DATETIME     NULL DEFAULT NULL",
		"last_viewed_at DATETIME     NULL DEFAULT NULL",
		"created_at     DATETIME     NOT NULL DEFAULT (UTC_TIMESTAMP())",
		"CONSTRAINT fk_folder_shares_user   FOREIGN KEY (user_id)   REFERENCES users(id)   ON DELETE CASCADE",
		"CONSTRAINT fk_folder_shares_folder FOREIGN KEY (folder_id) REFERENCES folders(id) ON DELETE CASCADE",
		"UNIQUE KEY uq_folder_shares_token (token)",
		"UNIQUE KEY uq_folder_shares_folder_id (folder_id)",
	} {
		assert.Contains(t, declarations, want, "the MySQL up migration must carry %q", want)
	}

	assert.NotContains(t, declarations, "revoked_at")
	assert.NotContains(t, declarations, "last_used_at")

	assert.Contains(t, string(down), "DROP TABLE IF EXISTS folder_shares;")
}

// TestMigration009_MySQL_FolderShares_PopulatedUpgrade runs the same populated
// upgrade against the real MariaDB: at version 008, with a live note share, then
// up to 009, and the note share still resolves.
//
// The shared test schema is moved to 008 and back to the newest version, so it is
// restored at the end whatever happens: a downgraded schema left behind would
// fail every later test in the package for a reason that has nothing to do with
// them.
func TestMigration009_MySQL_FolderShares_PopulatedUpgrade(t *testing.T) {
	dsn := mysqlTestDSN(t)

	m, conn := mysqlMigrator(t, dsn)

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		require.NoError(t, err)
	}

	require.NoError(t, m.Migrate(8))
	version, _, err := m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(8), version, "the fixture must sit at the version before 009")

	t.Cleanup(func() {
		if err := m.Up(); err != nil && err != migrate.ErrNoChange {
			t.Errorf("restore schema to latest after the downgrade test: %v", err)
		}
	})

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	res, err := conn.Exec(`INSERT INTO users (username, password_hash) VALUES (?, 'x')`, "folder_upgrader_"+suffix)
	require.NoError(t, err)
	userID, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		// The foreign keys cascade, so removing the user removes its folder, its
		// notes and any folder share.
		_, _ = conn.Exec(`DELETE FROM users WHERE id = ?`, userID)
	})

	res, err = conn.Exec(
		`INSERT INTO folders (user_id, parent_id, name, disk_path) VALUES (?, NULL, 'work', ?)`,
		userID, "mysql-root-"+suffix+"/work")
	require.NoError(t, err)
	folderID, err := res.LastInsertId()
	require.NoError(t, err)

	token := strings.Repeat("g", 32)
	_, err = conn.Exec(`
		INSERT INTO notes (user_id, folder_id, title, slug, disk_path, content, content_hash, tags, share_token)
		VALUES (?, ?, 'MySQL surviving share', 'mysql-surviving-share', ?, '', '', '[]', ?)`,
		userID, folderID, "mysql-root-"+suffix+"/work/mysql-surviving-share.md", token)
	require.NoError(t, err)

	require.NoError(t, m.Up())
	version, _, err = m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(9), version)

	// The note share the database already had still resolves, and its row is
	// untouched.
	var (
		title      string
		shareToken sql.NullString
	)
	require.NoError(t, conn.QueryRow(
		`SELECT title, share_token FROM notes WHERE share_token = ?`, token).Scan(&title, &shareToken))
	assert.Equal(t, "MySQL surviving share", title, "an existing note share must survive the upgrade")
	require.True(t, shareToken.Valid)
	assert.Equal(t, token, shareToken.String)

	// Negative control: the new table must be real and writable, or a migration
	// that silently no-oped would satisfy every assertion above.
	_, err = conn.Exec(
		`INSERT INTO folder_shares (user_id, folder_id, token) VALUES (?, ?, ?)`,
		userID, folderID, strings.Repeat("h", 32))
	require.NoError(t, err, "the migrated table must accept a folder share")

	// One live share per folder, structurally.
	_, err = conn.Exec(
		`INSERT INTO folder_shares (user_id, folder_id, token) VALUES (?, ?, ?)`,
		userID, folderID, strings.Repeat("i", 32))
	require.Error(t, err, "folder_id must be unique: two live tokens for one folder must be unrepresentable")

	// Deleting the folder revokes the link by cascade — a share cannot outlive
	// the thing it points at.
	_, err = conn.Exec(`DELETE FROM folders WHERE id = ?`, folderID)
	require.NoError(t, err)

	var remaining int
	require.NoError(t, conn.QueryRow(
		`SELECT COUNT(*) FROM folder_shares WHERE folder_id = ?`, folderID).Scan(&remaining))
	assert.Zero(t, remaining, "deleting the folder must delete its share row")
}
