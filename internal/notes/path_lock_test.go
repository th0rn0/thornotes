package notes

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/repository"
	sqlite_repo "github.com/th0rn0/thornotes/internal/repository/sqlite"
)

// H-10: path-changing mutations took no lock. The reproducible one was
// RenameFolder renaming the directory and then cascading disk_path updates
// while a concurrent UpdateNoteContent wrote to a stale disk_path — recreating
// the old directory, leaving both trees on disk with the note split between
// them.

// blockingNoteRepo parks the first GetByID until the test releases it, so a
// note write can be held open deterministically while another mutation runs.
type blockingNoteRepo struct {
	repository.NoteRepository
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingNoteRepo) GetByID(ctx context.Context, userID, noteID int64) (*model.Note, error) {
	r.once.Do(func() {
		close(r.entered)
		<-r.release
	})
	return r.NoteRepository.GetByID(ctx, userID, noteID)
}

type lockTestStack struct {
	svc      *Service
	user     *model.User
	userID   int64
	notesDir string
	pool     *db.Pool
	entered  chan struct{}
	release  chan struct{}
}

func newLockTestStack(t *testing.T) *lockTestStack {
	t.Helper()
	dir := t.TempDir()
	pool, err := db.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	notesDir := filepath.Join(dir, "notes")
	fs, err := NewFileStore(notesDir)
	require.NoError(t, err)

	userRepo := sqlite_repo.NewUserRepo(pool.WriteDB)
	folderRepo := sqlite_repo.NewFolderRepo(pool.ReadDB, pool.WriteDB)
	baseNoteRepo := sqlite_repo.NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := sqlite_repo.NewSearchRepo(pool.ReadDB, pool.WriteDB)

	entered := make(chan struct{})
	release := make(chan struct{})
	wrapped := &blockingNoteRepo{NoteRepository: baseNoteRepo, entered: entered, release: release}

	ctx := context.Background()
	user, err := userRepo.Create(ctx, "testuser", "$2a$12$fakehash0000000000000000000000000000000000000000000000", "test-uuid")
	require.NoError(t, err)

	svc := NewService(wrapped, folderRepo, searchRepo, sqlite_repo.NewJournalRepo(pool.ReadDB, pool.WriteDB), fs)
	return &lockTestStack{svc: svc, user: user, userID: user.ID, notesDir: notesDir, pool: pool, entered: entered, release: release}
}

// The primitive: an exclusive holder waits for shared holders, and shared
// holders may run together.
func TestTreeLock_ExclusiveWaitsForShared(t *testing.T) {
	var locks treeLocks

	rwShared := locks.forUser(1)
	rwShared.RLock()
	unlockShared := rwShared.RUnlock

	var (
		acquired atomic.Bool
		done     = make(chan struct{})
	)
	go func() {
		defer close(done)
		rw := locks.forUser(1)
		rw.Lock()
		// The critical section records that the exclusive side was granted.
		// Reaching this line at all is the assertion — it can only run once
		// every shared holder has released.
		acquired.Store(true)
		rw.Unlock()
	}()

	select {
	case <-done:
		t.Fatal("exclusive lock was acquired while a shared lock was still held")
	case <-time.After(150 * time.Millisecond):
	}
	assert.False(t, acquired.Load(), "exclusive side must not be granted while a shared holder runs")

	unlockShared()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("exclusive lock was not acquired after the shared lock was released")
	}
	assert.True(t, acquired.Load(), "exclusive side must be granted once the shared holder released")
}

// Different users do not contend: the lock is per tree, not global.
func TestTreeLock_DifferentUsersDoNotBlock(t *testing.T) {
	var locks treeLocks
	rw := locks.forUser(1)
	rw.RLock()
	defer rw.RUnlock()

	var acquired atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		other := locks.forUser(2)
		other.Lock()
		// Non-empty on purpose: recording that the second user's lock was
		// granted is exactly what this test observes.
		acquired.Store(true)
		other.Unlock()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a second user's tree lock should not be blocked by the first user's")
	}
	assert.True(t, acquired.Load(), "a second user's tree must not wait on the first user's")
}

