package notes

import (
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path"
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
//
// Containment is structural, not advisory: every operation goes through an
// *os.Root opened on the notes root, which on Linux resolves each path with
// openat2(2) and RESOLVE_BENEATH, and elsewhere walks components with openat(2)
// and O_NOFOLLOW. There is no point at which a checked path is re-resolved by a
// later syscall, so a component swapped for a symlink between a check and its
// use cannot redirect the operation outside the root. That race was real: a
// lstat-based check followed by a fresh path-based syscall let a probe write and
// read outside the root in 11 of 4000 iterations (see the H-01 security review).
//
// On top of containment sits a POLICY: symlinks inside the notes root are
// refused because links are not part of the notes model (reconcile treats them
// as non-regular and skips them). The policy check is a per-component Lstat
// walk; where an attacker wins a race against it, the worst case is that the
// operation is refused or acts on another in-root entry — it can no longer
// escape, because os.Root decides what "inside" means.
//
// Scope of the guarantee, stated exactly: this stops symlinks and path
// traversal. It does not stop a hardlink placed inside the tree by someone with
// local write access and enough permission on the target — that appears as an
// ordinary regular file to every check available to us, and it aliases an inode
// rather than resolving to a new path. "No symlink escape" is the invariant, not
// "a real file under the root".
type FileStore struct {
	notesRoot string
	root      *os.Root       // confined handle; every operation goes through it
	wg        sync.WaitGroup // tracks in-flight writes for graceful shutdown
	git       *gitHistory    // nil when git history is disabled

	// ownerMu guards the advisory cross-process lock on the notes root.
	ownerMu       sync.Mutex
	ownerLock     *os.File
	ownerLockPath string
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
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("open notes root %q: %w", abs, err)
	}
	return &FileStore{notesRoot: abs, root: root}, nil
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

// resolveRel validates a caller-supplied relative path and returns it in the
// form os.Root wants.
//
// Two distinct jobs, and it matters that they are distinct:
//
//   - lexical: reject absolute paths and paths that climb out with "..", so the
//     caller gets ErrPathTraversal rather than a confusing syscall error.
//   - policy: refuse a symlink in any component, walking each prefix with Lstat
//     (which never follows the final link, and each prefix is its own final
//     link here). allowLeafSymlink skips the last component, for the operations
//     where unlinking the link itself is both safe and the only way a user can
//     clean one up.
//
// Containment is NOT this function's job. os.Root enforces that in the syscall
// that does the work, which is the only place it can be enforced without a
// window.
func (fs *FileStore) resolveRel(relativePath string, allowLeafSymlink bool) (string, error) {
	clean := path.Clean(relativePath)
	if clean == "." || clean == "/" {
		return "", apperror.ErrPathTraversal
	}
	if path.IsAbs(clean) {
		return "", apperror.ErrPathTraversal
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", apperror.ErrPathTraversal
	}

	parts := strings.Split(clean, "/")
	limit := len(parts)
	if allowLeafSymlink {
		limit--
	}
	for i := 0; i < limit; i++ {
		prefix := strings.Join(parts[:i+1], "/")
		info, err := fs.root.Lstat(prefix)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// Nothing exists here yet; the rest is ours to create.
				return clean, nil
			}
			return "", fmt.Errorf("stat %q: %w", prefix, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			log.Warn().
				Str("path", clean).
				Str("symlink", prefix).
				Msg("notes: refusing path — symlink inside the notes root is not supported")
			return "", apperror.ErrPathTraversal
		}
	}
	return clean, nil
}

// safePath returns the absolute path for relativePath after the same checks as
// resolveRel. It exists for the read-only directory walk, which needs real paths
// to enumerate; everything that opens, creates, renames or removes goes through
// fs.root instead so it cannot be redirected.
func (fs *FileStore) safePath(relativePath string) (string, error) {
	clean, err := fs.resolveRel(relativePath, false)
	if err != nil {
		return "", err
	}
	return filepath.Join(fs.notesRoot, filepath.FromSlash(clean)), nil
}

