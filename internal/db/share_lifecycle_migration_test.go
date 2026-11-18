package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	migratemysql "github.com/golang-migrate/migrate/v4/database/mysql"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/repository/sqlite"
)

// Migration 011 (SQLite) / 008 (MySQL) — the share-link lifecycle columns.
//
// The failure these tests exist for is the one this repo has already shipped
// once: a migration only ever exercised on a fresh install. A fresh database
// reaches the new version with no rows, so nothing proves that the rows an
// upgrading instance already has survive. These tests therefore build a
// database at the *previous* version, put real shares in it, and upgrade.

// sqliteMigrator opens path and returns a migrate handle driven by the embedded
// SQLite migration set, plus the raw connection.
func sqliteMigrator(t *testing.T, path string) (*migrate.Migrate, *sql.DB) {
	t.Helper()

	migDB, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = migDB.Close() })

	src, err := iofs.New(migrationsFS, "migrations")
	require.NoError(t, err)
	driver, err := migratesqlite.WithInstance(migDB, &migratesqlite.Config{})
	require.NoError(t, err)
	m, err := migrate.NewWithInstance("iofs", src, "sqlite", driver)
	require.NoError(t, err)
	return m, migDB
}

// seedSharedNotes inserts a user and three notes at the pre-011 schema: two
// shared, one not. It returns the first shared note's id and both tokens.
func seedSharedNotes(t *testing.T, conn *sql.DB) (noteA int64, tokenA, tokenB string) {
	t.Helper()

	res, err := conn.Exec(
		`INSERT INTO users (username, password_hash) VALUES ('upgrader', 'x')`)
	require.NoError(t, err)
	userID, err := res.LastInsertId()
	require.NoError(t, err)

	tokenA = strings.Repeat("a", 32)
	tokenB = strings.Repeat("b", 32)
	notes := []struct {
		title, slug, path string
		token             any
	}{
		{"Shared Alpha", "shared-alpha", "Shared Alpha.md", tokenA},
		{"Shared Beta", "shared-beta", "Shared Beta.md", tokenB},
		{"Private Gamma", "private-gamma", "Private Gamma.md", nil},
	}
	for _, n := range notes {
		res, err := conn.Exec(`
			INSERT INTO notes (user_id, title, slug, disk_path, content, content_hash, tags, share_token)
			VALUES (?, ?, ?, ?, '', '', '[]', ?)`,
			userID, n.title, n.slug, n.path, n.token)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		if n.title == "Shared Alpha" {
			noteA = id
		}
	}
	return noteA, tokenA, tokenB
}

// TestMigration011_ShareLifecycle_PopulatedUpgrade is the acceptance test for
// ticket 601's migration: a populated database at version 010 upgrades, and the
// shares it already had still resolve with expires_at NULL — i.e. the upgrade
// does not retro-expire a single link.
func TestMigration011_ShareLifecycle_PopulatedUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "populated.db")
	m, conn := sqliteMigrator(t, path)

	// Build the database at the PREVIOUS version and populate it. A fresh
	// database at the new version would prove nothing about existing rows.
	require.NoError(t, m.Migrate(10))
	version, _, err := m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(10), version, "the fixture must sit at the version before 011")

	noteA, tokenA, tokenB := seedSharedNotes(t, conn)

	// Upgrade, exactly as the app does on boot.
	require.NoError(t, m.Up())
	version, _, err = m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(latestSQLiteVersion(t)), version)

	// The rows resolve through the query the app actually runs on /s/<token>,
	// not through a hand-written SELECT: that is what "still resolves" means.
	repo := sqlite.NewNoteRepo(conn, conn)
	ctx := context.Background()
	for _, token := range []string{tokenA, tokenB} {
		note, err := repo.GetByShareToken(ctx, token)
		require.NoError(t, err, "an existing share must still resolve after the upgrade")
		assert.Nil(t, note.ExpiresAt,
			"expires_at must be NULL after the upgrade: no expiry is the default and an upgrade must not retro-expire a link")
		assert.Nil(t, note.LastViewedAt, "an existing link has no recorded view yet")
	}

	// Negative control: prove the columns are real and writable. Without this,
	// a migration that silently no-oped would satisfy every assertion above —
	// "the column is missing" and "the column is NULL" look identical to a
	// SELECT.
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, repo.SetShareExpiry(ctx, 1, noteA, &now))
	touched, err := repo.TouchShareView(ctx, noteA, nil, now, 10*time.Minute)
	require.NoError(t, err)
	require.True(t, touched, "the migrated table must accept a last_viewed_at write")

	stored, err := repo.GetByShareToken(ctx, tokenA)
	require.NoError(t, err)
	require.NotNil(t, stored.ExpiresAt, "expires_at must persist once written")
	require.NotNil(t, stored.LastViewedAt, "last_viewed_at must persist once written")
	assert.WithinDuration(t, now, stored.ExpiresAt.UTC(), time.Second)
}