// The reported bug, in the reported order: a folder rename must not run while a
// note write is in flight, because the write would recreate the old directory.
func TestRenameFolder_WaitsForInFlightNoteWrite(t *testing.T) {
	st := newLockTestStack(t)
	ctx := context.Background()

	folder, err := st.svc.CreateFolder(ctx, st.userID, "test-uuid", nil, "Work")
	require.NoError(t, err)
	folderID := folder.ID

	// Close the release channel late so the note write stays parked.
	writeDone := make(chan error, 1)
	go func() {
		_, werr := st.svc.UpdateNoteContent(ctx, st.userID, 0, "# body\n", "")
		writeDone <- werr
	}()

	// Wait until the writer is inside UpdateNoteContent, holding the shared lock.
	select {
	case <-st.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("note write never started")
	}

	renameDone := make(chan error, 1)
	go func() { renameDone <- st.svc.RenameFolder(ctx, st.userID, "test-uuid", folderID, "Work2") }()

	select {
	case earlyErr := <-renameDone:
		t.Fatalf("RenameFolder completed while a note write was still in flight: %v", earlyErr)
	case <-time.After(200 * time.Millisecond):
	}

	oldDir := filepath.Join(st.notesDir, "test-uuid", "Work")
	newDir := filepath.Join(st.notesDir, "test-uuid", "Work2")
	_, statErr := os.Stat(oldDir)
	assert.NoError(t, statErr, "the old directory must still be the live one while the write is parked")
	_, statErr = os.Stat(newDir)
	assert.True(t, os.IsNotExist(statErr), "the rename must not have happened yet")

	close(st.release)
	// The parked write targets a note that does not exist, so it must come back
	// with an error rather than hang; waiting for it also keeps the goroutine
	// from leaking. Its error value is asserted, not discarded.
	require.Error(t, <-writeDone, "the parked note write must return once released")

	select {
	case rerr := <-renameDone:
		require.NoError(t, rerr)
	case <-time.After(3 * time.Second):
		t.Fatal("RenameFolder did not proceed after the note write finished")
	}

	_, statErr = os.Stat(newDir)
	assert.NoError(t, statErr, "the rename must land once the writer is done")
	_, statErr = os.Stat(oldDir)
	assert.True(t, os.IsNotExist(statErr), "the old directory must not survive the rename")
}

// ── cross-process ownership ─────────────────────────────────────────────────

func TestOwnerLock_SecondOwnerFails(t *testing.T) {
	root := t.TempDir()

	first, err := NewFileStore(root)
	require.NoError(t, err)
	require.NoError(t, first.AcquireOwnerLock())
	require.True(t, first.OwnerLockHeld())
	t.Cleanup(first.ReleaseOwnerLock)

	second, err := NewFileStore(root)
	require.NoError(t, err)
	err = second.AcquireOwnerLock()
	require.Error(t, err, "a second process on the same notes root must fail to start")
	assert.ErrorIs(t, err, ErrNotesRootOwned)
	assert.False(t, second.OwnerLockHeld())
}

func TestOwnerLock_ReacquireAfterRelease(t *testing.T) {
	root := t.TempDir()

	first, err := NewFileStore(root)
	require.NoError(t, err)
	require.NoError(t, first.AcquireOwnerLock())
	first.ReleaseOwnerLock()
	assert.False(t, first.OwnerLockHeld())

	second, err := NewFileStore(root)
	require.NoError(t, err)
	assert.NoError(t, second.AcquireOwnerLock(), "the lock must be free once the first owner released it")
	second.ReleaseOwnerLock()
}

func TestOwnerLock_IdempotentWithinOneStore(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)
	require.NoError(t, fs.AcquireOwnerLock())
	assert.NoError(t, fs.AcquireOwnerLock(), "re-acquiring the lock on the same store is a no-op")
	fs.ReleaseOwnerLock()
}
