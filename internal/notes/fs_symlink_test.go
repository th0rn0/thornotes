package notes

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
)

// H-01: the notes root is a boundary. safePath was purely lexical, so a symlink
// anywhere in the path let reads and writes land outside the root: a .md symlink
// resolved to any file on the host and its content was served, and a symlinked
// parent directory let a write create files wherever it pointed.

func TestFileStore_Read_SymlinkToOutsideRoot_Rejected(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	require.NoError(t, os.WriteFile(secret, []byte("# /etc/shadow equivalent\n"), 0600))

	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(root, "user1"), 0700))
	require.NoError(t, os.Symlink(secret, filepath.Join(root, "user1", "evil.md")))

	content, err := fs.Read("user1/evil.md")
	require.Error(t, err, "reading a symlink out of the notes root must be refused")
	assert.True(t, errors.Is(err, apperror.ErrPathTraversal), "want ErrPathTraversal, got %v", err)
	assert.NotContains(t, content, "shadow", "file content must not be served")
}

func TestFileStore_Read_SymlinkedParentDir_Rejected(t *testing.T) {
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.md"), []byte("outside"), 0600))

	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "user1"), 0700))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "user1", "escape")))

	_, err = fs.Read("user1/escape/secret.md")
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperror.ErrPathTraversal), "got %v", err)
}

func TestFileStore_Write_ThroughSymlinkedParentDir_Rejected(t *testing.T) {
	outside := t.TempDir()

	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "user1"), 0700))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "user1", "escape")))

	err = fs.Write("user1/escape/planted.md", "planted")
	require.Error(t, err, "writing through a symlinked directory must be refused")
	assert.True(t, errors.Is(err, apperror.ErrPathTraversal), "got %v", err)

	_, statErr := os.Stat(filepath.Join(outside, "planted.md"))
	assert.True(t, os.IsNotExist(statErr), "nothing may be created outside the notes root")
}

func TestFileStore_WriteExclusive_ThroughSymlinkedParentDir_Rejected(t *testing.T) {
	outside := t.TempDir()

	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "user1"), 0700))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "user1", "escape")))

	err = fs.WriteExclusive("user1/escape/planted.md", "planted")
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperror.ErrPathTraversal), "got %v", err)
	_, statErr := os.Stat(filepath.Join(outside, "planted.md"))
	assert.True(t, os.IsNotExist(statErr))
}

// Deleting a symlink is allowed and removes only the link: it is the user's only
// way to clean one up from the UI (unlink(2) never touches the target). Every
// operation that would FOLLOW the link is still refused.
func TestFileStore_Delete_SymlinkRemovesLinkOnly(t *testing.T) {
	outside := t.TempDir()
	victim := filepath.Join(outside, "precious.md")
	require.NoError(t, os.WriteFile(victim, []byte("keep me"), 0600))

	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "user1"), 0700))
	link := filepath.Join(root, "user1", "evil.md")
	require.NoError(t, os.Symlink(victim, link))

	require.NoError(t, fs.Delete("user1/evil.md"))

	_, statErr := os.Lstat(link)
	assert.True(t, os.IsNotExist(statErr), "the link itself must be gone")
	_, statErr = os.Stat(victim)
	assert.NoError(t, statErr, "the file the link pointed at must be untouched")

	// Reading through the link is still refused, even though the link is gone
	// here, and a fresh one is refused too.
	require.NoError(t, os.Symlink(victim, link))
	_, err = fs.Read("user1/evil.md")
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperror.ErrPathTraversal), "got %v", err)
}

func TestFileStore_Write_SymlinkInMiddleOfPath_Rejected(t *testing.T) {
	outside := t.TempDir()
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(root, "user1"), 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "deep"), 0700))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "user1", "a")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "deep"), filepath.Join(outside, "b")))

	err = fs.Write("user1/a/b/planted.md", "planted")
	require.Error(t, err)
	assert.True(t, errors.Is(err, apperror.ErrPathTraversal), "got %v", err)
	_, statErr := os.Stat(filepath.Join(outside, "deep", "planted.md"))
	assert.True(t, os.IsNotExist(statErr))
}

// A regular path is still fine: the boundary check must not break normal use.
func TestFileStore_Write_And_Read_StillWorkAfterBoundaryCheck(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	require.NoError(t, fs.Write("user1/a/b/c.md", "# ok"))
	content, err := fs.Read("user1/a/b/c.md")
	require.NoError(t, err)
	assert.Equal(t, "# ok", content)

	require.NoError(t, fs.WriteExclusive("user1/a/excl.md", "x"))
	require.NoError(t, fs.EnsureDir("user1/dir"))
	require.NoError(t, fs.RenameFile("user1/a/b/c.md", "user1/a/moved.md"))
	require.NoError(t, fs.Delete("user1/a/moved.md"))
}
