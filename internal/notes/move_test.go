package notes_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// MoveNote
// ---------------------------------------------------------------------------

func TestService_MoveNote_ToFolder(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	folder, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Dest")
	require.NoError(t, err)

	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Root Note", nil)
	require.NoError(t, err)

	require.NoError(t, svc.MoveNote(ctx, userID, "test-uuid", note.ID, &folder.ID))

	moved, err := svc.GetNote(ctx, userID, note.ID)
	require.NoError(t, err)
	assert.Equal(t, &folder.ID, moved.FolderID)
	assert.Contains(t, moved.DiskPath, "Dest")
}

func TestService_MoveNote_ToRoot(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	folder, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "SomeFolder")
	require.NoError(t, err)

	note, err := svc.CreateNote(ctx, userID, "test-uuid", &folder.ID, "Folder Note", nil)
	require.NoError(t, err)

	require.NoError(t, svc.MoveNote(ctx, userID, "test-uuid", note.ID, nil))

	moved, err := svc.GetNote(ctx, userID, note.ID)
	require.NoError(t, err)
	assert.Nil(t, moved.FolderID)
	assert.NotContains(t, moved.DiskPath, "SomeFolder")
}

func TestService_MoveNote_NoOp_SameFolder(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	folder, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Folder")
	require.NoError(t, err)

	note, err := svc.CreateNote(ctx, userID, "test-uuid", &folder.ID, "Note", nil)
	require.NoError(t, err)

	// Move to same folder — should not error.
	require.NoError(t, svc.MoveNote(ctx, userID, "test-uuid", note.ID, &folder.ID))

	moved, err := svc.GetNote(ctx, userID, note.ID)
	require.NoError(t, err)
	assert.Equal(t, note.DiskPath, moved.DiskPath)
}

func TestService_MoveNote_FileOnDiskMoved(t *testing.T) {
	stack := newTestStackFull(t)
	svc, userID := stack.svc, stack.userID
	ctx := context.Background()

	folder, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Target")
	require.NoError(t, err)

	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "File Note", nil)
	require.NoError(t, err)
	_, err = svc.UpdateNoteContent(ctx, userID, note.ID, "content", note.ContentHash)
	require.NoError(t, err)

	require.NoError(t, svc.MoveNote(ctx, userID, "test-uuid", note.ID, &folder.ID))

	moved, err := svc.GetNote(ctx, userID, note.ID)
	require.NoError(t, err)

	// File must exist at new path.
	newAbs := filepath.Join(stack.notesDir, moved.DiskPath)
	_, err = os.Stat(newAbs)
	assert.NoError(t, err, "file should exist at new disk path")
}

// ---------------------------------------------------------------------------
// MoveFolder
// ---------------------------------------------------------------------------

func TestService_MoveFolder_ToAnotherFolder(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	parent, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Parent")
	require.NoError(t, err)
	child, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Child")
	require.NoError(t, err)

	require.NoError(t, svc.MoveFolder(ctx, userID, "test-uuid", child.ID, &parent.ID))

	// Verify child now has parent as its parent.
	stack := newTestStackFull(t) // separate stack for read-back via DB
	_ = stack                    // child's parent_id checked via service call below

	// Re-fetch the folders tree.
	tree, err := svc.FolderTree(ctx, userID)
	require.NoError(t, err)
	for _, f := range tree {
		if f.ID == child.ID {
			require.NotNil(t, f.ParentID)
			assert.Equal(t, parent.ID, *f.ParentID)
			return
		}
	}
	t.Fatal("child folder not found in tree")
}

func TestService_MoveFolder_ToRoot(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	parent, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Parent")
	require.NoError(t, err)
	child, err := svc.CreateFolder(ctx, userID, "test-uuid", &parent.ID, "Child")
	require.NoError(t, err)

	require.NoError(t, svc.MoveFolder(ctx, userID, "test-uuid", child.ID, nil))

	tree, err := svc.FolderTree(ctx, userID)
	require.NoError(t, err)
	for _, f := range tree {
		if f.ID == child.ID {
			assert.Nil(t, f.ParentID)
			return
		}
	}
	t.Fatal("child folder not found in tree")
}

func TestService_MoveFolder_NoOp_SameParent(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	parent, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Parent")
	require.NoError(t, err)
	child, err := svc.CreateFolder(ctx, userID, "test-uuid", &parent.ID, "Child")
	require.NoError(t, err)

	require.NoError(t, svc.MoveFolder(ctx, userID, "test-uuid", child.ID, &parent.ID))
}

func TestService_MoveFolder_IntoItself_ReturnsError(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	folder, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Folder")
	require.NoError(t, err)

	err = svc.MoveFolder(ctx, userID, "test-uuid", folder.ID, &folder.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "itself")
}

func TestService_MoveFolder_IntoDescendant_ReturnsError(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	parent, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Parent")
	require.NoError(t, err)
	child, err := svc.CreateFolder(ctx, userID, "test-uuid", &parent.ID, "Child")
	require.NoError(t, err)

	// Try to move parent into its own child.
	err = svc.MoveFolder(ctx, userID, "test-uuid", parent.ID, &child.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "descendant")
}

func TestService_MoveFolder_CascadesDiskPath(t *testing.T) {
	stack := newTestStackFull(t)
	svc, userID := stack.svc, stack.userID
	ctx := context.Background()

	parent, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "NewParent")
	require.NoError(t, err)
	folder, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Folder")
	require.NoError(t, err)
	note, err := svc.CreateNote(ctx, userID, "test-uuid", &folder.ID, "Note", nil)
	require.NoError(t, err)

	require.NoError(t, svc.MoveFolder(ctx, userID, "test-uuid", folder.ID, &parent.ID))

	moved, err := svc.GetNote(ctx, userID, note.ID)
	require.NoError(t, err)
	assert.Contains(t, moved.DiskPath, "NewParent")
	assert.Contains(t, moved.DiskPath, "Folder")
}
