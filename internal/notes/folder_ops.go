package notes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

// CreateFolder creates a folder on disk and in the DB.
func (s *Service) CreateFolder(ctx context.Context, userID int64, userUUID string, parentID *int64, name string) (*model.Folder, error) {
	defer s.lockTreeExclusive(userID)()
	if len(name) == 0 || len(name) > 255 {
		return nil, apperror.BadRequest("folder name must be 1–255 characters")
	}
	if filepath.Base(name) != name || name == ".." || name == "." {
		return nil, apperror.BadRequest("folder name must not contain path separators")
	}

	var parentPath string
	if parentID != nil {
		parent, err := s.folders.GetByID(ctx, userID, *parentID)
		if err != nil {
			return nil, err
		}
		parentPath = parent.DiskPath
	}

	diskPath := folderDiskPath(userUUID, parentPath, name)

	if _, err := s.folders.GetByDiskPath(ctx, diskPath); err == nil {
		return nil, apperror.Conflict(fmt.Sprintf("folder %q already exists", name))
	} else if !errors.Is(err, apperror.ErrNotFound) {
		return nil, err
	}

	createdOnDisk := false
	if err := s.fs.CreateDirExclusive(diskPath); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, apperror.Internal("create folder on disk", err)
		}
	} else {
		createdOnDisk = true
	}

	folder, err := s.folders.Create(ctx, userID, parentID, name, diskPath)
	if err != nil {
		// Remove the directory only if we created it AND no DB row now claims
		// diskPath (e.g. a concurrent caller won folders.Create after we won
		// CreateDirExclusive).
		if createdOnDisk && !apperror.IsConflict(err) {
			if _, lookupErr := s.folders.GetByDiskPath(ctx, diskPath); errors.Is(lookupErr, apperror.ErrNotFound) {
				_ = s.fs.RemoveEmptyDir(diskPath)
			}
		}
		return nil, err
	}

	return folder, nil
}

// RenameFolder renames a folder. It:
// 1. Renames the OS directory.
// 2. Updates the folder's own disk_path in DB.
// 3. Updates all descendant folder and note disk_paths in DB.
func (s *Service) RenameFolder(ctx context.Context, userID int64, userUUID string, folderID int64, newName string) error {
	defer s.lockTreeExclusive(userID)()
	if len(newName) == 0 || len(newName) > 255 {
		return apperror.BadRequest("folder name must be 1–255 characters")
	}
	if filepath.Base(newName) != newName || newName == ".." || newName == "." {
		return apperror.BadRequest("folder name must not contain path separators")
	}

	folder, err := s.folders.GetByID(ctx, userID, folderID)
	if err != nil {
		return err
	}

	// Compute new disk path by replacing only the last segment.
	var parentPath string
	if folder.ParentID != nil {
		parent, err := s.folders.GetByID(ctx, userID, *folder.ParentID)
		if err != nil {
			return err
		}
		parentPath = parent.DiskPath
	}

	oldDiskPath := folder.DiskPath
	newDiskPath := folderDiskPath(userUUID, parentPath, newName)

	// Reject a destination collision before touching the filesystem so an
	// existing folder is never replaced (os.Rename would silently replace an
	// empty destination directory on Linux).
	if existing, err := s.folders.GetByDiskPath(ctx, newDiskPath); err == nil && existing.ID != folder.ID {
		return apperror.Conflict(fmt.Sprintf("folder %q already exists", newName))
	} else if err != nil && !errors.Is(err, apperror.ErrNotFound) {
		return err
	}

	// Rename on disk first, without replacing an existing destination.
	if err := s.fs.RenameDirExclusive(oldDiskPath, newDiskPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return apperror.Conflict(fmt.Sprintf("folder %q already exists", newName))
		}
		return apperror.Internal("rename folder on disk", err)
	}

	// Update folder's own record.
	if err := s.folders.Rename(ctx, userID, folderID, newName, newDiskPath); err != nil {
		// Attempt to roll back the OS rename.
		_ = s.fs.RenameDir(newDiskPath, oldDiskPath)
		return err
	}

	// Cascade disk_path updates to all descendants.
	if err := s.folders.UpdateDescendantPaths(ctx, oldDiskPath, newDiskPath); err != nil {
		// At this point the OS dir is renamed but DB is partially updated.
		// Log and return — startup reconciliation will detect and fix.
		return apperror.Internal("cascade folder rename", err)
	}

	return nil
}

