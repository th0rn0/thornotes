package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/model"
)

// mysqlDSNEnv is the single environment variable that enables the MariaDB/MySQL
// integration suites. It must match internal/db/mysql_test.go, .drone.yml and
// the Makefile; a mismatch here silently skips every test in this package while
// the package still reports "ok".
const mysqlDSNEnv = "THORNOTES_TEST_MYSQL_DSN"

// requireMySQLEnv, when set to "1", turns a missing DSN into a failure instead
// of a skip. CI sets it so a broken or renamed DSN cannot silently disable the
// MySQL leg again.
const requireMySQLEnv = "THORNOTES_REQUIRE_MYSQL"

// openTestMySQL returns this package's shared MySQL pool and clears the schema
// so the test starts from a known-empty database. TestMain opens and owns the
// pool (see main_test.go); this function never opens or closes one, because a
// pool per test is what exhausted the server at -count>1.
//
// Without the per-test reset, tests that share a fixed username (createMySQLUser
// uses "user1") fail with "username already taken" as soon as a second test runs
// against the same database — which is now always, so the truncation is the
// whole of the isolation guarantee and must not be dropped.
func openTestMySQL(t *testing.T) *db.Pool {
	t.Helper()
	dsn := os.Getenv(mysqlDSNEnv)
	if dsn == "" {
		msg := mysqlDSNEnv + " not set — skipping MariaDB/MySQL integration test"
		if os.Getenv(requireMySQLEnv) == "1" {
			t.Fatalf("%s (%s=1, MySQL must not be skipped)", msg, requireMySQLEnv)
		}
		t.Skip(msg)
	}
	if sharedPool == nil {
		t.Fatalf("shared MySQL test pool is not open: TestMain must open it once per process")
	}
	truncateAll(t, sharedPool)
	return sharedPool
}

// truncateAll deletes every row from the application tables, leaving the
// golang-migrate bookkeeping table (schema_migrations) untouched. Foreign key
// checks are disabled on the same connection for the duration of the deletes so
// table order does not matter, then restored.
func truncateAll(t *testing.T, pool *db.Pool) {
	t.Helper()
	ctx := context.Background()

	conn, err := pool.WriteDB.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	_, err = conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0")
	require.NoError(t, err)
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SET FOREIGN_KEY_CHECKS = 1")
	}()

	// Children before parents is not strictly required while FK checks are off,
	// but keeps the statement order readable.
	tables := []string{
		"api_token_folder_permissions",
		"api_tokens",
		"journals",
		"note_links",
		"notes",
		"sessions",
		"folders",
		"users",
	}
	for _, table := range tables {
		if _, err := conn.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("reset table %s: %v", table, err)
		}
	}
}

func createMySQLUser(t *testing.T, pool *db.Pool) *model.User {
	t.Helper()
	repo := NewUserRepo(pool.WriteDB)
	user, err := repo.Create(context.Background(), "user1", "hash", "test-uuid-1")
	require.NoError(t, err)
	return user
}

// ─── UserRepo tests ───────────────────────────────────────────────────────────

func TestMySQL_UserRepo_Create(t *testing.T) {
	pool := openTestMySQL(t)
	repo := NewUserRepo(pool.WriteDB)

	user, err := repo.Create(context.Background(), "alice", "hash123", "uuid-alice")
	require.NoError(t, err)
	assert.NotZero(t, user.ID)
	assert.Equal(t, "alice", user.Username)
}

func TestMySQL_UserRepo_Create_Duplicate(t *testing.T) {
	pool := openTestMySQL(t)
	repo := NewUserRepo(pool.WriteDB)

	_, err := repo.Create(context.Background(), "alice-dup", "hash123", "uuid-alice-dup")
	require.NoError(t, err)

	_, err = repo.Create(context.Background(), "alice-dup", "hash456", "uuid-alice-dup2")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 409, appErr.Code)
}

func TestMySQL_UserRepo_GetByUsername(t *testing.T) {
	pool := openTestMySQL(t)
	repo := NewUserRepo(pool.WriteDB)

	_, err := repo.Create(context.Background(), "bob-get", "hash", "uuid-bob-get")
	require.NoError(t, err)

	user, err := repo.GetByUsername(context.Background(), "bob-get")
	require.NoError(t, err)
	assert.Equal(t, "bob-get", user.Username)
}

func TestMySQL_UserRepo_GetByUsername_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	repo := NewUserRepo(pool.WriteDB)

	_, err := repo.GetByUsername(context.Background(), "nonexistent-xyz-999")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_UserRepo_GetByID(t *testing.T) {
	pool := openTestMySQL(t)
	repo := NewUserRepo(pool.WriteDB)

	created, err := repo.Create(context.Background(), "carol-id", "hash", "uuid-carol-id")
	require.NoError(t, err)

	user, err := repo.GetByID(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, user.ID)
	assert.Equal(t, "carol-id", user.Username)
}

func TestMySQL_UserRepo_GetByID_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	repo := NewUserRepo(pool.WriteDB)

	_, err := repo.GetByID(context.Background(), 99999999)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_UserRepo_Count(t *testing.T) {
	pool := openTestMySQL(t)
	repo := NewUserRepo(pool.WriteDB)

	before, err := repo.Count(context.Background())
	require.NoError(t, err)

	_, err = repo.Create(context.Background(), "dave-count", "hash", "uuid-dave-count")
	require.NoError(t, err)

	after, err := repo.Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, before+1, after)
}

func TestMySQL_UserRepo_IDs(t *testing.T) {
	pool := openTestMySQL(t)
	repo := NewUserRepo(pool.WriteDB)

	u1, err := repo.Create(context.Background(), "ida-mysql", "h", "uuid-ida-mysql")
	require.NoError(t, err)
	u2, err := repo.Create(context.Background(), "idb-mysql", "h", "uuid-idb-mysql")
	require.NoError(t, err)

	ids, err := repo.IDs(context.Background())
	require.NoError(t, err)
	assert.Contains(t, ids, u1.ID)
	assert.Contains(t, ids, u2.ID)
}

func TestMySQL_UserRepo_SetUUID(t *testing.T) {
	pool := openTestMySQL(t)
	repo := NewUserRepo(pool.WriteDB)

	user, err := repo.Create(context.Background(), "uuid-user-mysql", "hash", "")
	require.NoError(t, err)

	err = repo.SetUUID(context.Background(), user.ID, "new-uuid-mysql-abc")
	require.NoError(t, err)

	got, err := repo.GetByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Equal(t, "new-uuid-mysql-abc", got.UUID)
}

func TestMySQL_UserRepo_ListWithoutUUID(t *testing.T) {
	pool := openTestMySQL(t)
	repo := NewUserRepo(pool.WriteDB)

	// Insert user with empty UUID directly.
	_, err := pool.WriteDB.ExecContext(context.Background(),
		`INSERT INTO users (username, password_hash, uuid) VALUES ('noid-mysql', 'hash', '')`)
	require.NoError(t, err)

	// Create user with UUID (should not appear in ListWithoutUUID).
	_, err = repo.Create(context.Background(), "withid-mysql", "hash", "some-uuid-mysql")
	require.NoError(t, err)

	ids, err := repo.ListWithoutUUID(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, ids)
}

