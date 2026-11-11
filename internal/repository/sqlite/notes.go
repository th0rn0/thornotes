package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/repository"
)

type NoteRepo struct {
	readDB  *sql.DB
	writeDB *sql.DB
}

func NewNoteRepo(readDB, writeDB *sql.DB) *NoteRepo {
	return &NoteRepo{readDB: readDB, writeDB: writeDB}
}

func (r *NoteRepo) Create(ctx context.Context, n *model.Note) (*model.Note, error) {
	tagsJSON, err := json.Marshal(n.Tags)
	if err != nil {
		return nil, fmt.Errorf("marshal tags: %w", err)
	}

	var id int64
	err = r.writeDB.QueryRowContext(ctx, `
		INSERT INTO notes (user_id, folder_id, title, slug, title_key, disk_path, content, content_hash, tags)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		n.UserID, n.FolderID, n.Title, n.Slug, n.TitleKey, n.DiskPath, n.Content, n.ContentHash, string(tagsJSON),
	).Scan(&id)
	if err != nil {
		if isUniqueConstraint(err) {
			return nil, apperror.Conflict(fmt.Sprintf("note %q already exists in this folder", n.Title))
		}
		return nil, fmt.Errorf("create note: %w", err)
	}

	return r.GetByID(ctx, n.UserID, id)
}

func (r *NoteRepo) GetByID(ctx context.Context, userID, noteID int64) (*model.Note, error) {
	return r.scanNote(r.readDB.QueryRowContext(ctx, `
		SELECT id, user_id, folder_id, title, slug, title_key, disk_path, content, content_hash,
		       tags, share_token, fts_synced_at, created_at, updated_at
		FROM notes WHERE id = ? AND user_id = ?`, noteID, userID))
}

func (r *NoteRepo) GetByShareToken(ctx context.Context, token string) (*model.Note, error) {
	// Share token lookup doesn't require user_id — it's public by design.
	return r.scanNote(r.readDB.QueryRowContext(ctx, `
		SELECT id, user_id, folder_id, title, slug, title_key, disk_path, content, content_hash,
		       tags, share_token, fts_synced_at, created_at, updated_at
		FROM notes WHERE share_token = ?`, token))
}

func (r *NoteRepo) GetByFolderAndSlug(ctx context.Context, userID int64, folderID *int64, slug string) (*model.Note, error) {
	if folderID == nil {
		return r.scanNote(r.readDB.QueryRowContext(ctx, `
			SELECT id, user_id, folder_id, title, slug, title_key, disk_path, content, content_hash,
			       tags, share_token, fts_synced_at, created_at, updated_at
			FROM notes WHERE user_id = ? AND folder_id IS NULL AND slug = ?`,
			userID, slug))
	}
	return r.scanNote(r.readDB.QueryRowContext(ctx, `
		SELECT id, user_id, folder_id, title, slug, title_key, disk_path, content, content_hash,
		       tags, share_token, fts_synced_at, created_at, updated_at
		FROM notes WHERE user_id = ? AND folder_id = ? AND slug = ?`,
		userID, *folderID, slug))
}

func (r *NoteRepo) ListByFolder(ctx context.Context, userID int64, folderID *int64) ([]*model.NoteListItem, error) {
	var rows *sql.Rows
	var err error

	if folderID == nil {
		rows, err = r.readDB.QueryContext(ctx, `
			SELECT id, folder_id, title, slug, tags, updated_at FROM notes
			WHERE user_id = ? AND folder_id IS NULL
			ORDER BY updated_at DESC`, userID)
	} else {
		rows, err = r.readDB.QueryContext(ctx, `
			SELECT id, folder_id, title, slug, tags, updated_at FROM notes
			WHERE user_id = ? AND folder_id = ?
			ORDER BY updated_at DESC`, userID, *folderID)
	}
	if err != nil {
		return nil, fmt.Errorf("list notes: %w", err)
	}
	defer rows.Close()

	return scanNoteListItems(rows)
}

func (r *NoteRepo) ListAll(ctx context.Context, userID int64) ([]*model.NoteListItem, error) {
	rows, err := r.readDB.QueryContext(ctx, `
		SELECT id, folder_id, title, slug, tags, updated_at FROM notes
		WHERE user_id = ?
		ORDER BY updated_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list all notes: %w", err)
	}
	defer rows.Close()

	return scanNoteListItems(rows)
}

