package notes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/apperror"
)

func TestFileStore_Write_And_Read(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	err = fs.Write("user1/notes/hello.md", "# Hello")
	require.NoError(t, err)

	content, err := fs.Read("user1/notes/hello.md")
	require.NoError(t, err)
	assert.Equal(t, "# Hello", content)
}

func TestFileStore_Write_AtomicRename(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	// Write once.
	err = fs.Write("note.md", "v1")
	require.NoError(t, err)

	// Overwrite.
	err = fs.Write("note.md", "v2")
	require.NoError(t, err)

	content, err := fs.Read("note.md")
	require.NoError(t, err)
	assert.Equal(t, "v2", content)

	// No temp files should remain.
	matches, _ := filepath.Glob(filepath.Join(root, ".thornotes-*.tmp"))
	assert.Empty(t, matches)
}

func TestFileStore_PathTraversal_DotDot(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	cases := []string{
		"../../../etc/passwd",
		"../../other_user/secret.md",
		"../sibling",
		"valid/../../escape",
	}

	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			// Write must return an error.
			writeErr := fs.Write(c, "thornotes-attack-content")
			assert.Error(t, writeErr, "expected error for traversal path %q", c)

			// Ensure no file containing our sentinel was written anywhere under root.
			walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return nil
				}
				data, readErr := os.ReadFile(path)
				if readErr == nil {
					assert.NotContains(t, string(data), "thornotes-attack-content",
						"attack content found at %s", path)
				}
				return nil
			})
			assert.NoError(t, walkErr)
		})
	}
}

func TestFileStore_PathTraversal_AbsolutePath(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	err = fs.Write("/etc/thornotes-escape.md", "attack")
	assert.Error(t, err)
}

func TestFileStore_Delete_NonExistent(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	// Deleting a non-existent file should not error.
	err = fs.Delete("nonexistent.md")
	assert.NoError(t, err)
}

func TestFileStore_RenameDir(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	// Create a directory with a file.
	err = fs.Write("Work/note.md", "content")
	require.NoError(t, err)

	err = fs.RenameDir("Work", "Archive")
	require.NoError(t, err)

	// File should now be at new path.
	content, err := fs.Read("Archive/note.md")
	require.NoError(t, err)
	assert.Equal(t, "content", content)

	// Old path should be gone.
	_, err = fs.Read("Work/note.md")
	assert.Error(t, err)
}

func TestFileStore_Wait_CompletesInFlightWrites(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	// Write should complete before Wait returns.
	err = fs.Write("a.md", "hello")
	require.NoError(t, err)

	// Should not block or panic.
	fs.Wait()
}

func TestFileStore_EnsureDir(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	err = fs.EnsureDir("mydir")
	require.NoError(t, err)

	// Write a file inside it.
	err = fs.Write("mydir/note.md", "content")
	require.NoError(t, err)

	// Read it back.
	content, err := fs.Read("mydir/note.md")
	require.NoError(t, err)
	assert.Equal(t, "content", content)
}

func TestFileStore_RemoveDir(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	// Create dir with a file.
	err = fs.Write("toremove/note.md", "hello")
	require.NoError(t, err)

	err = fs.RemoveDir("toremove")
	require.NoError(t, err)

	// Reading from the removed dir should fail.
	_, err = fs.Read("toremove/note.md")
	assert.Error(t, err)
}

func TestFileStore_Read_NotFound(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	_, err = fs.Read("nonexistent.md")
	require.Error(t, err)
	assert.ErrorIs(t, err, apperror.ErrNotFound)
}

func TestFileStore_NewFileStore_CreatesRoot(t *testing.T) {
	root := t.TempDir()
	subdir := root + "/subdir"
	fs, err := NewFileStore(subdir)
	require.NoError(t, err)
	assert.NotNil(t, fs)

	// The subdir should have been created.
	_, statErr := os.Stat(subdir)
	assert.NoError(t, statErr)
}