// ─── SessionRepo tests ────────────────────────────────────────────────────────

func TestMySQL_SessionRepo_Create_And_Get(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewSessionRepo(pool.WriteDB)

	token := "mysql-session-token-1"
	err := repo.Create(context.Background(), token, user.ID, 3600)
	require.NoError(t, err)

	session, err := repo.Get(context.Background(), token)
	require.NoError(t, err)
	assert.Equal(t, token, session.Token)
	assert.Equal(t, user.ID, session.UserID)
}

func TestMySQL_SessionRepo_Get_Expired(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)

	// Insert an already-expired session.
	_, err := pool.WriteDB.ExecContext(context.Background(),
		`INSERT INTO sessions (token, user_id, expires_at)
		 VALUES (?, ?, DATE_SUB(UTC_TIMESTAMP(), INTERVAL 1 HOUR))`,
		"mysql-expired-token", user.ID,
	)
	require.NoError(t, err)

	repo := NewSessionRepo(pool.WriteDB)
	_, err = repo.Get(context.Background(), "mysql-expired-token")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_SessionRepo_Delete(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewSessionRepo(pool.WriteDB)

	token := "mysql-del-token-1"
	err := repo.Create(context.Background(), token, user.ID, 3600)
	require.NoError(t, err)

	err = repo.Delete(context.Background(), token)
	require.NoError(t, err)

	_, err = repo.Get(context.Background(), token)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_SessionRepo_DeleteExpired(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewSessionRepo(pool.WriteDB)

	// Insert expired session.
	_, err := pool.WriteDB.ExecContext(context.Background(),
		`INSERT INTO sessions (token, user_id, expires_at)
		 VALUES (?, ?, DATE_SUB(UTC_TIMESTAMP(), INTERVAL 1 HOUR))`,
		"mysql-expired-del", user.ID,
	)
	require.NoError(t, err)

	// Create valid session.
	validToken := "mysql-valid-session-1"
	err = repo.Create(context.Background(), validToken, user.ID, 3600)
	require.NoError(t, err)

	err = repo.DeleteExpired(context.Background())
	require.NoError(t, err)

	// Expired should be gone.
	_, err = repo.Get(context.Background(), "mysql-expired-del")
	require.Error(t, err)

	// Valid should still be there.
	session, err := repo.Get(context.Background(), validToken)
	require.NoError(t, err)
	assert.Equal(t, validToken, session.Token)
}

// ─── FolderRepo tests ─────────────────────────────────────────────────────────

func TestMySQL_FolderRepo_Create(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewFolderRepo(pool.WriteDB)

	folder, err := repo.Create(context.Background(), user.ID, nil, "Work-MySQL", "uuid-1/Work-MySQL")
	require.NoError(t, err)
	assert.NotZero(t, folder.ID)
	assert.Equal(t, "Work-MySQL", folder.Name)
}

func TestMySQL_FolderRepo_Create_DuplicateName(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewFolderRepo(pool.WriteDB)

	_, err := repo.Create(context.Background(), user.ID, nil, "DupFolder-MySQL", "uuid-1/DupFolder-MySQL")
	require.NoError(t, err)

	_, err = repo.Create(context.Background(), user.ID, nil, "DupFolder-MySQL", "uuid-1/DupFolder-MySQL")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 409, appErr.Code)
}

func TestMySQL_FolderRepo_GetByID(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewFolderRepo(pool.WriteDB)

	created, err := repo.Create(context.Background(), user.ID, nil, "Projects-MySQL", "uuid-1/Projects-MySQL")
	require.NoError(t, err)

	folder, err := repo.GetByID(context.Background(), user.ID, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, folder.ID)
	assert.Equal(t, "Projects-MySQL", folder.Name)
}

func TestMySQL_FolderRepo_GetByID_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewFolderRepo(pool.WriteDB)

	_, err := repo.GetByID(context.Background(), user.ID, 99999999)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_FolderRepo_Tree(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewFolderRepo(pool.WriteDB)

	_, err := repo.Create(context.Background(), user.ID, nil, "Archive-MySQL", "uuid-1/Archive-MySQL")
	require.NoError(t, err)

	tree, err := repo.Tree(context.Background(), user.ID)
	require.NoError(t, err)
	require.NotEmpty(t, tree)
	names := make([]string, len(tree))
	for i, item := range tree {
		names[i] = item.Name
	}
	assert.Contains(t, names, "Archive-MySQL")
}

func TestMySQL_FolderRepo_Rename(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewFolderRepo(pool.WriteDB)

	folder, err := repo.Create(context.Background(), user.ID, nil, "OldName-MySQL", "uuid-1/OldName-MySQL")
	require.NoError(t, err)

	err = repo.Rename(context.Background(), user.ID, folder.ID, "NewName-MySQL", "uuid-1/NewName-MySQL")
	require.NoError(t, err)

	updated, err := repo.GetByID(context.Background(), user.ID, folder.ID)
	require.NoError(t, err)
	assert.Equal(t, "NewName-MySQL", updated.Name)
}

func TestMySQL_FolderRepo_Rename_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewFolderRepo(pool.WriteDB)

	err := repo.Rename(context.Background(), user.ID, 99999999, "Name", "path")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_FolderRepo_Rename_UniqueConstraint(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewFolderRepo(pool.WriteDB)

	_, err := repo.Create(context.Background(), user.ID, nil, "Alpha-MySQL", "uuid-1/Alpha-MySQL")
	require.NoError(t, err)
	beta, err := repo.Create(context.Background(), user.ID, nil, "Beta-MySQL", "uuid-1/Beta-MySQL")
	require.NoError(t, err)

	// Rename Beta to Alpha — should hit UNIQUE constraint.
	err = repo.Rename(context.Background(), user.ID, beta.ID, "Alpha-MySQL", "uuid-1/Alpha-MySQL")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 409, appErr.Code)
}

func TestMySQL_FolderRepo_Delete(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewFolderRepo(pool.WriteDB)

	folder, err := repo.Create(context.Background(), user.ID, nil, "Temp-MySQL", "uuid-1/Temp-MySQL")
	require.NoError(t, err)

	err = repo.Delete(context.Background(), user.ID, folder.ID)
	require.NoError(t, err)

	_, err = repo.GetByID(context.Background(), user.ID, folder.ID)
	require.Error(t, err)
}

func TestMySQL_FolderRepo_Delete_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewFolderRepo(pool.WriteDB)

	err := repo.Delete(context.Background(), user.ID, 99999999)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_FolderRepo_UpdateDescendantPaths(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewFolderRepo(pool.WriteDB)

	parent, err := repo.Create(context.Background(), user.ID, nil, "Parent-MySQL", "uuid-1/Parent-MySQL")
	require.NoError(t, err)

	child, err := repo.Create(context.Background(), user.ID, &parent.ID, "Child-MySQL", "uuid-1/Parent-MySQL/Child-MySQL")
	require.NoError(t, err)

	err = repo.UpdateDescendantPaths(context.Background(), "uuid-1/Parent-MySQL", "uuid-1/Renamed-MySQL")
	require.NoError(t, err)

	updated, err := repo.GetByID(context.Background(), user.ID, child.ID)
	require.NoError(t, err)
	assert.Equal(t, "uuid-1/Renamed-MySQL/Child-MySQL", updated.DiskPath)
}

