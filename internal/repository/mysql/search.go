package mysql

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
	db *sql.DB
}

func NewSearchRepo(db *sql.DB) *SearchRepo {
	return &SearchRepo{db: db}
}

// SyncNote is a no-op for MySQL: FULLTEXT indexes are updated synchronously by
// InnoDB on every write, so there is no deferred sync step needed.
func (r *SearchRepo) SyncNote(_ context.Context, _ int64) error {
	return nil
}

// Search runs a MySQL FULLTEXT search in boolean mode and returns matching
// notes for userID.
//
// The semantics are the search contract documented in internal/searchtext: every
// text term must match, every requested tag must be carried by the note, the
// snippet is computed in Go, and rows are ordered by relevance, then title,
// then id.
func (r *SearchRepo) Search(ctx context.Context, userID int64, query string, tags []string) ([]*model.SearchResult, error) {
	// Text terms are AND. searchtext.Terms is the one splitter, so this query
	// and the SQLite one search for exactly the same words.
	terms := searchtext.Terms(query)
	match := searchtext.BooleanAnd(terms)
	if match == "" {
		return nil, nil
	}

	// MATCH...AGAINST clause — must reference the FULLTEXT index columns directly.
	where := []string{"MATCH(n.title, n.content) AGAINST(? IN BOOLEAN MODE)", "n.user_id = ?"}
	args := []any{match, userID}

	// Tags are AND. One correlated EXISTS per tag, each unnesting the note's own
	// tags array with JSON_TABLE, so every tag gets a row of its own to match.
	// Filtering several tags on one unnested row (the pre-452 shape) can only
	// ever be satisfied by a note whose tags array holds a single value.
	// The comparison is forced to utf8mb4_bin because the column's default
	// collation is case-insensitive and a tag is an exact value in the contract.
	for _, tag := range tags {
		where = append(where, `EXISTS (SELECT 1 FROM JSON_TABLE(n.tags, '$[*]' COLUMNS (tag_val VARCHAR(255) PATH '$')) AS jt WHERE jt.tag_val COLLATE utf8mb4_bin = ?)`)
		args = append(args, tag)
	}

	// The snippet is built in Go by searchtext.Snippet from the note's content,
	// not in SQL — that is what makes it identical to the SQLite one.
	q := fmt.Sprintf(`
		SELECT n.id, n.title, n.slug, n.content, n.tags
		FROM notes n
		WHERE %s
		ORDER BY MATCH(n.title, n.content) AGAINST(? IN BOOLEAN MODE) DESC,
		         n.title COLLATE utf8mb4_bin,
		         n.id
		LIMIT 50`, strings.Join(where, " AND "))

	// Relevance argument for ORDER BY.
	args = append(args, match)

	rows, err := r.db.QueryContext(ctx, q, args...)
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
			res.Tags = nil
		}
		res.Snippet = searchtext.Snippet(content, terms)
		results = append(results, res)
	}
	return results, rows.Err()
}

// Ensure SearchRepo satisfies the repository.SearchRepository interface at compile time.
var _ interface {
	SyncNote(context.Context, int64) error
	Search(context.Context, int64, string, []string) ([]*model.SearchResult, error)
} = (*SearchRepo)(nil)
