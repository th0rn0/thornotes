package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/th0rn0/thornotes/internal/model"
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
	// Merge-insert into FTS. FTS5 external content: we must delete then insert.
	_, err := r.writeDB.ExecContext(ctx,
		`INSERT INTO notes_fts(notes_fts, rowid, title, content)
		 VALUES('delete', ?, '', '')`, noteID)
	// Ignore error — note may not exist in FTS yet.
	_ = err

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

// Search runs FTS5 and returns matching notes for userID.
// Notes with fts_synced_at IS NULL (modified since last sync) are synced first.
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

	// Sanitize FTS query — escape special FTS5 characters.
	safeQuery := sanitizeFTSQuery(query)
	if safeQuery == "" {
		return nil, nil
	}

	// Build tag filter clause.
	var whereClauses []string
	args := []any{safeQuery, userID}
	whereClauses = append(whereClauses, "fts.rowid = n.id")
	whereClauses = append(whereClauses, "n.user_id = ?")

	for _, tag := range tags {
		whereClauses = append(whereClauses, "json_each.value = ?")
		args = append(args, tag)
	}

	tagJoin := ""
	if len(tags) > 0 {
		tagJoin = ", json_each(n.tags)"
	}

	where := strings.Join(whereClauses, " AND ")

	q := fmt.Sprintf(`
		SELECT n.id, n.title, n.slug, snippet(notes_fts, 1, '<mark>', '</mark>', '…', 20), n.tags
		FROM notes_fts fts
		JOIN notes n %s
		WHERE notes_fts MATCH ?
		AND %s
		ORDER BY rank
		LIMIT 50`, tagJoin, where)

	// Re-order args: query goes first for the MATCH clause.
	ftsArgs := []any{safeQuery}
	ftsArgs = append(ftsArgs, args[1:]...)

	rows, err := r.readDB.QueryContext(ctx, q, ftsArgs...)
	if err != nil {
		return nil, fmt.Errorf("search query: %w", err)
	}
	defer rows.Close()

	var results []*model.SearchResult
	for rows.Next() {
		res := &model.SearchResult{}
		var tagsJSON string
		if err := rows.Scan(&res.NoteID, &res.Title, &res.Slug, &res.Snippet, &tagsJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(tagsJSON), &res.Tags); err != nil {
			res.Tags = nil // malformed stored JSON — treat as no tags
		}
		results = append(results, res)
	}
	return results, rows.Err()
}

// sanitizeFTSQuery escapes FTS5 special characters to prevent injection.
// FTS5 special chars: " * ^ ( ) :
func sanitizeFTSQuery(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return ""
	}
	// Wrap each token in double quotes so special chars are treated as literals.
	tokens := strings.Fields(q)
	escaped := make([]string, 0, len(tokens))
	for _, t := range tokens {
		// Strip any embedded double-quotes first, then wrap.
		t = strings.ReplaceAll(t, `"`, `""`)
		escaped = append(escaped, `"`+t+`"`)
	}
	return strings.Join(escaped, " OR ")
}
