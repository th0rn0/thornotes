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
	user, err := repo.Create(context.Background(), "user1", "hash", "test-uuid-1")
	require.NoError(t, err)
	return user
}

// ─── UserRepo tests ───────────────────────────────────────────────────────────

func TestUserRepo_Create(t *testing.T) {
	pool := openTestDB(t)
	repo := NewUserRepo(pool.WriteDB)

	user, err := repo.Create(context.Background(), "alice", "hash123", "uuid-alice")
	require.NoError(t, err)
	assert.NotZero(t, user.ID)
	assert.Equal(t, "alice", user.Username)
}

func TestUserRepo_Create_Duplicate(t *testing.T) {
	pool := openTestDB(t)
	repo := NewUserRepo(pool.WriteDB)

	_, err := repo.Create(context.Background(), "alice", "hash123", "uuid-alice")
	require.NoError(t, err)

	_, err = repo.Create(context.Background(), "alice", "hash456", "uuid-alice2")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 409, appErr.Code)
}

func TestUserRepo_GetByUsername(t *testing.T) {
	pool := openTestDB(t)
	repo := NewUserRepo(pool.WriteDB)

	_, err := repo.Create(context.Background(), "bob", "hash", "uuid-bob")
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

	created, err := repo.Create(context.Background(), "carol", "hash", "uuid-carol")
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

	_, err = repo.Create(context.Background(), "dave", "hash", "uuid-dave")
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

// ─── JournalRepo tests ────────────────────────────────────────────────────────

func TestJournalRepo_Create(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewJournalRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	j, err := repo.Create(ctx, user.ID, "Personal")
	require.NoError(t, err)
	assert.NotZero(t, j.ID)
	assert.Equal(t, "Personal", j.Name)
	assert.Equal(t, user.ID, j.UserID)
}

func TestJournalRepo_Create_Duplicate(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewJournalRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	_, err := repo.Create(ctx, user.ID, "Work")
	require.NoError(t, err)

	_, err = repo.Create(ctx, user.ID, "Work")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 409, appErr.Code)
}

func TestJournalRepo_GetByID(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewJournalRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	j, err := repo.Create(ctx, user.ID, "Daily")
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, user.ID, j.ID)
	require.NoError(t, err)
	assert.Equal(t, j.ID, got.ID)
	assert.Equal(t, "Daily", got.Name)
}

func TestJournalRepo_GetByID_NotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewJournalRepo(pool.ReadDB, pool.WriteDB)

	_, err := repo.GetByID(context.Background(), user.ID, 99999)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestJournalRepo_ListByUser_Empty(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewJournalRepo(pool.ReadDB, pool.WriteDB)

	journals, err := repo.ListByUser(context.Background(), user.ID)
	require.NoError(t, err)
	assert.NotNil(t, journals)
	assert.Empty(t, journals)
}

func TestJournalRepo_ListByUser_Multiple(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewJournalRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	_, err := repo.Create(ctx, user.ID, "Personal")
	require.NoError(t, err)
	_, err = repo.Create(ctx, user.ID, "Work")
	require.NoError(t, err)

	journals, err := repo.ListByUser(ctx, user.ID)
	require.NoError(t, err)
	assert.Len(t, journals, 2)
}

func TestJournalRepo_Delete(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewJournalRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	j, err := repo.Create(ctx, user.ID, "Temp")
	require.NoError(t, err)

	err = repo.Delete(ctx, user.ID, j.ID)
	require.NoError(t, err)

	journals, err := repo.ListByUser(ctx, user.ID)
	require.NoError(t, err)
	assert.Empty(t, journals)
}

func TestJournalRepo_Delete_NotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewJournalRepo(pool.ReadDB, pool.WriteDB)

	err := repo.Delete(context.Background(), user.ID, 99999)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

// ─── FolderRepo.GetByDiskPath tests ──────────────────────────────────────────

