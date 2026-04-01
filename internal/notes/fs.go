package notes

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/th0rn0/thornotes/internal/apperror"
)

// FileStore manages note files on disk.
// All paths passed to it are RELATIVE to notesRoot.
// Atomic writes via os.CreateTemp + os.Rename.
type FileStore struct {
	notesRoot string
	wg        sync.WaitGroup // tracks in-flight writes for graceful shutdown
}

func NewFileStore(notesRoot string) (*FileStore, error) {
	if err := os.MkdirAll(notesRoot, 0700); err != nil {
		return nil, fmt.Errorf("create notes root %q: %w", notesRoot, err)
	}
	abs, err := filepath.Abs(notesRoot)
	if err != nil {
		return nil, err
	}
	// Verify the directory is writable by creating and immediately removing a
	// probe file. This catches read-only mounts and permission errors at startup
	// rather than silently failing on the first note save.
	if err := probeWritable(abs); err != nil {
		return nil, fmt.Errorf("notes root %q is not writable: %w", abs, err)
	}
	return &FileStore{notesRoot: abs}, nil
}

// probeWritable creates and removes a temporary file in dir to confirm write access.
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".thornotes-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}

// Write atomically writes content to relativePath.
// The WaitGroup is incremented before the write begins and decremented after
// os.Rename completes, allowing graceful shutdown to drain in-flight writes.
func (fs *FileStore) Write(relativePath, content string) error {
	absPath, err := fs.safePath(relativePath)
	if err != nil {
		return err
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(absPath), 0700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	// Atomic write: write to temp file in the same directory, then rename.
	dir := filepath.Dir(absPath)
	tmp, err := os.CreateTemp(dir, ".thornotes-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		if isENOSPC(err) {
			return apperror.DiskFull()
		}
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		if isENOSPC(err) {
			return apperror.DiskFull()
		}
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		if isENOSPC(err) {
			return apperror.DiskFull()
		}
		return fmt.Errorf("close temp: %w", err)
	}

	fs.wg.Add(1)
	defer fs.wg.Done()

	if err := os.Rename(tmpName, absPath); err != nil {
		os.Remove(tmpName)
		if isENOSPC(err) {
			return apperror.DiskFull()
		}
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// Read returns the content of relativePath.
func (fs *FileStore) Read(relativePath string) (string, error) {
	absPath, err := fs.safePath(relativePath)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", apperror.ErrNotFound
		}
		return "", fmt.Errorf("read file: %w", err)
	}
	return string(b), nil
}

// Delete removes relativePath from disk.
func (fs *FileStore) Delete(relativePath string) error {
	absPath, err := fs.safePath(relativePath)
	if err != nil {
		return err
	}
	if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete file: %w", err)
	}
	return nil
}

// RenameDir renames a directory on disk.
// Both paths are relative to notesRoot.
func (fs *FileStore) RenameDir(oldRelPath, newRelPath string) error {
	oldAbs, err := fs.safePath(oldRelPath)
	if err != nil {
		return err
	}
	newAbs, err := fs.safePath(newRelPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(newAbs), 0700); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	return os.Rename(oldAbs, newAbs)
}

// EnsureDir creates the directory at relativePath if it doesn't exist.
func (fs *FileStore) EnsureDir(relativePath string) error {
	absPath, err := fs.safePath(relativePath)
	if err != nil {
		return err
	}
	return os.MkdirAll(absPath, 0700)
}

// RemoveDir removes an empty or non-empty directory at relativePath.
func (fs *FileStore) RemoveDir(relativePath string) error {
	absPath, err := fs.safePath(relativePath)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(absPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove dir: %w", err)
	}
	return nil
}

// Wait blocks until all in-flight writes complete (for graceful shutdown).
func (fs *FileStore) Wait() {
	fs.wg.Wait()
}

// isENOSPC reports whether err is (or wraps) a "no space left on device" error.
func isENOSPC(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == syscall.ENOSPC
}

// safePath resolves relativePath against notesRoot and verifies the result
// stays within notesRoot (prevents path traversal attacks).
func (fs *FileStore) safePath(relativePath string) (string, error) {
	// Clean first to resolve any ../ components.
	clean := filepath.Clean(relativePath)

	// Reject absolute paths — they would bypass the HasPrefix check.
	if filepath.IsAbs(clean) {
		return "", apperror.ErrPathTraversal
	}

	abs := filepath.Join(fs.notesRoot, clean)

	// Ensure the resolved path is still under notesRoot.
	if !strings.HasPrefix(abs, fs.notesRoot+string(filepath.Separator)) &&
		abs != fs.notesRoot {
		return "", apperror.ErrPathTraversal
	}

	return abs, nil
}