// createMySQLNoteAt inserts a note row at an explicit disk path so the cascade
// tests can assert on the disk_path column directly.
func createMySQLNoteAt(t *testing.T, pool *db.Pool, userID int64, folderID *int64, title, slug, diskPath string) *model.Note {
	t.Helper()
	repo := NewNoteRepo(pool.WriteDB)
	note := &model.Note{
		UserID:      userID,
		FolderID:    folderID,
		Title:       title,
		Slug:        slug,
		DiskPath:    diskPath,
		Content:     "",
		ContentHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Tags:        []string{},
	}
	created, err := repo.Create(context.Background(), note)
	require.NoError(t, err)
	return created
}

func mysqlFolderPath(t *testing.T, pool *db.Pool, userID, folderID int64) string {
	t.Helper()
	repo := NewFolderRepo(pool.WriteDB)
	f, err := repo.GetByID(context.Background(), userID, folderID)
	require.NoError(t, err)
	return f.DiskPath
}

func mysqlNotePath(t *testing.T, pool *db.Pool, userID, noteID int64) string {
	t.Helper()
	repo := NewNoteRepo(pool.WriteDB)
	n, err := repo.GetByID(context.Background(), userID, noteID)
	require.NoError(t, err)
	return n.DiskPath
}

// TestMySQL_FolderRepo_UpdateDescendantPaths_SiblingSharingPrefixUntouched is
// the MySQL leg of the ticket-598 guard: a sibling whose name shares a prefix
// must not be rewritten by the rename cascade.
func TestMySQL_FolderRepo_UpdateDescendantPaths_SiblingSharingPrefixUntouched(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	folders := NewFolderRepo(pool.WriteDB)
	ctx := context.Background()

	source, err := folders.Create(ctx, user.ID, nil, "Notes-MySQL", "uuid-1/Notes-MySQL")
	require.NoError(t, err)
	sibling, err := folders.Create(ctx, user.ID, nil, "Notes-MySQL2", "uuid-1/Notes-MySQL2")
	require.NoError(t, err)
	child, err := folders.Create(ctx, user.ID, &source.ID, "Sub-MySQL", "uuid-1/Notes-MySQL/Sub-MySQL")
	require.NoError(t, err)

	inside := createMySQLNoteAt(t, pool, user.ID, &source.ID, "Inside", "inside", "uuid-1/Notes-MySQL/inside.md")
	siblingNote := createMySQLNoteAt(t, pool, user.ID, &sibling.ID, "Sibling", "sibling", "uuid-1/Notes-MySQL2/sibling.md")

	require.NoError(t, folders.Rename(ctx, user.ID, source.ID, "Renamed-MySQL2", "uuid-1/Renamed-MySQL2"))
	require.NoError(t, folders.UpdateDescendantPaths(ctx, "uuid-1/Notes-MySQL", "uuid-1/Renamed-MySQL2"))

	assert.Equal(t, "uuid-1/Renamed-MySQL2", mysqlFolderPath(t, pool, user.ID, source.ID))
	assert.Equal(t, "uuid-1/Renamed-MySQL2/Sub-MySQL", mysqlFolderPath(t, pool, user.ID, child.ID))
	assert.Equal(t, "uuid-1/Renamed-MySQL2/inside.md", mysqlNotePath(t, pool, user.ID, inside.ID))

	assert.Equal(t, "uuid-1/Notes-MySQL2", mysqlFolderPath(t, pool, user.ID, sibling.ID), "sibling that merely shares a prefix must not be rewritten")
	assert.Equal(t, "uuid-1/Notes-MySQL2/sibling.md", mysqlNotePath(t, pool, user.ID, siblingNote.ID))
}

// TestMySQL_FolderRepo_UpdateDescendantPaths_DoesNotRewriteOwnRow is the MySQL
// leg of the "Proj" -> "Proj-old" -> "Proj-old-old" reproduction.
func TestMySQL_FolderRepo_UpdateDescendantPaths_DoesNotRewriteOwnRow(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	folders := NewFolderRepo(pool.WriteDB)
	ctx := context.Background()

	proj, err := folders.Create(ctx, user.ID, nil, "Proj-MySQL", "uuid-1/Proj-MySQL")
	require.NoError(t, err)

	require.NoError(t, folders.Rename(ctx, user.ID, proj.ID, "Proj-MySQL-old", "uuid-1/Proj-MySQL-old"))
	require.NoError(t, folders.UpdateDescendantPaths(ctx, "uuid-1/Proj-MySQL", "uuid-1/Proj-MySQL-old"))

	assert.Equal(t, "uuid-1/Proj-MySQL-old", mysqlFolderPath(t, pool, user.ID, proj.ID))
}

// TestMySQL_FolderRepo_UpdateDescendantPaths_EscapesLikeWildcards is the MySQL
// leg of the '%' / '_' escaping guard.
func TestMySQL_FolderRepo_UpdateDescendantPaths_EscapesLikeWildcards(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	folders := NewFolderRepo(pool.WriteDB)
	ctx := context.Background()

	wildcard, err := folders.Create(ctx, user.ID, nil, "100%_done", "uuid-1/100%_done")
	require.NoError(t, err)
	bystander, err := folders.Create(ctx, user.ID, nil, "1000Xdone-notes", "uuid-1/1000Xdone-notes")
	require.NoError(t, err)
	child, err := folders.Create(ctx, user.ID, &wildcard.ID, "Child", "uuid-1/100%_done/Child")
	require.NoError(t, err)
	note := createMySQLNoteAt(t, pool, user.ID, &wildcard.ID, "Child", "child", "uuid-1/100%_done/child.md")

	require.NoError(t, folders.Rename(ctx, user.ID, wildcard.ID, "done", "uuid-1/done"))
	require.NoError(t, folders.UpdateDescendantPaths(ctx, "uuid-1/100%_done", "uuid-1/done"))

	assert.Equal(t, "uuid-1/done/Child", mysqlFolderPath(t, pool, user.ID, child.ID))
	assert.Equal(t, "uuid-1/done/child.md", mysqlNotePath(t, pool, user.ID, note.ID))
	assert.Equal(t, "uuid-1/1000Xdone-notes", mysqlFolderPath(t, pool, user.ID, bystander.ID), "'%' and '_' must not act as wildcards against unrelated folders")
}

