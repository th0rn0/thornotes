package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/th0rn0/thornotes/internal/model"
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

// Search runs a MySQL FULLTEXT search in boolean mode and returns matching notes
// for userID. Tags are filtered using JSON_TABLE (requires MySQL 8.0+).
func (r *SearchRepo) Search(ctx context.Context, userID int64, query string, tags []string) ([]*model.SearchResult, error) {
	boolQuery := toBooleanMode(query)
	if boolQuery == "" {
		return nil, nil
	}

	var (
		whereClauses []string
		args         []any
	)
	// MATCH...AGAINST clause — must reference the FULLTEXT index columns directly.
	whereClauses = append(whereClauses, "MATCH(n.title, n.content) AGAINST(? IN BOOLEAN MODE)")
	args = append(args, boolQuery)
	whereClauses = append(whereClauses, "n.user_id = ?")
	args = append(args, userID)

	tagJoin := ""
	for _, tag := range tags {
		// Use JSON_TABLE to unnest the tags JSON array and filter by value.
		tagJoin = `, JSON_TABLE(n.tags, '$[*]' COLUMNS (tag_val VARCHAR(255) PATH '$')) AS jt`
		whereClauses = append(whereClauses, "jt.tag_val = ?")
		args = append(args, tag)
		break // one JSON_TABLE join covers all tag filters
	}
	// If multiple tags, add extra WHERE clauses on the same join column.
	for i, tag := range tags {
		if i == 0 {
			continue // already handled above
		}
		whereClauses = append(whereClauses, "jt.tag_val = ?")
		args = append(args, tag)
	}

	where := strings.Join(whereClauses, " AND ")

	q := fmt.Sprintf(`
		SELECT n.id, n.title, n.slug,
		       LEFT(n.content, 200) AS snippet,
		       n.tags
		FROM notes n%s
		WHERE %s
		ORDER BY MATCH(n.title, n.content) AGAINST(? IN BOOLEAN MODE) DESC
		LIMIT 50`, tagJoin, where)

	// Append the relevance score arg for ORDER BY.
	args = append(args, boolQuery)

	rows, err := r.db.QueryContext(ctx, q, args...)
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
			res.Tags = nil
		}
		results = append(results, res)
	}
	return results, rows.Err()
}

// toBooleanMode converts a plain text query into MySQL FULLTEXT boolean mode syntax.
// Each word is prefixed with '+' (must appear) and quoted to handle special chars.
func toBooleanMode(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return ""
	}
	tokens := strings.Fields(q)
	parts := make([]string, 0, len(tokens))
	for _, t := range tokens {
		// Strip MySQL boolean-mode special characters to avoid syntax errors.
		t = strings.NewReplacer(
			`+`, ``, `-`, ``, `~`, ``, `*`, ``, `(`, ``, `)`, ``, `"`, ``, `<`, ``, `>`, ``,
		).Replace(t)
		if t == "" {
			continue
		}
		parts = append(parts, `+"`+t+`"`)
	}
	return strings.Join(parts, " ")
}

// Ensure SearchRepo satisfies the repository.SearchRepository interface at compile time.
var _ interface {
	SyncNote(context.Context, int64) error
	Search(context.Context, int64, string, []string) ([]*model.SearchResult, error)
} = (*SearchRepo)(nil)
