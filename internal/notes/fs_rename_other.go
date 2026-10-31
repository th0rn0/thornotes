//go:build !linux

package notes

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"
)

// RenameDirExclusive moves a directory from oldRelPath to newRelPath without
// replacing an existing destination directory. There is no portable atomic
// no-replace rename primitive, so on non-Linux platforms this uses an existence
// pre-check followed by os.Rename; the DB unique constraint remains the backstop
// for the small TOCTOU window this leaves.
func (fs *FileStore) RenameDirExclusive(oldRelPath, newRelPath string) error {
	oldAbs, err := fs.safePath(oldRelPath)
	if err != nil {
		return err
	}
	newAbs, err := fs.safePath(newRelPath)
	if err != nil {
		return err
	}
	// A no-op rename is a success, not a conflict.
	if oldAbs == newAbs {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(newAbs), 0700); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}

	if _, err := os.Lstat(newAbs); err == nil {
		return os.ErrExist
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("lstat destination: %w", err)
	}
	if err := os.Rename(oldAbs, newAbs); err != nil {
		return fmt.Errorf("rename dir: %w", err)
	}

	if fs.git != nil {
		if err := fs.git.CommitRename(oldRelPath, newRelPath); err != nil {
			log.Warn().Err(err).Str("old", oldRelPath).Str("new", newRelPath).Msg("git commit rename")
		}
	}
	return nil
}