func scanNoteListItems(rows *sql.Rows) ([]*model.NoteListItem, error) {
	var items []*model.NoteListItem
	for rows.Next() {
		item := &model.NoteListItem{}
		var tagsJSON string
		if err := rows.Scan(&item.ID, &item.FolderID, &item.Title, &item.Slug, &tagsJSON, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(tagsJSON), &item.Tags); err != nil {
			item.Tags = nil
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *NoteRepo) ListAllForWatch(ctx context.Context, userID int64) ([]*model.NoteWatchRecord, error) {
	rows, err := r.readDB.QueryContext(ctx,
		`SELECT id, folder_id, title, slug, disk_path, content_hash FROM notes WHERE user_id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("list notes for watch: %w", err)
	}
	defer rows.Close()

	var records []*model.NoteWatchRecord
	for rows.Next() {
		rec := &model.NoteWatchRecord{}
		if err := rows.Scan(&rec.ID, &rec.FolderID, &rec.Title, &rec.Slug, &rec.DiskPath, &rec.ContentHash); err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

func (r *NoteRepo) ListSubtree(ctx context.Context, userID int64, diskPath string) ([]*model.NoteExportRecord, error) {
	predicate, scopeArgs := repository.DescendantPathPredicate(diskPath)
	args := append([]any{userID}, scopeArgs...)

	rows, err := r.readDB.QueryContext(ctx,
		`SELECT id, folder_id, title, disk_path, updated_at FROM notes
		 WHERE user_id = ? AND (`+predicate+`)
		 ORDER BY disk_path`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("list note subtree: %w", err)
	}
	defer rows.Close()

	var records []*model.NoteExportRecord
	for rows.Next() {
		rec := &model.NoteExportRecord{}
		if err := rows.Scan(&rec.ID, &rec.FolderID, &rec.Title, &rec.DiskPath, &rec.UpdatedAt); err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

func (r *NoteRepo) Update(ctx context.Context, n *model.Note) error {
	tagsJSON, err := json.Marshal(n.Tags)
	if err != nil {
		return fmt.Errorf("marshal tags: %w", err)
	}

	// The title is indexed, so a rename has to mark the note stale for the same
	// reason a content edit does: until the sync path runs, the index still
	// holds the old title's tokens and a search for a word the note no longer
	// carries returns it (ticket 451).
	res, err := r.writeDB.ExecContext(ctx, `
		UPDATE notes SET title = ?, slug = ?, title_key = ?, disk_path = ?, tags = ?,
		       fts_synced_at = NULL, updated_at = datetime('now')
		WHERE id = ? AND user_id = ?`,
		n.Title, n.Slug, n.TitleKey, n.DiskPath, string(tagsJSON), n.ID, n.UserID,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return apperror.Conflict(fmt.Sprintf("note %q already exists in this folder", n.Title))
		}
		return fmt.Errorf("update note: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return apperror.ErrNotFound
	}
	return nil
}

// UpdateMetadataAndContent writes the note's metadata and its body in one
// statement. UpdateNoteMetadata needs that: a title change rewrites the file's
// "# H1" (so the body moves too), and the row must not be observable in a state
// where the title and the heading it was derived from disagree.
func (r *NoteRepo) UpdateMetadataAndContent(ctx context.Context, n *model.Note) error {
	tagsJSON, err := json.Marshal(n.Tags)
	if err != nil {
		return fmt.Errorf("marshal tags: %w", err)
	}

	res, err := r.writeDB.ExecContext(ctx, `
		UPDATE notes SET title = ?, slug = ?, title_key = ?, disk_path = ?, tags = ?,
		       content = ?, content_hash = ?, fts_synced_at = NULL, updated_at = datetime('now')
		WHERE id = ? AND user_id = ?`,
		n.Title, n.Slug, n.TitleKey, n.DiskPath, string(tagsJSON),
		n.Content, n.ContentHash, n.ID, n.UserID,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return apperror.Conflict(fmt.Sprintf("note %q already exists in this folder", n.Title))
		}
		return fmt.Errorf("update note: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return apperror.ErrNotFound
	}
	return nil
}

func (r *NoteRepo) UpdateContent(ctx context.Context, userID, noteID int64, content, contentHash, expectedHash string) error {
	res, err := r.writeDB.ExecContext(ctx, `
		UPDATE notes
		SET content = ?, content_hash = ?, fts_synced_at = NULL, updated_at = datetime('now')
		WHERE id = ? AND user_id = ? AND content_hash = ?`,
		content, contentHash, noteID, userID, expectedHash,
	)
	if err != nil {
		return fmt.Errorf("update content: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		// Either note doesn't exist, or optimistic concurrency conflict.
		// Distinguish by checking existence.
		var exists bool
		err = r.readDB.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM notes WHERE id = ? AND user_id = ?)`,
			noteID, userID,
		).Scan(&exists)
		if err != nil {
			return err
		}
		if !exists {
			return apperror.ErrNotFound
		}
		return apperror.ErrConflict
	}
	return nil
}

// SetTitle repairs a note's title and its normalized title key, leaving slug,
// disk_path, content and tags alone. The reconciler calls it when the file's
// "# H1" no longer matches the row (ticket N7); it holds no tag list to write
// back and must not invent one, and the filename is the disk's business.
//
// fts_synced_at is cleared because title is an indexed column: until the sync
// path runs, FTS still holds the old title's tokens and a search for a word the
// note no longer carries returns it.
func (r *NoteRepo) SetTitle(ctx context.Context, userID, noteID int64, title, titleKey string) error {
	res, err := r.writeDB.ExecContext(ctx,
		`UPDATE notes SET title = ?, title_key = ?, fts_synced_at = NULL, updated_at = datetime('now')
		 WHERE id = ? AND user_id = ?`,
		title, titleKey, noteID, userID,
	)
	if err != nil {
		return fmt.Errorf("set title: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperror.ErrNotFound
	}
	return nil
}

func (r *NoteRepo) SetTitleKey(ctx context.Context, userID, noteID int64, titleKey string) error {
	res, err := r.writeDB.ExecContext(ctx,
		`UPDATE notes SET title_key = ? WHERE id = ? AND user_id = ?`,
		titleKey, noteID, userID,
	)
	if err != nil {
		return fmt.Errorf("set title key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperror.ErrNotFound
	}
	return nil
}

func (r *NoteRepo) Move(ctx context.Context, userID, noteID int64, newFolderID *int64, newDiskPath string) error {
	res, err := r.writeDB.ExecContext(ctx,
		`UPDATE notes SET folder_id = ?, disk_path = ?, updated_at = datetime('now')
		 WHERE id = ? AND user_id = ?`,
		newFolderID, newDiskPath, noteID, userID,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return apperror.Conflict("a note with that name already exists in the destination folder")
		}
		return fmt.Errorf("move note: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apperror.ErrNotFound
	}
	return nil
}

func (r *NoteRepo) Delete(ctx context.Context, userID, noteID int64) error {
	// The note's index entry goes with the row, in one transaction. Search joins
	// `notes`, so an entry left behind by a deleted note is invisible to a
	// query — but it stays in the index for ever, and it still contributes to
	// the collection statistics `rank` is computed from. A contentless-delete
	// FTS5 table (migration 009) can drop the entry from the rowid alone, which
	// is why this is possible at all: an external-content table would need the
	// note's values, and the note is being deleted (ticket 451).
	tx, err := r.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`DELETE FROM notes WHERE id = ? AND user_id = ?`, noteID, userID)
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return apperror.ErrNotFound
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM notes_fts WHERE rowid = ?`, noteID); err != nil {
		return fmt.Errorf("delete note %d from fts: %w", noteID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete note: commit: %w", err)
	}
	return nil
}

func (r *NoteRepo) SetShareToken(ctx context.Context, userID, noteID int64, token *string) error {
	res, err := r.writeDB.ExecContext(ctx,
		`UPDATE notes SET share_token = ? WHERE id = ? AND user_id = ?`,
		token, noteID, userID,
	)
	if err != nil {
		return err
	}
	// Zero rows matched means no note with this id belongs to this user: either
	// it does not exist or it is somebody else's. SQLite reports every row the
	// UPDATE matched (even when the value is unchanged), so no distinction is
	// needed here. Returning nil would let the handler hand out a token that was
	// never written and tell the caller the note is shared. A driver that cannot
	// report the count is an error, not a success: we do not know the write
	// landed, so we must not claim it did.
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set share token: rows affected: %w", err)
	}
	if n == 0 {
		return apperror.ErrNotFound
	}
	return nil
}

func (r *NoteRepo) ListForContext(ctx context.Context, userID int64, folderID *int64) ([]*model.Note, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if folderID == nil {
		rows, err = r.readDB.QueryContext(ctx, `
			SELECT id, user_id, folder_id, title, slug, title_key, disk_path, content, content_hash,
			       tags, share_token, fts_synced_at, created_at, updated_at
			FROM notes WHERE user_id = ?
			ORDER BY updated_at DESC`, userID)
	} else {
		rows, err = r.readDB.QueryContext(ctx, `
			SELECT id, user_id, folder_id, title, slug, title_key, disk_path, content, content_hash,
			       tags, share_token, fts_synced_at, created_at, updated_at
			FROM notes WHERE user_id = ? AND folder_id = ?
			ORDER BY updated_at DESC`, userID, *folderID)
	}
	if err != nil {
		return nil, fmt.Errorf("list notes for context: %w", err)
	}
	defer rows.Close()
	return scanNotes(rows)
}

// IterateForContext streams notes for the context endpoint, newest-first,
// stopping as soon as fn says it has enough. See repository.NoteRepository for
// the contract; the short version is that Content is capped at contentChars so
// one oversized note cannot pull its whole body into memory.
func (r *NoteRepo) IterateForContext(ctx context.Context, userID int64, folderID *int64, contentChars int, fn func(*model.Note) (bool, error)) error {
	q := `
		SELECT id, user_id, folder_id, title, slug, title_key, disk_path,
		       SUBSTR(content, 1, ?), content_hash, tags, share_token, fts_synced_at,
		       created_at, updated_at
		FROM notes WHERE user_id = ?`
	args := []any{contentChars, userID}
	if folderID != nil {
		q += ` AND folder_id = ?`
		args = append(args, *folderID)
	}
	q += ` ORDER BY updated_at DESC`

	rows, err := r.readDB.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("iterate notes for context: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		n := &model.Note{}
		var tagsJSON string
		if err := rows.Scan(
			&n.ID, &n.UserID, &n.FolderID, &n.Title, &n.Slug, &n.TitleKey, &n.DiskPath,
			&n.Content, &n.ContentHash, &tagsJSON, &n.ShareToken,
			&n.FtsSyncedAt, &n.CreatedAt, &n.UpdatedAt,
		); err != nil {
			return fmt.Errorf("scan note: %w", err)
		}
		if err := json.Unmarshal([]byte(tagsJSON), &n.Tags); err != nil {
			n.Tags = nil
		}
		keep, err := fn(n)
		if err != nil {
			return err
		}
		if !keep {
			return nil
		}
	}
	return rows.Err()
}

func scanNotes(rows *sql.Rows) ([]*model.Note, error) {
	var notes []*model.Note
	for rows.Next() {
		n := &model.Note{}
		var tagsJSON string
		if err := rows.Scan(
			&n.ID, &n.UserID, &n.FolderID, &n.Title, &n.Slug, &n.TitleKey, &n.DiskPath,
			&n.Content, &n.ContentHash, &tagsJSON, &n.ShareToken,
			&n.FtsSyncedAt, &n.CreatedAt, &n.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan note: %w", err)
		}
		if err := json.Unmarshal([]byte(tagsJSON), &n.Tags); err != nil {
			n.Tags = nil
		}
		notes = append(notes, n)
	}
	return notes, rows.Err()
}

func (r *NoteRepo) scanNote(row *sql.Row) (*model.Note, error) {
	n := &model.Note{}
	var tagsJSON string
	err := row.Scan(
		&n.ID, &n.UserID, &n.FolderID, &n.Title, &n.Slug, &n.TitleKey, &n.DiskPath,
		&n.Content, &n.ContentHash, &tagsJSON, &n.ShareToken,
		&n.FtsSyncedAt, &n.CreatedAt, &n.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperror.ErrNotFound
		}
		return nil, fmt.Errorf("scan note: %w", err)
	}
	if err := json.Unmarshal([]byte(tagsJSON), &n.Tags); err != nil {
		n.Tags = nil
	}
	return n, nil
}