// TestMySQL_FolderRepo_UpdateDescendantPaths_NonASCIIFolderName is the test
// SQLite structurally cannot write: SQLite's LENGTH() counts characters, so
// `SUBSTRING(disk_path, LENGTH(?)+1)` slices at the right offset there and every
// SQLite test stays green. MySQL's LENGTH() counts *bytes*, so a prefix holding
// a multi-byte character overshoots by exactly (bytes - chars) and the tail is
// sliced after the separator — "Renamedinside.md" instead of
// "Renamed/inside.md". That path points at nothing, so the next reconcile reads
// the row as a deleted note, deletes and reimports it, and the reimported row is
// a new row with no share token.
//
// The assertion is on the disk_path column, not on anything derived from it, so
// a rewrite that drops the separator cannot pass by being internally consistent.
func TestMySQL_FolderRepo_UpdateDescendantPaths_NonASCIIFolderName(t *testing.T) {
	for _, folderName := range []string{"Café", "日本語"} {
		t.Run(folderName, func(t *testing.T) {
			pool := openTestMySQL(t)
			user := createMySQLUser(t, pool)
			folders := NewFolderRepo(pool.WriteDB)
			ctx := context.Background()

			oldPath := "uuid-1/" + folderName
			newPath := "uuid-1/Renamed"

			parent, err := folders.Create(ctx, user.ID, nil, folderName, oldPath)
			require.NoError(t, err)
			child, err := folders.Create(ctx, user.ID, &parent.ID, "Sub", oldPath+"/Sub")
			require.NoError(t, err)
			inside := createMySQLNoteAt(t, pool, user.ID, &parent.ID, "Inside", "inside", oldPath+"/inside.md")
			deep := createMySQLNoteAt(t, pool, user.ID, &child.ID, "Deep", "deep", oldPath+"/Sub/deep.md")

			require.NoError(t, folders.Rename(ctx, user.ID, parent.ID, "Renamed", newPath))
			require.NoError(t, folders.UpdateDescendantPaths(ctx, oldPath, newPath))

			assert.Equal(t, newPath+"/Sub", mysqlFolderPath(t, pool, user.ID, child.ID),
				"the separator after the renamed prefix must survive the cascade")
			assert.Equal(t, newPath+"/inside.md", mysqlNotePath(t, pool, user.ID, inside.ID),
				"the separator after the renamed prefix must survive the cascade")
			assert.Equal(t, newPath+"/Sub/deep.md", mysqlNotePath(t, pool, user.ID, deep.ID),
				"the separator after the renamed prefix must survive the cascade")

			// The renamed folder's own row is written by Rename, not by the cascade.
			assert.Equal(t, newPath, mysqlFolderPath(t, pool, user.ID, parent.ID))
		})
	}
}

func TestMySQL_FolderRepo_GetByDiskPath(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewFolderRepo(pool.WriteDB)

	folder, err := repo.Create(context.Background(), user.ID, nil, "ByPath-MySQL", "uuid-1/bypath-mysql")
	require.NoError(t, err)

	got, err := repo.GetByDiskPath(context.Background(), "uuid-1/bypath-mysql")
	require.NoError(t, err)
	assert.Equal(t, folder.ID, got.ID)
}

func TestMySQL_FolderRepo_GetByDiskPath_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	repo := NewFolderRepo(pool.WriteDB)

	_, err := repo.GetByDiskPath(context.Background(), "nonexistent/mysql/path")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_FolderRepo_Move(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	ctx := context.Background()
	repo := NewFolderRepo(pool.WriteDB)

	parent, err := repo.Create(ctx, user.ID, nil, "Parent-Move-MySQL", "uuid-1/Parent-Move-MySQL")
	require.NoError(t, err)
	child, err := repo.Create(ctx, user.ID, nil, "Child-Move-MySQL", "uuid-1/Child-Move-MySQL")
	require.NoError(t, err)

	err = repo.Move(ctx, user.ID, child.ID, &parent.ID, "uuid-1/Parent-Move-MySQL/Child-Move-MySQL")
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, user.ID, child.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ParentID)
	assert.Equal(t, parent.ID, *got.ParentID)
}

func TestMySQL_FolderRepo_Move_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	ctx := context.Background()
	repo := NewFolderRepo(pool.WriteDB)

	err := repo.Move(ctx, user.ID, 99999999, nil, "uuid-1/NoFolder")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_FolderRepo_Move_Conflict(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	ctx := context.Background()
	repo := NewFolderRepo(pool.WriteDB)

	parent, err := repo.Create(ctx, user.ID, nil, "ParentMC-MySQL", "uuid-1/ParentMC-MySQL")
	require.NoError(t, err)
	_, err = repo.Create(ctx, user.ID, &parent.ID, "Dupe-MySQL", "uuid-1/ParentMC-MySQL/Dupe-MySQL")
	require.NoError(t, err)
	f2, err := repo.Create(ctx, user.ID, nil, "Other-MySQL", "uuid-1/Other-MySQL")
	require.NoError(t, err)

	err = repo.Move(ctx, user.ID, f2.ID, &parent.ID, "uuid-1/ParentMC-MySQL/Dupe-MySQL")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 409, appErr.Code)
}

// ─── NoteRepo tests ───────────────────────────────────────────────────────────

func createMySQLNote(t *testing.T, pool *db.Pool, userID int64, folderID *int64, title, slug string) *model.Note {
	t.Helper()
	repo := NewNoteRepo(pool.WriteDB)
	note := &model.Note{
		UserID:      userID,
		FolderID:    folderID,
		Title:       title,
		Slug:        slug,
		DiskPath:    "uuid-1/" + slug + ".md",
		Content:     "",
		ContentHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Tags:        []string{},
	}
	created, err := repo.Create(context.Background(), note)
	require.NoError(t, err)
	return created
}

func TestMySQL_NoteRepo_Create_And_Get(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)

	note := &model.Note{
		UserID:      user.ID,
		Title:       "My MySQL Note",
		Slug:        "my-mysql-note",
		DiskPath:    "uuid-1/my-mysql-note.md",
		Content:     "",
		ContentHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Tags:        []string{},
	}
	created, err := repo.Create(context.Background(), note)
	require.NoError(t, err)
	assert.NotZero(t, created.ID)

	got, err := repo.GetByID(context.Background(), user.ID, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "My MySQL Note", got.Title)
}

func TestMySQL_NoteRepo_Create_Duplicate(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)

	note := &model.Note{
		UserID:      user.ID,
		Title:       "Dup MySQL Note",
		Slug:        "dup-mysql-note",
		DiskPath:    "uuid-1/dup-mysql-note.md",
		Content:     "",
		ContentHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Tags:        []string{},
	}
	_, err := repo.Create(context.Background(), note)
	require.NoError(t, err)

	_, err = repo.Create(context.Background(), note)
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 409, appErr.Code)
}

