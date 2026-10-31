package notes_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/notes"
)

// countRows is a tiny helper for "did the index survive?" assertions.
func countRows(t *testing.T, st *serviceStack, table string) int {
	t.Helper()
	var n int
	require.NoError(t, st.pool.ReadDB.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&n))
	return n
}

// snapshotIndex records everything a data-loss bug would destroy: the row
// counts plus the per-note metadata (tags, share token) that reconciliation
// would otherwise strip if it re-inserted the note from disk.
func snapshotIndex(t *testing.T, st *serviceStack) (notes_, folders_, tags, tokens int) {
	t.Helper()
	return countRows(t, st, "notes"),
		countRows(t, st, "folders"),
		countRows(t, st, "notes WHERE tags != '[]'"),
		countRows(t, st, "notes WHERE share_token IS NOT NULL")
}

// ── C-03 decisive test ───────────────────────────────────────────────────────
//
// A populated index plus a notes root that resolves to an empty directory (a
// bind-mount typo, a failed mount, a freshly-created-but-empty volume) must
// ABORT. Before the fix, walkUserTree returned "no notes on disk" and the
// reconciler deleted every note and folder row — taking tags and share tokens
// with them — while the files sat safe on the real volume.
func TestReconcile_EmptyRootWithPopulatedDB_Aborts(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	folder, err := st.svc.CreateFolder(ctx, st.userID, "test-uuid", nil, "Work")
	require.NoError(t, err)
	folderID := folder.ID
	for _, title := range []string{"Alpha", "Beta", "Gamma"} {
		n, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", &folderID, title, []string{"keepme"})
		require.NoError(t, err)
		_, err = st.svc.SetShareToken(ctx, st.userID, n.ID, false)
		require.NoError(t, err)
	}

	beforeNotes, beforeFolders, beforeTags, beforeTokens := snapshotIndex(t, st)
	require.Equal(t, 3, beforeNotes)
	require.Equal(t, 3, beforeTokens)

	// The volume is not where it should be: the root exists but is empty.
	require.NoError(t, os.RemoveAll(st.notesDir))
	require.NoError(t, os.MkdirAll(st.notesDir, 0700))

	_, err = st.svc.Reconcile(ctx, st.user)
	require.Error(t, err, "reconcile must refuse to wipe a populated index against an empty root")
	require.True(t, errors.Is(err, notes.ErrReconcileAborted), "want ErrReconcileAborted, got %v", err)

	afterNotes, afterFolders, afterTags, afterTokens := snapshotIndex(t, st)
	assert.Equal(t, beforeNotes, afterNotes, "note rows must survive an empty-root reconcile")
	assert.Equal(t, beforeFolders, afterFolders, "folder rows must survive an empty-root reconcile")
	assert.Equal(t, beforeTags, afterTags, "tags must survive an empty-root reconcile")
	assert.Equal(t, beforeTokens, afterTokens, "share tokens must survive an empty-root reconcile")
}

// The same failure with the root directory missing entirely (a volume that
// never mounted) rather than present-but-empty.
func TestReconcile_MissingRootWithPopulatedDB_Aborts(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	_, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Solo", nil)
	require.NoError(t, err)
	beforeNotes, _, _, _ := snapshotIndex(t, st)

	require.NoError(t, os.RemoveAll(st.notesDir))

	_, err = st.svc.Reconcile(ctx, st.user)
	require.Error(t, err)
	require.True(t, errors.Is(err, notes.ErrReconcileAborted), "got %v", err)
	assert.Equal(t, beforeNotes, countRows(t, st, "notes"))
}

// An unreadable root (chmod 000) is unknown, not absent.
func TestReconcile_UnreadableRoot_Aborts(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission semantics are bypassed when running as root")
	}
	st := newTestStackFull(t)
	ctx := context.Background()

	_, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Solo", nil)
	require.NoError(t, err)
	beforeNotes, _, _, _ := snapshotIndex(t, st)

	require.NoError(t, os.Chmod(st.notesDir, 0))
	t.Cleanup(func() { _ = os.Chmod(st.notesDir, 0700) })

	_, err = st.svc.Reconcile(ctx, st.user)
	require.Error(t, err)
	require.True(t, errors.Is(err, notes.ErrReconcileAborted), "got %v", err)
	assert.Equal(t, beforeNotes, countRows(t, st, "notes"))
}

// A per-directory read error must leave that subtree's rows alone while the
// rest of the tree still reconciles normally.
func TestReconcile_UnreadableSubtree_DoesNotDeleteItsRows(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission semantics are bypassed when running as root")
	}
	st := newTestStackFull(t)
	ctx := context.Background()

	locked, err := st.svc.CreateFolder(ctx, st.userID, "test-uuid", nil, "Locked")
	require.NoError(t, err)
	other, err := st.svc.CreateFolder(ctx, st.userID, "test-uuid", nil, "Other")
	require.NoError(t, err)
	lockedID, otherID := locked.ID, other.ID

	inner, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", &lockedID, "Inner", []string{"tag"})
	require.NoError(t, err)
	outer, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", &otherID, "Outer", nil)
	require.NoError(t, err)
	require.NotZero(t, inner.ID)
	require.NotZero(t, outer.ID)

	lockedDir := filepath.Join(st.notesDir, "test-uuid", "Locked")
	require.NoError(t, os.Chmod(lockedDir, 0))
	t.Cleanup(func() { _ = os.Chmod(lockedDir, 0700) })

	_, err = st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err, "an unreadable subtree must not fail the whole pass")

	assert.Equal(t, 2, countRows(t, st, "notes"), "rows under the unreadable subtree must not be deleted")
	assert.Equal(t, 2, countRows(t, st, "folders"))
}

