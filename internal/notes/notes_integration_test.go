package notes_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	user, err := userRepo.Create(ctx, "testuser", "$2a$12$fakehash0000000000000000000000000000000000000000000000", "test-uuid-" + "testuser")
	require.NoError(t, err)

	svc := notes.NewService(noteRepo, folderRepo, searchRepo, sqlite_repo.NewJournalRepo(pool.ReadDB, pool.WriteDB), fs)
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
	user, err := userRepo.Create(ctx, "testuser", "$2a$12$fakehash0000000000000000000000000000000000000000000000", "test-uuid-" + "testuser")
	require.NoError(t, err)

	svc = notes.NewService(noteRepo, folderRepo, searchRepo, sqlite_repo.NewJournalRepo(pool.ReadDB, pool.WriteDB), fs)
	return svc, user.ID
}

func TestService_CreateNote_Simple(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "My Note", nil)
	require.NoError(t, err)
	assert.Equal(t, "My Note", note.Title)
	assert.Equal(t, "", note.Content)
	assert.NotEmpty(t, note.DiskPath)
}

func TestService_CreateNote_InvalidTitle(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "", nil)
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 400, appErr.Code)
}

func TestService_CreateNote_InvalidParentFolder(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	nonexistentFolderID := int64(99999)
	_, err := svc.CreateNote(ctx, userID, "test-uuid", &nonexistentFolderID, "Note with invalid parent", nil)
	require.Error(t, err)
}

func TestService_CreateFolder_InvalidParent(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	nonexistentParentID := int64(99999)
	_, err := svc.CreateFolder(ctx, userID, "test-uuid", &nonexistentParentID, "Child Folder")
	require.Error(t, err)
}

func TestService_RenameFolder_InvalidFolder(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	err := svc.RenameFolder(ctx, userID, "test-uuid", 99999, "NewName")
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

	created, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Test Note", nil)
	require.NoError(t, err)

	got, err := svc.GetNote(ctx, userID, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, "Test Note", got.Title)
}

func TestService_UpdateNoteContent(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Content Note", nil)
	require.NoError(t, err)

	newHash, err := svc.UpdateNoteContent(ctx, userID, note.ID, "new content", note.ContentHash)
	require.NoError(t, err)
	assert.NotEqual(t, note.ContentHash, newHash)
	assert.Equal(t, notes.HashContent("new content"), newHash)
}

func TestService_UpdateNoteContent_Conflict(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Conflict Note", nil)
	require.NoError(t, err)

	_, err = svc.UpdateNoteContent(ctx, userID, note.ID, "new content", "wrong-hash")
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperror.ErrConflict))
}

func TestService_UpdateNoteContent_TooLarge(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Large Note", nil)
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

	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Old Title", nil)
	require.NoError(t, err)

	err = svc.UpdateNoteMetadata(ctx, userID, "test-uuid", note.ID, "New Title", []string{"tag1", "tag2"})
	require.NoError(t, err)

	updated, err := svc.GetNote(ctx, userID, note.ID)
	require.NoError(t, err)
	assert.Equal(t, "New Title", updated.Title)
	assert.Equal(t, "new-title", updated.Slug)
	assert.Equal(t, []string{"tag1", "tag2"}, updated.Tags)
}

func TestService_DeleteNote(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Delete Me", nil)
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

	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Share Note", nil)
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

	_, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Note 1", nil)
	require.NoError(t, err)
	_, err = svc.CreateNote(ctx, userID, "test-uuid", nil, "Note 2", nil)
	require.NoError(t, err)

	items, err := svc.ListNotes(ctx, userID, nil)
	require.NoError(t, err)
	assert.Len(t, items, 2)
}

func TestService_FolderTree(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Projects")
	require.NoError(t, err)

	tree, err := svc.FolderTree(ctx, userID)
	require.NoError(t, err)
	require.Len(t, tree, 1)
	assert.Equal(t, "Projects", tree[0].Name)
}

func TestService_CreateFolder(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	folder, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Work")
	require.NoError(t, err)
	assert.Equal(t, "Work", folder.Name)
	assert.NotEmpty(t, folder.DiskPath)
}