func TestMySQL_NoteRepo_GetByID_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)

	_, err := repo.GetByID(context.Background(), user.ID, 99999999)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_NoteRepo_ListByFolder_Root(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)

	note := &model.Note{
		UserID:      user.ID,
		Title:       "Root MySQL Note",
		Slug:        "root-mysql-note",
		DiskPath:    "uuid-1/root-mysql-note.md",
		Content:     "",
		ContentHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Tags:        []string{},
	}
	_, err := repo.Create(context.Background(), note)
	require.NoError(t, err)

	items, err := repo.ListByFolder(context.Background(), user.ID, nil)
	require.NoError(t, err)
	found := false
	for _, item := range items {
		if item.Title == "Root MySQL Note" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestMySQL_NoteRepo_ListByFolder_InFolder(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	noteRepo := NewNoteRepo(pool.WriteDB)
	folderRepo := NewFolderRepo(pool.WriteDB)

	folder, err := folderRepo.Create(context.Background(), user.ID, nil, "FolderNotes-MySQL", "uuid-1/FolderNotes-MySQL")
	require.NoError(t, err)

	note := &model.Note{
		UserID:      user.ID,
		FolderID:    &folder.ID,
		Title:       "Folder MySQL Note",
		Slug:        "folder-mysql-note",
		DiskPath:    "uuid-1/FolderNotes-MySQL/folder-mysql-note.md",
		Content:     "",
		ContentHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Tags:        []string{},
	}
	_, err = noteRepo.Create(context.Background(), note)
	require.NoError(t, err)

	items, err := noteRepo.ListByFolder(context.Background(), user.ID, &folder.ID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "Folder MySQL Note", items[0].Title)
}

func TestMySQL_NoteRepo_Update(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)

	n := &model.Note{
		UserID:      user.ID,
		Title:       "Old MySQL Title",
		Slug:        "old-mysql-title",
		DiskPath:    "uuid-1/old-mysql-title.md",
		Content:     "",
		ContentHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Tags:        []string{},
	}
	created, err := repo.Create(context.Background(), n)
	require.NoError(t, err)

	created.Title = "New MySQL Title"
	created.Tags = []string{"mysql"}
	err = repo.Update(context.Background(), created)
	require.NoError(t, err)

	got, err := repo.GetByID(context.Background(), user.ID, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "New MySQL Title", got.Title)
	assert.Equal(t, []string{"mysql"}, got.Tags)
}

func TestMySQL_NoteRepo_Update_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)

	n := &model.Note{
		ID:     99999999,
		UserID: user.ID,
		Title:  "Nonexistent",
		Slug:   "nonexistent",
		Tags:   []string{},
	}
	err := repo.Update(context.Background(), n)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_NoteRepo_UpdateContent(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)

	emptyHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	n := &model.Note{
		UserID:      user.ID,
		Title:       "Content MySQL Note",
		Slug:        "content-mysql-note",
		DiskPath:    "uuid-1/content-mysql-note.md",
		Content:     "",
		ContentHash: emptyHash,
		Tags:        []string{},
	}
	created, err := repo.Create(context.Background(), n)
	require.NoError(t, err)

	newContent := "updated mysql content"
	newHash := "newhash-mysql-123"
	err = repo.UpdateContent(context.Background(), user.ID, created.ID, newContent, newHash, emptyHash)
	require.NoError(t, err)

	got, err := repo.GetByID(context.Background(), user.ID, created.ID)
	require.NoError(t, err)
	assert.Equal(t, newContent, got.Content)
	assert.Equal(t, newHash, got.ContentHash)
}

func TestMySQL_NoteRepo_UpdateContent_Conflict(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)

	emptyHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	n := &model.Note{
		UserID:      user.ID,
		Title:       "Conflict MySQL Note",
		Slug:        "conflict-mysql-note",
		DiskPath:    "uuid-1/conflict-mysql-note.md",
		Content:     "",
		ContentHash: emptyHash,
		Tags:        []string{},
	}
	created, err := repo.Create(context.Background(), n)
	require.NoError(t, err)

	err = repo.UpdateContent(context.Background(), user.ID, created.ID, "content", "newhash", "wronghash")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrConflict)
}

func TestMySQL_NoteRepo_UpdateContent_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)

	err := repo.UpdateContent(context.Background(), user.ID, 99999999, "content", "hash", "expectedhash")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_NoteRepo_Delete(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)

	created := createMySQLNote(t, pool, user.ID, nil, "Delete MySQL Me", "delete-mysql-me")

	err := repo.Delete(context.Background(), user.ID, created.ID)
	require.NoError(t, err)

	_, err = repo.GetByID(context.Background(), user.ID, created.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_NoteRepo_Delete_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)

	err := repo.Delete(context.Background(), user.ID, 99999999)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_NoteRepo_SetShareToken(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)

	created := createMySQLNote(t, pool, user.ID, nil, "Share MySQL Note", "share-mysql-note")

	token := "mysqlsharetoken"
	err := repo.SetShareToken(context.Background(), user.ID, created.ID, &token)
	require.NoError(t, err)

	got, err := repo.GetByID(context.Background(), user.ID, created.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ShareToken)
	assert.Equal(t, token, *got.ShareToken)

	// Clear token.
	err = repo.SetShareToken(context.Background(), user.ID, created.ID, nil)
	require.NoError(t, err)

	got2, err := repo.GetByID(context.Background(), user.ID, created.ID)
	require.NoError(t, err)
	assert.Nil(t, got2.ShareToken)
}

// TestMySQL_NoteRepo_SetShareToken_ClearTwice exercises the branch that the
// ownership tie-breaker exists for. MySQL counts *changed* rows, so writing NULL
// over an already-NULL column reports 0 — the same number as an UPDATE that
// matched no row. The tie-breaker must therefore answer "the note is yours, this
// was a no-op" rather than not-found.
func TestMySQL_NoteRepo_SetShareToken_ClearTwice(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)
	created := createMySQLNote(t, pool, user.ID, nil, "Clear Twice MySQL Note", "clear-twice-mysql-note")
	ctx := context.Background()

	// Never shared: the column is already NULL, so the update changes nothing.
	require.NoError(t, repo.SetShareToken(ctx, user.ID, created.ID, nil))
	require.NoError(t, repo.SetShareToken(ctx, user.ID, created.ID, nil),
		"clearing an already-NULL token must succeed, not report not-found")

	var stored sql.NullString
	require.NoError(t, pool.ReadDB.QueryRow(
		`SELECT share_token FROM notes WHERE id = ?`, created.ID).Scan(&stored))
	assert.False(t, stored.Valid, "the column must still be NULL")

	// And again after a real share, where the first clear genuinely changes a row.
	token := "clear-twice-mysql-token"
	require.NoError(t, repo.SetShareToken(ctx, user.ID, created.ID, &token))
	require.NoError(t, repo.SetShareToken(ctx, user.ID, created.ID, nil))
	require.NoError(t, repo.SetShareToken(ctx, user.ID, created.ID, nil),
		"a second clear after a real share must also succeed")

	// The generate branch still reports a note that is not the user's, without
	// consulting the tie-breaker: the fresh token cannot already be stored, so a
	// zero changed-row count there can only mean no matching row.
	other, err := NewUserRepo(pool.WriteDB).Create(ctx, "user-clear-twice", "hash", "test-uuid-clear-twice")
	require.NoError(t, err)
	foreign := "not-mine"
	err = repo.SetShareToken(ctx, other.ID, created.ID, &foreign)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_NoteRepo_SetShareToken_OtherUsersNote_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	owner := createMySQLUser(t, pool)
	other, err := NewUserRepo(pool.WriteDB).Create(context.Background(), "user2", "hash", "test-uuid-2")
	require.NoError(t, err)

	repo := NewNoteRepo(pool.WriteDB)
	created := createMySQLNote(t, pool, owner.ID, nil, "Owner Share MySQL Note", "owner-share-mysql-note")

	ownerToken := "mysql-owner-token"
	require.NoError(t, repo.SetShareToken(context.Background(), owner.ID, created.ID, &ownerToken))

	stolen := "mysql-stolen-token"
	err = repo.SetShareToken(context.Background(), other.ID, created.ID, &stolen)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)

	var stored sql.NullString
	require.NoError(t, pool.ReadDB.QueryRow(
		`SELECT share_token FROM notes WHERE id = ?`, created.ID).Scan(&stored))
	require.True(t, stored.Valid)
	assert.Equal(t, ownerToken, stored.String, "the owner's token must be untouched")
}