func TestFolderRepo_GetByDiskPath(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewFolderRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	folder, err := repo.Create(ctx, user.ID, nil, "MyFolder", "1/myfolder")
	require.NoError(t, err)

	got, err := repo.GetByDiskPath(ctx, "1/myfolder")
	require.NoError(t, err)
	assert.Equal(t, folder.ID, got.ID)
	assert.Equal(t, "MyFolder", got.Name)
}

func TestFolderRepo_GetByDiskPath_NotFound(t *testing.T) {
	pool := openTestDB(t)
	repo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	_, err := repo.GetByDiskPath(context.Background(), "nonexistent/path")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

// ─── NoteRepo.GetByFolderAndSlug tests ───────────────────────────────────────

func TestNoteRepo_GetByFolderAndSlug_Root(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	n := &model.Note{
		UserID:      user.ID,
		Title:       "Root Note",
		Slug:        "root-note",
		DiskPath:    "1/root-note.md",
		Content:     "",
		ContentHash: "abc",
		Tags:        []string{},
	}
	created, err := noteRepo.Create(ctx, n)
	require.NoError(t, err)

	got, err := noteRepo.GetByFolderAndSlug(ctx, user.ID, nil, "root-note")
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)
}

func TestNoteRepo_GetByFolderAndSlug_InFolder(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	folderRepo := NewFolderRepo(pool.ReadDB, pool.WriteDB)
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	folder, err := folderRepo.Create(ctx, user.ID, nil, "Docs", "1/docs")
	require.NoError(t, err)

	n := &model.Note{
		UserID:      user.ID,
		FolderID:    &folder.ID,
		Title:       "Design Doc",
		Slug:        "design-doc",
		DiskPath:    "1/docs/design-doc.md",
		Content:     "",
		ContentHash: "xyz",
		Tags:        []string{},
	}
	created, err := noteRepo.Create(ctx, n)
	require.NoError(t, err)

	got, err := noteRepo.GetByFolderAndSlug(ctx, user.ID, &folder.ID, "design-doc")
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)
}

func TestNoteRepo_GetByFolderAndSlug_NotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	_, err := noteRepo.GetByFolderAndSlug(context.Background(), user.ID, nil, "nonexistent")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

// ─── NoteRepo.ListAll tests ───────────────────────────────────────────────────

func TestNoteRepo_ListAll(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	folderRepo := NewFolderRepo(pool.ReadDB, pool.WriteDB)
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	// Root note.
	_, err := noteRepo.Create(ctx, &model.Note{
		UserID: user.ID, Title: "Root", Slug: "root",
		DiskPath: "1/root.md", ContentHash: "h1", Tags: []string{},
	})
	require.NoError(t, err)

	// Note in folder.
	folder, err := folderRepo.Create(ctx, user.ID, nil, "Folder", "1/folder")
	require.NoError(t, err)
	_, err = noteRepo.Create(ctx, &model.Note{
		UserID: user.ID, FolderID: &folder.ID, Title: "In Folder", Slug: "in-folder",
		DiskPath: "1/folder/in-folder.md", ContentHash: "h2", Tags: []string{},
	})
	require.NoError(t, err)

	all, err := noteRepo.ListAll(ctx, user.ID)
	require.NoError(t, err)
	assert.Len(t, all, 2)

	// Verify folder_id is populated correctly.
	byTitle := make(map[string]*int64)
	for _, item := range all {
		byTitle[item.Title] = item.FolderID
	}
	assert.Nil(t, byTitle["Root"])
	require.NotNil(t, byTitle["In Folder"])
	assert.Equal(t, folder.ID, *byTitle["In Folder"])
}

