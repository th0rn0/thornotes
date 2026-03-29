package notes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