// MoveFolder reparents a folder to newParentID (nil = root). It:
// 1. Validates the move (no circular references).
// 2. Renames the OS directory.
// 3. Updates the folder's parent_id and disk_path in DB.
// 4. Cascades disk_path updates to all descendants.
func (s *Service) MoveFolder(ctx context.Context, userID int64, userUUID string, folderID int64, newParentID *int64) error {
	defer s.lockTreeExclusive(userID)()
	folder, err := s.folders.GetByID(ctx, userID, folderID)
	if err != nil {
		return err
	}

	// No-op if already in the target location.
	if ptrEq(folder.ParentID, newParentID) {
		return nil
	}

	// Validate the target parent.
	var newParentPath string
	if newParentID != nil {
		// Prevent moving into itself.
		if *newParentID == folderID {
			return apperror.BadRequest("cannot move a folder into itself")
		}
		// Prevent moving into a descendant.
		desc, err := s.isFolderDescendant(ctx, userID, folderID, *newParentID)
		if err != nil {
			return err
		}
		if desc {
			return apperror.BadRequest("cannot move a folder into one of its own descendants")
		}
		parent, err := s.folders.GetByID(ctx, userID, *newParentID)
		if err != nil {
			return err
		}
		newParentPath = parent.DiskPath
	}

	oldDiskPath := folder.DiskPath
	newDiskPath := folderDiskPath(userUUID, newParentPath, folder.Name)

	// Reject a destination collision before touching the filesystem so an
	// existing folder is never replaced.
	if existing, err := s.folders.GetByDiskPath(ctx, newDiskPath); err == nil && existing.ID != folder.ID {
		return apperror.Conflict(fmt.Sprintf("a folder named %q already exists at the destination", folder.Name))
	} else if err != nil && !errors.Is(err, apperror.ErrNotFound) {
		return err
	}

	if err := s.fs.RenameDirExclusive(oldDiskPath, newDiskPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return apperror.Conflict(fmt.Sprintf("a folder named %q already exists at the destination", folder.Name))
		}
		return apperror.Internal("move folder on disk", err)
	}

	if err := s.folders.Move(ctx, userID, folderID, newParentID, newDiskPath); err != nil {
		_ = s.fs.RenameDir(newDiskPath, oldDiskPath)
		return err
	}

	if err := s.folders.UpdateDescendantPaths(ctx, oldDiskPath, newDiskPath); err != nil {
		return apperror.Internal("cascade folder move", err)
	}

	return nil
}

// isFolderDescendant reports whether candidateID is a descendant of ancestorID
// by walking up the parent chain from candidateID.
func (s *Service) isFolderDescendant(ctx context.Context, userID, ancestorID, candidateID int64) (bool, error) {
	visited := map[int64]bool{candidateID: true}
	current := candidateID
	for {
		f, err := s.folders.GetByID(ctx, userID, current)
		if err != nil {
			return false, err
		}
		if f.ParentID == nil {
			return false, nil
		}
		if *f.ParentID == ancestorID {
			return true, nil
		}
		if visited[*f.ParentID] {
			// Cycle guard (shouldn't happen with valid data).
			return false, nil
		}
		visited[*f.ParentID] = true
		current = *f.ParentID
	}
}