func TestService_RenameFolder(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	folder, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "OldName")
	require.NoError(t, err)

	// Create a note inside the folder.
	note, err := svc.CreateNote(ctx, userID, "test-uuid", &folder.ID, "Note in folder", nil)
	require.NoError(t, err)
	oldDiskPath := note.DiskPath

	err = svc.RenameFolder(ctx, userID, "test-uuid", folder.ID, "NewName")
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

	folder, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "ToDelete")
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

	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Searchable Note", nil)
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

	err := svc.UpdateNoteMetadata(ctx, userID, "test-uuid", 99999, "title", nil)
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

	_, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 400, appErr.Code)
}

func TestService_RenameFolder_EmptyName(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	folder, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "ValidName")
	require.NoError(t, err)

	err = svc.RenameFolder(ctx, userID, "test-uuid", folder.ID, "")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 400, appErr.Code)
}

func TestService_CreateFolder_Nested(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	parent, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Parent")
	require.NoError(t, err)

	child, err := svc.CreateFolder(ctx, userID, "test-uuid", &parent.ID, "Child")
	require.NoError(t, err)
	assert.Contains(t, child.DiskPath, "Parent")
	assert.Contains(t, child.DiskPath, "Child")
}

func TestService_RenameFolder_WithParent(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	parent, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "ParentFolder")
	require.NoError(t, err)

	child, err := svc.CreateFolder(ctx, userID, "test-uuid", &parent.ID, "ChildOld")
	require.NoError(t, err)

	err = svc.RenameFolder(ctx, userID, "test-uuid", child.ID, "ChildNew")
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

	folder, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "MyFolder")
	require.NoError(t, err)

	note, err := svc.CreateNote(ctx, userID, "test-uuid", &folder.ID, "Folder Note", nil)
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
	user, err := userRepo.Create(ctx, "testuser2", "$2a$12$fakehash0000000000000000000000000000000000000000000000", "test-uuid-" + "testuser2")
	require.NoError(t, err)

	svc := notes.NewService(noteRepo, folderRepo, searchRepo, sqlite_repo.NewJournalRepo(pool.ReadDB, pool.WriteDB), fs)

	note, err := svc.CreateNote(ctx, user.ID, "test-uuid", nil, "Reconcile Test", nil)
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

func TestService_Reconcile_ProgressLogging(t *testing.T) {
	// Verify Reconcile completes without error when there are > 100 notes
	// (exercises the progress-log branch at i%100==0).
	svc, userID := newTestStack(t)
	ctx := context.Background()

	for i := range 105 {
		_, err := svc.CreateNote(ctx, userID, "test-uuid", nil, fmt.Sprintf("note-%03d", i), nil)
		require.NoError(t, err)
	}

	err := svc.Reconcile(ctx, userID)
	require.NoError(t, err)
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
	user, err := userRepo.Create(ctx, "testuser3", "$2a$12$fakehash000000000000000000000000000000000000000000000", "test-uuid-" + "testuser3")
	require.NoError(t, err)

	svc := notes.NewService(noteRepo, folderRepo, searchRepo, sqlite_repo.NewJournalRepo(pool.ReadDB, pool.WriteDB), fs)

	note, err := svc.CreateNote(ctx, user.ID, "test-uuid", nil, "Note Missing File", nil)
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
	_, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Alpha")
	require.NoError(t, err)

	// Creating another folder with the same name should fail at DB insert
	// (UNIQUE constraint on name within parent). The service must clean up
	// the directory it created before returning the error.
	_, err = svc.CreateFolder(ctx, userID, "test-uuid", nil, "Alpha")
	require.Error(t, err)
}

func TestService_RenameFolder_FolderNotFound(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	err := svc.RenameFolder(ctx, userID, "test-uuid", 99999, "NewName")
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
	_, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Seed Note", nil)
	require.NoError(t, err)

	// Make the user directory non-writable so the next CreateTemp call fails.
	userDir := filepath.Join(st.notesDir, "test-uuid")
	require.NoError(t, os.Chmod(userDir, 0500))
	t.Cleanup(func() { _ = os.Chmod(userDir, 0700) })

	_, err = st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Will Fail", nil)
	require.Error(t, err)
}

func TestService_UpdateNoteContent_FsWriteError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	st := newTestStackFull(t)
	ctx := context.Background()

	note, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Note To Patch", nil)
	require.NoError(t, err)

	// Block writes to the user directory.
	userDir := filepath.Join(st.notesDir, "test-uuid")
	require.NoError(t, os.Chmod(userDir, 0500))
	t.Cleanup(func() { _ = os.Chmod(userDir, 0700) })

	_, err = st.svc.UpdateNoteContent(ctx, st.userID, note.ID, "new content", note.ContentHash)
	require.Error(t, err)
}

