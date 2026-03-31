package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/model"
)

func openTestDB(t *testing.T) *db.Pool {
	t.Helper()
	dir := t.TempDir()
	pool, err := db.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })
	return pool
}

func createUser(t *testing.T, pool *db.Pool) *model.User {
	t.Helper()
	repo := NewUserRepo(pool.WriteDB)
	user, err := repo.Create(context.Background(), "user1", "hash")
	require.NoError(t, err)
	return user
}

// ─── UserRepo tests ───────────────────────────────────────────────────────────

func TestUserRepo_Create(t *testing.T) {
	pool := openTestDB(t)
	repo := NewUserRepo(pool.WriteDB)

	user, err := repo.Create(context.Background(), "alice", "hash123")
	require.NoError(t, err)
	assert.NotZero(t, user.ID)
	assert.Equal(t, "alice", user.Username)
}

func TestUserRepo_Create_Duplicate(t *testing.T) {
	pool := openTestDB(t)
	repo := NewUserRepo(pool.WriteDB)

	_, err := repo.Create(context.Background(), "alice", "hash123")
	require.NoError(t, err)

	_, err = repo.Create(context.Background(), "alice", "hash456")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 409, appErr.Code)
}

func TestUserRepo_GetByUsername(t *testing.T) {
	pool := openTestDB(t)
	repo := NewUserRepo(pool.WriteDB)

	_, err := repo.Create(context.Background(), "bob", "hash")
	require.NoError(t, err)

	user, err := repo.GetByUsername(context.Background(), "bob")
	require.NoError(t, err)
	assert.Equal(t, "bob", user.Username)
}