func TestNoteRepo_ListAll_Empty(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	all, err := noteRepo.ListAll(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Empty(t, all)
}

// ─── NoteRepo.ListAllForWatch tests ──────────────────────────────────────────

func TestNoteRepo_ListAllForWatch(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	_, err := noteRepo.Create(context.Background(), &model.Note{UserID: user.ID, Title: "watch-note", Content: "body"})
	require.NoError(t, err)

	records, err := noteRepo.ListAllForWatch(context.Background(), user.ID)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.NotZero(t, records[0].ID)
}

// ─── UserRepo.IDs tests ───────────────────────────────────────────────────────

func TestUserRepo_IDs(t *testing.T) {
	pool := openTestDB(t)
	userRepo := NewUserRepo(pool.WriteDB)

	ids, err := userRepo.IDs(context.Background())
	require.NoError(t, err)
	assert.Empty(t, ids)

	u1, err := userRepo.Create(context.Background(), "alice", "hash", "uuid-alice-ids")
	require.NoError(t, err)
	u2, err := userRepo.Create(context.Background(), "bob", "hash", "uuid-bob-ids")
	require.NoError(t, err)

	ids, err = userRepo.IDs(context.Background())
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{u1.ID, u2.ID}, ids)
}

// ─── APITokenRepo tests ───────────────────────────────────────────────────────

func TestAPITokenRepo_CreateAndGetByToken(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewAPITokenRepo(pool.ReadDB, pool.WriteDB)

	rawToken := "tn_testtoken12345678"
	created, err := repo.Create(context.Background(), user.ID, "my token", rawToken, "readwrite")
	require.NoError(t, err)
	assert.NotZero(t, created.ID)
	assert.Equal(t, rawToken, created.Token) // raw returned once
	assert.Equal(t, "tn_testt", created.Prefix)
	assert.Equal(t, "my token", created.Name)

	// GetByToken must find via hash.
	found, err := repo.GetByToken(context.Background(), rawToken)
	require.NoError(t, err)
	assert.Equal(t, created.ID, found.ID)
	assert.Equal(t, "tn_testt", found.Prefix)
}

func TestAPITokenRepo_GetByToken_NotFound(t *testing.T) {
	pool := openTestDB(t)
	repo := NewAPITokenRepo(pool.ReadDB, pool.WriteDB)

	_, err := repo.GetByToken(context.Background(), "tn_doesnotexist000")
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperror.ErrNotFound))
}

func TestAPITokenRepo_ListByUser(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewAPITokenRepo(pool.ReadDB, pool.WriteDB)

	_, err := repo.Create(context.Background(), user.ID, "token-a", "tn_aaaaaaaa12345678", "readwrite")
	require.NoError(t, err)
	_, err = repo.Create(context.Background(), user.ID, "token-b", "tn_bbbbbbbb12345678", "readwrite")
	require.NoError(t, err)

	tokens, err := repo.ListByUser(context.Background(), user.ID)
	require.NoError(t, err)
	require.Len(t, tokens, 2)
	assert.Equal(t, "token-a", tokens[0].Name)
	assert.Equal(t, "token-b", tokens[1].Name)
	// Raw token must NOT be present in list results.
	assert.Empty(t, tokens[0].Token)
	assert.Empty(t, tokens[1].Token)
}

func TestAPITokenRepo_Delete(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewAPITokenRepo(pool.ReadDB, pool.WriteDB)

	token, err := repo.Create(context.Background(), user.ID, "to-delete", "tn_deletetoken12345", "readwrite")
	require.NoError(t, err)

	err = repo.Delete(context.Background(), user.ID, token.ID)
	require.NoError(t, err)

	_, err = repo.GetByToken(context.Background(), "tn_deletetoken12345")
	require.Error(t, err)
}

func TestAPITokenRepo_Delete_NotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewAPITokenRepo(pool.ReadDB, pool.WriteDB)

	err := repo.Delete(context.Background(), user.ID, 99999)
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperror.ErrNotFound))
}