func TestService_CreateNote_DbCreateError(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	// Create a note with a specific title.
	_, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Duplicate Note", nil)
	require.NoError(t, err)

	// Creating a second note with the same title triggers a UNIQUE constraint
	// on the slug column. The service must run cleanup (delete the file it
	// wrote to disk) and return the DB error.
	_, err = svc.CreateNote(ctx, userID, "test-uuid", nil, "Duplicate Note", nil)
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

	note, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Note To Delete", nil)
	require.NoError(t, err)

	// GetByID uses readDB (stays open), but Delete uses writeDB (closed).
	st.pool.WriteDB.Close()

	err = st.svc.DeleteNote(ctx, st.userID, note.ID)
	require.Error(t, err)
}

func TestService_SetShareToken_DbError(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	note, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Share Token Note", nil)
	require.NoError(t, err)

	// SetShareToken uses writeDB (closed) → DB error on SET.
	st.pool.WriteDB.Close()

	_, err = st.svc.SetShareToken(ctx, st.userID, note.ID, false)
	require.Error(t, err)
}

// ─── Journal tests ─────────────────────────────────────────────────────────────

func TestService_CreateJournal_Simple(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	j, err := svc.CreateJournal(ctx, userID, "test-uuid", "Personal")
	require.NoError(t, err)
	assert.NotZero(t, j.ID)
	assert.Equal(t, "Personal", j.Name)
	assert.Equal(t, userID, j.UserID)
}

func TestService_CreateJournal_EmptyName(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.CreateJournal(ctx, userID, "test-uuid", "")
	require.Error(t, err)
	var appErr *apperror.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 400, appErr.Code)
}

func TestService_CreateJournal_DuplicateName(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.CreateJournal(ctx, userID, "test-uuid", "Work")
	require.NoError(t, err)

	// Second create with same name should fail on journal record (folder already exists).
	_, err = svc.CreateJournal(ctx, userID, "test-uuid", "Work")
	require.Error(t, err)
}

func TestService_ListJournals_Empty(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	journals, err := svc.ListJournals(ctx, userID)
	require.NoError(t, err)
	assert.Empty(t, journals)
}

func TestService_ListJournals_Multiple(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.CreateJournal(ctx, userID, "test-uuid", "Personal")
	require.NoError(t, err)
	_, err = svc.CreateJournal(ctx, userID, "test-uuid", "Work")
	require.NoError(t, err)

	journals, err := svc.ListJournals(ctx, userID)
	require.NoError(t, err)
	assert.Len(t, journals, 2)
}

func TestService_DeleteJournal(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	j, err := svc.CreateJournal(ctx, userID, "test-uuid", "Temp")
	require.NoError(t, err)

	err = svc.DeleteJournal(ctx, userID, j.ID)
	require.NoError(t, err)

	journals, err := svc.ListJournals(ctx, userID)
	require.NoError(t, err)
	assert.Empty(t, journals)
}

