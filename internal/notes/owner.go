package notes

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Cross-process writer policy
// ===========================
//
// One process owns a notes root. The FileStore is the only writer to the tree,
// the reconciler assumes it is the only thing changing it, and the in-process
// locks below are what keep concurrent requests from interleaving. None of that
// reasoning survives a second process writing the same tree: two reconcilers
// will fight over the same rows, and two writers can produce exactly the
// split-tree state the H-10 lock work exists to prevent.
//
// AcquireOwnerLock turns that from a silent corruption risk into a startup
// failure. It takes a non-blocking advisory flock on <notesRoot>/.thornotes-owner.lock
// and holds it for the lifetime of the process. A second process pointed at the
// same root gets EWOULDBLOCK immediately and is expected to exit with a clear
// message rather than start serving.
//
// The lock file itself is excluded from the reconciler's view: it lives at the
// root (never inside a user subtree) and starts with a dot.

// ownerLockName is the advisory lock file at the notes root.
const ownerLockName = ".thornotes-owner.lock"

// ErrNotesRootOwned is returned when a notes root already has a live owner.
var ErrNotesRootOwned = errors.New("notes root is owned by another process")

// AcquireOwnerLock claims exclusive ownership of the notes root for this
// process. It is safe to call once per FileStore; a second process (or a second
// FileStore in the same process) fails with ErrNotesRootOwned.
func (fs *FileStore) AcquireOwnerLock() error {
	fs.ownerMu.Lock()
	defer fs.ownerMu.Unlock()

	if fs.ownerLock != nil {
		return nil // already held by this FileStore
	}

	path := filepath.Join(fs.notesRoot, ownerLockName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open owner lock %q: %w", path, err)
	}
	if err := lockFileExclusive(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errLockBusy) {
			return fmt.Errorf("%w: %q is already owned by another thornotes process — one process per notes root",
				ErrNotesRootOwned, fs.notesRoot)
		}
		return fmt.Errorf("lock notes root %q: %w", fs.notesRoot, err)
	}
	fs.ownerLock = f
	fs.ownerLockPath = path
	return nil
}

// ReleaseOwnerLock drops the advisory lock. Safe to call when no lock is held.
func (fs *FileStore) ReleaseOwnerLock() {
	fs.ownerMu.Lock()
	defer fs.ownerMu.Unlock()
	if fs.ownerLock == nil {
		return
	}
	_ = unlockFile(fs.ownerLock)
	_ = fs.ownerLock.Close()
	fs.ownerLock = nil
	// The lock file itself is left in place: removing it would race another
	// process that is about to open and lock the same path.
}

// OwnerLockHeld reports whether this FileStore currently owns the notes root.
func (fs *FileStore) OwnerLockHeld() bool {
	fs.ownerMu.Lock()
	defer fs.ownerMu.Unlock()
	return fs.ownerLock != nil
}

// treeLocks hands out one RWMutex per user so that path-changing mutations on
// one user's tree cannot interleave while different users stay independent.
//
// Granularity, stated plainly: subtree operations (create/rename/move/delete
// folder, and a full reconcile) take the exclusive side, and single-note path
// writes (create/move/delete/rename a note, write its content) take the shared
// side. That is coarser than a true per-folder lock — two folder renames for the
// same user serialise even when they touch disjoint subtrees — and deliberately
// so: the coarse version is easy to prove correct, and folder mutations are rare
// next to note writes.
type treeLocks struct {
	mu sync.Mutex
	m  map[int64]*sync.RWMutex
}

func (t *treeLocks) forUser(userID int64) *sync.RWMutex {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = make(map[int64]*sync.RWMutex)
	}
	rw, ok := t.m[userID]
	if !ok {
		rw = &sync.RWMutex{}
		t.m[userID] = rw
	}
	return rw
}

// lockTreeShared takes the shared (reader) side of the user's tree lock and
// returns the function that releases it. Shared holders are the single-note
// path writes: they may run concurrently with each other but never with a
// subtree mutation.
func (s *Service) lockTreeShared(userID int64) func() {
	rw := s.tree.forUser(userID)
	rw.RLock()
	return rw.RUnlock
}

// lockTreeExclusive takes the exclusive side of the user's tree lock and
// returns the function that releases it. Subtree mutations hold it across the
// (disk change + row update) pair so no note write can observe a half-renamed
// tree and recreate the old directory.
func (s *Service) lockTreeExclusive(userID int64) func() {
	rw := s.tree.forUser(userID)
	rw.Lock()
	return rw.Unlock
}
