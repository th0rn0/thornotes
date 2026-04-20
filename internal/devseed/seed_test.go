package devseed_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/auth"
	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/devseed"
	"github.com/th0rn0/thornotes/internal/notes"
	sqliterepo "github.com/th0rn0/thornotes/internal/repository/sqlite"
)

// newStack wires a real SQLite pool + filesystem store + services so the
// seed exercise hits the same code paths as the live binary. The auth
// service uses the low-cost bcrypt constructor so the test stays fast.
func newStack(t *testing.T) (*auth.Service, *notes.Service, *sqliterepo.UserRepo) {
	t.Helper()
	dir := t.TempDir()

	pool, err := db.Open(filepath.Join(dir, "seed.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	fs, err := notes.NewFileStore(filepath.Join(dir, "notes"))
	require.NoError(t, err)

	userRepo := sqliterepo.NewUserRepo(pool.WriteDB)
	sessionRepo := sqliterepo.NewSessionRepo(pool.WriteDB)
	folderRepo := sqliterepo.NewFolderRepo(pool.ReadDB, pool.WriteDB)
	noteRepo := sqliterepo.NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := sqliterepo.NewSearchRepo(pool.ReadDB, pool.WriteDB)
	journalRepo := sqliterepo.NewJournalRepo(pool.ReadDB, pool.WriteDB)

	// NewServiceForTest uses bcrypt.MinCost so Register is fast in tests.
	authSvc := auth.NewServiceForTest(userRepo, sessionRepo, true)
	notesSvc := notes.NewService(noteRepo, folderRepo, searchRepo, journalRepo, fs)
	return authSvc, notesSvc, userRepo
}

func TestSeed_CreatesUserFoldersAndNotes(t *testing.T) {
	authSvc, notesSvc, userRepo := newStack(t)
	ctx := context.Background()

	stats, err := devseed.Seed(ctx, authSvc, notesSvc, userRepo)
	require.NoError(t, err)

	assert.False(t, stats.Skipped, "fresh DB should not skip seeding")
	assert.Equal(t, 18, stats.Folders, "seed tree should produce 18 folders")
	assert.Equal(t, 100, stats.Notes, "seed should produce 100 notes")

	// Verify the dev user exists and the notes landed under it.
	user, err := userRepo.GetByUsername(ctx, devseed.DevUsername)
	require.NoError(t, err)
	assert.Equal(t, devseed.DevUsername, user.Username)

	allNotes, err := notesSvc.ListAllNotes(ctx, user.ID)
	require.NoError(t, err)
	assert.Len(t, allNotes, 100)

	tree, err := notesSvc.FolderTree(ctx, user.ID)
	require.NoError(t, err)
	assert.Len(t, tree, 18)
}

func TestSeed_IdempotentWhenUserExists(t *testing.T) {
	authSvc, notesSvc, userRepo := newStack(t)
	ctx := context.Background()

	// First call seeds.
	first, err := devseed.Seed(ctx, authSvc, notesSvc, userRepo)
	require.NoError(t, err)
	require.False(t, first.Skipped)

	// Second call must be a no-op.
	second, err := devseed.Seed(ctx, authSvc, notesSvc, userRepo)
	require.NoError(t, err)
	assert.True(t, second.Skipped, "second seed should skip")
	assert.Zero(t, second.Folders)
	assert.Zero(t, second.Notes)

	// Data volumes should not change.
	user, err := userRepo.GetByUsername(ctx, devseed.DevUsername)
	require.NoError(t, err)
	allNotes, err := notesSvc.ListAllNotes(ctx, user.ID)
	require.NoError(t, err)
	assert.Len(t, allNotes, 100)
}