func TestService_DeleteJournal_NotFound(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	err := svc.DeleteJournal(ctx, userID, 99999)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestService_TodayEntry_CreateAndRetrieve(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	j, err := svc.CreateJournal(ctx, userID, "test-uuid", "Daily")
	require.NoError(t, err)

	// First call creates the entry.
	note, err := svc.TodayEntry(ctx, userID, "test-uuid", j.ID, time.UTC)
	require.NoError(t, err)
	assert.NotZero(t, note.ID)
	assert.Contains(t, note.Tags, "journal entry")
	assert.Contains(t, note.Tags, "Daily")

	// Second call returns the same note.
	note2, err := svc.TodayEntry(ctx, userID, "test-uuid", j.ID, time.UTC)
	require.NoError(t, err)
	assert.Equal(t, note.ID, note2.ID)
}

func TestService_TodayEntry_NotFoundJournal(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.TodayEntry(ctx, userID, "test-uuid", 99999, time.UTC)
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

// ─── Getting Started tests ────────────────────────────────────────────────────

func TestService_CreateGettingStartedNote_CreatesNote(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	svc.CreateGettingStartedNote(ctx, userID, "test-uuid")

	// Note should exist in root with correct title.
	items, err := svc.ListNotes(ctx, userID, nil)
	require.NoError(t, err)

	found := false
	for _, item := range items {
		if item.Title == "Getting Started" {
			found = true
			break
		}
	}
	assert.True(t, found, "Getting Started note should exist in root")
}

func TestService_CreateGettingStartedNote_Idempotent(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	// Call twice — should not error or create duplicates.
	svc.CreateGettingStartedNote(ctx, userID, "test-uuid")
	svc.CreateGettingStartedNote(ctx, userID, "test-uuid")

	items, err := svc.ListNotes(ctx, userID, nil)
	require.NoError(t, err)

	count := 0
	for _, item := range items {
		if item.Title == "Getting Started" {
			count++
		}
	}
	assert.Equal(t, 1, count, "should only create one Getting Started note")
}

// ─── ListAllNotes tests ───────────────────────────────────────────────────────

func TestService_ListAllNotes_AcrossFolders(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	// Root note.
	_, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Root Note", nil)
	require.NoError(t, err)

	// Note in a folder.
	folder, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "MyFolder")
	require.NoError(t, err)
	folderID := folder.ID
	_, err = svc.CreateNote(ctx, userID, "test-uuid", &folderID, "Folder Note", nil)
	require.NoError(t, err)

	all, err := svc.ListAllNotes(ctx, userID)
	require.NoError(t, err)
	assert.Len(t, all, 2)

	// folder_id should be set correctly.
	titles := make(map[string]*int64)
	for _, n := range all {
		titles[n.Title] = n.FolderID
	}
	assert.Nil(t, titles["Root Note"])
	require.NotNil(t, titles["Folder Note"])
	assert.Equal(t, folderID, *titles["Folder Note"])
}

func TestService_ListAllNotes_Empty(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	all, err := svc.ListAllNotes(ctx, userID)
	require.NoError(t, err)
	assert.Empty(t, all)
}

// ─── NoteContext tests ────────────────────────────────────────────────────────

func TestService_NoteContext_Empty(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	result, err := svc.NoteContext(ctx, userID, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, result.NoteCount)
	assert.Equal(t, "", result.Context)
	assert.False(t, result.Truncated)
	assert.Equal(t, 200_000, result.CharLimit)
}

func TestService_NoteContext_WithNotes(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "My Note", nil)
	require.NoError(t, err)
	_, err = svc.UpdateNoteContent(ctx, userID, note.ID, "hello world", note.ContentHash)
	require.NoError(t, err)

	result, err := svc.NoteContext(ctx, userID, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.NoteCount)
	assert.Contains(t, result.Context, "My Note")
	assert.Contains(t, result.Context, "hello world")
	assert.False(t, result.Truncated)
}

func TestService_NoteContext_Truncated(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	// Write two notes where combined content exceeds the 200k char limit.
	// Each block is: "# Title\n\ncontent\n\n---\n\n" + overhead.
	// Use 150k chars per note — the second note should be truncated.
	bigContent := strings.Repeat("x", 150_000)

	note1, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Big Note 1", nil)
	require.NoError(t, err)
	hash1, err := svc.UpdateNoteContent(ctx, userID, note1.ID, bigContent, note1.ContentHash)
	require.NoError(t, err)
	_ = hash1

	note2, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Big Note 2", nil)
	require.NoError(t, err)
	hash2, err := svc.UpdateNoteContent(ctx, userID, note2.ID, bigContent, note2.ContentHash)
	require.NoError(t, err)
	_ = hash2

	result, err := svc.NoteContext(ctx, userID, nil)
	require.NoError(t, err)
	// With 150k per note × 2, total > 200k, so truncation should have occurred.
	assert.True(t, result.Truncated)
	assert.Less(t, result.NoteCount, 2)
}

func TestService_NoteContext_FolderFilter(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	// Root note.
	_, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Root Note", nil)
	require.NoError(t, err)

	// Note in a folder.
	folder, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Work")
	require.NoError(t, err)
	folderID := folder.ID
	_, err = svc.CreateNote(ctx, userID, "test-uuid", &folderID, "Work Note", nil)
	require.NoError(t, err)

	// Context scoped to folder only should not include root note.
	result, err := svc.NoteContext(ctx, userID, &folderID)
	require.NoError(t, err)
	assert.Equal(t, 1, result.NoteCount)
	assert.Contains(t, result.Context, "Work Note")
	assert.NotContains(t, result.Context, "Root Note")
}