func TestAPITokenRepo_Delete_WrongUser(t *testing.T) {
	pool := openTestDB(t)
	user1 := createUser(t, pool)
	userRepo := NewUserRepo(pool.WriteDB)
	user2, err := userRepo.Create(context.Background(), "user2", "hash", "uuid-user2-fixed")
	require.NoError(t, err)

	repo := NewAPITokenRepo(pool.ReadDB, pool.WriteDB)
	token, err := repo.Create(context.Background(), user1.ID, "tok", "tn_crossusertoken123", "readwrite")
	require.NoError(t, err)

	// user2 cannot delete user1's token.
	err = repo.Delete(context.Background(), user2.ID, token.ID)
	require.Error(t, err)
}

func TestAPITokenRepo_SetAndListPermissions(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	ctx := context.Background()
	tokenRepo := NewAPITokenRepo(pool.ReadDB, pool.WriteDB)
	folderRepo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	token, err := tokenRepo.Create(ctx, user.ID, "scoped", "tn_scopedperms123456", "readwrite")
	require.NoError(t, err)

	f1, err := folderRepo.Create(ctx, user.ID, nil, "Work", "test-uuid-1/Work")
	require.NoError(t, err)
	f2, err := folderRepo.Create(ctx, user.ID, nil, "Journal", "test-uuid-1/Journal")
	require.NoError(t, err)

	// Initially empty.
	perms, err := tokenRepo.ListPermissions(ctx, token.ID)
	require.NoError(t, err)
	assert.Empty(t, perms)

	// Grant write on Work, read on Journal, and read on root.
	err = tokenRepo.SetPermissions(ctx, user.ID, token.ID, []model.TokenFolderPermission{
		{FolderID: &f1.ID, Permission: "write"},
		{FolderID: &f2.ID, Permission: "read"},
		{FolderID: nil, Permission: "read"},
	})
	require.NoError(t, err)

	got, err := tokenRepo.ListPermissions(ctx, token.ID)
	require.NoError(t, err)
	require.Len(t, got, 3)

	// Replace: keep only one entry; old rows must be gone.
	err = tokenRepo.SetPermissions(ctx, user.ID, token.ID, []model.TokenFolderPermission{
		{FolderID: &f1.ID, Permission: "read"},
	})
	require.NoError(t, err)
	got, err = tokenRepo.ListPermissions(ctx, token.ID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "read", got[0].Permission)
	require.NotNil(t, got[0].FolderID)
	assert.Equal(t, f1.ID, *got[0].FolderID)

	// Clear.
	err = tokenRepo.SetPermissions(ctx, user.ID, token.ID, nil)
	require.NoError(t, err)
	got, err = tokenRepo.ListPermissions(ctx, token.ID)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestAPITokenRepo_SetPermissions_RejectsInvalidPermission(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	ctx := context.Background()
	tokenRepo := NewAPITokenRepo(pool.ReadDB, pool.WriteDB)

	token, err := tokenRepo.Create(ctx, user.ID, "scoped", "tn_badpermstok123abcd", "readwrite")
	require.NoError(t, err)

	err = tokenRepo.SetPermissions(ctx, user.ID, token.ID, []model.TokenFolderPermission{
		{FolderID: nil, Permission: "admin"},
	})
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 400, appErr.Code)
}

func TestAPITokenRepo_SetPermissions_RejectsCrossUserFolder(t *testing.T) {
	pool := openTestDB(t)
	user1 := createUser(t, pool)
	userRepo := NewUserRepo(pool.WriteDB)
	user2, err := userRepo.Create(context.Background(), "user2", "hash", "test-uuid-2")
	require.NoError(t, err)
	ctx := context.Background()

	tokenRepo := NewAPITokenRepo(pool.ReadDB, pool.WriteDB)
	folderRepo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	token, err := tokenRepo.Create(ctx, user1.ID, "scoped", "tn_crossfoldertokenx1", "readwrite")
	require.NoError(t, err)
	theirFolder, err := folderRepo.Create(ctx, user2.ID, nil, "Theirs", "test-uuid-2/Theirs")
	require.NoError(t, err)

	err = tokenRepo.SetPermissions(ctx, user1.ID, token.ID, []model.TokenFolderPermission{
		{FolderID: &theirFolder.ID, Permission: "read"},
	})
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 400, appErr.Code)
}

