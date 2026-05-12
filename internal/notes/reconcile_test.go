package notes_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/notes"
)

// TestReconcile_DiscoversNewFileOnDisk regression-guards Issue 1: dropping a
// .md file directly into the notes tree (rsync, git pull, manual copy) used
// to never reach the UI because the watcher only iterated the DB.
func TestReconcile_DiscoversNewFileOnDisk(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	// Seed the user dir by creating one real note first (also ensures the
	// FileStore probe-writes are not interfered with).
	_, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Seed", nil)
	require.NoError(t, err)

	// Drop an externally-authored file directly into the user dir.
	external := filepath.Join(st.notesDir, st.user.UUID, "from-rsync.md")
	require.NoError(t, os.WriteFile(external, []byte("# Heading From rsync\n\nbody"), 0600))

	_, err = st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)

	all, err := st.svc.ListAllNotes(ctx, st.userID)
	require.NoError(t, err)
	var titles []string
	for _, n := range all {
		titles = append(titles, n.Title)
	}
	assert.Contains(t, titles, "Heading From rsync", "external file should appear in the note list")
}

// TestReconcile_DeletesGhostNoteWhenFileRemovedFromDisk regression-guards
// Issue 1 (delete half): rm of an .md file used to leave a DB row pointing at
// a missing path forever.
func TestReconcile_DeletesGhostNoteWhenFileRemovedFromDisk(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	n, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Will Vanish", nil)
	require.NoError(t, err)

	require.NoError(t, os.Remove(filepath.Join(st.notesDir, n.DiskPath)))

	_, err = st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)

	_, err = st.svc.GetNote(ctx, st.userID, n.ID)
	require.Error(t, err, "ghost note row should be gone")
}

// TestReconcile_DiscoversNewFolderOnDisk regression-guards Issue 1 for
// directories: mkdir + .md inside used to never appear in the folder tree.
func TestReconcile_DiscoversNewFolderOnDisk(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	dir := filepath.Join(st.notesDir, st.user.UUID, "ImportedFolder")
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "child.md"), []byte("child body"), 0600))

	_, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)

	tree, err := st.svc.FolderTree(ctx, st.userID)
	require.NoError(t, err)
	var folderNames []string
	for _, f := range tree {
		folderNames = append(folderNames, f.Name)
	}
	assert.Contains(t, folderNames, "ImportedFolder")
}

// TestReconcile_RepairsStaleFolderDiskPath regression-guards Issue 2: when
// the folder-rename DB cascade fails (descendants keep old disk_path) the
// reconciler should repair the paths instead of deleting + reinserting.
// We simulate the failure by directly mutating the DB row's disk_path to a
// stale value after the on-disk dir already moved.
func TestReconcile_RepairsStaleFolderDiskPath(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	folder, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "OldName")
	require.NoError(t, err)
	n, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &folder.ID, "Inside", nil)
	require.NoError(t, err)

	// Simulate cascade-failure: rename the dir on disk, update the folder
	// row's disk_path, but leave the child note's disk_path stale.
	oldDir := filepath.Join(st.notesDir, folder.DiskPath)
	newDir := filepath.Join(st.notesDir, st.user.UUID, "NewName")
	require.NoError(t, os.Rename(oldDir, newDir))

	newFolderPath := filepath.Join(st.user.UUID, "NewName")
	_, err = st.pool.WriteDB.ExecContext(ctx, `UPDATE folders SET name = ?, disk_path = ? WHERE id = ?`, "NewName", newFolderPath, folder.ID)
	require.NoError(t, err)
	// Note row is intentionally left with old disk_path.

	_, err = st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)

	got, err := st.svc.GetNote(ctx, st.userID, n.ID)
	require.NoError(t, err, "note row must survive — not be deleted")
	assert.Equal(t, filepath.Join(newFolderPath, "inside.md"), got.DiskPath, "stale disk_path must be repaired in-place")
}

// TestUpdateNoteContent_RejectsStaleHash regression-guards Issue 4 + 5: the
// loser of an optimistic-concurrency race used to overwrite the file on disk
// before the DB rejected its update, leaving the disk and DB out of sync.
// After the fix the hash check happens before the file write.
func TestUpdateNoteContent_RejectsStaleHash(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	n, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Race Target", nil)
	require.NoError(t, err)
	staleHash := n.ContentHash

	// Tab A wins.
	_, err = st.svc.UpdateNoteContent(ctx, st.userID, n.ID, "A wins", staleHash)
	require.NoError(t, err)

	// Tab B (loser) tries to save with the now-stale hash.
	_, err = st.svc.UpdateNoteContent(ctx, st.userID, n.ID, "B loses", staleHash)
	require.Error(t, err, "stale-hash save must be rejected")

	// File on disk must still contain A's content, not B's.
	got, err := os.ReadFile(filepath.Join(st.notesDir, n.DiskPath))
	require.NoError(t, err)
	assert.Equal(t, "A wins", string(got))

	dbNote, err := st.svc.GetNote(ctx, st.userID, n.ID)
	require.NoError(t, err)
	assert.Equal(t, "A wins", dbNote.Content)
}

// TestUpdateNoteContent_ConcurrentSavesConverge regression-guards the wider
// two-tab race: many concurrent saves serialize cleanly, the final disk
// content matches the final DB content, and the file never lags behind.
func TestUpdateNoteContent_ConcurrentSavesConverge(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	n, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Concurrent Target", nil)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each goroutine reads-then-writes. Most will collide and get
			// ErrConflict; the lock ensures none clobbers the disk after a
			// failed DB write.
			fresh, err := st.svc.GetNote(ctx, st.userID, n.ID)
			if err != nil {
				return
			}
			_, _ = st.svc.UpdateNoteContent(ctx, st.userID, n.ID, "writer-content", fresh.ContentHash)
		}(i)
	}
	wg.Wait()

	finalDB, err := st.svc.GetNote(ctx, st.userID, n.ID)
	require.NoError(t, err)
	finalDisk, err := os.ReadFile(filepath.Join(st.notesDir, n.DiskPath))
	require.NoError(t, err)
	assert.Equal(t, finalDB.Content, string(finalDisk), "disk content must equal DB content after concurrent saves")
	assert.Equal(t, finalDB.ContentHash, notes.HashContent(string(finalDisk)), "hash must match disk content")
}

// TestUpdateNoteMetadata_RollsBackRenameOnDBFailure regression-guards Issue
// 3: when the DB Update half failed, the file rename used to remain on disk,
// leaving a DB row pointing at the pre-rename path forever.
//
// We simulate the DB failure by closing the DB pool just before the metadata
// update lands. The file rename must be undone.
func TestUpdateNoteMetadata_RollsBackRenameOnDBFailure(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	n, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Original Title", nil)
	require.NoError(t, err)
	oldDiskPath := filepath.Join(st.notesDir, n.DiskPath)

	// Force the DB Update to fail.
	st.pool.WriteDB.Close()

	err = st.svc.UpdateNoteMetadata(ctx, st.userID, st.user.UUID, n.ID, "New Title", nil)
	require.Error(t, err, "DB failure must propagate")

	// The file must NOT have moved — it should still be at the original disk_path.
	_, statErr := os.Stat(oldDiskPath)
	assert.NoError(t, statErr, "file must be rolled back to original disk_path after DB failure")
}
