package notes

import (
	"context"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

// CreateFolder creates a folder on disk and in the DB.
func (s *Service) CreateFolder(ctx context.Context, userID int64, parentID *int64, name string) (*model.Folder, error) {
	if len(name) == 0 || len(name) > 255 {
		return nil, apperror.BadRequest("folder name must be 1–255 characters")
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
