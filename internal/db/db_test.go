package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDB_Open_And_Close(t *testing.T) {
	dir := t.TempDir()
	pool, err := Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	require.NotNil(t, pool)

	err = pool.WriteDB.Ping()
	require.NoError(t, err)

	pool.Close()
}

func TestDB_Open_InvalidPath(t *testing.T) {
	// Pass a path in a non-existent directory.
	_, err := Open("/nonexistent/path/to/db.sqlite")
	require.Error(t, err)
}

func TestDB_Close_NilSafe(t *testing.T) {
	// Create a Pool with nil DBs — Close should not panic.
	p := &Pool{ReadDB: nil, WriteDB: nil}
	assert.NotPanics(t, func() {
		p.Close()
	})
}

// ─── rebasePath ───────────────────────────────────────────────────────────────

func TestRebasePath_PrefixMatch(t *testing.T) {
	result := rebasePath("42/Work/todo.md", "42", "abc-uuid")
	assert.Equal(t, "abc-uuid/Work/todo.md", result)
}

func TestRebasePath_ExactMatch(t *testing.T) {
	result := rebasePath("42", "42", "abc-uuid")
	assert.Equal(t, "abc-uuid", result)
}

func TestRebasePath_NoMatch(t *testing.T) {
	result := rebasePath("99/other.md", "42", "abc-uuid")
	assert.Equal(t, "99/other.md", result)
}

func TestRebasePath_PartialNoMatch(t *testing.T) {
	// "420/file.md" should NOT match prefix "42"
	result := rebasePath("420/file.md", "42", "abc-uuid")
	assert.Equal(t, "420/file.md", result)
}

// ─── EnsureUserUUIDs ──────────────────────────────────────────────────────────

func openTestPool(t *testing.T) *Pool {
	t.Helper()
	dir := t.TempDir()
	pool, err := Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })
	return pool
}

func createUserWithoutUUID(t *testing.T, pool *Pool, username string) int64 {
	t.Helper()
	var id int64
	err := pool.WriteDB.QueryRowContext(context.Background(),
		`INSERT INTO users (username, password_hash, uuid) VALUES (?, 'hash', '') RETURNING id`,
		username,
	).Scan(&id)
	require.NoError(t, err)
	return id
}

func TestEnsureUserUUIDs_NoUsers(t *testing.T) {
	pool := openTestPool(t)
	notesRoot := t.TempDir()
	err := EnsureUserUUIDs(context.Background(), pool.WriteDB, notesRoot)
	require.NoError(t, err)
}

func TestEnsureUserUUIDs_UserAlreadyHasUUID(t *testing.T) {
	pool := openTestPool(t)
	notesRoot := t.TempDir()

	// Insert a user with a UUID already set.
	_, err := pool.WriteDB.ExecContext(context.Background(),
		`INSERT INTO users (username, password_hash, uuid) VALUES ('alice', 'hash', 'existing-uuid')`)
	require.NoError(t, err)

	err = EnsureUserUUIDs(context.Background(), pool.WriteDB, notesRoot)
	require.NoError(t, err)

	var uuid string
	require.NoError(t, pool.WriteDB.QueryRowContext(context.Background(),
		`SELECT uuid FROM users WHERE username = 'alice'`).Scan(&uuid))
	assert.Equal(t, "existing-uuid", uuid) // unchanged
}

func TestEnsureUserUUIDs_MigratesUser(t *testing.T) {
	pool := openTestPool(t)
	notesRoot := t.TempDir()

	id := createUserWithoutUUID(t, pool, "bob")

	// Create an on-disk directory using the old int-based path.
	oldDir := filepath.Join(notesRoot, fmt.Sprintf("%d", id))
	require.NoError(t, os.MkdirAll(oldDir, 0700))

	// Create a folder and note record with the old disk paths.
	_, err := pool.WriteDB.ExecContext(context.Background(),
		`INSERT INTO folders (user_id, name, disk_path) VALUES (?, 'Work', ?)`,
		id, fmt.Sprintf("%d/Work", id),
	)
	require.NoError(t, err)

	_, err = pool.WriteDB.ExecContext(context.Background(),
		`INSERT INTO notes (user_id, title, slug, disk_path, content, content_hash, tags)
		 VALUES (?, 'Todo', 'todo', ?, '', 'abc', '[]')`,
		id, fmt.Sprintf("%d/todo.md", id),
	)
	require.NoError(t, err)

	err = EnsureUserUUIDs(context.Background(), pool.WriteDB, notesRoot)
	require.NoError(t, err)

	// UUID should be set and non-empty.
	var uuid string
	require.NoError(t, pool.WriteDB.QueryRowContext(context.Background(),
		`SELECT uuid FROM users WHERE id = ?`, id).Scan(&uuid))
	assert.NotEmpty(t, uuid)

	// disk_path in folders should now use the UUID prefix.
	var folderPath string
	require.NoError(t, pool.WriteDB.QueryRowContext(context.Background(),
		`SELECT disk_path FROM folders WHERE user_id = ?`, id).Scan(&folderPath))
	assert.Contains(t, folderPath, uuid)

	// disk_path in notes should now use the UUID prefix.
	var notePath string
	require.NoError(t, pool.WriteDB.QueryRowContext(context.Background(),
		`SELECT disk_path FROM notes WHERE user_id = ?`, id).Scan(&notePath))
	assert.Contains(t, notePath, uuid)
}

func TestEnsureUserUUIDs_NoOldDirOnDisk(t *testing.T) {
	pool := openTestPool(t)
	notesRoot := t.TempDir()

	// User without UUID but no on-disk directory (pure DB migration).
	createUserWithoutUUID(t, pool, "charlie")

	err := EnsureUserUUIDs(context.Background(), pool.WriteDB, notesRoot)
	require.NoError(t, err)
}

func TestEnsureUserUUIDs_MigrationFailure_ContinuesAndReturnsNil(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	pool := openTestPool(t)
	notesRoot := t.TempDir()

	id := createUserWithoutUUID(t, pool, "dave")

	// Create the old disk directory so the rename path is exercised.
	oldDir := filepath.Join(notesRoot, fmt.Sprintf("%d", id))
	require.NoError(t, os.MkdirAll(oldDir, 0700))

	// Make notesRoot non-writable so os.Rename fails.
	require.NoError(t, os.Chmod(notesRoot, 0500))
	t.Cleanup(func() { _ = os.Chmod(notesRoot, 0700) })

	// EnsureUserUUIDs should return nil even though migration failed for this user.
	err := EnsureUserUUIDs(context.Background(), pool.WriteDB, notesRoot)
	require.NoError(t, err)
}

func TestMigrateUserToUUID_FolderPathUpdated(t *testing.T) {
	pool := openTestPool(t)
	notesRoot := t.TempDir()

	id := createUserWithoutUUID(t, pool, "eve")
	_, err := pool.WriteDB.ExecContext(context.Background(),
		`INSERT INTO folders (user_id, name, disk_path) VALUES (?, 'Work', ?)`,
		id, fmt.Sprintf("%d/Work", id),
	)
	require.NoError(t, err)

	newUUID := "test-uuid-for-eve"
	err = migrateUserToUUID(context.Background(), pool.WriteDB, notesRoot, id, newUUID)
	require.NoError(t, err)

	var folderPath string
	require.NoError(t, pool.WriteDB.QueryRowContext(context.Background(),
		`SELECT disk_path FROM folders WHERE user_id = ?`, id).Scan(&folderPath))
	assert.Equal(t, "test-uuid-for-eve/Work", folderPath)
}
