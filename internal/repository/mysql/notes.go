package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

type NoteRepo struct {
	db *sql.DB
}

func NewNoteRepo(db *sql.DB) *NoteRepo {
	return &NoteRepo{db: db}
}

func (r *NoteRepo) Create(ctx context.Context, n *model.Note) (*model.Note, error) {
	tagsJSON, err := json.Marshal(n.Tags)
	if err != nil {
		return nil, fmt.Errorf("marshal tags: %w", err)
	}

	res, err := r.db.ExecContext(ctx, `
		INSERT INTO notes (user_id, folder_id, title, slug, title_key, disk_path, content, content_hash, tags)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.UserID, n.FolderID, n.Title, n.Slug, n.TitleKey, n.DiskPath, n.Content, n.ContentHash, string(tagsJSON),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return nil, apperror.Conflict(fmt.Sprintf("note %q already exists in this folder", n.Title))
		}
		return nil, fmt.Errorf("create note: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("create note last insert id: %w", err)
	}
	return r.GetByID(ctx, n.UserID, id)
}

func (r *NoteRepo) GetByID(ctx context.Context, userID, noteID int64) (*model.Note, error) {
	return r.scanNote(r.db.QueryRowContext(ctx, `
		SELECT id, user_id, folder_id, title, slug, title_key, disk_path, content, content_hash,
		       tags, share_token, fts_synced_at, created_at, updated_at
		FROM notes WHERE id = ? AND user_id = ?`, noteID, userID))
}

func (r *NoteRepo) GetByShareToken(ctx context.Context, token string) (*model.Note, error) {
	return r.scanNote(r.db.QueryRowContext(ctx, `
		SELECT id, user_id, folder_id, title, slug, title_key, disk_path, content, content_hash,
		       tags, share_token, fts_synced_at, created_at, updated_at
		FROM notes WHERE share_token = ?`, token))
}

func (r *NoteRepo) GetByFolderAndSlug(ctx context.Context, userID int64, folderID *int64, slug string) (*model.Note, error) {
	if folderID == nil {
		return r.scanNote(r.db.QueryRowContext(ctx, `
			SELECT id, user_id, folder_id, title, slug, title_key, disk_path, content, content_hash,
			       tags, share_token, fts_synced_at, created_at, updated_at
			FROM notes WHERE user_id = ? AND folder_id IS NULL AND slug = ?`,
			userID, slug))
	}
	return r.scanNote(r.db.QueryRowContext(ctx, `
		SELECT id, user_id, folder_id, title, slug, title_key, disk_path, content, content_hash,
		       tags, share_token, fts_synced_at, created_at, updated_at
		FROM notes WHERE user_id = ? AND folder_id = ? AND slug = ?`,
		userID, *folderID, slug))
}

func (r *NoteRepo) ListByFolder(ctx context.Context, userID int64, folderID *int64) ([]*model.NoteListItem, error) {
	var rows *sql.Rows
	var err error

	if folderID == nil {
		rows, err = r.db.QueryContext(ctx, `
			SELECT id, folder_id, title, slug, tags, updated_at FROM notes
			WHERE user_id = ? AND folder_id IS NULL
			ORDER BY updated_at DESC`, userID)
	} else {
		rows, err = r.db.QueryContext(ctx, `
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
	rows, err := r.db.QueryContext(ctx, `
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
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, folder_id, slug, disk_path, content_hash FROM notes WHERE user_id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("list notes for watch: %w", err)
	}
	defer rows.Close()

	var records []*model.NoteWatchRecord
	for rows.Next() {
		rec := &model.NoteWatchRecord{}
		if err := rows.Scan(&rec.ID, &rec.FolderID, &rec.Slug, &rec.DiskPath, &rec.ContentHash); err != nil {
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

	// A same-value save (a PUT that repeats the current title and tags, or an
	// edit whose updated_at does not move within the same second) changes no
	// column and so reports 0 rows affected. The row still matched: success. Only
	// a row that is not the caller's — or does not exist — is not-found, and both
	// answer 404 so a foreign id leaks nothing.
	matched, err := updateScoped(ctx, r.db, `
		UPDATE notes SET title = ?, slug = ?, title_key = ?, disk_path = ?, tags = ?, updated_at = UTC_TIMESTAMP()
		WHERE id = ? AND user_id = ?`,
		[]any{n.Title, n.Slug, n.TitleKey, n.DiskPath, string(tagsJSON), n.ID, n.UserID},
		`SELECT EXISTS(SELECT 1 FROM notes WHERE id = ? AND user_id = ?)`,
		[]any{n.ID, n.UserID},
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return apperror.Conflict(fmt.Sprintf("note %q already exists in this folder", n.Title))
		}
		return fmt.Errorf("update note: %w", err)
	}
	if !matched {
		return apperror.ErrNotFound
	}
	return nil
}

func (r *NoteRepo) UpdateContent(ctx context.Context, userID, noteID int64, content, contentHash, expectedHash string) error {
	// The CAS predicate includes content_hash, so it must be the existence check
	// too: a row that matched on (id, user_id, content_hash) but changed no
	// column was re-saved with the content it already had — a no-op write, and
	// success. Treating that as "the hash is stale" reported a 409 conflict for
	// an unchanged save (ticket 599).
	matched, err := updateScoped(ctx, r.db, `
		UPDATE notes
		SET content = ?, content_hash = ?, fts_synced_at = NULL, updated_at = UTC_TIMESTAMP()
		WHERE id = ? AND user_id = ? AND content_hash = ?`,
		[]any{content, contentHash, noteID, userID, expectedHash},
		`SELECT EXISTS(SELECT 1 FROM notes WHERE id = ? AND user_id = ? AND content_hash = ?)`,
		[]any{noteID, userID, expectedHash},
	)
	if err != nil {
		return fmt.Errorf("update content: %w", err)
	}
	if matched {
		return nil
	}

	// No row matched the CAS predicate. Two causes, and the caller cannot tell
	// them apart from here: the note is not the caller's (or gone) — 404 — or it
	// is the caller's and its content_hash moved on since expectedHash was read,
	// which is the optimistic-concurrency conflict the write API documents as
	// 409. The note's existence decides which.
	if err := r.ensureNoteOwned(ctx, userID, noteID); err != nil {
		return err
	}
	return apperror.ErrConflict
}

func (r *NoteRepo) SetTitleKey(ctx context.Context, userID, noteID int64, titleKey string) error {
	res, err := r.db.ExecContext(ctx,
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
	res, err := r.db.ExecContext(ctx,
		`UPDATE notes SET folder_id = ?, disk_path = ?, updated_at = NOW()
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
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM notes WHERE id = ? AND user_id = ?`, noteID, userID)
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return apperror.ErrNotFound
	}
	return nil
}

func (r *NoteRepo) SetShareToken(ctx context.Context, userID, noteID int64, token *string) error {
	// Both branches go through updateScoped, because both can write a value the
	// row already holds and MySQL would count that as "no rows affected":
	//
	//   - Clearing (token == nil): writing NULL over an already-NULL column is a
	//     genuine no-op, so the zero count cannot distinguish "already
	//     unpublished" — which is success — from "no such row".
	//   - Generating (token != nil): the token is freshly random, so a matched row
	//     practically cannot already hold it and the changed count is decisive;
	//     the existence check never runs. It stays wired up anyway so both
	//     branches share one rule: matched means success, unmatched means
	//     not-found.
	//
	// The predicate comparison uses MySQL's NULL-safe `<=>`, so the existence
	// check matches a NULL share_token as well as a set one.
	//
	// Returning nil on an unmatched row would be the bug: a token handed to the
	// caller that was never written.
	matched, err := updateScoped(ctx, r.db,
		`UPDATE notes SET share_token = ? WHERE id = ? AND user_id = ?`,
		[]any{token, noteID, userID},
		`SELECT EXISTS(SELECT 1 FROM notes WHERE id = ? AND user_id = ? AND share_token <=> ?)`,
		[]any{noteID, userID, token},
	)
	if err != nil {
		return fmt.Errorf("set share token: %w", err)
	}
	if !matched {
		return apperror.ErrNotFound
	}
	return nil
}

// ensureNoteOwned returns apperror.ErrNotFound unless a note with this id belongs
// to userID. It is the tie-breaker for a by-(id, user_id) UPDATE that matched no
// row on a driver that counts changed rows: it separates "the note is not
// yours", which is not-found, from "the note is yours but the update's further
// predicate did not match", which the caller reports as a conflict.
func (r *NoteRepo) ensureNoteOwned(ctx context.Context, userID, noteID int64) error {
	var exists bool
	if err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM notes WHERE id = ? AND user_id = ?)`,
		noteID, userID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check note ownership: %w", err)
	}
	if !exists {
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
		rows, err = r.db.QueryContext(ctx, `
			SELECT id, user_id, folder_id, title, slug, title_key, disk_path, content, content_hash,
			       tags, share_token, fts_synced_at, created_at, updated_at
			FROM notes WHERE user_id = ?
			ORDER BY updated_at DESC`, userID)
	} else {
		rows, err = r.db.QueryContext(ctx, `
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
