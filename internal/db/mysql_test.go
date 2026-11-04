package db

import (
	"database/sql"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mysqlTestDSN returns the DSN for MariaDB/MySQL integration tests, or skips
// the test if THORNOTES_TEST_MYSQL_DSN is not set. When THORNOTES_REQUIRE_MYSQL
// is "1" (CI) a missing DSN is a failure instead of a skip, so the MySQL leg
// cannot silently disappear.
func mysqlTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("THORNOTES_TEST_MYSQL_DSN")
	if dsn == "" {
		msg := "THORNOTES_TEST_MYSQL_DSN not set — skipping MariaDB/MySQL integration test"
		if os.Getenv("THORNOTES_REQUIRE_MYSQL") == "1" {
			t.Fatalf("%s (THORNOTES_REQUIRE_MYSQL=1, MySQL must not be skipped)", msg)
		}
		t.Skip(msg)
	}
	return dsn
}

// TestMySQL_Migrations verifies that OpenMySQL runs all migrations successfully
// and that every expected table exists in the database.
func TestMySQL_Migrations(t *testing.T) {
	dsn := mysqlTestDSN(t)

	pool, err := OpenMySQL(dsn)
	require.NoError(t, err)
	defer pool.Close()

	tables := []string{"users", "sessions", "folders", "notes", "api_tokens", "journals"}
	for _, table := range tables {
		var name string
		err := pool.ReadDB.QueryRow(
			"SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?",
			table,
		).Scan(&name)
		assert.NoError(t, err, "table %q should exist after migrations", table)
	}
}

// TestMySQL_Migrations_Idempotent verifies that running OpenMySQL a second time
// against an already-migrated database does not return an error.
func TestMySQL_Migrations_Idempotent(t *testing.T) {
	dsn := mysqlTestDSN(t)

	pool1, err := OpenMySQL(dsn)
	require.NoError(t, err)
	pool1.Close()

	pool2, err := OpenMySQL(dsn)
	require.NoError(t, err, "second OpenMySQL call should be a no-op, not an error")
	defer pool2.Close()
}

// TestMySQL_DirtyState_RefusesLoudly verifies that a database left dirty by an
// interrupted migration is refused with a typed error naming the version, rather
// than silently forced forward or failed with migrate's own message. Recovery is
// a human decision: inspect the schema, then force the version, or restore.
func TestMySQL_DirtyState_RefusesLoudly(t *testing.T) {
	dsn := mysqlTestDSN(t)

	// First open — clean migration.
	pool, err := OpenMySQL(dsn)
	require.NoError(t, err)

	// Simulate a crash mid-migration by marking the current version dirty.
	_, err = pool.WriteDB.Exec("UPDATE schema_migrations SET dirty = 1")
	require.NoError(t, err)
	pool.Close()

	// The database is repaired at the end of the test whatever happens, because
	// the MySQL suite shares one database across tests.
	t.Cleanup(func() {
		repair, err := sql.Open("mysql", dsn)
		if err != nil {
			return
		}
		defer repair.Close()
		_, _ = repair.Exec("UPDATE schema_migrations SET dirty = 0")
	})

	// Second open — must refuse, naming the dirty version.
	_, err = OpenMySQL(dsn)
	var dirty *ErrSchemaDirty
	require.ErrorAs(t, err, &dirty, "a dirty database must produce ErrSchemaDirty")
	assert.Contains(t, err.Error(), "is dirty")
	assert.Contains(t, err.Error(), "force version")

	// The dirty flag is left in place for the operator to inspect.
	check, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	defer check.Close()
	var flagged int
	require.NoError(t, check.QueryRow("SELECT dirty FROM schema_migrations LIMIT 1").Scan(&flagged))
	assert.Equal(t, 1, flagged, "the refusal must not modify the database")
}
