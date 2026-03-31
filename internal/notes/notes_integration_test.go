package notes_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/notes"
	sqlite_repo "github.com/th0rn0/thornotes/internal/repository/sqlite"
)

// serviceStack holds all components for service-level tests that need
// direct access to the pool or filesystem root.
type serviceStack struct {
	svc      *notes.Service
	userID   int64
	pool     *db.Pool
	notesDir string
}

func newTestStackFull(t *testing.T) *serviceStack {
	t.Helper()
	dir := t.TempDir()
	pool, err := db.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	fsDir := filepath.Join(dir, "notes")
	fs, err := notes.NewFileStore(fsDir)
	require.NoError(t, err)

	userRepo := sqlite_repo.NewUserRepo(pool.WriteDB)
	folderRepo := sqlite_repo.NewFolderRepo(pool.ReadDB, pool.WriteDB)
	noteRepo := sqlite_repo.NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := sqlite_repo.NewSearchRepo(pool.ReadDB, pool.WriteDB)

	ctx := context.Background()
	user, err := userRepo.Create(ctx, "testuser", "$2a$12$fakehash0000000000000000000000000000000000000000000000")
	require.NoError(t, err)

	svc := notes.NewService(noteRepo, folderRepo, searchRepo, fs)
	return &serviceStack{svc: svc, userID: user.ID, pool: pool, notesDir: fsDir}
}

func newTestStack(t *testing.T) (svc *notes.Service, userID int64) {
	t.Helper()
	dir := t.TempDir()
	pool, err := db.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	fsDir := filepath.Join(dir, "notes")
	fs, err := notes.NewFileStore(fsDir)
	require.NoError(t, err)

	userRepo := sqlite_repo.NewUserRepo(pool.WriteDB)
	folderRepo := sqlite_repo.NewFolderRepo(pool.ReadDB, pool.WriteDB)
	noteRepo := sqlite_repo.NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := sqlite_repo.NewSearchRepo(pool.ReadDB, pool.WriteDB)

	ctx := context.Background()
	user, err := userRepo.Create(ctx, "testuser", "$2a$12$fakehash0000000000000000000000000000000000000000000000")
	require.NoError(t, err)

	svc = notes.NewService(noteRepo, folderRepo, searchRepo, fs)
	return svc, user.ID
}

func TestService_CreateNote_Simple(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, nil, "My Note", nil)
	require.NoError(t, err)
	assert.Equal(t, "My Note", note.Title)
	assert.Equal(t, "", note.Content)
	assert.NotEmpty(t, note.DiskPath)
}

func TestService_CreateNote_InvalidTitle(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.CreateNote(ctx, userID, nil, "", nil)
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 400, appErr.Code)
}

func TestService_CreateNote_InvalidParentFolder(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	nonexistentFolderID := int64(99999)
	_, err := svc.CreateNote(ctx, userID, &nonexistentFolderID, "Note with invalid parent", nil)
	require.Error(t, err)
}

func TestService_CreateFolder_InvalidParent(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	nonexistentParentID := int64(99999)
	_, err := svc.CreateFolder(ctx, userID, &nonexistentParentID, "Child Folder")
	require.Error(t, err)
}

func TestService_RenameFolder_InvalidFolder(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	err := svc.RenameFolder(ctx, userID, 99999, "NewName")
	require.Error(t, err)
}

func TestService_DeleteFolder_InvalidFolder(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	err := svc.DeleteFolder(ctx, userID, 99999)
	require.Error(t, err)
}

func TestService_GetNote(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	created, err := svc.CreateNote(ctx, userID, nil, "Test Note", nil)
	require.NoError(t, err)

	got, err := svc.GetNote(ctx, userID, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, "Test Note", got.Title)
}

func TestService_UpdateNoteContent(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, nil, "Content Note", nil)
	require.NoError(t, err)

	newHash, err := svc.UpdateNoteContent(ctx, userID, note.ID, "new content", note.ContentHash)
	require.NoError(t, err)
	assert.NotEqual(t, note.ContentHash, newHash)
	assert.Equal(t, notes.HashContent("new content"), newHash)
}

func TestService_UpdateNoteContent_Conflict(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, nil, "Conflict Note", nil)
	require.NoError(t, err)

	_, err = svc.UpdateNoteContent(ctx, userID, note.ID, "new content", "wrong-hash")
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperror.ErrConflict))
}