// DeleteFolder removes a folder and every note it (or any descendant folder) contains.
// notes.folder_id is ON DELETE SET NULL in the schema, so DB cascade alone would orphan
// child notes to the root — we explicitly delete them first.
func (s *Service) DeleteFolder(ctx context.Context, userID, folderID int64) error {
	defer s.lockTreeExclusive(userID)()
	folder, err := s.folders.GetByID(ctx, userID, folderID)
	if err != nil {
		return err
	}
	diskPath := folder.DiskPath

	descendantIDs, err := s.collectDescendantFolderIDs(ctx, userID, folderID)
	if err != nil {
		return err
	}

	// Disk first, and in one atomic rename: the whole subtree leaves the live
	// tree before a single row is removed, so no file can outlive its row and
	// come back through a reconcile as an untagged stranger.
	token, err := s.fs.MoveToTrash(diskPath)
	if err != nil {
		return err
	}

	if err := s.deleteFolderRows(ctx, userID, folderID, descendantIDs); err != nil {
		if token != "" {
			if restoreErr := s.fs.RestoreFromTrash(token, diskPath); restoreErr != nil {
				log.Error().Err(restoreErr).
					Str("disk_path", diskPath).Str("trash", token).
					Msg("notes: could not restore the folder after a failed delete — it is still in the trash")
			}
		}
		return err
	}

	if token != "" {
		if purgeErr := s.fs.PurgeTrash(token); purgeErr != nil {
			log.Warn().Err(purgeErr).Str("trash", token).
				Msg("notes: could not purge the trash entry (harmless, cleaned up later)")
		}
		s.fs.RecordDelete(diskPath)
	}
	return nil
}

// deleteFolderRows removes the note rows inside a folder subtree and then the
// folder rows themselves. Split out so DeleteFolder can restore the directory
// when the database half fails.
func (s *Service) deleteFolderRows(ctx context.Context, userID, folderID int64, descendantIDs []int64) error {
	for _, fid := range descendantIDs {
		fid := fid
		childNotes, err := s.notes.ListByFolder(ctx, userID, &fid)
		if err != nil {
			return err
		}
		for _, n := range childNotes {
			if err := s.notes.Delete(ctx, userID, n.ID); err != nil {
				return err
			}
		}
	}

	// DB deletion cascades to child folder rows.
	return s.folders.Delete(ctx, userID, folderID)
}

// collectDescendantFolderIDs returns rootID followed by every descendant folder ID,
// using a single Tree() query and an in-memory BFS.
func (s *Service) collectDescendantFolderIDs(ctx context.Context, userID, rootID int64) ([]int64, error) {
	tree, err := s.folders.Tree(ctx, userID)
	if err != nil {
		return nil, err
	}
	childrenByParent := map[int64][]int64{}
	for _, f := range tree {
		if f.ParentID != nil {
			childrenByParent[*f.ParentID] = append(childrenByParent[*f.ParentID], f.ID)
		}
	}
	out := []int64{rootID}
	queue := []int64{rootID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, child := range childrenByParent[current] {
			out = append(out, child)
			queue = append(queue, child)
		}
	}
	return out, nil
}

// FindFoldersByName returns folders whose name contains query (case-insensitive).
// An empty query returns all folders.
func (s *Service) FindFoldersByName(ctx context.Context, userID int64, query string) ([]*model.FolderTreeItem, error) {
	all, err := s.folders.Tree(ctx, userID)
	if err != nil {
		return nil, err
	}
	if query == "" {
		return all, nil
	}
	lower := toLower(query)
	var out []*model.FolderTreeItem
	for _, f := range all {
		if containsIgnoreCase(f.Name, lower) {
			out = append(out, f)
		}
	}
	return out, nil
}

// toLower returns s lowercased (ASCII only, avoids importing strings).
func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func containsIgnoreCase(name, lowerQuery string) bool {
	lowerName := toLower(name)
	if len(lowerQuery) > len(lowerName) {
		return false
	}
	for i := 0; i <= len(lowerName)-len(lowerQuery); i++ {
		if lowerName[i:i+len(lowerQuery)] == lowerQuery {
			return true
		}
	}
	return false
}
