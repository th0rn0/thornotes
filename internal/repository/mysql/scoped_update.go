package mysql

import (
	"context"
	"database/sql"
	"fmt"
)

// updateScoped runs a single-row UPDATE and answers the question RowsAffected()
// cannot answer on MySQL: did a row match, or did no row match at all?
//
// MySQL and MariaDB report how many rows an UPDATE *changed*, not how many it
// *matched*. An UPDATE that rewrites a row with the values it already holds
// changes nothing and reports 0 — indistinguishable, by the count alone, from an
// UPDATE whose WHERE clause matched nothing. Every write in this package that
// targets one row by (id, user_id) therefore has to resolve that ambiguity, or a
// same-value write reaches the user as a false 404 or 409 (ticket 599):
//
//   - a note saved twice with the same title/tags is a 404,
//   - a note whose content is re-saved unchanged is a 409 conflict,
//   - a folder renamed to the name it already has is a 404.
//
// None of those appear on SQLite, which counts matched rows; the identical code
// paths are covered there by passing tests, which is why the bug survived.
//
// matchedQuery must ask, argument for argument, whether the row the UPDATE would
// have hit still exists — i.e. it must repeat the UPDATE's WHERE predicate.
// Passing a *weaker* predicate (say, only (id, user_id) for an UPDATE that also
// filters on a version column) turns a genuine conflict back into a success.
//
// The cost is paid only on the ambiguous branch: a row that actually changed
// returns matched=true straight from the update, with no follow-up query.
//
// It takes the *sql.DB rather than a transaction so call sites keep their
// existing single-statement semantics; the follow-up SELECT runs after the
// UPDATE commits, so a row deleted in between reports "no row matched", which is
// the truthful answer for a write that no longer has anything to write.
func updateScoped(
	ctx context.Context,
	db *sql.DB,
	query string,
	args []any,
	matchedQuery string,
	matchedArgs []any,
) (bool, error) {
	res, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		// Returned raw: call sites distinguish unique-constraint violations from
		// transport errors and wrap them differently.
		return false, err
	}

	changed, err := res.RowsAffected()
	if err != nil {
		// A count we could not read is an error, never a success.
		return false, fmt.Errorf("rows affected: %w", err)
	}
	if changed > 0 {
		return true, nil
	}

	var matched bool
	if err := db.QueryRowContext(ctx, matchedQuery, matchedArgs...).Scan(&matched); err != nil {
		return false, fmt.Errorf("check row matched: %w", err)
	}
	return matched, nil
}
