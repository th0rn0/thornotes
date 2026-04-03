package notes

import (
	"context"
	"path/filepath"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

// CreateFolder creates a folder on disk and in the DB.
func (s *Service) CreateFolder(ctx context.Context, userID int64, parentID *int64, name string) (*model.Folder, error) {
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

	diskPath := folderDiskPath(userID, parentPath, name)

	if err := s.fs.EnsureDir(diskPath); err != nil {
		return nil, apperror.Internal("create folder on disk", err)
	}

	folder, err := s.folders.Create(ctx, userID, parentID, name, diskPath)
	if err != nil {
		_ = s.fs.RemoveDir(diskPath)
		return nil, err
	}

	return folder, nil
}

// RenameFolder renames a folder. It:
// 1. Renames the OS directory.
// 2. Updates the folder's own disk_path in DB.
// 3. Updates all descendant folder and note disk_paths in DB.
func (s *Service) RenameFolder(ctx context.Context, userID, folderID int64, newName string) error {
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
	newDiskPath := folderDiskPath(userID, parentPath, newName)

	// Rename on disk first.
	if err := s.fs.RenameDir(oldDiskPath, newDiskPath); err != nil {
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
func (s *Service) MoveFolder(ctx context.Context, userID, folderID int64, newParentID *int64) error {
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
	newDiskPath := folderDiskPath(userID, newParentPath, folder.Name)

	if err := s.fs.RenameDir(oldDiskPath, newDiskPath); err != nil {
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

// DeleteFolder removes a folder and all its contents (cascade in DB via ON DELETE CASCADE).
func (s *Service) DeleteFolder(ctx context.Context, userID, folderID int64) error {
	folder, err := s.folders.GetByID(ctx, userID, folderID)
	if err != nil {
		return err
	}
	diskPath := folder.DiskPath

	// DB deletion cascades to child folders and notes.
	if err := s.folders.Delete(ctx, userID, folderID); err != nil {
		return err
	}

	// Remove directory from disk after DB is consistent.
	_ = s.fs.RemoveDir(diskPath)
	return nil
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