func TestFileStore_NewFileStore_Fail(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	// Create a regular file where the directory should be.
	parent := t.TempDir()
	blockPath := filepath.Join(parent, "block")
	require.NoError(t, os.WriteFile(blockPath, []byte(""), 0600))

	// Trying to create FileStore at block/subdir should fail because block is a file.
	_, err := NewFileStore(filepath.Join(blockPath, "subdir"))
	require.Error(t, err)
}

func TestFileStore_Write_MkdirFail(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	// Create a FILE named "user1" where Write would want to create a directory.
	require.NoError(t, os.WriteFile(filepath.Join(root, "user1"), []byte("block"), 0600))

	// Write to user1/note.md — MkdirAll("user1") fails because user1 is a file.
	err = fs.Write("user1/note.md", "content")
	require.Error(t, err)
}

func TestFileStore_Write_CreateTempFail(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	// Create the dir first, then make it non-writable.
	require.NoError(t, fs.EnsureDir("nowrite"))
	dirPath := filepath.Join(root, "nowrite")
	require.NoError(t, os.Chmod(dirPath, 0500))
	t.Cleanup(func() { os.Chmod(dirPath, 0700) })

	// Writing should fail because CreateTemp can't create files in a non-writable dir.
	err = fs.Write("nowrite/note.md", "content")
	require.Error(t, err)
}

func TestFileStore_Delete_ErrorOnNonRemovable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	// Create a file in a directory, then make the directory non-writable.
	require.NoError(t, fs.Write("locked/file.md", "content"))
	dirPath := filepath.Join(root, "locked")
	require.NoError(t, os.Chmod(dirPath, 0500))
	t.Cleanup(func() { os.Chmod(dirPath, 0700) })

	// Delete should fail because the parent directory is not writable.
	err = fs.Delete("locked/file.md")
	require.Error(t, err)
}

func TestFileStore_RenameDir_ErrorOnBadDest(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	// Create source dir.
	require.NoError(t, fs.EnsureDir("srcdir"))

	// Make the root non-writable so MkdirAll for the dest fails.
	require.NoError(t, os.Chmod(root, 0500))
	t.Cleanup(func() { os.Chmod(root, 0700) })

	// RenameDir to a new path whose parent can't be created.
	err = fs.RenameDir("srcdir", "newdir/nested")
	require.Error(t, err)
}

func TestFileStore_Read_TraversalError(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	_, err = fs.Read("../../etc/passwd")
	assert.Error(t, err)
}

func TestFileStore_Read_PermissionDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	require.NoError(t, fs.Write("secret.md", "content"))
	secretPath := filepath.Join(root, "secret.md")
	require.NoError(t, os.Chmod(secretPath, 0000))
	t.Cleanup(func() { os.Chmod(secretPath, 0600) })

	_, err = fs.Read("secret.md")
	require.Error(t, err)
	assert.NotErrorIs(t, err, apperror.ErrNotFound)
}

func TestFileStore_Delete_TraversalError(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	err = fs.Delete("../../etc/passwd")
	assert.Error(t, err)
}

func TestFileStore_EnsureDir_TraversalError(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	err = fs.EnsureDir("../../malicious")
	assert.Error(t, err)
}

func TestFileStore_RemoveDir_TraversalError(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	err = fs.RemoveDir("../../malicious")
	assert.Error(t, err)
}

func TestFileStore_RemoveDir_PermissionError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping permission test when running as root")
	}
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	require.NoError(t, fs.Write("locked/file.md", "content"))
	lockedDir := filepath.Join(root, "locked")
	require.NoError(t, os.Chmod(lockedDir, 0500))
	t.Cleanup(func() { os.Chmod(lockedDir, 0700) })

	err = fs.RemoveDir("locked")
	require.Error(t, err)
}

func TestFileStore_RenameDir_TraversalOldPath(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	err = fs.RenameDir("../../malicious", "dest")
	assert.Error(t, err)
}

func TestFileStore_RenameDir_TraversalNewPath(t *testing.T) {
	root := t.TempDir()
	fs, err := NewFileStore(root)
	require.NoError(t, err)

	require.NoError(t, fs.EnsureDir("src"))
	err = fs.RenameDir("src", "../../malicious")
	assert.Error(t, err)
}