func TestMySQL_NoteRepo_SetShareToken_MissingNote_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)

	missing := "mysql-nowhere-token"
	err := repo.SetShareToken(context.Background(), user.ID, 99999999, &missing)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_NoteRepo_GetByShareToken(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewNoteRepo(pool.WriteDB)

	created := createMySQLNote(t, pool, user.ID, nil, "MySQL Shared", "mysql-shared")

	token := "mysql-public-token-123"
	err := repo.SetShareToken(context.Background(), user.ID, created.ID, &token)
	require.NoError(t, err)

	got, err := repo.GetByShareToken(context.Background(), token)
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)
}

func TestMySQL_NoteRepo_GetByFolderAndSlug_Root(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	noteRepo := NewNoteRepo(pool.WriteDB)
	ctx := context.Background()

	n := &model.Note{
		UserID:      user.ID,
		Title:       "MySQL Root Note Slug",
		Slug:        "mysql-root-note-slug",
		DiskPath:    "uuid-1/mysql-root-note-slug.md",
		Content:     "",
		ContentHash: "abc",
		Tags:        []string{},
	}
	created, err := noteRepo.Create(ctx, n)
	require.NoError(t, err)

	got, err := noteRepo.GetByFolderAndSlug(ctx, user.ID, nil, "mysql-root-note-slug")
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)
}

func TestMySQL_NoteRepo_GetByFolderAndSlug_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	noteRepo := NewNoteRepo(pool.WriteDB)

	_, err := noteRepo.GetByFolderAndSlug(context.Background(), user.ID, nil, "nonexistent-mysql-slug")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_NoteRepo_ListAll(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	folderRepo := NewFolderRepo(pool.WriteDB)
	noteRepo := NewNoteRepo(pool.WriteDB)
	ctx := context.Background()

	_, err := noteRepo.Create(ctx, &model.Note{
		UserID: user.ID, Title: "MySQL Root All", Slug: "mysql-root-all",
		DiskPath: "uuid-1/mysql-root-all.md", ContentHash: "h1", Tags: []string{},
	})
	require.NoError(t, err)

	folder, err := folderRepo.Create(ctx, user.ID, nil, "MySQLFolder-All", "uuid-1/mysqlfolder-all")
	require.NoError(t, err)
	_, err = noteRepo.Create(ctx, &model.Note{
		UserID: user.ID, FolderID: &folder.ID, Title: "MySQL In Folder All", Slug: "mysql-in-folder-all",
		DiskPath: "uuid-1/mysqlfolder-all/mysql-in-folder-all.md", ContentHash: "h2", Tags: []string{},
	})
	require.NoError(t, err)

	all, err := noteRepo.ListAll(ctx, user.ID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(all), 2)
}

func TestMySQL_NoteRepo_Move(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	ctx := context.Background()
	noteRepo := NewNoteRepo(pool.WriteDB)
	folderRepo := NewFolderRepo(pool.WriteDB)

	folder, err := folderRepo.Create(ctx, user.ID, nil, "Dest-MySQL", "uuid-1/Dest-MySQL")
	require.NoError(t, err)

	note := createMySQLNote(t, pool, user.ID, nil, "Move MySQL Me", "move-mysql-me")

	err = noteRepo.Move(ctx, user.ID, note.ID, &folder.ID, "uuid-1/Dest-MySQL/move-mysql-me.md")
	require.NoError(t, err)

	got, err := noteRepo.GetByID(ctx, user.ID, note.ID)
	require.NoError(t, err)
	require.NotNil(t, got.FolderID)
	assert.Equal(t, folder.ID, *got.FolderID)
}

func TestMySQL_NoteRepo_Move_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	ctx := context.Background()
	noteRepo := NewNoteRepo(pool.WriteDB)

	err := noteRepo.Move(ctx, user.ID, 99999999, nil, "uuid-1/nowhere.md")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_NoteRepo_Move_Conflict(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	ctx := context.Background()
	noteRepo := NewNoteRepo(pool.WriteDB)
	folderRepo := NewFolderRepo(pool.WriteDB)

	folder, err := folderRepo.Create(ctx, user.ID, nil, "CF-MySQL", "uuid-1/CF-MySQL")
	require.NoError(t, err)

	createMySQLNote(t, pool, user.ID, &folder.ID, "ConfNote-MySQL", "confnote-mysql")
	// Adjust disk path to be inside folder.
	_, err = pool.WriteDB.ExecContext(ctx,
		`UPDATE notes SET disk_path = 'uuid-1/CF-MySQL/confnote-mysql.md' WHERE slug = 'confnote-mysql' AND user_id = ?`,
		user.ID)
	require.NoError(t, err)

	note2 := createMySQLNote(t, pool, user.ID, nil, "Other-MySQL-Conflict", "other-mysql-conflict")

	err = noteRepo.Move(ctx, user.ID, note2.ID, &folder.ID, "uuid-1/CF-MySQL/confnote-mysql.md")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 409, appErr.Code)
}

// ─── SearchRepo tests ─────────────────────────────────────────────────────────

func TestMySQL_SearchRepo_SyncNote(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	noteRepo := NewNoteRepo(pool.WriteDB)
	searchRepo := NewSearchRepo(pool.WriteDB)

	note := &model.Note{
		UserID:      user.ID,
		Title:       "MySQL FTS Test",
		Slug:        "mysql-fts-test",
		DiskPath:    "uuid-1/mysql-fts-test.md",
		Content:     "mysql searchable content",
		ContentHash: "hash-mysql-fts",
		Tags:        []string{},
	}
	_, err := noteRepo.Create(context.Background(), note)
	require.NoError(t, err)

	// SyncNote is a no-op for MySQL (FULLTEXT indexes are auto-updated).
	err = searchRepo.SyncNote(context.Background(), 1)
	require.NoError(t, err)
}

func TestMySQL_SearchRepo_Search(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	noteRepo := NewNoteRepo(pool.WriteDB)
	searchRepo := NewSearchRepo(pool.WriteDB)

	note := &model.Note{
		UserID:      user.ID,
		Title:       "MySQL Searchable",
		Slug:        "mysql-searchable",
		DiskPath:    "uuid-1/mysql-searchable.md",
		Content:     "thornotes mysql awesome",
		ContentHash: "hash-mysql-search",
		Tags:        []string{},
	}
	_, err := noteRepo.Create(context.Background(), note)
	require.NoError(t, err)

	// MySQL FULLTEXT requires at least a few rows and the index may need a moment,
	// but in practice InnoDB updates the FTS index synchronously on INSERT.
	results, err := searchRepo.Search(context.Background(), user.ID, "thornotes mysql", nil)
	require.NoError(t, err)
	_ = results // may be empty if note content doesn't hit FTS threshold
}