// createTempIn creates a uniquely named temp file in dirRel (relative to the
// roots) and returns it with its relative path. Names follow the
// ".thornotes-*.tmp" pattern so the generated .gitignore keeps excluding them.
func (fs *FileStore) createTempIn(dirRel string) (*os.File, string, error) {
	for attempt := 0; attempt < 100; attempt++ {
		name := fmt.Sprintf(".thornotes-%d-%d.tmp", time.Now().UnixNano(), rand.Uint32())
		rel := name
		if dirRel != "" && dirRel != "." {
			rel = dirRel + "/" + name
		}
		f, err := fs.root.OpenFile(rel, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			return f, rel, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("create temp: could not find a free name")
}

// Write atomically writes content to relativePath.
// The WaitGroup is incremented before the write begins and decremented after the
// rename completes, allowing graceful shutdown to drain in-flight writes.
func (fs *FileStore) Write(relativePath, content string) error {
	rel, err := fs.resolveRel(relativePath, false)
	if err != nil {
		return err
	}

	dir := path.Dir(rel)
	if dir != "." {
		if err := fs.root.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("mkdir: %w", err)
		}
	}

	tmp, tmpRel, err := fs.createTempIn(dir)
	if err != nil {
		if isENOSPC(err) {
			return apperror.DiskFull()
		}
		return fmt.Errorf("create temp: %w", err)
	}

	discard := func() {
		tmp.Close()
		_ = fs.root.Remove(tmpRel)
	}
	if _, err := tmp.WriteString(content); err != nil {
		discard()
		if isENOSPC(err) {
			return apperror.DiskFull()
		}
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		discard()
		if isENOSPC(err) {
			return apperror.DiskFull()
		}
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = fs.root.Remove(tmpRel)
		if isENOSPC(err) {
			return apperror.DiskFull()
		}
		return fmt.Errorf("close temp: %w", err)
	}

	fs.wg.Add(1)
	defer fs.wg.Done()

	if err := fs.root.Rename(tmpRel, rel); err != nil {
		_ = fs.root.Remove(tmpRel)
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
	rel, err := fs.resolveRel(relativePath, false)
	if err != nil {
		return err
	}

	dir := path.Dir(rel)
	if dir != "." {
		if err := fs.root.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("mkdir: %w", err)
		}
	}

	fs.wg.Add(1)
	defer fs.wg.Done()

	f, err := fs.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if isENOSPC(err) {
			return apperror.DiskFull()
		}
		return fmt.Errorf("create exclusive: %w", err)
	}

	if _, err := f.WriteString(content); err != nil {
		f.Close()
		_ = fs.root.Remove(rel)
		if isENOSPC(err) {
			return apperror.DiskFull()
		}
		return fmt.Errorf("write exclusive: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = fs.root.Remove(rel)
		if isENOSPC(err) {
			return apperror.DiskFull()
		}
		return fmt.Errorf("sync exclusive: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = fs.root.Remove(rel)
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
	rel, err := fs.resolveRel(relativePath, false)
	if err != nil {
		return "", err
	}
	f, err := fs.root.Open(rel)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			return "", apperror.ErrNotFound
		case errors.Is(err, apperror.ErrPathTraversal), isPathEscape(err):
			log.Warn().Str("path", relativePath).Msg("notes: refusing a path that resolves outside the notes root")
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
//
// Unlike the other operations this ALLOWS a symlink at the leaf: unlink(2)
// removes the link, never the target, so refusing it only meant a user could not
// clean up a stray link from the UI at all — the sole recovery was shell access
// to the volume. Anything that would *follow* the link is still refused.
func (fs *FileStore) Delete(relativePath string) error {
	rel, err := fs.resolveRel(relativePath, true)
	if err != nil {
		return err
	}
	if err := fs.root.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
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
	return fs.rename(oldRelPath, newRelPath, false)
}

// RenameFile moves a single file from oldRelPath to newRelPath.
// Both paths are relative to notesRoot.
func (fs *FileStore) RenameFile(oldRelPath, newRelPath string) error {
	return fs.rename(oldRelPath, newRelPath, true)
}

// rename is the shared body of RenameDir and RenameFile. wrap distinguishes the
// error text the two callers have always produced.
func (fs *FileStore) rename(oldRelPath, newRelPath string, wrap bool) error {
	oldRel, err := fs.resolveRel(oldRelPath, false)
	if err != nil {
		return err
	}
	newRel, err := fs.resolveRel(newRelPath, false)
	if err != nil {
		return err
	}
	if dir := path.Dir(newRel); dir != "." {
		if err := fs.root.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("mkdir parent: %w", err)
		}
	}
	if err := fs.root.Rename(oldRel, newRel); err != nil {
		if wrap {
			return fmt.Errorf("rename file: %w", err)
		}
		return err
	}
	if fs.git != nil {
		if err := fs.git.CommitRename(oldRelPath, newRelPath); err != nil {
			log.Warn().Err(err).Str("old", oldRelPath).Str("new", newRelPath).Msg("git commit rename")
		}
	}
	return nil
}

// RenameFileExclusive moves a single file from oldRelPath to newRelPath without
// replacing an existing destination file. Link(2) fails atomically with
// os.ErrExist in the kernel if the destination already exists, followed by an
// unlink of the source.
func (fs *FileStore) RenameFileExclusive(oldRelPath, newRelPath string) error {
	oldRel, err := fs.resolveRel(oldRelPath, false)
	if err != nil {
		return err
	}
	newRel, err := fs.resolveRel(newRelPath, false)
	if err != nil {
		return err
	}
	if dir := path.Dir(newRel); dir != "." {
		if err := fs.root.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("mkdir parent: %w", err)
		}
	}
	if err := fs.root.Link(oldRel, newRel); err != nil {
		return fmt.Errorf("link file exclusive: %w", err)
	}
	if err := fs.root.Remove(oldRel); err != nil {
		_ = fs.root.Remove(newRel)
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
	rel, err := fs.resolveRel(relativePath, false)
	if err != nil {
		return err
	}
	return fs.root.MkdirAll(rel, 0700)
}

// CreateDirExclusive creates the leaf directory at relativePath exclusively
// (parent directories are created if needed). Returns os.ErrExist if the leaf
// directory already exists on disk.
func (fs *FileStore) CreateDirExclusive(relativePath string) error {
	rel, err := fs.resolveRel(relativePath, false)
	if err != nil {
		return err
	}
	if dir := path.Dir(rel); dir != "." {
		if err := fs.root.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("mkdir parent: %w", err)
		}
	}
	return fs.root.Mkdir(rel, 0700)
}

// RemoveEmptyDir removes relativePath only if it is an empty directory.
func (fs *FileStore) RemoveEmptyDir(relativePath string) error {
	rel, err := fs.resolveRel(relativePath, false)
	if err != nil {
		return err
	}
	if err := fs.root.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove empty dir: %w", err)
	}
	return nil
}

// trashDirName is where a file or directory is parked while its row is being
// removed. It lives at the notes root so the move is a rename within one
// filesystem; user paths are always <uuid>/<name>, so this can never collide
// with one of them, and the reconciler never walks the root itself.
const trashDirName = ".thornotes-trash"

// MoveToTrash atomically renames relativePath into the notes-root trash and
// returns an opaque token for RestoreFromTrash / PurgeTrash.
//
// This is the commit point of a delete: it either succeeds (the path is now
// gone from the live tree) or fails with the original file untouched. A missing
// source returns ("", nil) — nothing exists to resurrect, so the caller may
// proceed to remove the row. A symlink leaf is allowed for the same reason as
// Delete: moving the link is safe and is the user's only way to get rid of one.
func (fs *FileStore) MoveToTrash(relativePath string) (string, error) {
	rel, err := fs.resolveRel(relativePath, true)
	if err != nil {
		return "", err
	}
	if _, statErr := fs.root.Lstat(rel); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("trash stat: %w", statErr)
	}

	if err := fs.root.MkdirAll(trashDirName, 0700); err != nil {
		return "", fmt.Errorf("trash mkdir: %w", err)
	}
	// One unique slot per deletion, so concurrent deletes of same-named leaves
	// cannot collide and each purge is independent.
	var slot string
	for attempt := 0; attempt < 100; attempt++ {
		candidate := fmt.Sprintf("%s/del-%d-%d", trashDirName, time.Now().UnixNano(), rand.Uint32())
		if err := fs.root.Mkdir(candidate, 0700); err == nil {
			slot = candidate
			break
		} else if !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("trash slot: %w", err)
		}
	}
	if slot == "" {
		return "", errors.New("trash slot: could not find a free name")
	}

	base := path.Base(rel)
	if base == "." || base == "/" || base == "" {
		base = "entry"
	}
	if err := fs.root.Rename(rel, slot+"/"+base); err != nil {
		_ = fs.root.RemoveAll(slot)
		return "", fmt.Errorf("move to trash: %w", err)
	}
	return slot, nil
}

