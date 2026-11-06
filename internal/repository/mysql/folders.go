package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/repository"
)

type FolderRepo struct {
	db *sql.DB
}

func NewFolderRepo(db *sql.DB) *FolderRepo {
	return &FolderRepo{db: db}
}

func (r *FolderRepo) Create(ctx context.Context, userID int64, parentID *int64, name, diskPath string) (*model.Folder, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO folders (user_id, parent_id, name, disk_path) VALUES (?, ?, ?, ?)`,
		userID, parentID, name, diskPath,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return nil, apperror.Conflict(fmt.Sprintf("folder %q already exists", name))
		}
		return nil, fmt.Errorf("create folder: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("create folder last insert id: %w", err)
	}
	return r.GetByID(ctx, userID, id)
}

func (r *FolderRepo) GetByID(ctx context.Context, userID, folderID int64) (*model.Folder, error) {
	f := &model.Folder{}
	err := r.db.QueryRowContext(ctx,
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
	err := r.db.QueryRowContext(ctx,
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
	// MySQL does not support NULLS FIRST; use ISNULL(parent_id) DESC to put roots first.
	rows, err := r.db.QueryContext(ctx, `
		SELECT
			f.id,
			f.parent_id,
			f.name,
			f.disk_path,
			(SELECT COUNT(*) FROM folders c WHERE c.parent_id = f.id) AS child_count,
			(SELECT COUNT(*) FROM notes n WHERE n.folder_id = f.id) AS note_count
		FROM folders f
		WHERE f.user_id = ?
		ORDER BY ISNULL(f.parent_id) DESC, f.name`, userID)
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
	// Renaming a folder to the name it already has (or re-asserting its path)
	// changes no column, so MySQL reports 0 rows affected — the same count a
	// folder that is not the caller's produces. A matched row is success; a row
	// that is not the caller's, or does not exist, is not-found. Both answer 404,
	// so a foreign folder id leaks nothing (ticket 599).
	matched, err := updateScoped(ctx, r.db,
		`UPDATE folders SET name = ?, disk_path = ? WHERE id = ? AND user_id = ?`,
		[]any{newName, newDiskPath, folderID, userID},
		`SELECT EXISTS(SELECT 1 FROM folders WHERE id = ? AND user_id = ?)`,
		[]any{folderID, userID},
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return apperror.Conflict(fmt.Sprintf("folder %q already exists", newName))
		}
		return fmt.Errorf("rename folder: %w", err)
	}
	if !matched {
		return apperror.ErrNotFound
	}
	return nil
}

func (r *FolderRepo) Move(ctx context.Context, userID, folderID int64, newParentID *int64, newDiskPath string) error {
	res, err := r.db.ExecContext(ctx,
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
	// path underneath it by a separator. The unscoped `LIKE CONCAT(?, '%')`
	// this replaces also matched siblings whose name merely shared a prefix, and
	// treated '%'/'_' in a folder name as wildcards (ticket 598). The boundary
	// and the escaping live in repository.DescendantPathPredicate.
	predicate, scopeArgs := repository.DescendantPathPredicate(oldPrefix)
	args := append([]any{newPrefix, oldPrefix}, scopeArgs...)

	// CHAR_LENGTH, not LENGTH: MariaDB/MySQL LENGTH() counts BYTES while
	// SUBSTRING() counts CHARACTERS, so for an old prefix containing a
	// multi-byte character LENGTH() overshoots by exactly (bytes - chars) and
	// the tail is sliced past the separator — "uuid/Renamed/note.md" becomes
	// "uuid/Renamednote.md". That path points at nothing, and the next reconcile
	// reads it as a deleted note, deletes and reimports it, which drops its share
	// token. SQLite's LENGTH() counts characters, so the SQLite suite cannot see
	// this: keep these two expressions in step and do not "simplify" them back.
	_, err := r.db.ExecContext(ctx,
		`UPDATE folders
		 SET disk_path = CONCAT(?, SUBSTRING(disk_path, CHAR_LENGTH(?)+1))
		 WHERE `+predicate,
		args...,
	)
	if err != nil {
		return fmt.Errorf("update descendant folder paths: %w", err)
	}

	_, err = r.db.ExecContext(ctx,
		`UPDATE notes
		 SET disk_path = CONCAT(?, SUBSTRING(disk_path, CHAR_LENGTH(?)+1))
		 WHERE `+predicate,
		args...,
	)
	if err != nil {
		return fmt.Errorf("update descendant note paths: %w", err)
	}
	return nil
}

func (r *FolderRepo) Delete(ctx context.Context, userID, folderID int64) error {
	res, err := r.db.ExecContext(ctx,
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