// TestMigration011_ShareLifecycle_Down verifies the down half is present and
// removes both columns without disturbing the rows that survive it. The app
// never runs this automatically (see schema.go: it refuses a database newer
// than the build), so it is exercised here and nowhere else.
func TestMigration011_ShareLifecycle_Down(t *testing.T) {
	path := filepath.Join(t.TempDir(), "down.db")
	m, conn := sqliteMigrator(t, path)

	require.NoError(t, m.Migrate(10))
	_, tokenA, _ := seedSharedNotes(t, conn)
	require.NoError(t, m.Up())

	require.True(t, columnExists(t, conn, "expires_at"))
	require.True(t, columnExists(t, conn, "last_viewed_at"))

	// Step down to 010, through whatever landed above 011 first (012 adds a
	// table of its own and leaves these columns alone). The target is what
	// matters, not the number of steps.
	applied, _, err := m.Version()
	require.NoError(t, err)
	require.GreaterOrEqual(t, int(applied), 11, "011 must be applied for its down half to be exercised")
	require.NoError(t, m.Steps(-(int(applied) - 10)))
	version, _, err := m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(10), version)

	assert.False(t, columnExists(t, conn, "expires_at"), "down must drop expires_at")
	assert.False(t, columnExists(t, conn, "last_viewed_at"), "down must drop last_viewed_at")

	// The notes and their tokens are untouched: the down half removes columns,
	// it does not revoke links.
	var title string
	require.NoError(t, conn.QueryRow(
		`SELECT title FROM notes WHERE share_token = ?`, tokenA).Scan(&title))
	assert.Equal(t, "Shared Alpha", title)

	// And it is re-appliable.
	require.NoError(t, m.Up())
	assert.True(t, columnExists(t, conn, "expires_at"))
}

// TestMigration011_ShareLifecycle_SQL pins the statements themselves, so a
// rewrite that drops a column from one half is caught even if the round trip
// above happens to still pass on SQLite's permissive ALTER TABLE.
func TestMigration011_ShareLifecycle_SQL(t *testing.T) {
	up, err := migrationsFS.ReadFile("migrations/011_share_lifecycle.up.sql")
	require.NoError(t, err)
	down, err := migrationsFS.ReadFile("migrations/011_share_lifecycle.down.sql")
	require.NoError(t, err)

	for _, want := range []string{
		"ALTER TABLE notes ADD COLUMN expires_at DATETIME;",
		"ALTER TABLE notes ADD COLUMN last_viewed_at DATETIME;",
	} {
		assert.Contains(t, string(up), want)
	}
	for _, want := range []string{
		"ALTER TABLE notes DROP COLUMN last_viewed_at;",
		"ALTER TABLE notes DROP COLUMN expires_at;",
	} {
		assert.Contains(t, string(down), want)
	}

	// No DEFAULT and no NOT NULL: an expiry column with a default would expire
	// links the moment they were created, which is exactly what the ticket says
	// must not happen.
	assert.NotContains(t, string(up), "NOT NULL")
	assert.NotContains(t, string(up), "DEFAULT")
}

// TestMigration008_MySQL_ShareLifecycle_SQL pins the MySQL halves structurally.
func TestMigration008_MySQL_ShareLifecycle_SQL(t *testing.T) {
	up, err := mysqlMigrationsFS.ReadFile("mysql_migrations/008_share_lifecycle.up.sql")
	require.NoError(t, err)
	down, err := mysqlMigrationsFS.ReadFile("mysql_migrations/008_share_lifecycle.down.sql")
	require.NoError(t, err)

	upSQL := string(up)
	assert.Contains(t, upSQL, "ADD COLUMN expires_at     DATETIME NULL DEFAULT NULL")
	assert.Contains(t, upSQL, "ADD COLUMN last_viewed_at DATETIME NULL DEFAULT NULL")

	downSQL := string(down)
	assert.Contains(t, downSQL, "DROP COLUMN last_viewed_at")
	assert.Contains(t, downSQL, "DROP COLUMN expires_at")
}

