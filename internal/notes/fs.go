package notes

import (
	"errors"
	"fmt"
	"io"
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
//
// O_NOFOLLOW closes the window between the boundary check and the open: the
// final component is opened as a leaf or not at all, so a symlink swapped in
// after lstat cannot be followed.
func (fs *FileStore) Read(relativePath string) (string, error) {
	absPath, err := fs.safePath(relativePath)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(absPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		switch {
		case os.IsNotExist(err):
			return "", apperror.ErrNotFound
		case errors.Is(err, syscall.ELOOP):
			log.Warn().Str("path", relativePath).Msg("notes: refusing to follow a symlink on read")
			return "", apperror.ErrPathTraversal
		}
		return "", fmt.Errorf("read file: %w", err)
	}
	defer f.Close()

	b, err := io.ReadAll(f)
	if err != nil {
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

// trashDirName is where a file or directory is parked while its row is being
// removed. It lives inside the notes root so the move is a rename within one
// filesystem, and it is dot-prefixed and outside any user subtree so neither the
// reconciler's walk nor the user's tree can ever see it.
const trashDirName = ".thornotes-trash"

// MoveToTrash atomically renames relativePath into the notes-root trash and
// returns an opaque token for RestoreFromTrash / PurgeTrash.
//
// This is the commit point of a delete: it either succeeds (the path is now
// gone from the live tree) or fails with the original file untouched. A missing
// source returns ("", nil) — nothing exists to resurrect, so the caller may
// proceed to remove the row.
func (fs *FileStore) MoveToTrash(relativePath string) (string, error) {
	src, err := fs.safePath(relativePath)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Lstat(src); statErr != nil {
		if os.IsNotExist(statErr) {
			return "", nil
		}
		return "", fmt.Errorf("trash stat: %w", statErr)
	}

	trashRoot := filepath.Join(fs.notesRoot, trashDirName)
	if err := os.MkdirAll(trashRoot, 0700); err != nil {
		return "", fmt.Errorf("trash mkdir: %w", err)
	}
	// One unique slot per deletion, so concurrent deletes of same-named leaves
	// cannot collide and each purge is independent.
	slot, err := os.MkdirTemp(trashRoot, "del-*")
	if err != nil {
		return "", fmt.Errorf("trash slot: %w", err)
	}

	base := filepath.Base(src)
	if base == "." || base == string(filepath.Separator) || base == "" {
		base = "entry"
	}
	dst := filepath.Join(slot, base)
	if err := os.Rename(src, dst); err != nil {
		_ = os.RemoveAll(slot)
		return "", fmt.Errorf("move to trash: %w", err)
	}

	token, err := filepath.Rel(fs.notesRoot, slot)
	if err != nil {
		return "", apperror.ErrPathTraversal
	}
	return token, nil
}

// RestoreFromTrash puts a trashed entry back at relativePath. Used to undo a
// delete whose database half failed.
func (fs *FileStore) RestoreFromTrash(token, relativePath string) error {
	if token == "" {
		return nil
	}
	slotAbs, err := fs.safePath(token)
	if err != nil {
		return err
	}
	dst, err := fs.safePath(relativePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return fmt.Errorf("restore mkdir: %w", err)
	}
	return os.Rename(filepath.Join(slotAbs, filepath.Base(dst)), dst)
}

// PurgeTrash removes a trashed entry for good. Failure here is not data loss:
// the entry is out of the live tree and out of the index, so it can be cleaned
// up later.
func (fs *FileStore) PurgeTrash(token string) error {
	if token == "" {
		return nil
	}
	abs, err := fs.safePath(token)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(abs); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("purge trash: %w", err)
	}
	return nil
}

// RecordDelete records a completed delete in the git history, if it is enabled.
// The delete itself goes through MoveToTrash, so this is called once the trash
// entry has been purged and the working tree shows only the removal.
func (fs *FileStore) RecordDelete(relativePath string) {
	if fs.git == nil {
		return
	}
	if err := fs.git.CommitDelete(relativePath); err != nil {
		log.Warn().Err(err).Str("path", relativePath).Msg("git commit delete")
	}
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
//
// Two checks, because they catch different attacks:
//
//   - a lexical check, which rejects ".." and absolute paths outright;
//   - a physical check, which walks every existing component from the root with
//     lstat(2) and refuses a symlink anywhere in the path. The lexical check
//     alone accepted "user1/escape/secret.md" when "escape" was a symlink to
//     /etc: reads then served files from outside the root and writes created
//     them there.
//
// os.Lstat never follows the final link, so an existing symlink at any level is
// visible as a symlink. A component that does not exist yet cannot be one, and
// everything below it is created by us after the parent has been verified.
//
// Symlinks that point *inside* the root are refused too, deliberately: links are
// not part of the notes model (reconcile treats them as non-regular and skips
// them), and refusing them keeps the invariant — every path we touch is a real
// file under the root — simple enough to reason about.
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

	if err := fs.assertNoSymlinkInPath(abs); err != nil {
		return "", err
	}

	return abs, nil
}

// assertNoSymlinkInPath refuses a path that has a symlink in any component
// between the notes root and the target.
func (fs *FileStore) assertNoSymlinkInPath(abs string) error {
	rel, err := filepath.Rel(fs.notesRoot, abs)
	if err != nil {
		return apperror.ErrPathTraversal
	}
	if rel == "." {
		return nil
	}
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return apperror.ErrPathTraversal
	}

	cur := fs.notesRoot
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		info, statErr := os.Lstat(cur)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				// Nothing exists here yet; everything below is ours to create.
				return nil
			}
			return fmt.Errorf("stat %q: %w", cur, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			log.Warn().
				Str("path", rel).
				Str("symlink", cur).
				Msg("notes: refusing path — symlink inside the notes root would escape it")
			return apperror.ErrPathTraversal
		}
	}
	return nil
}