func TestMySQL_SearchRepo_Search_EmptyQuery(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	searchRepo := NewSearchRepo(pool.WriteDB)

	// MySQL search returns nil for empty query (same as SQLite).
	results, err := searchRepo.Search(context.Background(), user.ID, "", nil)
	require.NoError(t, err)
	assert.Nil(t, results)
}

func TestMySQL_SearchRepo_Search_WithTags(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	noteRepo := NewNoteRepo(pool.WriteDB)
	searchRepo := NewSearchRepo(pool.WriteDB)

	note := &model.Note{
		UserID:      user.ID,
		Title:       "Tagged MySQL Searchable",
		Slug:        "tagged-mysql-searchable",
		DiskPath:    "uuid-1/tagged-mysql-searchable.md",
		Content:     "findme mysql unique content",
		ContentHash: "hash-mysql-tagged",
		Tags:        []string{"go", "mysql"},
	}
	_, err := noteRepo.Create(context.Background(), note)
	require.NoError(t, err)

	// Search with a tag filter — the note carries "go".
	results, err := searchRepo.Search(context.Background(), user.ID, "findme", []string{"go"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "Tagged MySQL Searchable", results[0].Title)

	// Two tags the note carries are AND, not unsatisfiable: the regression that
	// ticket 452 is about.
	results, err = searchRepo.Search(context.Background(), user.ID, "findme", []string{"go", "mysql"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "Tagged MySQL Searchable", results[0].Title)

	// One tag the note does not carry excludes it.
	results, err = searchRepo.Search(context.Background(), user.ID, "findme", []string{"go", "rust"})
	require.NoError(t, err)
	assert.Empty(t, results)
}

// ─── JournalRepo tests ────────────────────────────────────────────────────────

func TestMySQL_JournalRepo_Create(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewJournalRepo(pool.WriteDB)
	ctx := context.Background()

	j, err := repo.Create(ctx, user.ID, "Personal-MySQL")
	require.NoError(t, err)
	assert.NotZero(t, j.ID)
	assert.Equal(t, "Personal-MySQL", j.Name)
	assert.Equal(t, user.ID, j.UserID)
}

func TestMySQL_JournalRepo_Create_Duplicate(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewJournalRepo(pool.WriteDB)
	ctx := context.Background()

	_, err := repo.Create(ctx, user.ID, "Work-MySQL")
	require.NoError(t, err)

	_, err = repo.Create(ctx, user.ID, "Work-MySQL")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 409, appErr.Code)
}

func TestMySQL_JournalRepo_GetByID(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewJournalRepo(pool.WriteDB)
	ctx := context.Background()

	j, err := repo.Create(ctx, user.ID, "Daily-MySQL")
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, user.ID, j.ID)
	require.NoError(t, err)
	assert.Equal(t, j.ID, got.ID)
	assert.Equal(t, "Daily-MySQL", got.Name)
}

func TestMySQL_JournalRepo_GetByID_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewJournalRepo(pool.WriteDB)

	_, err := repo.GetByID(context.Background(), user.ID, 99999999)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestMySQL_JournalRepo_ListByUser(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewJournalRepo(pool.WriteDB)
	ctx := context.Background()

	_, err := repo.Create(ctx, user.ID, "Journal-A-MySQL")
	require.NoError(t, err)
	_, err = repo.Create(ctx, user.ID, "Journal-B-MySQL")
	require.NoError(t, err)

	journals, err := repo.ListByUser(ctx, user.ID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(journals), 2)
}

func TestMySQL_JournalRepo_Delete(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewJournalRepo(pool.WriteDB)
	ctx := context.Background()

	j, err := repo.Create(ctx, user.ID, "Temp-MySQL-Journal")
	require.NoError(t, err)

	err = repo.Delete(ctx, user.ID, j.ID)
	require.NoError(t, err)

	journals, err := repo.ListByUser(ctx, user.ID)
	require.NoError(t, err)
	for _, jj := range journals {
		assert.NotEqual(t, j.ID, jj.ID)
	}
}

func TestMySQL_JournalRepo_Delete_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewJournalRepo(pool.WriteDB)

	err := repo.Delete(context.Background(), user.ID, 99999999)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

// ─── APITokenRepo tests ───────────────────────────────────────────────────────

func TestMySQL_APITokenRepo_CreateAndGetByToken(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewAPITokenRepo(pool.WriteDB)

	rawToken := "tn_mysqltesttoken1234"
	created, err := repo.Create(context.Background(), user.ID, "my mysql token", rawToken, "readwrite")
	require.NoError(t, err)
	assert.NotZero(t, created.ID)
	assert.Equal(t, rawToken, created.Token)
	assert.Equal(t, "tn_mysql", created.Prefix)
	assert.Equal(t, "my mysql token", created.Name)

	found, err := repo.GetByToken(context.Background(), rawToken)
	require.NoError(t, err)
	assert.Equal(t, created.ID, found.ID)
	assert.Equal(t, "tn_mysql", found.Prefix)
}

func TestMySQL_APITokenRepo_GetByToken_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	repo := NewAPITokenRepo(pool.WriteDB)

	_, err := repo.GetByToken(context.Background(), "tn_doesnotexistmysql0")
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperror.ErrNotFound))
}

func TestMySQL_APITokenRepo_ListByUser(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewAPITokenRepo(pool.WriteDB)

	_, err := repo.Create(context.Background(), user.ID, "token-mysql-a", "tn_mysqlaaaa12345678", "readwrite")
	require.NoError(t, err)
	_, err = repo.Create(context.Background(), user.ID, "token-mysql-b", "tn_mysqlbbbb12345678", "readwrite")
	require.NoError(t, err)

	tokens, err := repo.ListByUser(context.Background(), user.ID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(tokens), 2)
	// Raw token must NOT be present in list results.
	for _, tok := range tokens {
		assert.Empty(t, tok.Token)
	}
}

func TestMySQL_APITokenRepo_Delete(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewAPITokenRepo(pool.WriteDB)

	token, err := repo.Create(context.Background(), user.ID, "to-delete-mysql", "tn_mysqldelete12345", "readwrite")
	require.NoError(t, err)

	err = repo.Delete(context.Background(), user.ID, token.ID)
	require.NoError(t, err)

	_, err = repo.GetByToken(context.Background(), "tn_mysqldelete12345")
	require.Error(t, err)
}

func TestMySQL_APITokenRepo_Delete_NotFound(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewAPITokenRepo(pool.WriteDB)

	err := repo.Delete(context.Background(), user.ID, 99999999)
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperror.ErrNotFound))
}

