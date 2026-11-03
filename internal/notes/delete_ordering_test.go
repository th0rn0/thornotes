package notes_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/notes"
	"github.com/th0rn0/thornotes/internal/repository"
	sqlite_repo "github.com/th0rn0/thornotes/internal/repository/sqlite"
)

// H-09: deletes were DB-first and the filesystem error was discarded. If the
// unlink then failed (permissions, busy mount, read-only volume) the row was
// already gone, and the next reconcile re-inserted it from the still-present
// file — the note "came back" as a new row, without its tags or share token.

// failingDeleteRepo fails Delete on demand so the rollback path can be tested
// without a real database outage.
type failingDeleteRepo struct {
	repository.NoteRepository
	fail bool
	err  error
}

func (r *failingDeleteRepo) Delete(ctx context.Context, userID, noteID int64) error {
	if r.fail {
		return r.err
	}
	return r.NoteRepository.Delete(ctx, userID, noteID)
}

func noteFilePath(t *testing.T, st *serviceStack, noteID int64) string {
	t.Helper()
	var diskPath string
	require.NoError(t, st.pool.ReadDB.QueryRow(`SELECT disk_path FROM notes WHERE id = ?`, noteID).Scan(&diskPath))
	return filepath.Join(st.notesDir, diskPath)
}

// A filesystem failure must leave both the row and the file, and must surface
// as an error rather than being swallowed.
func TestDeleteNote_DiskFailure_LeavesRowAndFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission semantics are bypassed when running as root")
	}
	st := newTestStackFull(t)
	ctx := context.Background()

	note, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Precious", []string{"important"})
	require.NoError(t, err)
	shareToken, err := st.svc.SetShareToken(ctx, st.userID, note.ID, false)
	require.NoError(t, err)
	require.NotNil(t, shareToken)

	path := noteFilePath(t, st, note.ID)
	require.FileExists(t, path)

	// Make the containing directory read-only so the atomic move to the trash
	// fails the way a read-only volume or a busy mount would.
	userDir := filepath.Dir(path)
	require.NoError(t, os.Chmod(userDir, 0500))
	t.Cleanup(func() { _ = os.Chmod(userDir, 0700) })

	err = st.svc.DeleteNote(ctx, st.userID, note.ID)
	require.Error(t, err, "a failed disk delete must not be swallowed")
	assert.FileExists(t, path, "the file must still be there")

	var count int
	require.NoError(t, st.pool.ReadDB.QueryRow(`SELECT COUNT(*) FROM notes WHERE id = ?`, note.ID).Scan(&count))
	assert.Equal(t, 1, count, "the row must survive when the file could not be removed")

	// The real harm was resurrection: after a reconcile the note came back with
	// a new identity and no tags or share token. Prove the identity is intact.
	_, err = st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	var title, token string
	require.NoError(t, st.pool.ReadDB.QueryRow(`SELECT title, COALESCE(share_token,'') FROM notes WHERE id = ?`, note.ID).Scan(&title, &token))
	assert.Equal(t, "Precious", title)
	assert.Equal(t, *shareToken, token, "the share token must survive a failed delete")
}

// Success path: the file and the row are both gone.
func TestDeleteNote_RemovesFileAndRow(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	note, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Doomed", nil)
	require.NoError(t, err)
	path := noteFilePath(t, st, note.ID)

	require.NoError(t, st.svc.DeleteNote(ctx, st.userID, note.ID))
	assert.NoFileExists(t, path)
	assert.Equal(t, 0, countRows(t, st, "notes"))
}

// If the row delete fails after the file has moved, the file must be put back.
func TestDeleteNote_DBFailure_RestoresFile(t *testing.T) {
	dir := t.TempDir()
	pool, err := db.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	fsDir := filepath.Join(dir, "notes")
	fs, err := notes.NewFileStore(fsDir)
	require.NoError(t, err)

	userRepo := sqlite_repo.NewUserRepo(pool.WriteDB)
	folderRepo := sqlite_repo.NewFolderRepo(pool.ReadDB, pool.WriteDB)
	baseNoteRepo := sqlite_repo.NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := sqlite_repo.NewSearchRepo(pool.ReadDB, pool.WriteDB)

	ctx := context.Background()
	user, err := userRepo.Create(ctx, "testuser", "$2a$12$fakehash0000000000000000000000000000000000000000000000", "test-uuid")
	require.NoError(t, err)

	// Insert the row through the healthy repo, then serve the service a repo
	// whose Delete fails — the file move has already happened by then.
	note, err := baseNoteRepo.Create(ctx, &model.Note{
		UserID: user.ID, Title: "Precious", Slug: "precious",
		DiskPath: "test-uuid/precious.md", Content: "# Precious", ContentHash: notes.HashContent("# Precious"),
	})
	require.NoError(t, err)
	require.NoError(t, fs.Write("test-uuid/precious.md", "# Precious"))

	failing := &failingDeleteRepo{NoteRepository: baseNoteRepo, fail: true, err: errors.New("db is down")}
	svc := notes.NewService(failing, folderRepo, searchRepo, sqlite_repo.NewJournalRepo(pool.ReadDB, pool.WriteDB), fs)

	err = svc.DeleteNote(ctx, user.ID, note.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db is down")
	assert.FileExists(t, filepath.Join(fsDir, "test-uuid", "precious.md"),
		"the file must be restored when the row could not be deleted")
}

// A row whose file is already gone must still be deletable — there is nothing
// to resurrect, so the missing file is not an error.
func TestDeleteNote_MissingFileStillDeletesRow(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	note, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Ghost", nil)
	require.NoError(t, err)
	require.NoError(t, os.Remove(noteFilePath(t, st, note.ID)))

	require.NoError(t, st.svc.DeleteNote(ctx, st.userID, note.ID))
	assert.Equal(t, 0, countRows(t, st, "notes"))
}

func TestDeleteFolder_DiskFailure_LeavesRowsAndFiles(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission semantics are bypassed when running as root")
	}
	st := newTestStackFull(t)
	ctx := context.Background()

	folder, err := st.svc.CreateFolder(ctx, st.userID, "test-uuid", nil, "Work")
	require.NoError(t, err)
	folderID := folder.ID
	n, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", &folderID, "Inside", []string{"tag"})
	require.NoError(t, err)
	path := noteFilePath(t, st, n.ID)

	userDir := filepath.Join(st.notesDir, "test-uuid")
	require.NoError(t, os.Chmod(userDir, 0500))
	t.Cleanup(func() { _ = os.Chmod(userDir, 0700) })

	err = st.svc.DeleteFolder(ctx, st.userID, folderID)
	require.Error(t, err, "a failed disk delete of a folder must not be swallowed")
	assert.FileExists(t, path)
	assert.Equal(t, 1, countRows(t, st, "notes"), "note rows must survive")
	assert.Equal(t, 1, countRows(t, st, "folders"), "folder rows must survive")
}

func TestDeleteFolder_RemovesTreeAndRows(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	folder, err := st.svc.CreateFolder(ctx, st.userID, "test-uuid", nil, "Work")
	require.NoError(t, err)
	folderID := folder.ID
	n, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", &folderID, "Inside", nil)
	require.NoError(t, err)
	path := noteFilePath(t, st, n.ID)

	require.NoError(t, st.svc.DeleteFolder(ctx, st.userID, folderID))
	assert.NoFileExists(t, path)
	assert.Equal(t, 0, countRows(t, st, "notes"))
	assert.Equal(t, 0, countRows(t, st, "folders"))
}