func TestAPITokenRepo_SetPermissions_RejectsWrongOwner(t *testing.T) {
	pool := openTestDB(t)
	user1 := createUser(t, pool)
	userRepo := NewUserRepo(pool.WriteDB)
	user2, err := userRepo.Create(context.Background(), "user2", "hash", "test-uuid-2")
	require.NoError(t, err)
	ctx := context.Background()

	tokenRepo := NewAPITokenRepo(pool.ReadDB, pool.WriteDB)
	token, err := tokenRepo.Create(ctx, user1.ID, "theirs", "tn_ownerchecktokenabcd", "readwrite")
	require.NoError(t, err)

	err = tokenRepo.SetPermissions(ctx, user2.ID, token.ID, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestAPITokenRepo_TouchLastUsed(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	repo := NewAPITokenRepo(pool.ReadDB, pool.WriteDB)

	token, err := repo.Create(context.Background(), user.ID, "touch-tok", "tn_touchtokenabcdef", "readwrite")
	require.NoError(t, err)

	err = repo.TouchLastUsed(context.Background(), token.ID)
	require.NoError(t, err)

	found, err := repo.GetByToken(context.Background(), "tn_touchtokenabcdef")
	require.NoError(t, err)
	assert.NotNil(t, found.LastUsedAt)
}

func TestHashToken_Deterministic(t *testing.T) {
	h1 := hashToken("tn_sometoken")
	h2 := hashToken("tn_sometoken")
	assert.Equal(t, h1, h2)
	assert.Len(t, h1, 64) // SHA-256 hex = 64 chars
}

func TestHashToken_DifferentInputs(t *testing.T) {
	h1 := hashToken("tn_sometoken")
	h2 := hashToken("tn_othertoken")
	assert.NotEqual(t, h1, h2)
}

// ─── UserRepo additional coverage ─────────────────────────────────────────────

func TestUserRepo_SetUUID(t *testing.T) {
	pool := openTestDB(t)
	repo := NewUserRepo(pool.WriteDB)

	user, err := repo.Create(context.Background(), "uuid-user", "hash", "")
	require.NoError(t, err)

	err = repo.SetUUID(context.Background(), user.ID, "new-uuid-abc")
	require.NoError(t, err)

	got, err := repo.GetByID(context.Background(), user.ID)
	require.NoError(t, err)
	assert.Equal(t, "new-uuid-abc", got.UUID)
}

func TestUserRepo_ListWithoutUUID(t *testing.T) {
	pool := openTestDB(t)
	repo := NewUserRepo(pool.WriteDB)

	// Create user without UUID.
	_, err := pool.WriteDB.ExecContext(context.Background(),
		`INSERT INTO users (username, password_hash, uuid) VALUES ('noid', 'hash', '')`)
	require.NoError(t, err)

	// Create user with UUID (should not appear).
	_, err = repo.Create(context.Background(), "withid", "hash", "some-uuid")
	require.NoError(t, err)

	ids, err := repo.ListWithoutUUID(context.Background())
	require.NoError(t, err)
	assert.Len(t, ids, 1)
}

func TestUserRepo_IDs_Empty(t *testing.T) {
	pool := openTestDB(t)
	repo := NewUserRepo(pool.WriteDB)

	ids, err := repo.IDs(context.Background())
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestUserRepo_IDs_Multiple(t *testing.T) {
	pool := openTestDB(t)
	repo := NewUserRepo(pool.WriteDB)

	_, err := repo.Create(context.Background(), "ida", "h", "uuid-ida")
	require.NoError(t, err)
	_, err = repo.Create(context.Background(), "idb", "h", "uuid-idb")
	require.NoError(t, err)

	ids, err := repo.IDs(context.Background())
	require.NoError(t, err)
	assert.Len(t, ids, 2)
}

// ─── FolderRepo.Move ──────────────────────────────────────────────────────────

func TestFolderRepo_Move(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	ctx := context.Background()
	repo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	parent, err := repo.Create(ctx, user.ID, nil, "Parent", "test-uuid-1/Parent")
	require.NoError(t, err)
	child, err := repo.Create(ctx, user.ID, nil, "Child", "test-uuid-1/Child")
	require.NoError(t, err)

	// Move child into parent.
	err = repo.Move(ctx, user.ID, child.ID, &parent.ID, "test-uuid-1/Parent/Child")
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, user.ID, child.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ParentID)
	assert.Equal(t, parent.ID, *got.ParentID)
}

func TestFolderRepo_Move_NotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	ctx := context.Background()
	repo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	err := repo.Move(ctx, user.ID, 99999, nil, "test-uuid-1/NoFolder")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestFolderRepo_Move_Conflict(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	ctx := context.Background()
	repo := NewFolderRepo(pool.ReadDB, pool.WriteDB)

	parent, err := repo.Create(ctx, user.ID, nil, "Parent", "test-uuid-1/Parent")
	require.NoError(t, err)
	_, err = repo.Create(ctx, user.ID, &parent.ID, "Dupe", "test-uuid-1/Parent/Dupe")
	require.NoError(t, err)
	f2, err := repo.Create(ctx, user.ID, nil, "Other", "test-uuid-1/Other")
	require.NoError(t, err)

	// Force a unique conflict by setting disk_path equal to f1's.
	err = repo.Move(ctx, user.ID, f2.ID, &parent.ID, "test-uuid-1/Parent/Dupe")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 409, appErr.Code)
}

// ─── NoteRepo.Move ────────────────────────────────────────────────────────────

func createTestFolder(t *testing.T, pool *db.Pool, user *model.User, name string) *model.Folder {
	t.Helper()
	repo := NewFolderRepo(pool.ReadDB, pool.WriteDB)
	f, err := repo.Create(context.Background(), user.ID, nil, name, "test-uuid-1/"+name)
	require.NoError(t, err)
	return f
}

func createNoteInFolder(t *testing.T, pool *db.Pool, userID int64, folderID *int64, title, slug, diskPath string) *model.Note {
	t.Helper()
	repo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	n, err := repo.Create(context.Background(), &model.Note{
		UserID:      userID,
		FolderID:    folderID,
		Title:       title,
		Slug:        slug,
		DiskPath:    diskPath,
		Tags:        []string{},
		ContentHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	})
	require.NoError(t, err)
	return n
}

func TestNoteRepo_Move(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	ctx := context.Background()
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	folder := createTestFolder(t, pool, user, "Dest")

	note := createNoteInFolder(t, pool, user.ID, nil, "Move Me", "move-me", "test-uuid-1/move-me.md")

	err := noteRepo.Move(ctx, user.ID, note.ID, &folder.ID, "test-uuid-1/Dest/move-me.md")
	require.NoError(t, err)

	got, err := noteRepo.GetByID(ctx, user.ID, note.ID)
	require.NoError(t, err)
	require.NotNil(t, got.FolderID)
	assert.Equal(t, folder.ID, *got.FolderID)
}

func TestNoteRepo_Move_NotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	ctx := context.Background()
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	err := noteRepo.Move(ctx, user.ID, 99999, nil, "test-uuid-1/nowhere.md")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestNoteRepo_Move_Conflict(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	ctx := context.Background()
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	folder := createTestFolder(t, pool, user, "CF")

	createNoteInFolder(t, pool, user.ID, &folder.ID, "ConfNote", "confnote", "test-uuid-1/CF/confnote.md")
	note2 := createNoteInFolder(t, pool, user.ID, nil, "Other", "other", "test-uuid-1/other.md")

	// Move note2 to the same disk path as note1 — should conflict.
	err := noteRepo.Move(ctx, user.ID, note2.ID, &folder.ID, "test-uuid-1/CF/confnote.md")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 409, appErr.Code)
}