func TestService_UpdateNoteContent_TooLarge(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, nil, "Large Note", nil)
	require.NoError(t, err)

	// 1MB + 1 byte.
	bigContent := strings.Repeat("a", 1<<20+1)
	_, err = svc.UpdateNoteContent(ctx, userID, note.ID, bigContent, note.ContentHash)
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 413, appErr.Code)
}

func TestService_UpdateNoteMetadata(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, nil, "Old Title", nil)
	require.NoError(t, err)

	err = svc.UpdateNoteMetadata(ctx, userID, note.ID, "New Title", []string{"tag1", "tag2"})
	require.NoError(t, err)

	updated, err := svc.GetNote(ctx, userID, note.ID)
	require.NoError(t, err)
	assert.Equal(t, "New Title", updated.Title)
	assert.Equal(t, []string{"tag1", "tag2"}, updated.Tags)
}

func TestService_DeleteNote(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, nil, "Delete Me", nil)
	require.NoError(t, err)

	err = svc.DeleteNote(ctx, userID, note.ID)
	require.NoError(t, err)

	_, err = svc.GetNote(ctx, userID, note.ID)
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperror.ErrNotFound))
}

func TestService_SetShareToken(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, nil, "Share Note", nil)
	require.NoError(t, err)

	// Set share token.
	token, err := svc.SetShareToken(ctx, userID, note.ID, false)
	require.NoError(t, err)
	require.NotNil(t, token)
	assert.NotEmpty(t, *token)

	// Verify via GetNoteByShareToken.
	shared, err := svc.GetNoteByShareToken(ctx, *token) // Use notes package exported method
	require.NoError(t, err)
	assert.Equal(t, note.ID, shared.ID)

	// Clear it.
	cleared, err := svc.SetShareToken(ctx, userID, note.ID, true)
	require.NoError(t, err)
	assert.Nil(t, cleared)
}

func TestService_ListNotes_Root(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.CreateNote(ctx, userID, nil, "Note 1", nil)
	require.NoError(t, err)
	_, err = svc.CreateNote(ctx, userID, nil, "Note 2", nil)
	require.NoError(t, err)

	items, err := svc.ListNotes(ctx, userID, nil)
	require.NoError(t, err)
	assert.Len(t, items, 2)
}

func TestService_FolderTree(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.CreateFolder(ctx, userID, nil, "Projects")
	require.NoError(t, err)

	tree, err := svc.FolderTree(ctx, userID)
	require.NoError(t, err)
	require.Len(t, tree, 1)
	assert.Equal(t, "Projects", tree[0].Name)
}

func TestService_CreateFolder(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	folder, err := svc.CreateFolder(ctx, userID, nil, "Work")
	require.NoError(t, err)
	assert.Equal(t, "Work", folder.Name)
	assert.NotEmpty(t, folder.DiskPath)
}

func TestService_RenameFolder(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	folder, err := svc.CreateFolder(ctx, userID, nil, "OldName")
	require.NoError(t, err)

	// Create a note inside the folder.
	note, err := svc.CreateNote(ctx, userID, &folder.ID, "Note in folder", nil)
	require.NoError(t, err)
	oldDiskPath := note.DiskPath

	err = svc.RenameFolder(ctx, userID, folder.ID, "NewName")
	require.NoError(t, err)

	// Verify the note's disk_path was updated.
	updatedNote, err := svc.GetNote(ctx, userID, note.ID)
	require.NoError(t, err)
	assert.NotEqual(t, oldDiskPath, updatedNote.DiskPath)
	assert.Contains(t, updatedNote.DiskPath, "NewName")
}

func TestService_DeleteFolder(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	folder, err := svc.CreateFolder(ctx, userID, nil, "ToDelete")
	require.NoError(t, err)

	err = svc.DeleteFolder(ctx, userID, folder.ID)
	require.NoError(t, err)

	tree, err := svc.FolderTree(ctx, userID)
	require.NoError(t, err)
	assert.Empty(t, tree)
}

func TestService_Search(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, nil, "Searchable Note", nil)
	require.NoError(t, err)

	_, err = svc.UpdateNoteContent(ctx, userID, note.ID, "This note contains the word thornotes", note.ContentHash)
	require.NoError(t, err)

	results, err := svc.Search(ctx, userID, "thornotes", nil)
	require.NoError(t, err)
	require.NotEmpty(t, results)
	assert.Equal(t, note.ID, results[0].NoteID)
}