// ── Deletion circuit-breaker ─────────────────────────────────────────────────

func TestReconcile_DeletionCircuitBreaker_Aborts(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	ids := make([]int64, 0, 10)
	for _, title := range []string{"N1", "N2", "N3", "N4", "N5", "N6", "N7", "N8", "N9", "N10"} {
		n, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, title, []string{"t"})
		require.NoError(t, err)
		ids = append(ids, n.ID)
	}
	beforeNotes, _, beforeTags, _ := snapshotIndex(t, st)
	require.Equal(t, 10, beforeNotes)

	// 8 of 10 files vanish from disk — 80%, far above the default guard.
	for _, id := range ids[:8] {
		var path string
		require.NoError(t, st.pool.ReadDB.QueryRow(`SELECT disk_path FROM notes WHERE id = ?`, id).Scan(&path))
		require.NoError(t, os.Remove(filepath.Join(st.notesDir, path)))
	}

	_, err := st.svc.Reconcile(ctx, st.user)
	require.Error(t, err, "deleting 80%% of a user's notes must trip the circuit breaker")
	require.True(t, errors.Is(err, notes.ErrReconcileAborted), "got %v", err)

	assert.Equal(t, beforeNotes, countRows(t, st, "notes"), "nothing may be deleted when the breaker trips")
	assert.Equal(t, beforeTags, countRows(t, st, "notes WHERE tags != '[]'"))
}

// A small, plausible deletion stays below the guard and still works.
func TestReconcile_BelowDeleteThreshold_StillDeletes(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	ids := make([]int64, 0, 10)
	for _, title := range []string{"N1", "N2", "N3", "N4", "N5", "N6", "N7", "N8", "N9", "N10"} {
		n, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, title, nil)
		require.NoError(t, err)
		ids = append(ids, n.ID)
	}

	var path string
	require.NoError(t, st.pool.ReadDB.QueryRow(`SELECT disk_path FROM notes WHERE id = ?`, ids[0]).Scan(&path))
	require.NoError(t, os.Remove(filepath.Join(st.notesDir, path)))

	_, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	assert.Equal(t, 9, countRows(t, st, "notes"), "one deletion out of ten is below the guard")
}

// The guard is configurable so an operator can deliberately reconcile a large
// intentional deletion.
func TestReconcile_DeletionGuardIsConfigurable(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()
	st.svc.SetReconcileDeleteGuard(1.0, 0) // permissive

	ids := make([]int64, 0, 10)
	for _, title := range []string{"N1", "N2", "N3", "N4", "N5", "N6", "N7", "N8", "N9", "N10"} {
		n, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, title, nil)
		require.NoError(t, err)
		ids = append(ids, n.ID)
	}
	for _, id := range ids[:8] {
		var path string
		require.NoError(t, st.pool.ReadDB.QueryRow(`SELECT disk_path FROM notes WHERE id = ?`, id).Scan(&path))
		require.NoError(t, os.Remove(filepath.Join(st.notesDir, path)))
	}

	_, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err, "a permissive guard allows the deliberate reconcile")
	assert.Equal(t, 2, countRows(t, st, "notes"))
}

// ── Notes-root health readiness ──────────────────────────────────────────────

func TestNotesRootHealth_HealthyWhenReadableAndReconciled(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()
	_, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Solo", nil)
	require.NoError(t, err)

	_, err = st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)

	h := st.svc.NotesRootHealth()
	assert.True(t, h.Exists)
	assert.True(t, h.Readable)
	assert.True(t, h.Healthy(), "a readable, successfully-reconciled root is healthy")
	require.NotNil(t, h.LastReconcile)
	assert.True(t, h.LastReconcile.OK)
}

func TestNotesRootHealth_UnhealthyAfterAbortedReconcile(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()
	_, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Solo", nil)
	require.NoError(t, err)

	require.NoError(t, os.RemoveAll(st.notesDir))
	require.NoError(t, os.MkdirAll(st.notesDir, 0700))

	_, err = st.svc.Reconcile(ctx, st.user)
	require.Error(t, err)

	h := st.svc.NotesRootHealth()
	assert.False(t, h.Healthy(), "an aborted reconcile must leave readiness unhealthy")
	require.NotNil(t, h.LastReconcile)
	assert.False(t, h.LastReconcile.OK)
	assert.NotEmpty(t, h.LastReconcile.Error)
}

func TestNotesRootHealth_UnhealthyWhenRootMissing(t *testing.T) {
	st := newTestStackFull(t)
	require.NoError(t, os.RemoveAll(st.notesDir))

	h := st.svc.NotesRootHealth()
	assert.False(t, h.Exists)
	assert.False(t, h.Readable)
	assert.False(t, h.Healthy())
}

// A root that is present but empty (the mount came up, but on the wrong volume)
// has to be refused too — the user's directory exists, so an "it's missing"
// check alone would not catch it.
func TestReconcile_EmptyUserDirWithPopulatedDB_Aborts(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	for _, title := range []string{"Alpha", "Beta", "Gamma"} {
		_, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, title, nil)
		require.NoError(t, err)
	}
	beforeNotes, _, _, _ := snapshotIndex(t, st)
	require.Equal(t, 3, beforeNotes)

	userDir := filepath.Join(st.notesDir, "test-uuid")
	require.NoError(t, os.RemoveAll(userDir))
	require.NoError(t, os.MkdirAll(userDir, 0700))

	_, err := st.svc.Reconcile(ctx, st.user)
	require.Error(t, err)
	require.True(t, errors.Is(err, notes.ErrReconcileAborted), "got %v", err)
	assert.Equal(t, beforeNotes, countRows(t, st, "notes"))
}