// TestMigration008_MySQL_ShareLifecycle_PopulatedUpgrade runs the same populated
// upgrade against the real MariaDB. It skips without THORNOTES_TEST_MYSQL_DSN
// and fails rather than skips when THORNOTES_REQUIRE_MYSQL=1.
//
// The shared test schema is moved to 007 and back to 008, so it is restored at
// the end whatever happens: a downgraded schema left behind would fail every
// later test in the package for a reason that has nothing to do with them.
func TestMigration008_MySQL_ShareLifecycle_PopulatedUpgrade(t *testing.T) {
	dsn := mysqlTestDSN(t)

	m, conn := mysqlMigrator(t, dsn)

	// Restore the shared schema to the newest version first, so this test cannot
	// start from whatever a previous failure left behind. A schema already at the
	// newest version is the normal case and is not an error.
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		require.NoError(t, err)
	}

	// Populated at the previous version, not a fresh install.
	require.NoError(t, m.Migrate(7))
	version, _, err := m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(7), version, "the fixture must sit at the version before 008")

	t.Cleanup(func() {
		if err := m.Up(); err != nil && err != migrate.ErrNoChange {
			t.Errorf("restore schema to latest after the downgrade test: %v", err)
		}
	})

	suffix := time.Now().UnixNano()
	username := "upgrader_" + strconv.FormatInt(suffix, 10)
	res, err := conn.Exec(`INSERT INTO users (username, password_hash) VALUES (?, 'x')`, username)
	require.NoError(t, err)
	userID, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = conn.Exec(`DELETE FROM notes WHERE user_id = ?`, userID)
		_, _ = conn.Exec(`DELETE FROM users WHERE id = ?`, userID)
	})

	token := strings.Repeat("c", 32)
	_, err = conn.Exec(`
		INSERT INTO notes (user_id, title, slug, disk_path, content, content_hash, tags, share_token)
		VALUES (?, 'MySQL Shared', 'mysql-shared', ?, '', '', '[]', ?)`,
		userID, "MySQL Shared "+strconv.FormatInt(suffix, 10)+".md", token)
	require.NoError(t, err)

	require.NoError(t, m.Up())
	version, _, err = m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(8), version)

	var (
		title        string
		expiresAt    sql.NullTime
		lastViewedAt sql.NullTime
	)
	require.NoError(t, conn.QueryRow(`
		SELECT title, expires_at, last_viewed_at FROM notes WHERE share_token = ?`, token).
		Scan(&title, &expiresAt, &lastViewedAt))
	assert.Equal(t, "MySQL Shared", title, "an existing share must still resolve after the upgrade")
	assert.False(t, expiresAt.Valid, "expires_at must be NULL for a link that predates the migration")
	assert.False(t, lastViewedAt.Valid)

	// Negative control: the columns must be writable, so a no-op migration
	// cannot masquerade as a successful one.
	now := time.Now().UTC().Truncate(time.Second)
	_, err = conn.Exec(`UPDATE notes SET expires_at = ?, last_viewed_at = ? WHERE share_token = ?`,
		now, now, token)
	require.NoError(t, err)
	require.NoError(t, conn.QueryRow(`
		SELECT expires_at, last_viewed_at FROM notes WHERE share_token = ?`, token).
		Scan(&expiresAt, &lastViewedAt))
	require.True(t, expiresAt.Valid)
	require.True(t, lastViewedAt.Valid)
}

// mysqlMigrator returns a migrate handle against the shared MySQL test database.
func mysqlMigrator(t *testing.T, dsn string) (*migrate.Migrate, *sql.DB) {
	t.Helper()

	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	conn, err := sql.Open("mysql", dsn+sep+"multiStatements=true")
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	src, err := iofs.New(mysqlMigrationsFS, "mysql_migrations")
	require.NoError(t, err)
	driver, err := migratemysql.WithInstance(conn, &migratemysql.Config{})
	require.NoError(t, err)
	m, err := migrate.NewWithInstance("iofs", src, "mysql", driver)
	require.NoError(t, err)
	return m, conn
}
