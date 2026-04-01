package db

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mysqlTestDSN returns the DSN for MariaDB/MySQL integration tests, or skips
// the test if THORNOTES_TEST_MYSQL_DSN is not set.
func mysqlTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("THORNOTES_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("THORNOTES_TEST_MYSQL_DSN not set — skipping MariaDB/MySQL integration test")
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

// TestMySQL_DirtyStateRecovery verifies that if a previous migration run was
// interrupted (leaving a dirty flag in schema_migrations), the next OpenMySQL
// call self-heals and returns a working pool.
func TestMySQL_DirtyStateRecovery(t *testing.T) {
	dsn := mysqlTestDSN(t)

	// First open — clean migration.
	pool, err := OpenMySQL(dsn)
	require.NoError(t, err)

	// Simulate a crash mid-migration by marking version 1 as dirty.
	_, err = pool.WriteDB.Exec("UPDATE schema_migrations SET dirty = 1 WHERE version = 1")
	require.NoError(t, err)
	pool.Close()

	// Second open — should detect the dirty state and self-heal.
	pool2, err := OpenMySQL(dsn)
	require.NoError(t, err, "OpenMySQL should recover from a dirty migration state automatically")
	defer pool2.Close()

	// Basic sanity check: the users table is queryable after recovery.
	var count int
	err = pool2.ReadDB.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	assert.NoError(t, err, "users table should be accessible after dirty state recovery")
}
