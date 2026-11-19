package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/repository"
)

// FolderShareRepo stores the bearer tokens that expose a folder anonymously
// (GET /f/<token>). It mirrors the share half of NoteRepo, one table over.
type FolderShareRepo struct {
	readDB  *sql.DB
	writeDB *sql.DB
}

func NewFolderShareRepo(readDB, writeDB *sql.DB) *FolderShareRepo {
	return &FolderShareRepo{readDB: readDB, writeDB: writeDB}
}

// GetByToken returns the share carrying this token, or ErrNotFound.
func (r *FolderShareRepo) GetByToken(ctx context.Context, token string) (*model.FolderShare, error) {
	share := &model.FolderShare{}
	err := r.readDB.QueryRowContext(ctx,
		`SELECT id, user_id, folder_id, token, expires_at, last_viewed_at, created_at
		 FROM folder_shares WHERE token = ?`,
		token,
	).Scan(&share.ID, &share.UserID, &share.FolderID, &share.Token,
		&share.ExpiresAt, &share.LastViewedAt, &share.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperror.ErrNotFound
		}
		return nil, fmt.Errorf("get folder share: %w", err)
	}
	return share, nil
}

// UpsertToken issues or rotates the folder's link.
//
// One statement, not UPDATE-then-INSERT: the ownership guard and the rotation are
// the same write, so two concurrent shares of one folder cannot both install a
// token and leave the loser's link dead but stored.
//
//   - The `SELECT ... WHERE EXISTS (...)` half is the #40 fix in SQL: a folder id
//     the caller does not own produces no row to insert, so the statement reports
//     zero changes and the caller answers 404 — never a token for somebody
//     else's folder.
//   - `ON CONFLICT(folder_id) DO UPDATE` is the rotate: one row per folder (the
//     unique index on folder_id), and a new token clears expires_at and
//     last_viewed_at because both belong to the link that was just created.
//     Clearing the token without clearing the deadline is the ordering hazard
//     ticket 601 had to fix on notes; here the row is rewritten whole, so it
//     cannot recur.
//
// A zero change count therefore means exactly one thing — the folder is not the
// caller's — because a rotation always writes a fresh random token and SQLite
// counts matched rows, not changed ones.
func (r *FolderShareRepo) UpsertToken(ctx context.Context, userID, folderID int64, token string) error {
	res, err := r.writeDB.ExecContext(ctx, `
		INSERT INTO folder_shares (user_id, folder_id, token)
		SELECT ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM folders WHERE id = ? AND user_id = ?)
		ON CONFLICT(folder_id) DO UPDATE SET
			token = excluded.token,
			expires_at = NULL,
			last_viewed_at = NULL`,
		userID, folderID, token, folderID, userID,
	)
	if err != nil {
		return fmt.Errorf("upsert folder share: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("upsert folder share: rows affected: %w", err)
	}
	if n == 0 {
		return apperror.ErrNotFound
	}
	return nil
}

// Clear revokes the folder's link. Deleting the row is the whole operation: a
// revoked link and a folder that was never shared become the same state, which
// is what makes the anonymous 404 for both byte-identical.
//
// A folder with no row is success, not not-found: the caller has already
// established that the folder is theirs, and a revoke that errors when there is
// nothing to revoke would be a bug in the UI rather than a fact about the data.
func (r *FolderShareRepo) Clear(ctx context.Context, userID, folderID int64) error {
	if _, err := r.writeDB.ExecContext(ctx,
		`DELETE FROM folder_shares WHERE folder_id = ? AND user_id = ?`,
		folderID, userID,
	); err != nil {
		return fmt.Errorf("clear folder share: %w", err)
	}
	return nil
}

// TouchView stamps last_viewed_at with now, at most once per window.
//
// Deliberately not "did it match": here zero changed rows is the answer, because
// a row that matched but already carried a stamp inside the window means another
// view won the race and this one must not write. The SQL predicate repeats the
// Go-side bound so two racing views cannot both stamp, expressed with a bound
// time so both engines compare the same pair of values.
func (r *FolderShareRepo) TouchView(ctx context.Context, folderID int64, previous *time.Time, now time.Time, window time.Duration) (bool, error) {
	if !repository.ShareViewDue(previous, now, window) {
		return false, nil
	}
	res, err := r.writeDB.ExecContext(ctx, `
		UPDATE folder_shares SET last_viewed_at = ?
		WHERE folder_id = ?
		  AND (last_viewed_at IS NULL OR last_viewed_at <= ?)`,
		now.UTC(), folderID, now.Add(-window).UTC(),
	)
	if err != nil {
		return false, fmt.Errorf("touch folder share view: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("touch folder share view: rows affected: %w", err)
	}
	return n > 0, nil
}

// ListChildFolders returns one level of a folder's subfolders, ordered by name
// and keyset-paged on it.
//
// parentID is never nil here: the shared root is a row with an id, so the root
// case of ListByFolder (folder_id IS NULL) has no counterpart on this surface.
func (r *FolderShareRepo) ListChildFolders(ctx context.Context, userID, parentID int64, after string, limit int) ([]*model.Folder, error) {
	rows, err := r.readDB.QueryContext(ctx, `
		SELECT id, user_id, parent_id, name, disk_path, created_at FROM folders
		WHERE user_id = ? AND parent_id = ? AND name > ?
		ORDER BY name LIMIT ?`,
		userID, parentID, after, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list child folders: %w", err)
	}
	defer rows.Close()

	var folders []*model.Folder
	for rows.Next() {
		f := &model.Folder{}
		if err := rows.Scan(&f.ID, &f.UserID, &f.ParentID, &f.Name, &f.DiskPath, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan child folder: %w", err)
		}
		folders = append(folders, f)
	}
	return folders, rows.Err()
}

// ListChildNotes returns one level of a folder's notes, ordered by slug and
// keyset-paged on it. Tags are not selected: the anonymous listing renders a
// title and a link, and the fewer columns an unauthenticated query reads the
// better.
func (r *FolderShareRepo) ListChildNotes(ctx context.Context, userID, folderID int64, after string, limit int) ([]*model.NoteListItem, error) {
	rows, err := r.readDB.QueryContext(ctx, `
		SELECT id, folder_id, title, slug, updated_at FROM notes
		WHERE user_id = ? AND folder_id = ? AND slug > ?
		ORDER BY slug LIMIT ?`,
		userID, folderID, after, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list child notes: %w", err)
	}
	defer rows.Close()

	var items []*model.NoteListItem
	for rows.Next() {
		item := &model.NoteListItem{}
		if err := rows.Scan(&item.ID, &item.FolderID, &item.Title, &item.Slug, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan child note: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ListShares returns the caller's own folder links, one row per live share, with
// the token redacted to a display prefix and the folder's name as the title.
//
// It is the folder half of GET /api/v1/shares, and it exists because a folder
// share is a *recursive* grant: before it, the only way to find one was to
// remember which folder had been shared, which is the one thing a link is for
// not having to remember. Revoking still happens on the folder route; this makes
// the link findable so that route can be reached.
//
// Three deliberate choices:
//
//   - `fs.user_id = ?` is the whole authorisation, in SQL, like the note half.
//     Another account's folder link cannot be in this answer, whatever the
//     caller does with the request.
//   - The join to `folders` is what supplies the name for display; it is
//     INNER, so a share row that outlived its folder is not listed. The foreign
//     key cascades, so that row should not exist — and a link to a folder that
//     is not there does not resolve either.
//   - There is no expiry predicate. An expired link is listed with its deadline
//     visible, exactly as the note half lists one: the deadline is the only
//     thing that explains why a URL the owner still has stopped answering.
//     Filtering here would make the two halves of this one endpoint disagree
//     about what "the links you have handed out" means.
//
// The ORDER BY is for debuggability, not for the contract: notes.Service
// .ListShares merges this answer with the note half and imposes the one order the
// endpoint promises (updated_at DESC, then kind, then id).
func (r *FolderShareRepo) ListShares(ctx context.Context, userID int64) ([]model.ShareLink, error) {
	rows, err := r.readDB.QueryContext(ctx, `
		SELECT fs.folder_id, f.name, fs.token, fs.expires_at, fs.last_viewed_at, fs.created_at
		FROM folder_shares fs
		JOIN folders f ON f.id = fs.folder_id AND f.user_id = fs.user_id
		WHERE fs.user_id = ?
		ORDER BY fs.created_at DESC, fs.folder_id ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list folder shares: %w", err)
	}
	defer rows.Close()

	var shares []model.ShareLink
	for rows.Next() {
		var (
			link    model.ShareLink
			folder  int64
			title   string
			token   string
			created time.Time
		)
		if err := rows.Scan(&folder, &title, &token, &link.ExpiresAt, &link.LastViewedAt, &created); err != nil {
			return nil, fmt.Errorf("scan folder share: %w", err)
		}
		folderID := folder
		link.Kind = model.ShareKindFolder
		// No note: a folder link is not a note link, and a zero here would be a
		// note id that does not exist.
		link.NoteID = nil
		link.FolderID = &folderID
		link.Title = title
		link.TokenPrefix = repository.RedactShareToken(token)
		// A folder share row has no updated_at: nothing edits it but rotation,
		// which rewrites the token and the deadline and leaves created_at alone.
		// Its creation is therefore the only "when did this link appear" the row
		// can answer, which is what the list orders by.
		link.UpdatedAt = created
		shares = append(shares, link)
	}
	return shares, rows.Err()
}
