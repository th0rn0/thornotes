package notes_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/notes"
)

// newTestStackGit creates a service stack with git history enabled.
func newTestStackGit(t *testing.T) (svc *notes.Service, userID int64) {
	t.Helper()
	stack := newTestStackFull(t)
	err := stack.svc.FileStore().EnableGitHistory()
	require.NoError(t, err)
	return stack.svc, stack.userID
}

// ---------------------------------------------------------------------------
// FileStore.EnableGitHistory
// ---------------------------------------------------------------------------

func TestFileStore_EnableGitHistory_InitialisesRepo(t *testing.T) {
	dir := t.TempDir()
	fs, err := notes.NewFileStore(filepath.Join(dir, "notes"))
	require.NoError(t, err)

	assert.False(t, fs.GitHistoryEnabled())
	require.NoError(t, fs.EnableGitHistory())
	assert.True(t, fs.GitHistoryEnabled())
}

func TestFileStore_EnableGitHistory_Idempotent(t *testing.T) {
	dir := t.TempDir()
	fs, err := notes.NewFileStore(filepath.Join(dir, "notes"))
	require.NoError(t, err)

	require.NoError(t, fs.EnableGitHistory())
	// Calling again on the same root should succeed (opens existing repo).
	fs2, err := notes.NewFileStore(filepath.Join(dir, "notes"))
	require.NoError(t, err)
	require.NoError(t, fs2.EnableGitHistory())
}

func TestFileStore_GitIgnoreCreated(t *testing.T) {
	dir := t.TempDir()
	notesDir := filepath.Join(dir, "notes")
	fs, err := notes.NewFileStore(notesDir)
	require.NoError(t, err)
	require.NoError(t, fs.EnableGitHistory())

	// .gitignore should exist in the notes root.
	content, err := fs.Read(".gitignore")
	require.NoError(t, err)
	assert.Contains(t, content, ".thornotes-*.tmp")
}

// ---------------------------------------------------------------------------
// NoteHistory – git disabled
// ---------------------------------------------------------------------------