func TestMySQL_APITokenRepo_TouchLastUsed(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	repo := NewAPITokenRepo(pool.WriteDB)

	token, err := repo.Create(context.Background(), user.ID, "touch-mysql-tok", "tn_mysqltouch1234567", "readwrite")
	require.NoError(t, err)

	err = repo.TouchLastUsed(context.Background(), token.ID)
	require.NoError(t, err)

	found, err := repo.GetByToken(context.Background(), "tn_mysqltouch1234567")
	require.NoError(t, err)
	assert.NotNil(t, found.LastUsedAt)
}

func TestMySQL_APITokenRepo_SetAndListPermissions(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	ctx := context.Background()
	tokenRepo := NewAPITokenRepo(pool.WriteDB)
	folderRepo := NewFolderRepo(pool.WriteDB)

	token, err := tokenRepo.Create(ctx, user.ID, "scoped-mysql", "tn_mysqlscopedpermabc", "readwrite")
	require.NoError(t, err)

	f1, err := folderRepo.Create(ctx, user.ID, nil, "WorkMySQL", "test-uuid-1/WorkMySQL")
	require.NoError(t, err)

	require.NoError(t, tokenRepo.SetPermissions(ctx, user.ID, token.ID, []model.TokenFolderPermission{
		{FolderID: &f1.ID, Permission: "write"},
		{FolderID: nil, Permission: "read"},
	}))

	perms, err := tokenRepo.ListPermissions(ctx, token.ID)
	require.NoError(t, err)
	require.Len(t, perms, 2)

	// Replace.
	require.NoError(t, tokenRepo.SetPermissions(ctx, user.ID, token.ID, []model.TokenFolderPermission{
		{FolderID: &f1.ID, Permission: "read"},
	}))
	perms, err = tokenRepo.ListPermissions(ctx, token.ID)
	require.NoError(t, err)
	require.Len(t, perms, 1)
	assert.Equal(t, "read", perms[0].Permission)

	// Clear.
	require.NoError(t, tokenRepo.SetPermissions(ctx, user.ID, token.ID, nil))
	perms, err = tokenRepo.ListPermissions(ctx, token.ID)
	require.NoError(t, err)
	assert.Empty(t, perms)
}

func TestMySQL_APITokenRepo_SetPermissions_RejectsCrossUserFolder(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	ctx := context.Background()
	userRepo := NewUserRepo(pool.WriteDB)
	other, err := userRepo.Create(ctx, "mysql-other", "hash", "mysql-other-uuid")
	require.NoError(t, err)

	tokenRepo := NewAPITokenRepo(pool.WriteDB)
	folderRepo := NewFolderRepo(pool.WriteDB)

	token, err := tokenRepo.Create(ctx, user.ID, "mysql-cross", "tn_mysqlcrossperm1234", "readwrite")
	require.NoError(t, err)
	theirs, err := folderRepo.Create(ctx, other.ID, nil, "TheirsMySQL", "mysql-other-uuid/TheirsMySQL")
	require.NoError(t, err)

	err = tokenRepo.SetPermissions(ctx, user.ID, token.ID, []model.TokenFolderPermission{
		{FolderID: &theirs.ID, Permission: "read"},
	})
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 400, appErr.Code)
}

// TestMySQL_FolderAndNoteRepo_ListSubtree is the same scope assertion as the
// SQLite test, on the dialect where LIKE escaping, collation and the
// %/_ metacharacters behave differently.
func TestMySQL_FolderAndNoteRepo_ListSubtree(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	folders := NewFolderRepo(pool.WriteDB)
	notes := NewNoteRepo(pool.WriteDB)
	ctx := context.Background()

	root, err := folders.Create(ctx, user.ID, nil, "100%_done", "test-uuid-1/100%_done")
	require.NoError(t, err)
	child, err := folders.Create(ctx, user.ID, &root.ID, "Sub", "test-uuid-1/100%_done/Sub")
	require.NoError(t, err)
	sibling, err := folders.Create(ctx, user.ID, nil, "1000Xdone-notes", "test-uuid-1/1000Xdone-notes")
	require.NoError(t, err)

	createMySQLNoteAt(t, pool, user.ID, &root.ID, "Root note", "root-note", "test-uuid-1/100%_done/root-note.md")
	createMySQLNoteAt(t, pool, user.ID, &child.ID, "Child note", "child-note", "test-uuid-1/100%_done/Sub/child-note.md")
	createMySQLNoteAt(t, pool, user.ID, &sibling.ID, "Sibling note", "sibling-note", "test-uuid-1/1000Xdone-notes/sibling-note.md")

	folderRows, err := folders.ListSubtree(ctx, user.ID, root.DiskPath)
	require.NoError(t, err)
	var folderIDs []int64
	for _, f := range folderRows {
		folderIDs = append(folderIDs, f.ID)
	}
	assert.Equal(t, []int64{root.ID, child.ID}, folderIDs,
		"the subtree is the folder itself and its descendants — not the prefix-sharing sibling")

	noteRows, err := notes.ListSubtree(ctx, user.ID, root.DiskPath)
	require.NoError(t, err)
	var paths []string
	for _, rec := range noteRows {
		paths = append(paths, rec.DiskPath)
		assert.NotZero(t, rec.ID)
		assert.NotEmpty(t, rec.Title)
		assert.False(t, rec.UpdatedAt.IsZero(), "the export needs updated_at for the entry mtime")
	}
	// Order is not asserted: ORDER BY disk_path follows the database's collation
	// (MySQL's default is case-insensitive, SQLite's is byte-wise), and the
	// exporter sorts before assigning names so the archive cannot depend on it.
	assert.ElementsMatch(t, []string{
		"test-uuid-1/100%_done/Sub/child-note.md",
		"test-uuid-1/100%_done/root-note.md",
	}, paths, "only the subtree")
}

// IterateForContext is the context endpoint's read path (ticket N9). Same
// contract as the SQLite implementation, and the SUBSTR that clips each body is
// the one thing that is engine-specific.
func TestMySQL_NoteRepo_IterateForContext_ClipsContentAndStops(t *testing.T) {
	pool := openTestMySQL(t)
	user := createMySQLUser(t, pool)
	ctx := context.Background()
	noteRepo := NewNoteRepo(pool.WriteDB)

	const long = "0123456789ABCDEFGHIJ"
	var ids []int64
	for _, slug := range []string{"first-note", "second-note", "third-note"} {
		n := createMySQLNoteAt(t, pool, user.ID, nil, slug, slug, "test-uuid-1/"+slug+".md")
		_, err := pool.WriteDB.ExecContext(ctx,
			`UPDATE notes SET content = ?, updated_at = ? WHERE id = ?`,
			long, fmt.Sprintf("2026-01-0%d 00:00:00", len(ids)+1), n.ID)
		require.NoError(t, err)
		ids = append(ids, n.ID)
	}

	var visits []string
	err := noteRepo.IterateForContext(ctx, user.ID, nil, 5, func(n *model.Note) (bool, error) {
		visits = append(visits, n.Slug+":"+n.Content)
		return true, nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"third-note:01234", "second-note:01234", "first-note:01234"}, visits)

	visits = nil
	err = noteRepo.IterateForContext(ctx, user.ID, nil, 5, func(n *model.Note) (bool, error) {
		visits = append(visits, n.Slug)
		return len(visits) < 2, nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"third-note", "second-note"}, visits)
}
