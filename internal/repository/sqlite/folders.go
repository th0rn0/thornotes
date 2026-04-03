package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

type FolderRepo struct {
	readDB  *sql.DB
	writeDB *sql.DB
}

func NewFolderRepo(readDB, writeDB *sql.DB) *FolderRepo {
	return &FolderRepo{readDB: readDB, writeDB: writeDB}
}

func (r *FolderRepo) Create(ctx context.Context, userID int64, parentID *int64, name, diskPath string) (*model.Folder, error) {
	var id int64
	err := r.writeDB.QueryRowContext(ctx,
		`INSERT INTO folders (user_id, parent_id, name, disk_path) VALUES (?, ?, ?, ?) RETURNING id`,
		userID, parentID, name, diskPath,
	).Scan(&id)
	if err != nil {
		if isUniqueConstraint(err) {
			return nil, apperror.Conflict(fmt.Sprintf("folder %q already exists", name))
		}
		return nil, fmt.Errorf("create folder: %w", err)
	}
	return r.GetByID(ctx, userID, id)
}

func (r *FolderRepo) GetByID(ctx context.Context, userID, folderID int64) (*model.Folder, error) {
	f := &model.Folder{}
	err := r.readDB.QueryRowContext(ctx,
		`SELECT id, user_id, parent_id, name, disk_path, created_at FROM folders
		 WHERE id = ? AND user_id = ?`,
		folderID, userID,
	).Scan(&f.ID, &f.UserID, &f.ParentID, &f.Name, &f.DiskPath, &f.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperror.ErrNotFound
		}
		return nil, fmt.Errorf("get folder: %w", err)
	}
	return f, nil
}

func (r *FolderRepo) GetByDiskPath(ctx context.Context, diskPath string) (*model.Folder, error) {
	f := &model.Folder{}
	err := r.readDB.QueryRowContext(ctx,
		`SELECT id, user_id, parent_id, name, disk_path, created_at FROM folders WHERE disk_path = ?`,
		diskPath,
	).Scan(&f.ID, &f.UserID, &f.ParentID, &f.Name, &f.DiskPath, &f.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperror.ErrNotFound
		}
		return nil, fmt.Errorf("get folder by disk_path: %w", err)
	}
	return f, nil
}

func (r *FolderRepo) Tree(ctx context.Context, userID int64) ([]*model.FolderTreeItem, error) {
	rows, err := r.readDB.QueryContext(ctx, `
		SELECT
			f.id,
			f.parent_id,
			f.name,
			(SELECT COUNT(*) FROM folders c WHERE c.parent_id = f.id) AS child_count,
			(SELECT COUNT(*) FROM notes n WHERE n.folder_id = f.id) AS note_count
		FROM folders f
		WHERE f.user_id = ?
		ORDER BY f.parent_id NULLS FIRST, f.name`, userID)
	if err != nil {
		return nil, fmt.Errorf("tree query: %w", err)
	}
	defer rows.Close()

	var items []*model.FolderTreeItem
	for rows.Next() {
		item := &model.FolderTreeItem{}
		if err := rows.Scan(&item.ID, &item.ParentID, &item.Name, &item.ChildCount, &item.NoteCount); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *FolderRepo) Rename(ctx context.Context, userID, folderID int64, newName, newDiskPath string) error {
	res, err := r.writeDB.ExecContext(ctx,
		`UPDATE folders SET name = ?, disk_path = ? WHERE id = ? AND user_id = ?`,
		newName, newDiskPath, folderID, userID,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return apperror.Conflict(fmt.Sprintf("folder %q already exists", newName))
		}
		return fmt.Errorf("rename folder: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apperror.ErrNotFound
	}
	return nil
}

func (r *FolderRepo) Move(ctx context.Context, userID, folderID int64, newParentID *int64, newDiskPath string) error {
	res, err := r.writeDB.ExecContext(ctx,
		`UPDATE folders SET parent_id = ?, disk_path = ? WHERE id = ? AND user_id = ?`,
		newParentID, newDiskPath, folderID, userID,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return apperror.Conflict("a folder with that name already exists in the destination")
		}
		return fmt.Errorf("move folder: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apperror.ErrNotFound
	}
	return nil
}

func (r *FolderRepo) UpdateDescendantPaths(ctx context.Context, oldPrefix, newPrefix string) error {
	// Update all folders whose disk_path starts with oldPrefix.
	_, err := r.writeDB.ExecContext(ctx,
		`UPDATE folders SET disk_path = ? || SUBSTR(disk_path, LENGTH(?)+1)
		 WHERE disk_path LIKE ? || '%'`,
		newPrefix, oldPrefix, oldPrefix,
	)
	if err != nil {
		return fmt.Errorf("update descendant folder paths: %w", err)
	}

	// Update all notes whose disk_path starts with oldPrefix.
	_, err = r.writeDB.ExecContext(ctx,
		`UPDATE notes SET disk_path = ? || SUBSTR(disk_path, LENGTH(?)+1)
		 WHERE disk_path LIKE ? || '%'`,
		newPrefix, oldPrefix, oldPrefix,
	)
	if err != nil {
		return fmt.Errorf("update descendant note paths: %w", err)
	}

	return nil
}

func (r *FolderRepo) Delete(ctx context.Context, userID, folderID int64) error {
	res, err := r.writeDB.ExecContext(ctx,
		`DELETE FROM folders WHERE id = ? AND user_id = ?`,
		folderID, userID,
	)
	if err != nil {
		return fmt.Errorf("delete folder: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apperror.ErrNotFound
	}
	return nil
}

// isUniqueConstraint checks whether a SQLite error is a UNIQUE constraint violation.
func isUniqueConstraint(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "UNIQUE constraint failed") ||
		strings.Contains(err.Error(), "unique constraint"))
}