// ─── NoteRepo.ListForContext ──────────────────────────────────────────────────

func TestNoteRepo_ListForContext_NilFolder(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	ctx := context.Background()
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)

	createNoteInFolder(t, pool, user.ID, nil, "Root Note", "root-note", "test-uuid-1/root-note.md")

	got, err := noteRepo.ListForContext(ctx, user.ID, nil)
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, "Root Note", got[0].Title)
}

func TestNoteRepo_ListForContext_WithFolder(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	ctx := context.Background()
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	folder := createTestFolder(t, pool, user, "CTX")

	createNoteInFolder(t, pool, user.ID, &folder.ID, "Folder Note", "folder-note", "test-uuid-1/CTX/folder-note.md")
	createNoteInFolder(t, pool, user.ID, nil, "Root Note", "root-note", "test-uuid-1/root-note.md")

	got, err := noteRepo.ListForContext(ctx, user.ID, &folder.ID)
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, "Folder Note", got[0].Title)
}

// ─── APIToken SetName / SetScope same-value regressions ───────────────────────
//
// Before v1.5.11.1 these methods treated RowsAffected == 0 as "token not found,"
// which was wrong on MySQL (changed-rows semantics) and could also bite on
// SQLite for same-value updates. The repo now verifies existence before
// returning NotFound.

