package notes_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// H-01.2 — the dot namespace is reserved at the API boundary.
//
// The rejection is the second half of the fix and depends on the first: reconcile
// now treats dot-directories as unknown rather than absent, so an account that
// already has one keeps its rows instead of losing them the moment this goes
// live. Both orders matter and both are tested here.

func TestCreateFolder_RejectsDotPrefixedNames(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	for _, name := range []string{".office", ".thornotes-trash", ".hidden"} {
		_, err := st.svc.CreateFolder(ctx, st.userID, "test-uuid", nil, name)
		require.Error(t, err, "folder name %q must be rejected", name)
		assert.Contains(t, err.Error(), "reserved", "name %q should be rejected as reserved, got %v", name, err)
	}

	// A normal name still works.
	_, err := st.svc.CreateFolder(ctx, st.userID, "test-uuid", nil, "Office")
	require.NoError(t, err)
}

func TestRenameFolder_RejectsDotPrefixedNames(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	folder, err := st.svc.CreateFolder(ctx, st.userID, "test-uuid", nil, "Office")
	require.NoError(t, err)
	folderID := folder.ID

	err = st.svc.RenameFolder(ctx, st.userID, "test-uuid", folderID, ".office")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserved")

	var name string
	require.NoError(t, st.pool.ReadDB.QueryRow(`SELECT name FROM folders WHERE id = ?`, folderID).Scan(&name))
	assert.Equal(t, "Office", name, "a rejected rename must not touch the row")
}

// The upgrade case: an account that already has a dot-named folder must keep its
// rows the first time reconcile runs after dot-prefixes were reserved. This is
// the half that has to ship first.
func TestReconcile_DotDirectoryRowsSurvive(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	hiddenDir := filepath.Join(st.notesDir, "test-uuid", ".office")
	require.NoError(t, os.MkdirAll(hiddenDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(hiddenDir, "hidden-by-dot.md"), []byte("# Hidden\n"), 0600))

	// Rows as they would already exist in a database from before the reservation.
	_, err := st.pool.WriteDB.ExecContext(ctx,
		`INSERT INTO folders (user_id, parent_id, name, disk_path) VALUES (?, NULL, ?, ?)`,
		st.userID, ".office", "test-uuid/.office")
	require.NoError(t, err)
	var folderID int64
	require.NoError(t, st.pool.ReadDB.QueryRow(`SELECT id FROM folders WHERE disk_path = ?`, "test-uuid/.office").Scan(&folderID))
	_, err = st.pool.WriteDB.ExecContext(ctx,
		`INSERT INTO notes (user_id, folder_id, title, slug, disk_path, content, content_hash, tags)
		 VALUES (?, ?, ?, ?, ?, ?, ?, '["keepme"]')`,
		st.userID, folderID, "Hidden", "hidden-by-dot", "test-uuid/.office/hidden-by-dot.md", "# Hidden\n", "abc")
	require.NoError(t, err)

	_, err = st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)

	assert.Equal(t, 1, countRows(t, st, "notes"), "a dot-directory's rows must not be deleted")
	assert.Equal(t, 1, countRows(t, st, "folders"))
	assert.Equal(t, 1, countRows(t, st, "notes WHERE tags != '[]'"), "tags must survive too")
	assert.FileExists(t, filepath.Join(hiddenDir, "hidden-by-dot.md"))
}

// H-01.5 — a skipped symlink is surfaced as a count instead of vanishing
// silently; a synced vault full of links otherwise looks like missing notes.
func TestReconcile_SkippedSymlinkCountIsSurfaced(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "linked.md"), []byte("# Linked\n"), 0600))

	userDir := filepath.Join(st.notesDir, "test-uuid")
	require.NoError(t, os.MkdirAll(userDir, 0700))
	require.NoError(t, os.Symlink(filepath.Join(outside, "linked.md"), filepath.Join(userDir, "linked.md")))

	_, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)

	h := st.svc.NotesRootHealth()
	require.NotNil(t, h.LastReconcile)
	assert.Equal(t, 1, h.LastReconcile.SkippedNonRegular,
		"the operator must be told that an entry was skipped")
}
