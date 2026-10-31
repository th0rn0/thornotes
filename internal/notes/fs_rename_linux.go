//go:build linux

package notes

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"
	"golang.org/x/sys/unix"
)

// RenameDirExclusive moves a directory from oldRelPath to newRelPath without
// replacing an existing destination directory. On Linux it uses
// renameat2(RENAME_NOREPLACE) for an atomic no-replace rename, falling back to
// an existence pre-check plus os.Rename on kernels/filesystems that do not
// support the flag (the DB unique constraint remains the backstop there).
func (fs *FileStore) RenameDirExclusive(oldRelPath, newRelPath string) error {
	oldAbs, err := fs.safePath(oldRelPath)
	if err != nil {
		return err
	}
	newAbs, err := fs.safePath(newRelPath)
	if err != nil {
		return err
	}
	// A no-op rename must not hit the no-replace guard (renameat2(old==new, ...)
	// returns EEXIST, which callers would surface as a spurious conflict).
	if oldAbs == newAbs {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(newAbs), 0700); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}

	if err := unix.Renameat2(unix.AT_FDCWD, oldAbs, unix.AT_FDCWD, newAbs, unix.RENAME_NOREPLACE); err != nil {
		switch err {
		case unix.EEXIST:
			return os.ErrExist
		case unix.ENOSYS, unix.EINVAL, unix.ENOTSUP:
			// Kernel/filesystem without no-replace support: pre-check then rename.
			if _, statErr := os.Lstat(newAbs); statErr == nil {
				return os.ErrExist
			} else if !os.IsNotExist(statErr) {
				return fmt.Errorf("lstat destination: %w", statErr)
			}
			if err := os.Rename(oldAbs, newAbs); err != nil {
				return fmt.Errorf("rename dir: %w", err)
			}
		default:
			return fmt.Errorf("rename dir exclusive: %w", err)
		}
	}

	if fs.git != nil {
		if err := fs.git.CommitRename(oldRelPath, newRelPath); err != nil {
			log.Warn().Err(err).Str("old", oldRelPath).Str("new", newRelPath).Msg("git commit rename")
		}
	}
	return nil
}