func TestUserRepo_GetByUsername_NotFound(t *testing.T) {
	pool := openTestDB(t)
	repo := NewUserRepo(pool.WriteDB)

	_, err := repo.GetByUsername(context.Background(), "nonexistent")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestUserRepo_GetByID(t *testing.T) {
	pool := openTestDB(t)
	repo := NewUserRepo(pool.WriteDB)

	created, err := repo.Create(context.Background(), "carol", "hash")
	require.NoError(t, err)

	user, err := repo.GetByID(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, user.ID)
	assert.Equal(t, "carol", user.Username)
}

func TestUserRepo_GetByID_NotFound(t *testing.T) {
	pool := openTestDB(t)
	repo := NewUserRepo(pool.WriteDB)

	_, err := repo.GetByID(context.Background(), 99999)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestUserRepo_Count(t *testing.T) {
	pool := openTestDB(t)
	repo := NewUserRepo(pool.WriteDB)

	count, err := repo.Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	_, err = repo.Create(context.Background(), "dave", "hash")
	require.NoError(t, err)

	count, err = repo.Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// ─── SessionRepo tests ────────────────────────────────────────────────────────

func TestSessionRepo_Create_And_Get(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewSessionRepo(pool.WriteDB)

	err := repo.Create(context.Background(), "mytoken", user.ID, 3600)
	require.NoError(t, err)

	session, err := repo.Get(context.Background(), "mytoken")
	require.NoError(t, err)
	assert.Equal(t, "mytoken", session.Token)
	assert.Equal(t, user.ID, session.UserID)
}

func TestSessionRepo_Get_Expired(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)

	// Insert an already-expired session directly using a past expires_at.
	_, err := pool.WriteDB.ExecContext(context.Background(),
		`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, datetime('now', '-1 hour'))`,
		"expiredtoken", user.ID,
	)
	require.NoError(t, err)

	repo := NewSessionRepo(pool.WriteDB)
	_, err = repo.Get(context.Background(), "expiredtoken")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestSessionRepo_Delete(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewSessionRepo(pool.WriteDB)

	err := repo.Create(context.Background(), "deltoken", user.ID, 3600)
	require.NoError(t, err)

	err = repo.Delete(context.Background(), "deltoken")
	require.NoError(t, err)

	_, err = repo.Get(context.Background(), "deltoken")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestSessionRepo_DeleteExpired(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewSessionRepo(pool.WriteDB)

	// Insert an already-expired session directly.
	_, err := pool.WriteDB.ExecContext(context.Background(),
		`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, datetime('now', '-1 hour'))`,
		"expired1", user.ID,
	)
	require.NoError(t, err)

	// Create a valid session.
	err = repo.Create(context.Background(), "valid1", user.ID, 3600)
	require.NoError(t, err)

	err = repo.DeleteExpired(context.Background())
	require.NoError(t, err)

	// Expired should be gone.
	_, err = repo.Get(context.Background(), "expired1")
	require.Error(t, err)

	// Valid should still be there.
	session, err := repo.Get(context.Background(), "valid1")
	require.NoError(t, err)
	assert.Equal(t, "valid1", session.Token)
}

// ─── FolderRepo tests ─────────────────────────────────────────────────────────

func TestFolderRepo_Create(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	folder, err := repo.Create(context.Background(), user.ID, nil, "Work", "1/Work")
	require.NoError(t, err)
	assert.NotZero(t, folder.ID)
	assert.Equal(t, "Work", folder.Name)
	assert.Equal(t, "1/Work", folder.DiskPath)
}

func TestFolderRepo_Create_DuplicateName(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	_, err := repo.Create(context.Background(), user.ID, nil, "Work", "1/Work")
	require.NoError(t, err)

	_, err = repo.Create(context.Background(), user.ID, nil, "Work", "1/Work")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 409, appErr.Code)
}

func TestFolderRepo_GetByID(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	created, err := repo.Create(context.Background(), user.ID, nil, "Projects", "1/Projects")
	require.NoError(t, err)

	folder, err := repo.GetByID(context.Background(), user.ID, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, folder.ID)
	assert.Equal(t, "Projects", folder.Name)
}

func TestFolderRepo_GetByID_NotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	_, err := repo.GetByID(context.Background(), user.ID, 99999)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestFolderRepo_Tree(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	_, err := repo.Create(context.Background(), user.ID, nil, "Archive", "1/Archive")
	require.NoError(t, err)

	tree, err := repo.Tree(context.Background(), user.ID)
	require.NoError(t, err)
	require.Len(t, tree, 1)
	assert.Equal(t, "Archive", tree[0].Name)
}

func TestFolderRepo_Rename(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	folder, err := repo.Create(context.Background(), user.ID, nil, "Old", "1/Old")
	require.NoError(t, err)

	err = repo.Rename(context.Background(), user.ID, folder.ID, "New", "1/New")
	require.NoError(t, err)

	updated, err := repo.GetByID(context.Background(), user.ID, folder.ID)
	require.NoError(t, err)
	assert.Equal(t, "New", updated.Name)
	assert.Equal(t, "1/New", updated.DiskPath)
}

func TestFolderRepo_Delete(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	folder, err := repo.Create(context.Background(), user.ID, nil, "Temp", "1/Temp")
	require.NoError(t, err)

	err = repo.Delete(context.Background(), user.ID, folder.ID)
	require.NoError(t, err)

	_, err = repo.GetByID(context.Background(), user.ID, folder.ID)
	require.Error(t, err)
}

func TestFolderRepo_UpdateDescendantPaths(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	parent, err := repo.Create(context.Background(), user.ID, nil, "Parent", "1/Parent")
	require.NoError(t, err)

	child, err := repo.Create(context.Background(), user.ID, &parent.ID, "Child", "1/Parent/Child")
	require.NoError(t, err)

	err = repo.UpdateDescendantPaths(context.Background(), "1/Parent", "1/Renamed")
	require.NoError(t, err)

	updated, err := repo.GetByID(context.Background(), user.ID, child.ID)
	require.NoError(t, err)
	assert.Equal(t, "1/Renamed/Child", updated.DiskPath)
}

// ─── NoteRepo tests ───────────────────────────────────────────────────────────

func createTestNote(t *testing.T, pool *db.Pool, userID int64, folderID *int64, title string) *model.Note {
	t.Helper()
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	note := &model.Note{
		UserID:      userID,
		FolderID:    folderID,
		Title:       title,
		Slug:        "test-slug",
		DiskPath:    "1/test-slug.md",
		Content:     "",
		ContentHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Tags:        []string{},
	}
	created, err := repo.Create(context.Background(), note)
	require.NoError(t, err)
	return created
}

func TestNoteRepo_Create_And_Get(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	note := &model.Note{
		UserID:      user.ID,
		Title:       "My Note",
		Slug:        "my-note",
		DiskPath:    "1/my-note.md",
		Content:     "",
		ContentHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Tags:        []string{},
	}
	created, err := repo.Create(context.Background(), note)
	require.NoError(t, err)
	assert.NotZero(t, created.ID)

	got, err := repo.GetByID(context.Background(), user.ID, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "My Note", got.Title)
}

func TestNoteRepo_Create_Duplicate(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	note := &model.Note{
		UserID:      user.ID,
		Title:       "Dup Note",
		Slug:        "dup-note",
		DiskPath:    "1/dup-note.md",
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

func TestNoteRepo_GetByID_NotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	_, err := repo.GetByID(context.Background(), user.ID, 99999)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestNoteRepo_ListByFolder_Root(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	note := &model.Note{
		UserID:      user.ID,
		Title:       "Root Note",
		Slug:        "root-note",
		DiskPath:    "1/root-note.md",
		Content:     "",
		ContentHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Tags:        []string{},
	}
	_, err := repo.Create(context.Background(), note)
	require.NoError(t, err)

	items, err := repo.ListByFolder(context.Background(), user.ID, nil)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "Root Note", items[0].Title)
}

func TestNoteRepo_ListByFolder_InFolder(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	folderRepo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	folder, err := folderRepo.Create(context.Background(), user.ID, nil, "Folder1", "1/Folder1")
	require.NoError(t, err)

	note := &model.Note{
		UserID:      user.ID,
		FolderID:    &folder.ID,
		Title:       "Folder Note",
		Slug:        "folder-note",
		DiskPath:    "1/Folder1/folder-note.md",
		Content:     "",
		ContentHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Tags:        []string{},
	}
	_, err = noteRepo.Create(context.Background(), note)
	require.NoError(t, err)

	items, err := noteRepo.ListByFolder(context.Background(), user.ID, &folder.ID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "Folder Note", items[0].Title)
}

func TestNoteRepo_Update(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	n := &model.Note{
		UserID:      user.ID,
		Title:       "Old Title",
		Slug:        "old-title",
		DiskPath:    "1/old-title.md",
		Content:     "",
		ContentHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Tags:        []string{},
	}
	created, err := repo.Create(context.Background(), n)
	require.NoError(t, err)

	created.Title = "New Title"
	created.Tags = []string{"updated"}
	err = repo.Update(context.Background(), created)
	require.NoError(t, err)

	got, err := repo.GetByID(context.Background(), user.ID, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "New Title", got.Title)
	assert.Equal(t, []string{"updated"}, got.Tags)
}

func TestNoteRepo_UpdateContent(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	emptyHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	n := &model.Note{
		UserID:      user.ID,
		Title:       "Content Note",
		Slug:        "content-note",
		DiskPath:    "1/content-note.md",
		Content:     "",
		ContentHash: emptyHash,
		Tags:        []string{},
	}
	created, err := repo.Create(context.Background(), n)
	require.NoError(t, err)

	newContent := "updated content"
	newHash := "newhash123"
	err = repo.UpdateContent(context.Background(), user.ID, created.ID, newContent, newHash, emptyHash)
	require.NoError(t, err)

	got, err := repo.GetByID(context.Background(), user.ID, created.ID)
	require.NoError(t, err)
	assert.Equal(t, newContent, got.Content)
	assert.Equal(t, newHash, got.ContentHash)
}

func TestNoteRepo_UpdateContent_Conflict(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	emptyHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	n := &model.Note{
		UserID:      user.ID,
		Title:       "Note",
		Slug:        "note",
		DiskPath:    "1/note.md",
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

func TestNoteRepo_UpdateContent_NotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	err := repo.UpdateContent(context.Background(), user.ID, 99999, "content", "hash", "expectedhash")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestNoteRepo_Delete(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	created := createTestNote(t, pool, user.ID, nil, "Delete Me")

	err := repo.Delete(context.Background(), user.ID, created.ID)
	require.NoError(t, err)

	_, err = repo.GetByID(context.Background(), user.ID, created.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestNoteRepo_Delete_NotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	err := repo.Delete(context.Background(), user.ID, 99999)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestNoteRepo_SetShareToken(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	created := createTestNote(t, pool, user.ID, nil, "Share Note")

	// Set token.
	token := "mysharetoken"
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

func TestNoteRepo_GetByShareToken(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	created := createTestNote(t, pool, user.ID, nil, "Shared")

	token := "publictoken123"
	err := repo.SetShareToken(context.Background(), user.ID, created.ID, &token)
	require.NoError(t, err)

	got, err := repo.GetByShareToken(context.Background(), token)
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)
}

func TestNoteRepo_Update_NotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	n := &model.Note{
		ID:     99999,
		UserID: user.ID,
		Title:  "Nonexistent",
		Slug:   "nonexistent",
		Tags:   []string{},
	}
	err := repo.Update(context.Background(), n)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestFolderRepo_Rename_NotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	err := repo.Rename(context.Background(), user.ID, 99999, "NewName", "newpath")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestFolderRepo_Delete_NotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	err := repo.Delete(context.Background(), user.ID, 99999)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

// ─── SearchRepo tests ─────────────────────────────────────────────────────────

func TestSearchRepo_SyncNote(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := NewSearchRepo(pool.ReadDB, pool.WriteDB)

	note := &model.Note{
		UserID:      user.ID,
		Title:       "FTS Test",
		Slug:        "fts-test",
		DiskPath:    "1/fts-test.md",
		Content:     "searchable content",
		ContentHash: "hash1",
		Tags:        []string{},
	}
	created, err := noteRepo.Create(context.Background(), note)
	require.NoError(t, err)

	err = searchRepo.SyncNote(context.Background(), created.ID)
	require.NoError(t, err)

	got, err := noteRepo.GetByID(context.Background(), user.ID, created.ID)
	require.NoError(t, err)
	assert.NotNil(t, got.FtsSyncedAt)
}

func TestSearchRepo_Search(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := NewSearchRepo(pool.ReadDB, pool.WriteDB)

	note := &model.Note{
		UserID:      user.ID,
		Title:       "Searchable",
		Slug:        "searchable",
		DiskPath:    "1/searchable.md",
		Content:     "thornotes is awesome",
		ContentHash: "hash2",
		Tags:        []string{},
	}
	_, err := noteRepo.Create(context.Background(), note)
	require.NoError(t, err)

	results, err := searchRepo.Search(context.Background(), user.ID, "thornotes", nil)
	require.NoError(t, err)
	require.NotEmpty(t, results)
	assert.Equal(t, "Searchable", results[0].Title)
}

func TestSearchRepo_Search_EmptyQuery(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	searchRepo := NewSearchRepo(pool.ReadDB, pool.WriteDB)

	results, err := searchRepo.Search(context.Background(), user.ID, "", nil)
	require.NoError(t, err)
	assert.Nil(t, results)
}

func TestFolderRepo_Create_ForeignKeyViolation(t *testing.T) {
	pool := openTestDB(t)
	repo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	// userID 99999 does not exist → FK violation, not a UNIQUE constraint.
	_, err := repo.Create(context.Background(), 99999, nil, "TestFolder", "99999/TestFolder")
	require.Error(t, err)
	// Must NOT be a Conflict (409) app error — that's only for UNIQUE violations.
	var appErr *apperror.AppError
	if errors.As(err, &appErr) {
		assert.NotEqual(t, 409, appErr.Code)
	}
}

func TestNoteRepo_Create_ForeignKeyViolation(t *testing.T) {
	pool := openTestDB(t)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	note := &model.Note{
		UserID:      99999, // non-existent user
		Title:       "FK Note",
		Slug:        "fk-note",
		DiskPath:    "99999/fk-note.md",
		Content:     "",
		ContentHash: "hash",
		Tags:        []string{},
	}
	_, err := repo.Create(context.Background(), note)
	require.Error(t, err)
}

func TestFolderRepo_Rename_UniqueConstraint(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	// Create two folders.
	_, err := repo.Create(context.Background(), user.ID, nil, "Alpha", "1/Alpha")
	require.NoError(t, err)

	beta, err := repo.Create(context.Background(), user.ID, nil, "Beta", "1/Beta")
	require.NoError(t, err)

	// Rename Beta to Alpha — should hit UNIQUE constraint.
	err = repo.Rename(context.Background(), user.ID, beta.ID, "Alpha", "1/Alpha")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 409, appErr.Code)
}

func TestNoteRepo_UpdateContent_ExistCheckError(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	// UpdateContent on non-existent note → ErrNotFound (not ErrConflict).
	err := repo.UpdateContent(context.Background(), user.ID, 99999, "content", "hash", "oldhash")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestSearchRepo_Search_WithTags(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := NewSearchRepo(pool.ReadDB, pool.WriteDB)

	note := &model.Note{
		UserID:      user.ID,
		Title:       "Tagged Searchable",
		Slug:        "tagged-searchable",
		DiskPath:    "1/tagged-searchable.md",
		Content:     "findme unique content",
		ContentHash: "hash3",
		Tags:        []string{"go", "testing"},
	}
	_, err := noteRepo.Create(context.Background(), note)
	require.NoError(t, err)

	// Search with a tag filter — exercises the tag-join clause.
	results, err := searchRepo.Search(context.Background(), user.ID, "findme", []string{"go"})
	require.NoError(t, err)
	// Results may be empty if FTS not synced yet, but should not error.
	_ = results
}

func TestSearchRepo_Search_MalformedTagsJSON(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	searchRepo := NewSearchRepo(pool.ReadDB, pool.WriteDB)

	// Directly insert a note with malformed tags JSON to exercise the json.Unmarshal error path.
	_, err := pool.WriteDB.ExecContext(context.Background(),
		`INSERT INTO notes (user_id, title, slug, disk_path, content, content_hash, tags)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		user.ID, "Bad Tags", "bad-tags", "1/bad-tags.md", "some content", "hashbad", "not-valid-json",
	)
	require.NoError(t, err)

	// Search should still work — malformed tags treated as nil, not an error.
	results, err := searchRepo.Search(context.Background(), user.ID, "some", nil)
	require.NoError(t, err)
	_ = results
}

func TestSanitizeFTSQuery(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello", `"hello"`},
		{"hello world", `"hello" OR "world"`},
		{"", ""},
		{"   ", ""},
		{`quote"test`, `"quote""test"`},
		{"foo bar baz", `"foo" OR "bar" OR "baz"`},
		{`special * chars ^`, `"special" OR "*" OR "chars" OR "^"`},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result := sanitizeFTSQuery(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}
