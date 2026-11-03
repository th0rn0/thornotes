package notes_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// H-01 at the reconcile level: a symlink in the notes tree must not be followed
// into the index, and must not be mistaken for a deleted file either.

func TestReconcile_SymlinkedNoteFile_NotImportedAndRowPreserved(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	note, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Original", nil)
	require.NoError(t, err)

	// Anything that is not the notes root: the classic "read /etc/shadow" trick.
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	require.NoError(t, os.WriteFile(secret, []byte("# Attacker controlled content\n"), 0600))

	var diskPath string
	require.NoError(t, st.pool.ReadDB.QueryRow(`SELECT disk_path FROM notes WHERE id = ?`, note.ID).Scan(&diskPath))
	abs := filepath.Join(st.notesDir, diskPath)
	require.NoError(t, os.Remove(abs))
	require.NoError(t, os.Symlink(secret, abs))

	_, err = st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err, "a symlink must be skipped, not fail the whole walk")

	var content string
	require.NoError(t, st.pool.ReadDB.QueryRow(`SELECT content FROM notes WHERE id = ?`, note.ID).Scan(&content))
	assert.NotContains(t, content, "Attacker controlled content", "content outside the root must never reach the index")

	// And the row must still be there: the walk could not verify the file, so
	// the file is unknown, not absent.
	var count int
	require.NoError(t, st.pool.ReadDB.QueryRow(`SELECT COUNT(*) FROM notes WHERE id = ?`, note.ID).Scan(&count))
	assert.Equal(t, 1, count, "an unverifiable symlink must not delete the row")
}

func TestReconcile_SymlinkedDirectory_NotDescended(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "planted.md"), []byte("# Planted\n"), 0600))

	userDir := filepath.Join(st.notesDir, "test-uuid")
	require.NoError(t, os.MkdirAll(userDir, 0700))
	require.NoError(t, os.Symlink(outside, filepath.Join(userDir, "escape")))

	_, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)

	var n int
	require.NoError(t, st.pool.ReadDB.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&n))
	assert.Equal(t, 0, n, "notes reachable only through a symlinked directory must not be imported")
}
