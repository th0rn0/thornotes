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
func (r *SearchRepo) SyncNote(ctx context.Context, noteID int64) error {
	// Remove the note's existing index entry, if it has one. The 'delete'
	// command is the only way to remove a row from an external-content FTS5
	// table, and it fails when the row was never indexed — and that failure
	// leaves the index statistics unusable: rank and bm25() then return NULL for
	// every row, so relevance ordering silently stops working for the whole
	// database. Gate it on the docsize shadow table, which holds exactly one row
	// per indexed document (it exists because notes_fts is created with the
	// default columnsize=1).
	_, err := r.writeDB.ExecContext(ctx, `
		INSERT INTO notes_fts(notes_fts, rowid, title, content)
		SELECT 'delete', id, title, content FROM notes
		WHERE id = ? AND EXISTS (SELECT 1 FROM notes_fts_docsize WHERE id = ?)`,
		noteID, noteID)
	if err != nil {
		return fmt.Errorf("sync fts note %d (delete): %w", noteID, err)
	}

	_, err = r.writeDB.ExecContext(ctx, `
		INSERT INTO notes_fts(rowid, title, content)
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
