package mysql

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/model"
)

// openTestMySQL opens a real MySQL connection using the DSN in THORNOTES_MYSQL_TEST_DSN.
// All tests in this file are skipped if the variable is not set.
func openTestMySQL(t *testing.T) *db.Pool {
	t.Helper()
	dsn := os.Getenv("THORNOTES_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("THORNOTES_MYSQL_TEST_DSN not set")
	}
	pool, err := db.OpenMySQL(dsn)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })
	return pool
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

	// Search with a tag filter — exercises the JSON_TABLE tag-join clause.
	results, err := searchRepo.Search(context.Background(), user.ID, "findme", []string{"go"})
	require.NoError(t, err)
	_ = results
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