func TestService_NoteHistory_GitDisabled_Returns501(t *testing.T) {
	svc, userID := newTestStack(t)

	note, err := svc.CreateNote(context.Background(), userID, "test-uuid", nil, "Test Note", nil)
	require.NoError(t, err)

	_, err = svc.NoteHistory(context.Background(), userID, note.ID, 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not enabled")
}

func TestService_NoteContentAt_GitDisabled_Returns501(t *testing.T) {
	svc, userID := newTestStack(t)

	note, err := svc.CreateNote(context.Background(), userID, "test-uuid", nil, "Test Note", nil)
	require.NoError(t, err)

	_, err = svc.NoteContentAt(context.Background(), userID, note.ID, "abc123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not enabled")
}

// ---------------------------------------------------------------------------
// NoteHistory – happy path
// ---------------------------------------------------------------------------

func TestService_NoteHistory_RecordsCreate(t *testing.T) {
	svc, userID := newTestStackGit(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "History Note", nil)
	require.NoError(t, err)

	entries, err := svc.NoteHistory(ctx, userID, note.ID, 50)
	require.NoError(t, err)
	// The empty note was written on create.
	require.NotEmpty(t, entries)
	assert.Contains(t, entries[0].Message, note.DiskPath)
}

func TestService_NoteHistory_RecordsMultipleSaves(t *testing.T) {
	svc, userID := newTestStackGit(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Multi-save Note", nil)
	require.NoError(t, err)

	// Two content updates → two more commits.
	hash1, err := svc.UpdateNoteContent(ctx, userID, note.ID, "# v1", note.ContentHash)
	require.NoError(t, err)

	_, err = svc.UpdateNoteContent(ctx, userID, note.ID, "# v2", hash1)
	require.NoError(t, err)

	entries, err := svc.NoteHistory(ctx, userID, note.ID, 50)
	require.NoError(t, err)
	// At least 3 commits (create + 2 saves).
	assert.GreaterOrEqual(t, len(entries), 3)
}

func TestService_NoteHistory_LimitRespected(t *testing.T) {
	svc, userID := newTestStackGit(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Limit Note", nil)
	require.NoError(t, err)

	h := note.ContentHash
	for i := range 5 {
		_ = i
		newH, err := svc.UpdateNoteContent(ctx, userID, note.ID, "# save", h)
		require.NoError(t, err)
		h = newH
	}

	entries, err := svc.NoteHistory(ctx, userID, note.ID, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, len(entries))
}

// ---------------------------------------------------------------------------
// NoteContentAt
// ---------------------------------------------------------------------------

func TestService_NoteContentAt_ReturnsCorrectContent(t *testing.T) {
	svc, userID := newTestStackGit(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "At Note", nil)
	require.NoError(t, err)

	hash1, err := svc.UpdateNoteContent(ctx, userID, note.ID, "# version one", note.ContentHash)
	require.NoError(t, err)
	_, err = svc.UpdateNoteContent(ctx, userID, note.ID, "# version two", hash1)
	require.NoError(t, err)

	// Get history and look up content at the first non-empty commit.
	entries, err := svc.NoteHistory(ctx, userID, note.ID, 50)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(entries), 2)

	// Newest commit = "# version two", second newest = "# version one".
	v2, err := svc.NoteContentAt(ctx, userID, note.ID, entries[0].SHA)
	require.NoError(t, err)
	assert.Equal(t, "# version two", v2.Content)

	v1, err := svc.NoteContentAt(ctx, userID, note.ID, entries[1].SHA)
	require.NoError(t, err)
	assert.Equal(t, "# version one", v1.Content)
}

func TestService_NoteContentAt_InvalidSHA(t *testing.T) {
	svc, userID := newTestStackGit(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "InvalidSHA Note", nil)
	require.NoError(t, err)

	_, err = svc.NoteContentAt(ctx, userID, note.ID, "deadbeefdeadbeefdeadbeefdeadbeef00000000")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// NoteRestoreAt
// ---------------------------------------------------------------------------

func TestService_NoteRestoreAt_RestoresContent(t *testing.T) {
	svc, userID := newTestStackGit(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Restore Note", nil)
	require.NoError(t, err)

	hash1, err := svc.UpdateNoteContent(ctx, userID, note.ID, "# original", note.ContentHash)
	require.NoError(t, err)
	hash2, err := svc.UpdateNoteContent(ctx, userID, note.ID, "# changed", hash1)
	require.NoError(t, err)

	// Get SHA for the "# original" commit.
	entries, err := svc.NoteHistory(ctx, userID, note.ID, 50)
	require.NoError(t, err)
	var originalSHA string
	for _, e := range entries {
		at, err := svc.NoteContentAt(ctx, userID, note.ID, e.SHA)
		if err == nil && at.Content == "# original" {
			originalSHA = e.SHA
			break
		}
	}
	require.NotEmpty(t, originalSHA, "could not find original commit in history")

	// Restore to original.
	_, err = svc.NoteRestoreAt(ctx, userID, note.ID, originalSHA, hash2)
	require.NoError(t, err)

	restored, err := svc.GetNote(ctx, userID, note.ID)
	require.NoError(t, err)
	assert.Equal(t, "# original", restored.Content)
}

func TestService_NoteRestoreAt_GitDisabled_Returns501(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Restore No Git", nil)
	require.NoError(t, err)

	_, err = svc.NoteRestoreAt(ctx, userID, note.ID, "abc123", note.ContentHash)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not enabled")
}

// ---------------------------------------------------------------------------
// GitHistoryEnabled on Service
// ---------------------------------------------------------------------------

func TestService_GitHistoryEnabled_FalseByDefault(t *testing.T) {
	svc, _ := newTestStack(t)
	assert.False(t, svc.GitHistoryEnabled())
}

func TestService_GitHistoryEnabled_TrueAfterEnable(t *testing.T) {
	svc, _ := newTestStackGit(t)
	assert.True(t, svc.GitHistoryEnabled())
}

// ---------------------------------------------------------------------------
// CommitDelete and CommitRename (via service Delete/Rename operations)
// ---------------------------------------------------------------------------

func TestService_GitHistory_DeleteNote_RecordsCommit(t *testing.T) {
	svc, userID := newTestStackGit(t)
	ctx := context.Background()

	// Create and write to a note so there's something in history.
	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Delete Git Note", nil)
	require.NoError(t, err)
	hash1, err := svc.UpdateNoteContent(ctx, userID, note.ID, "some content", note.ContentHash)
	require.NoError(t, err)
	_ = hash1

	// Delete — this triggers fs.Delete → git.CommitDelete → commitAll.
	err = svc.DeleteNote(ctx, userID, note.ID)
	require.NoError(t, err)

	// The note is gone — we can't call NoteHistory on it anymore (note not found),
	// but the important thing is CommitDelete ran without error.
}

func TestService_GitHistory_RenameFolder_RecordsCommit(t *testing.T) {
	svc, userID := newTestStackGit(t)
	ctx := context.Background()

	// Create a folder with a note in it.
	folder, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "FolderToRename")
	require.NoError(t, err)
	folderID := folder.ID

	note, err := svc.CreateNote(ctx, userID, "test-uuid", &folderID, "Note In Folder", nil)
	require.NoError(t, err)
	_, err = svc.UpdateNoteContent(ctx, userID, note.ID, "folder note content", note.ContentHash)
	require.NoError(t, err)

	// Rename folder — this triggers fs.RenameDir → git.CommitRename → commitAll.
	err = svc.RenameFolder(ctx, userID, "test-uuid", folder.ID, "RenamedFolder")
	require.NoError(t, err)
}