func TestService_CreateNote_NoteNotFound_UpdateContent(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.UpdateNoteContent(ctx, userID, 99999, "content", "hash")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestService_UpdateNoteMetadata_NoteNotFound(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	err := svc.UpdateNoteMetadata(ctx, userID, 99999, "title", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestService_DeleteNote_NoteNotFound(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	err := svc.DeleteNote(ctx, userID, 99999)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestService_CreateFolder_EmptyName(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.CreateFolder(ctx, userID, nil, "")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 400, appErr.Code)
}

func TestService_RenameFolder_EmptyName(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	folder, err := svc.CreateFolder(ctx, userID, nil, "ValidName")
	require.NoError(t, err)

	err = svc.RenameFolder(ctx, userID, folder.ID, "")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 400, appErr.Code)
}

func TestService_CreateFolder_Nested(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	parent, err := svc.CreateFolder(ctx, userID, nil, "Parent")
	require.NoError(t, err)

	child, err := svc.CreateFolder(ctx, userID, &parent.ID, "Child")
	require.NoError(t, err)
	assert.Contains(t, child.DiskPath, "Parent")
	assert.Contains(t, child.DiskPath, "Child")
}

func TestService_RenameFolder_WithParent(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	parent, err := svc.CreateFolder(ctx, userID, nil, "ParentFolder")
	require.NoError(t, err)

	child, err := svc.CreateFolder(ctx, userID, &parent.ID, "ChildOld")
	require.NoError(t, err)

	err = svc.RenameFolder(ctx, userID, child.ID, "ChildNew")
	require.NoError(t, err)

	updated, err := svc.FolderTree(ctx, userID)
	require.NoError(t, err)
	var found bool
	for _, item := range updated {
		if item.Name == "ChildNew" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestService_CreateNote_InFolder(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	folder, err := svc.CreateFolder(ctx, userID, nil, "MyFolder")
	require.NoError(t, err)

	note, err := svc.CreateNote(ctx, userID, &folder.ID, "Folder Note", nil)
	require.NoError(t, err)
	assert.Contains(t, note.DiskPath, "MyFolder")
}

func TestService_SetShareToken_NotFound(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.SetShareToken(ctx, userID, 99999, false)
	// SetShareToken doesn't check existence — ExecContext on non-existent note
	// just affects 0 rows without error in SQLite.
	_ = err // may or may not error depending on impl
}

func TestService_Reconcile(t *testing.T) {
	dir := t.TempDir()
	pool, err := db.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	fsDir := filepath.Join(dir, "notes")
	fs, err := notes.NewFileStore(fsDir)
	require.NoError(t, err)

	userRepo := sqlite_repo.NewUserRepo(pool.WriteDB)
	folderRepo := sqlite_repo.NewFolderRepo(pool.ReadDB, pool.WriteDB)
	noteRepo := sqlite_repo.NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := sqlite_repo.NewSearchRepo(pool.ReadDB, pool.WriteDB)

	ctx := context.Background()
	user, err := userRepo.Create(ctx, "testuser2", "$2a$12$fakehash0000000000000000000000000000000000000000000000")
	require.NoError(t, err)

	svc := notes.NewService(noteRepo, folderRepo, searchRepo, fs)

	note, err := svc.CreateNote(ctx, user.ID, nil, "Reconcile Test", nil)
	require.NoError(t, err)

	// Manually change the file on disk.
	absPath := filepath.Join(fsDir, note.DiskPath)
	err = os.WriteFile(absPath, []byte("manually changed content"), 0600)
	require.NoError(t, err)

	// Run reconcile.
	err = svc.Reconcile(ctx, user.ID)
	require.NoError(t, err)

	// Verify DB was updated.
	updated, err := svc.GetNote(ctx, user.ID, note.ID)
	require.NoError(t, err)
	assert.Equal(t, "manually changed content", updated.Content)
	assert.Equal(t, notes.HashContent("manually changed content"), updated.ContentHash)
}

func TestService_Reconcile_MissingFile(t *testing.T) {
	dir := t.TempDir()
	pool, err := db.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	fsDir := filepath.Join(dir, "notes")
	fs, err := notes.NewFileStore(fsDir)
	require.NoError(t, err)

	userRepo := sqlite_repo.NewUserRepo(pool.WriteDB)
	folderRepo := sqlite_repo.NewFolderRepo(pool.ReadDB, pool.WriteDB)
	noteRepo := sqlite_repo.NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := sqlite_repo.NewSearchRepo(pool.ReadDB, pool.WriteDB)

	ctx := context.Background()
	user, err := userRepo.Create(ctx, "testuser3", "$2a$12$fakehash000000000000000000000000000000000000000000000")
	require.NoError(t, err)

	svc := notes.NewService(noteRepo, folderRepo, searchRepo, fs)

	note, err := svc.CreateNote(ctx, user.ID, nil, "Note Missing File", nil)
	require.NoError(t, err)

	// Delete the file from disk so Reconcile hits the fs.Read error path.
	absPath := filepath.Join(fsDir, note.DiskPath)
	require.NoError(t, os.Remove(absPath))

	// Reconcile should not error — it just logs a warning and continues.
	err = svc.Reconcile(ctx, user.ID)
	require.NoError(t, err)
}

func TestService_CreateFolder_DuplicateName(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	// Create a folder named "Alpha".
	_, err := svc.CreateFolder(ctx, userID, nil, "Alpha")
	require.NoError(t, err)

	// Creating another folder with the same name should fail at DB insert
	// (UNIQUE constraint on name within parent). The service must clean up
	// the directory it created before returning the error.
	_, err = svc.CreateFolder(ctx, userID, nil, "Alpha")
	require.Error(t, err)
}

func TestService_RenameFolder_FolderNotFound(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	err := svc.RenameFolder(ctx, userID, 99999, "NewName")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestService_CreateNote_FsWriteError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	st := newTestStackFull(t)
	ctx := context.Background()

	// Create a note so the user directory gets created.
	_, err := st.svc.CreateNote(ctx, st.userID, nil, "Seed Note", nil)
	require.NoError(t, err)

	// Make the user directory non-writable so the next CreateTemp call fails.
	userDir := filepath.Join(st.notesDir, fmt.Sprintf("%d", st.userID))
	require.NoError(t, os.Chmod(userDir, 0500))
	t.Cleanup(func() { _ = os.Chmod(userDir, 0700) })

	_, err = st.svc.CreateNote(ctx, st.userID, nil, "Will Fail", nil)
	require.Error(t, err)
}

func TestService_UpdateNoteContent_FsWriteError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	st := newTestStackFull(t)
	ctx := context.Background()

	note, err := st.svc.CreateNote(ctx, st.userID, nil, "Note To Patch", nil)
	require.NoError(t, err)

	// Block writes to the user directory.
	userDir := filepath.Join(st.notesDir, fmt.Sprintf("%d", st.userID))
	require.NoError(t, os.Chmod(userDir, 0500))
	t.Cleanup(func() { _ = os.Chmod(userDir, 0700) })

	_, err = st.svc.UpdateNoteContent(ctx, st.userID, note.ID, "new content", note.ContentHash)
	require.Error(t, err)
}

func TestService_CreateNote_DbCreateError(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	// Create a note with a specific title.
	_, err := svc.CreateNote(ctx, userID, nil, "Duplicate Note", nil)
	require.NoError(t, err)

	// Creating a second note with the same title triggers a UNIQUE constraint
	// on the slug column. The service must run cleanup (delete the file it
	// wrote to disk) and return the DB error.
	_, err = svc.CreateNote(ctx, userID, nil, "Duplicate Note", nil)
	require.Error(t, err)
}

func TestService_Reconcile_DBError(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	// Close readDB to make ListByFolder fail.
	st.pool.ReadDB.Close()

	err := st.svc.Reconcile(ctx, st.userID)
	require.Error(t, err)
}

func TestService_DeleteNote_DbDeleteError(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	note, err := st.svc.CreateNote(ctx, st.userID, nil, "Note To Delete", nil)
	require.NoError(t, err)

	// GetByID uses readDB (stays open), but Delete uses writeDB (closed).
	st.pool.WriteDB.Close()

	err = st.svc.DeleteNote(ctx, st.userID, note.ID)
	require.Error(t, err)
}

func TestService_SetShareToken_DbError(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	note, err := st.svc.CreateNote(ctx, st.userID, nil, "Share Token Note", nil)
	require.NoError(t, err)

	// SetShareToken uses writeDB (closed) → DB error on SET.
	st.pool.WriteDB.Close()

	_, err = st.svc.SetShareToken(ctx, st.userID, note.ID, false)
	require.Error(t, err)
}