// RestoreFromTrash puts a trashed entry back at relativePath. Used to undo a
// delete whose database half failed.
func (fs *FileStore) RestoreFromTrash(token, relativePath string) error {
	if token == "" {
		return nil
	}
	slot, err := fs.resolveRel(token, false)
	if err != nil {
		return err
	}
	dst, err := fs.resolveRel(relativePath, false)
	if err != nil {
		return err
	}
	if dir := path.Dir(dst); dir != "." {
		if err := fs.root.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("restore mkdir: %w", err)
		}
	}
	return fs.root.Rename(slot+"/"+path.Base(dst), dst)
}

// PurgeTrash removes a trashed entry for good. Failure here is not data loss:
// the entry is out of the live tree and out of the index, so it can be cleaned
// up later.
func (fs *FileStore) PurgeTrash(token string) error {
	if token == "" {
		return nil
	}
	rel, err := fs.resolveRel(token, false)
	if err != nil {
		return err
	}
	if err := fs.root.RemoveAll(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
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
	rel, err := fs.resolveRel(relativePath, false)
	if err != nil {
		return err
	}
	if err := fs.root.RemoveAll(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
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

// Close releases the confined root handle.
func (fs *FileStore) Close() error {
	fs.ReleaseOwnerLock()
	if fs.root == nil {
		return nil
	}
	return fs.root.Close()
}

// isENOSPC reports whether err is (or wraps) a "no space left on device" error.
func isENOSPC(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == syscall.ENOSPC
}

// isPathEscape reports whether err is os.Root refusing a path that resolves
// outside the root.
func isPathEscape(err error) bool {
	// os.Root reports escapes as "path escapes from parent"; match on the
	// sentinel the implementation documents and on the text as a fallback so a
	// wrapped error cannot slip through.
	var pe *os.PathError
	if errors.As(err, &pe) && strings.Contains(pe.Err.Error(), "escapes from parent") {
		return true
	}
	return strings.Contains(err.Error(), "escapes from parent")
}
