package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/repository"
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
			f.disk_path,
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
		if err := rows.Scan(&item.ID, &item.ParentID, &item.Name, &item.DiskPath, &item.ChildCount, &item.NoteCount); err != nil {
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
	// Scope the cascade to the real subtree: the folder's own disk_path, or a
	// path underneath it by a separator. The unscoped `LIKE oldPrefix || '%'`
	// this replaces also matched siblings whose name merely shared a prefix, and
	// treated '%'/'_' in a folder name as wildcards (ticket 598). The boundary
	// and the escaping live in repository.DescendantPathPredicate.
	predicate, scopeArgs := repository.DescendantPathPredicate(oldPrefix)
	args := append([]any{newPrefix, oldPrefix}, scopeArgs...)

	// Update all folders in the subtree.
	_, err := r.writeDB.ExecContext(ctx,
		`UPDATE folders SET disk_path = ? || SUBSTR(disk_path, LENGTH(?)+1)
		 WHERE `+predicate,
		args...,
	)
	if err != nil {
		return fmt.Errorf("update descendant folder paths: %w", err)
	}

	// Update all notes in the subtree.
	_, err = r.writeDB.ExecContext(ctx,
		`UPDATE notes SET disk_path = ? || SUBSTR(disk_path, LENGTH(?)+1)
		 WHERE `+predicate,
		args...,
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

// ListSubtree returns diskPath's folder row plus every folder underneath it.
// The subtree is expressed by repository.DescendantPathPredicate — the same
// predicate the rename cascade uses — so this cannot drift from it: the match
// stops at a path separator (a sibling called "Notes2" is not under "Notes")
// and LIKE metacharacters in a folder name are escaped. Scoped to userID as
// well, because disk_path is unique across users but a caller must never see
// another account's rows even if a prefix somehow overlapped.
func (r *FolderRepo) ListSubtree(ctx context.Context, userID int64, diskPath string) ([]*model.Folder, error) {
	predicate, scopeArgs := repository.DescendantPathPredicate(diskPath)
	args := append([]any{userID}, scopeArgs...)

	rows, err := r.readDB.QueryContext(ctx,
		`SELECT id, user_id, parent_id, name, disk_path, created_at FROM folders
		 WHERE user_id = ? AND (`+predicate+`)
		 ORDER BY disk_path`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("list folder subtree: %w", err)
	}
	defer rows.Close()

	var folders []*model.Folder
	for rows.Next() {
		f := &model.Folder{}
		if err := rows.Scan(&f.ID, &f.UserID, &f.ParentID, &f.Name, &f.DiskPath, &f.CreatedAt); err != nil {
			return nil, err
		}
		folders = append(folders, f)
	}
	return folders, rows.Err()
}

// isUniqueConstraint checks whether a SQLite error is a UNIQUE constraint violation.
func isUniqueConstraint(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "UNIQUE constraint failed") ||
		strings.Contains(err.Error(), "unique constraint"))
}
