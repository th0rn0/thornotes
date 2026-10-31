package notes

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
)

// H-01.1 — the check-then-use race.
//
// The previous implementation lstat'ed each component and then returned a path
// STRING that every caller re-resolved with a fresh syscall; a component swapped
// for a symlink in between redirected the operation outside the root. The
// security review reproduced 11 escapes in 4000 iterations on both the read and
// the write path. Containment now happens in the syscall that does the work
// (os.Root → openat2 RESOLVE_BENEATH on Linux), so there is no window to win.
//
// This is the same shape of probe: one component flips between a real directory
// and a symlink to an outside directory while reads and writes run against it.
// The assertion is a count of ZERO escapes.
func TestFileStore_SymlinkSwapRace_CannotEscape(t *testing.T) {
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.md"), []byte("OUTSIDE-SECRET"), 0600))

	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	userDir := filepath.Join(root, "u1")
	require.NoError(t, os.MkdirAll(userDir, 0700))
	// A real in-root directory the flipper can point at, so the same path is
	// sometimes a legitimate target and sometimes an escape attempt.
	realDir := filepath.Join(userDir, "real")
	require.NoError(t, os.MkdirAll(realDir, 0700))

	comp := filepath.Join(userDir, "escape")

	stop := make(chan struct{})
	var flipper sync.WaitGroup
	flipper.Add(1)
	go func() {
		defer flipper.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.Symlink(outside, comp)
			_ = os.Remove(comp)
			_ = os.Symlink(realDir, comp)
			_ = os.Remove(comp)
		}
	}()

	const iterations = 4000
	readEscapes := 0
	for i := 0; i < iterations; i++ {
		if content, rerr := fs.Read("u1/escape/secret.md"); rerr == nil && strings.Contains(content, "OUTSIDE-SECRET") {
			readEscapes++
		}
		_ = fs.Write("u1/escape/planted.md", "planted")
	}

	close(stop)
	flipper.Wait()

	// An escape also leaves the planted file, or a write temp, outside the root.
	wroteOutside := false
	if _, statErr := os.Lstat(filepath.Join(outside, "planted.md")); statErr == nil {
		wroteOutside = true
	}
	if temps, _ := filepath.Glob(filepath.Join(outside, ".thornotes-*.tmp")); len(temps) > 0 {
		wroteOutside = true
	}

	t.Logf("read escapes=%d write escaped=%v iterations=%d", readEscapes, wroteOutside, iterations)
	assert.Zero(t, readEscapes, "reads must never serve content from outside the notes root")
	assert.False(t, wroteOutside, "writes must never create files outside the notes root")
}

// The same race against delete and rename, which were path-string based too.
func TestFileStore_SymlinkSwapRace_RenameAndDelete(t *testing.T) {
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "victim.md"), []byte("keep"), 0600))

	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)
	userDir := filepath.Join(root, "u1")
	require.NoError(t, os.MkdirAll(userDir, 0700))
	realDir := filepath.Join(userDir, "real")
	require.NoError(t, os.MkdirAll(realDir, 0700))

	comp := filepath.Join(userDir, "escape")

	stop := make(chan struct{})
	var flipper sync.WaitGroup
	flipper.Add(1)
	go func() {
		defer flipper.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.Symlink(outside, comp)
			_ = os.Remove(comp)
			_ = os.Symlink(realDir, comp)
			_ = os.Remove(comp)
		}
	}()

	for i := 0; i < 2000; i++ {
		_ = fs.Delete("u1/escape/victim.md")
		_ = fs.RenameFile("u1/escape/victim.md", "u1/escape/moved.md")
		// Re-create the in-root file so the next iteration has work to do.
		_ = fs.Write("u1/real/victim.md", "in-root")
	}

	close(stop)
	flipper.Wait()

	_, statErr := os.Stat(filepath.Join(outside, "victim.md"))
	assert.NoError(t, statErr, "the outside file must survive: a delete must never reach through a symlink")
	_, movedErr := os.Stat(filepath.Join(outside, "moved.md"))
	assert.True(t, os.IsNotExist(movedErr), "a rename must never create a file outside the notes root")
}

// A stray symlink can be removed through the store (delete never follows it),
// which is the user's only way to clean one up short of shell access.
func TestFileStore_Delete_StraySymlinkIsCleanable(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "u1"), 0700))

	outside := t.TempDir()
	victim := filepath.Join(outside, "keep.md")
	require.NoError(t, os.WriteFile(victim, []byte("keep"), 0600))
	link := filepath.Join(root, "u1", "stray.md")
	require.NoError(t, os.Symlink(victim, link))

	require.NoError(t, fs.Delete("u1/stray.md"))
	_, statErr := os.Lstat(link)
	assert.True(t, os.IsNotExist(statErr), "the link itself must be gone")
	assert.FileExists(t, victim, "the target must be untouched")

	// Reading through a link is still refused, so the allowance is delete-only.
	require.NoError(t, os.Symlink(victim, link))
	_, err = fs.Read("u1/stray.md")
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperror.ErrPathTraversal), "got %v", err)
}

// H-01.4 — scope, recorded rather than fixed: a hardlink placed inside the tree
// resolves to an outside inode and is indistinguishable from an ordinary file,
// so it is out of scope. Documented here so the doc comment is not read as an
// absolute claim.
func TestFileStore_HardlinkIsOutOfScope(t *testing.T) {
	outside := t.TempDir()
	target := filepath.Join(outside, "target.md")
	require.NoError(t, os.WriteFile(target, []byte("HARDLINKED-SECRET"), 0600))

	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "u1"), 0700))
	require.NoError(t, os.Link(target, filepath.Join(root, "u1", "hard.md")))

	content, readErr := fs.Read("u1/hard.md")
	require.NoError(t, readErr)
	assert.Contains(t, content, "HARDLINKED-SECRET",
		"documented scope limit: a hardlink is a real in-root file that aliases an outside inode")
}
