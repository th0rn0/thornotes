package main

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
	"github.com/th0rn0/thornotes/internal/repository/sqlite"
)

// startupFixture is a database, a notes root and a service, wired the way main
// wires them but without a listener: these tests are about the readiness window
// the startup pass owns, not about HTTP.
type startupFixture struct {
	svc      *notes.Service
	users    repository.UserRepository
	user     *model.User
	notesDir string
}

func newStartupFixture(t *testing.T) *startupFixture {
	t.Helper()
	dir := t.TempDir()
	pool, err := db.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	notesDir := filepath.Join(dir, "notes")
	fs, err := notes.NewFileStore(notesDir)
	require.NoError(t, err)

	userRepo := sqlite.NewUserRepo(pool.WriteDB)
	svc := notes.NewService(
		sqlite.NewNoteRepo(pool.ReadDB, pool.WriteDB),
		sqlite.NewFolderRepo(pool.ReadDB, pool.WriteDB),
		sqlite.NewSearchRepo(pool.ReadDB, pool.WriteDB),
		sqlite.NewJournalRepo(pool.ReadDB, pool.WriteDB),
		sqlite.NewLinkRepo(pool.ReadDB, pool.WriteDB),
		fs,
	)

	user, err := userRepo.Create(context.Background(), "startup", "hash", "startup-uuid")
	require.NoError(t, err)

	return &startupFixture{svc: svc, users: userRepo, user: user, notesDir: notesDir}
}

// failingUsers fails the very first call the startup pass makes.
type failingUsers struct{ repository.UserRepository }

func (failingUsers) IDs(context.Context) ([]int64, error) {
	return nil, errors.New("database is gone")
}

// TestStartupReconcile_ClosesTheWindowOnEveryExitPath is the test the readiness
// window most needs: the pass has four ways out — skipped, user list failed, a
// user's pass failed, and the ordinary success — and each of them must leave the
// instance ready. A missed one is invisible: the process is up, the port is open,
// and every request answers 503 until somebody restarts it.
func TestStartupReconcile_ClosesTheWindowOnEveryExitPath(t *testing.T) {
	ctx := context.Background()

	t.Run("skipped by flag", func(t *testing.T) {
		f := newStartupFixture(t)
		f.svc.BeginStartup()

		startupReconcile(ctx, f.svc, f.users, true)

		assert.False(t, f.svc.StartupReconciling(), "a skipped pass must still close the window")
	})

	t.Run("listing users fails", func(t *testing.T) {
		f := newStartupFixture(t)
		f.svc.BeginStartup()

		startupReconcile(ctx, f.svc, failingUsers{}, false)

		assert.False(t, f.svc.StartupReconciling(), "a failed pass must still close the window")
	})

	t.Run("a user's reconcile aborts", func(t *testing.T) {
		f := newStartupFixture(t)
		ctx := context.Background()

		// Index one note, then take the notes root away: the mount precondition
		// refuses the pass (a populated index must not converge to a missing root),
		// which is the error path with a real error in it rather than a stub.
		require.NoError(t, os.MkdirAll(filepath.Join(f.notesDir, f.user.UUID), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(f.notesDir, f.user.UUID, "seed.md"),
			[]byte("# Seed\n\nbody\n"), 0o600))
		_, err := f.svc.Reconcile(ctx, f.user)
		require.NoError(t, err)
		require.NoError(t, os.RemoveAll(filepath.Join(f.notesDir, f.user.UUID)))

		f.svc.BeginStartup()
		startupReconcile(ctx, f.svc, f.users, false)

		assert.False(t, f.svc.StartupReconciling(), "an aborted pass must still close the window")
		// And the abort is still reported on its own terms: the instance is ready
		// to be asked, and the answer is "the notes root is gone".
		assert.NotEmpty(t, f.svc.NotesRootHealthError(),
			"a refused pass must remain visible as an unhealthy notes root")
	})

	t.Run("ordinary pass indexes the vault", func(t *testing.T) {
		f := newStartupFixture(t)
		ctx := context.Background()

		userDir := filepath.Join(f.notesDir, f.user.UUID)
		require.NoError(t, os.MkdirAll(filepath.Join(userDir, "Inbox"), 0o700))
		for _, name := range []string{"one", "two", "three"} {
			require.NoError(t, os.WriteFile(filepath.Join(userDir, "Inbox", name+".md"),
				[]byte("# "+name+"\n\nbody\n"), 0o600))
		}

		f.svc.BeginStartup()
		require.True(t, f.svc.StartupReconciling())

		startupReconcile(ctx, f.svc, f.users, false)

		assert.False(t, f.svc.StartupReconciling(), "a finished pass must close the window")
		listed, err := f.svc.ListAllNotes(ctx, f.user.ID)
		require.NoError(t, err)
		assert.Len(t, listed, 3, "every note on disk must be in the index once the pass is done")
		assert.Empty(t, f.svc.NotesRootHealthError())
	})
}

// TestStartupReconcile_NeverDeletesOnASecondPass guards the property the fix must
// not cost: reconcile stays idempotent, so running the startup pass twice changes
// nothing and deletes nothing.
func TestStartupReconcile_NeverDeletesOnASecondPass(t *testing.T) {
	f := newStartupFixture(t)
	ctx := context.Background()

	userDir := filepath.Join(f.notesDir, f.user.UUID)
	require.NoError(t, os.MkdirAll(userDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(userDir, "keep.md"), []byte("# Keep\n\nbody\n"), 0o600))

	startupReconcile(ctx, f.svc, f.users, false)
	first, err := f.svc.ListAllNotes(ctx, f.user.ID)
	require.NoError(t, err)
	require.Len(t, first, 1)

	changes, err := f.svc.Reconcile(ctx, f.user)
	require.NoError(t, err)
	assert.Zero(t, changes, "a second pass over an unchanged vault must apply no changes")

	second, err := f.svc.ListAllNotes(ctx, f.user.ID)
	require.NoError(t, err)
	assert.Len(t, second, 1, "no note may be deleted by a pass over an unchanged vault")
	assert.Equal(t, first[0].ID, second[0].ID)
	assert.FileExists(t, filepath.Join(userDir, "keep.md"))
}
