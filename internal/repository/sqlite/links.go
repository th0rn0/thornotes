package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

// LinkRepo persists and queries the resolved link graph (note_links).
type LinkRepo struct {
	readDB  *sql.DB
	writeDB *sql.DB
}

func NewLinkRepo(readDB, writeDB *sql.DB) *LinkRepo {
	return &LinkRepo{readDB: readDB, writeDB: writeDB}
}

const noteLinkColumns = `id, user_id, from_note_id, to_note_id, raw_target, target_hash, anchor, link_text, ambiguous, resolved_at, created_at`

func linkBoolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func (r *LinkRepo) ReplaceLinks(ctx context.Context, userID, fromNoteID int64, edges []model.NoteLink) error {
	tx, err := r.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("replace links begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once the tx is committed

	if _, err := tx.ExecContext(ctx, `DELETE FROM note_links WHERE user_id = ? AND from_note_id = ?`, userID, fromNoteID); err != nil {
		return fmt.Errorf("replace links delete: %w", err)
	}
	for _, e := range edges {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO note_links (user_id, from_note_id, to_note_id, raw_target, target_hash, anchor, link_text, ambiguous, resolved_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			userID, fromNoteID, e.ToNoteID, e.RawTarget, e.TargetHash, e.Anchor, e.LinkText, linkBoolInt(e.Ambiguous), e.ResolvedAt,
		); err != nil {
			return fmt.Errorf("replace links insert: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("replace links commit: %w", err)
	}
	return nil
}

func (r *LinkRepo) DeleteForUser(ctx context.Context, userID int64) error {
	_, err := r.writeDB.ExecContext(ctx, `DELETE FROM note_links WHERE user_id = ?`, userID)
	return err
}

func (r *LinkRepo) ListOutgoing(ctx context.Context, userID, noteID int64) ([]model.NoteLink, error) {
	rows, err := r.readDB.QueryContext(ctx,
		`SELECT `+noteLinkColumns+` FROM note_links WHERE user_id = ? AND from_note_id = ? ORDER BY id`,
		userID, noteID)
	if err != nil {
		return nil, fmt.Errorf("list outgoing: %w", err)
	}
	defer rows.Close()
	return scanNoteLinks(rows)
}

func (r *LinkRepo) ListBacklinks(ctx context.Context, userID, noteID int64) ([]model.Backlink, error) {
	rows, err := r.readDB.QueryContext(ctx, `
		SELECT nl.from_note_id, n.title, n.folder_id, nl.anchor, nl.link_text
		FROM note_links nl JOIN notes n ON n.id = nl.from_note_id
		WHERE nl.user_id = ? AND nl.to_note_id = ?
		ORDER BY n.title`, userID, noteID)
	if err != nil {
		return nil, fmt.Errorf("list backlinks: %w", err)
	}
	defer rows.Close()

	var out []model.Backlink
	for rows.Next() {
		b := model.Backlink{}
		if err := rows.Scan(&b.FromNoteID, &b.FromTitle, &b.FromFolderID, &b.Anchor, &b.LinkText); err != nil {
			return nil, fmt.Errorf("scan backlink: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (r *LinkRepo) ListByTargetHash(ctx context.Context, userID int64, targetHash string) ([]model.NoteLink, error) {
	rows, err := r.readDB.QueryContext(ctx,
		`SELECT `+noteLinkColumns+` FROM note_links WHERE user_id = ? AND target_hash = ? ORDER BY id`,
		userID, targetHash)
	if err != nil {
		return nil, fmt.Errorf("list by target hash: %w", err)
	}
	defer rows.Close()
	return scanNoteLinks(rows)
}

func (r *LinkRepo) ListInboundForRename(ctx context.Context, userID, noteID int64) ([]model.InboundLink, error) {
	rows, err := r.readDB.QueryContext(ctx, `
		SELECT nl.id, nl.from_note_id, n.folder_id, nl.raw_target
		FROM note_links nl JOIN notes n ON n.id = nl.from_note_id
		WHERE nl.user_id = ? AND nl.to_note_id = ?`, userID, noteID)
	if err != nil {
		return nil, fmt.Errorf("list inbound for rename: %w", err)
	}
	defer rows.Close()

	var out []model.InboundLink
	for rows.Next() {
		il := model.InboundLink{}
		if err := rows.Scan(&il.ID, &il.FromNoteID, &il.FromFolderID, &il.RawTarget); err != nil {
			return nil, fmt.Errorf("scan inbound: %w", err)
		}
		out = append(out, il)
	}
	return out, rows.Err()
}

func (r *LinkRepo) UpdateResolution(ctx context.Context, userID, linkID int64, toNoteID *int64, ambiguous bool) error {
	var resolvedAt *time.Time
	if toNoteID != nil {
		now := time.Now().UTC()
		resolvedAt = &now
	}
	res, err := r.writeDB.ExecContext(ctx,
		`UPDATE note_links SET to_note_id = ?, ambiguous = ?, resolved_at = ? WHERE id = ? AND user_id = ?`,
		toNoteID, linkBoolInt(ambiguous), resolvedAt, linkID, userID)
	if err != nil {
		return fmt.Errorf("update link resolution: %w", err)
	}
	// Zero rows matched means no edge with this id belongs to this user: it is
	// gone (a concurrent full reindex deletes and rebuilds the graph) or was
	// never theirs. Returning nil would report a link as re-resolved while its
	// row still points at the old target, so the count decides. SQLite reports
	// every row the UPDATE matched — even when the values written are the ones
	// already stored — so no follow-up query is needed here; MySQL counts
	// changed rows and goes through updateScoped instead.
	n, err := res.RowsAffected()
	if err != nil {
		// A count we could not read is an error, never a success.
		return fmt.Errorf("update link resolution: rows affected: %w", err)
	}
	if n == 0 {
		return apperror.ErrNotFound
	}
	return nil
}

func scanNoteLinks(rows *sql.Rows) ([]model.NoteLink, error) {
	var out []model.NoteLink
	for rows.Next() {
		l := model.NoteLink{}
		var ambiguous int64
		if err := rows.Scan(&l.ID, &l.UserID, &l.FromNoteID, &l.ToNoteID, &l.RawTarget,
			&l.TargetHash, &l.Anchor, &l.LinkText, &ambiguous, &l.ResolvedAt, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan note link: %w", err)
		}
		l.Ambiguous = ambiguous != 0
		out = append(out, l)
	}
	return out, rows.Err()
}
