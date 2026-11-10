package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/searchtext"
)

type SearchRepo struct {
	readDB  *sql.DB
	writeDB *sql.DB
}

func NewSearchRepo(readDB, writeDB *sql.DB) *SearchRepo {
	return &SearchRepo{readDB: readDB, writeDB: writeDB}
}

// SyncNote brings a single note's FTS entry up to date.
//
// notes_fts is a contentless-delete FTS5 table (migration 009), so a row can be
// unindexed from its rowid alone. That is the whole reason for the table's
// shape: an external-content table's 'delete' command removes the tokens of the
// values it is handed, and this path only ever has the note's *current* values —
// the values of the text that is being replaced are not available to it. Handing
// it the current values added the new tokens and removed whichever of them had
// been indexed before, leaving the tokens of the old text in the index with
// nothing to remove them (ticket 451).
//
// INSERT OR REPLACE is the documented update path for a contentless-delete table
// and replaces the row's whole entry in one statement, so a failed sync cannot
// leave the note half-unindexed.
func (r *SearchRepo) SyncNote(ctx context.Context, noteID int64) error {
	_, err := r.writeDB.ExecContext(ctx, `
		INSERT OR REPLACE INTO notes_fts(rowid, title, content)
		SELECT id, title, content FROM notes WHERE id = ?`, noteID)
	if err != nil {
		return fmt.Errorf("sync fts note %d: %w", noteID, err)
	}

	_, err = r.writeDB.ExecContext(ctx,
		`UPDATE notes SET fts_synced_at = datetime('now') WHERE id = ?`, noteID)
	return err
}

// Search runs an FTS5 query and returns matching notes for userID.
// Notes with fts_synced_at IS NULL (modified since last sync) are synced first.
//
// The semantics are the search contract documented in internal/searchtext: every
// text term must match, every requested tag must be carried by the note, the
// snippet is computed in Go, and rows are ordered by relevance, then title,
// then id.
func (r *SearchRepo) Search(ctx context.Context, userID int64, query string, tags []string) ([]*model.SearchResult, error) {
	// Sync stale notes first.
	stale, err := r.readDB.QueryContext(ctx,
		`SELECT id FROM notes WHERE user_id = ? AND fts_synced_at IS NULL`, userID)
	if err != nil {
		return nil, err
	}
	var staleIDs []int64
	for stale.Next() {
		var id int64
		if err := stale.Scan(&id); err != nil {
			stale.Close()
			return nil, err
		}
		staleIDs = append(staleIDs, id)
	}
	stale.Close()

	for _, id := range staleIDs {
		if err := r.SyncNote(ctx, id); err != nil {
			return nil, err
		}
	}

	// Text terms are AND. searchtext.Terms is the one splitter, so this query
	// and the MySQL one search for exactly the same words.
	terms := searchtext.Terms(query)
	match := searchtext.FTSAnd(terms)
	if match == "" {
		return nil, nil
	}

	// Tags are AND, and tag matching is case-insensitive: `#Home` and `#home`
	// are the same tag for query purposes (Thorno's call, 2026-11-09). NOCASE is
	// ASCII-only in SQLite, which is why the comparison is spelled out here
	// rather than left to the default BINARY collation.
	// One correlated EXISTS per tag: each subquery unnests the note's own tags
	// array, so every tag gets a row of its own to match.
	// Filtering several tags on one unnested row (the pre-452 shape) can only
	// ever be satisfied by a note whose tags array holds a single value.
	where := []string{"fts.rowid = n.id", "n.user_id = ?"}
	args := []any{match, userID}
	for _, tag := range tags {
		where = append(where, `EXISTS (SELECT 1 FROM json_each(n.tags) WHERE json_each.value COLLATE NOCASE = ?)`)
		args = append(args, tag)
	}

	// The snippet is built in Go by searchtext.Snippet from the note's content,
	// not by FTS5's snippet() — MySQL has no equivalent and production runs
	// MySQL, so both engines take the same path.
	q := fmt.Sprintf(`
		SELECT n.id, n.title, n.slug, n.content, n.tags
		FROM notes_fts fts
		JOIN notes n ON fts.rowid = n.id
		WHERE notes_fts MATCH ?
		AND %s
		ORDER BY rank, n.title, n.id
		LIMIT 50`, strings.Join(where, " AND "))

	rows, err := r.readDB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("search query: %w", err)
	}
	defer rows.Close()

	var results []*model.SearchResult
	for rows.Next() {
		res := &model.SearchResult{}
		var (
			tagsJSON string
			content  string
		)
		if err := rows.Scan(&res.NoteID, &res.Title, &res.Slug, &content, &tagsJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(tagsJSON), &res.Tags); err != nil {
			res.Tags = nil // malformed stored JSON — treat as no tags
		}
		res.Snippet = searchtext.Snippet(content, terms)
		results = append(results, res)
	}
	return results, rows.Err()
}