func TestAPITokenRepo_SetName_SameValue_IsNotNotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	ctx := context.Background()
	repo := NewAPITokenRepo(pool.ReadDB, pool.WriteDB)

	tok, err := repo.Create(ctx, user.ID, "Pinned", "tn_noopname", "readwrite")
	require.NoError(t, err)

	// Same name twice must both succeed.
	require.NoError(t, repo.SetName(ctx, user.ID, tok.ID, "Renamed"))
	require.NoError(t, repo.SetName(ctx, user.ID, tok.ID, "Renamed"))
}

func TestAPITokenRepo_SetName_Missing_ReturnsNotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	ctx := context.Background()
	repo := NewAPITokenRepo(pool.ReadDB, pool.WriteDB)

	err := repo.SetName(ctx, user.ID, 9999, "Whatever")
	require.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestAPITokenRepo_SetScope_SameValue_IsNotNotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	ctx := context.Background()
	repo := NewAPITokenRepo(pool.ReadDB, pool.WriteDB)

	tok, err := repo.Create(ctx, user.ID, "Token", "tn_noopscope", "readwrite")
	require.NoError(t, err)

	require.NoError(t, repo.SetScope(ctx, user.ID, tok.ID, "readwrite"))
	require.NoError(t, repo.SetScope(ctx, user.ID, tok.ID, "readwrite"))
}

func TestAPITokenRepo_SetScope_Missing_ReturnsNotFound(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	ctx := context.Background()
	repo := NewAPITokenRepo(pool.ReadDB, pool.WriteDB)

	err := repo.SetScope(ctx, user.ID, 9999, "read")
	require.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestAPITokenRepo_SetName_WrongOwner_ReturnsNotFound(t *testing.T) {
	pool := openTestDB(t)
	user1 := createUser(t, pool)
	userRepo := NewUserRepo(pool.WriteDB)
	user2, err := userRepo.Create(context.Background(), "user2", "hash", "test-uuid-2")
	require.NoError(t, err)
	ctx := context.Background()
	repo := NewAPITokenRepo(pool.ReadDB, pool.WriteDB)

	tok, err := repo.Create(ctx, user1.ID, "Mine", "tn_crossuser", "readwrite")
	require.NoError(t, err)

	// user2 tries to rename user1's token.
	err = repo.SetName(ctx, user2.ID, tok.ID, "Hijacked")
	require.ErrorIs(t, err, apperror.ErrNotFound)
}
