package notes

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

// FileStore manages note files on disk.
// All paths passed to it are RELATIVE to notesRoot.
// Atomic writes via os.CreateTemp + os.Rename.
type FileStore struct {
	notesRoot string
	wg        sync.WaitGroup // tracks in-flight writes for graceful shutdown
	git       *gitHistory    // nil when git history is disabled
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
	return os.Remove(name)
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

	// Record the save as a git commit (non-fatal: log and continue).
	if fs.git != nil {
		if err := fs.git.CommitSave(relativePath); err != nil {
			log.Warn().Err(err).Str("path", relativePath).Msg("git commit save")
		}
	}

	return nil
}

// WriteExclusive creates relativePath with O_CREATE|O_EXCL so an existing file
// is never truncated or overwritten. Returns os.ErrExist (wrapped) if the file
// already exists.
func (fs *FileStore) WriteExclusive(relativePath, content string) error {
	absPath, err := fs.safePath(relativePath)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(absPath), 0700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	fs.wg.Add(1)
	defer fs.wg.Done()

	f, err := os.OpenFile(absPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if isENOSPC(err) {
			return apperror.DiskFull()
		}
		return fmt.Errorf("create exclusive: %w", err)
	}

	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(absPath)
		if isENOSPC(err) {
			return apperror.DiskFull()
		}
		return fmt.Errorf("write exclusive: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(absPath)
		if isENOSPC(err) {
			return apperror.DiskFull()
		}
		return fmt.Errorf("sync exclusive: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(absPath)
		if isENOSPC(err) {
			return apperror.DiskFull()
		}
		return fmt.Errorf("close exclusive: %w", err)
	}

	if fs.git != nil {
		if err := fs.git.CommitSave(relativePath); err != nil {
			log.Warn().Err(err).Str("path", relativePath).Msg("git commit save")
		}
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
	// Record the deletion as a git commit (non-fatal).
	if fs.git != nil {
		if err := fs.git.CommitDelete(relativePath); err != nil {
			log.Warn().Err(err).Str("path", relativePath).Msg("git commit delete")
		}
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
	if err := os.Rename(oldAbs, newAbs); err != nil {
		return err
	}
	// Record the rename as a git commit (non-fatal).
	if fs.git != nil {
		if err := fs.git.CommitRename(oldRelPath, newRelPath); err != nil {
			log.Warn().Err(err).Str("old", oldRelPath).Str("new", newRelPath).Msg("git commit rename")
		}
	}
	return nil
}

// RenameFile moves a single file from oldRelPath to newRelPath.
// Both paths are relative to notesRoot.
func (fs *FileStore) RenameFile(oldRelPath, newRelPath string) error {
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
	if err := os.Rename(oldAbs, newAbs); err != nil {
		return fmt.Errorf("rename file: %w", err)
	}
	if fs.git != nil {
		if err := fs.git.CommitRename(oldRelPath, newRelPath); err != nil {
			log.Warn().Err(err).Str("old", oldRelPath).Str("new", newRelPath).Msg("git commit rename")
		}
	}
	return nil
}

// RenameFileExclusive moves a single file from oldRelPath to newRelPath without
// replacing an existing destination file. It uses os.Link(oldAbs, newAbs) which
// fails atomically with os.ErrExist in the kernel if newAbs already exists,
// followed by os.Remove(oldAbs).
func (fs *FileStore) RenameFileExclusive(oldRelPath, newRelPath string) error {
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
	if err := os.Link(oldAbs, newAbs); err != nil {
		return fmt.Errorf("link file exclusive: %w", err)
	}
	if err := os.Remove(oldAbs); err != nil {
		_ = os.Remove(newAbs)
		return fmt.Errorf("remove old file after link: %w", err)
	}
	if fs.git != nil {
		if err := fs.git.CommitRename(oldRelPath, newRelPath); err != nil {
			log.Warn().Err(err).Str("old", oldRelPath).Str("new", newRelPath).Msg("git commit rename")
		}
	}
	return nil
}

// EnsureDir creates the directory at relativePath if it doesn't exist.
func (fs *FileStore) EnsureDir(relativePath string) error {
	absPath, err := fs.safePath(relativePath)
	if err != nil {
		return err
	}
	return os.MkdirAll(absPath, 0700)
}

// CreateDirExclusive creates the leaf directory at relativePath exclusively
// (parent directories are created if needed). Returns os.ErrExist if the leaf
// directory already exists on disk.
func (fs *FileStore) CreateDirExclusive(relativePath string) error {
	absPath, err := fs.safePath(relativePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0700); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	return os.Mkdir(absPath, 0700)
}

// RemoveEmptyDir removes relativePath only if it is an empty directory.
func (fs *FileStore) RemoveEmptyDir(relativePath string) error {
	absPath, err := fs.safePath(relativePath)
	if err != nil {
		return err
	}
	if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove empty dir: %w", err)
	}
	return nil
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

// EnableGitHistory initialises (or opens) a git repository at the notes root
// so that every subsequent Write/Delete/RenameDir is recorded as a commit.
// Call this once after NewFileStore, before serving requests.
func (fs *FileStore) EnableGitHistory() error {
	g, err := openOrInitGitRepo(fs.notesRoot)
	if err != nil {
		return err
	}
	fs.git = g
	return nil
}

// GitHistoryEnabled reports whether git history tracking is active.
func (fs *FileStore) GitHistoryEnabled() bool {
	return fs.git != nil
}

// GitLogFile returns the commit history for the file at relativePath,
// newest first. Returns nil when git history is disabled.
func (fs *FileStore) GitLogFile(relativePath string, limit int) ([]model.HistoryEntry, error) {
	if fs.git == nil {
		return nil, nil
	}
	return fs.git.LogFile(relativePath, limit)
}

// GitFileAt returns the content of relativePath at the given commit SHA,
// along with the commit timestamp. Returns empty strings when git is disabled.
func (fs *FileStore) GitFileAt(sha, relativePath string) (string, time.Time, error) {
	if fs.git == nil {
		return "", time.Time{}, nil
	}
	return fs.git.FileAt(sha, relativePath)
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
