package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/repository"
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
//
// One term can be too short for the FULLTEXT index to hold: innodb_ft_min_token_size
// drops tokens of one or two characters, and a MATCH ... AGAINST for such a term
// is a valid query that matches nothing, which is indistinguishable from "no such
// note" (ticket 619). Those terms are matched by a LIKE scan over the caller's own
// notes instead — see repository.SplitSearchTerms and repository.ShortTermPredicate.
// Both halves are ANDed, so a query mixing a long and a short term means what it
// says.
func (r *SearchRepo) Search(ctx context.Context, userID int64, query string, tags []string) ([]*model.SearchResult, error) {
	// Text terms are AND. searchtext.Terms is the one splitter, so this query
	// and the SQLite one search for exactly the same words.
	terms := searchtext.Terms(query)
	if len(terms) == 0 {
		return nil, nil
	}

	// The split is engine-independent: a term below repository.MinIndexedTerm is
	// scanned here exactly as it is on SQLite, so the two engines cannot return
	// different rows for the same query.
	indexed, scanned := repository.SplitSearchTerms(terms)
	match := searchtext.BooleanAnd(indexed)
	if len(indexed) > 0 && match == "" {
		// Defensive, and deliberately fail-closed: an indexable term that
		// boolean mode cannot express must not be dropped, because dropping it
		// widens the query to every note the user owns. Unreachable while
		// BooleanAnd keeps the alphanumeric part of a term, kept so a future
		// rule about what the index can answer cannot fail open.
		return nil, nil
	}

	where := []string{"n.user_id = ?"}
	args := []any{userID}

	if match != "" {
		where = append(where, "MATCH(n.title, n.content) AGAINST(? IN BOOLEAN MODE)")
		args = append(args, match)
	}
	// Short terms: one LIKE scan predicate each, over the same user-scoped row
	// set. Two comparisons per row of this user's notes, no index to narrow them
	// further — the deliberate cost of the term being findable at all.
	for _, term := range scanned {
		predicate, predicateArgs := repository.ShortTermPredicate(term)
		where = append(where, predicate)
		args = append(args, predicateArgs...)
	}

	// Tags are AND. One correlated EXISTS per tag, each unnesting the note's own
	// tags array with JSON_TABLE, so every tag gets a row of its own to match.
	// Filtering several tags on one unnested row (the pre-452 shape) can only
	// ever be satisfied by a note whose tags array holds a single value.
	// Collation is pinned explicitly rather than inherited. Tags match
	// case-insensitively (`#Home` == `#home`, Thorno's call 2026-11-09), and
	// naming general_ci keeps that true whatever the server's default is --
	// utf8mb4_bin here would silently make matching exact again.
	for _, tag := range tags {
		where = append(where, `EXISTS (SELECT 1 FROM JSON_TABLE(n.tags, '$[*]' COLUMNS (tag_val VARCHAR(255) PATH '$')) AS jt WHERE jt.tag_val COLLATE utf8mb4_general_ci = ?)`)
		args = append(args, tag)
	}

	// Relevance ordering needs the index, so it is only available when at least
	// one term was indexed. A query of nothing but short terms gives every
	// surviving row the same (absent) relevance, and the contract's tie-breakers
	// order it — which is also exactly the order SQLite produces for the same
	// query, since both compare the title binarily.
	order := "n.title COLLATE utf8mb4_bin, n.id"
	if match != "" {
		order = "MATCH(n.title, n.content) AGAINST(? IN BOOLEAN MODE) DESC, " + order
	}

	// The snippet is built in Go by searchtext.Snippet from the note's content,
	// not in SQL — that is what makes it identical to the SQLite one.
	q := fmt.Sprintf(`
		SELECT n.id, n.title, n.slug, n.content, n.tags
		FROM notes n
		WHERE %s
		ORDER BY %s
		LIMIT 50`, strings.Join(where, " AND "), order)

	// Relevance argument for ORDER BY.
	if match != "" {
		args = append(args, match)
	}

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
