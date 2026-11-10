package repository

import (
	"unicode"
)

// MinIndexedTerm is the shortest search term the full-text index is trusted to
// answer for, in characters of a token.
//
// It is 3 because that is InnoDB's default innodb_ft_min_token_size: a token of
// one or two characters is not in the index at all, so MATCH ... AGAINST for
// such a term is a valid query that matches nothing. Nothing errors and nothing
// logs, so the user cannot tell "no such note" from "no such term", while SQLite
// — whose FTS5 porter tokenizer has no such floor — returns the notes it has.
// The same query answered differently by the two backends is the whole defect
// (ticket 619).
//
// The number is the application's, not the server's. innodb_ft_min_token_size
// is a server variable, and this project is public and self-hosted: the app
// cannot assume it may reconfigure a MariaDB it merely connects to, and changing
// it rebuilds every FULLTEXT index, which is deployment work. So the floor is
// fixed here and applied on both backends: a term at or above it goes through
// the index as before, a term below it is answered by a scan instead, on
// whichever engine is running. The residual is honest and documented — a
// deployment that raises the server variable above 3 gets the old silent zeros
// back for terms of exactly 3 characters, because no client protocol reports the
// setting.
const MinIndexedTerm = 3

// SplitSearchTerms divides a query's terms into the ones the full-text index can
// answer for and the ones it cannot, preserving the order within each group.
//
// The split is computed from the term alone, so it is identical on both engines
// and cannot drift with a server setting. It is not a second query splitter:
// searchtext.Terms still decides what the words are, this only measures them.
func SplitSearchTerms(terms []string) (indexed, scanned []string) {
	indexed = make([]string, 0, len(terms))
	scanned = make([]string, 0, len(terms))
	for _, term := range terms {
		if termIndexable(term) {
			indexed = append(indexed, term)
			continue
		}
		scanned = append(scanned, term)
	}
	return indexed, scanned
}

// termIndexable reports whether the index can answer for term, which it can only
// do if every token the term will be searched as is at least MinIndexedTerm
// characters long.
//
// Terms are measured token by token rather than by their raw length because both
// tokenizers break on punctuation: "go." is three characters but the indexed
// token is "go", and "(ip)" is four characters but the token is "ip". A term
// with any short token in it is therefore scanned as a whole, which can only
// find more than the index would, never less.
func termIndexable(term string) bool {
	tokens := termTokens(term)
	if len(tokens) == 0 {
		// Nothing a tokenizer would index at all — not "the index has it".
		return false
	}
	for _, token := range tokens {
		if len(token) < MinIndexedTerm {
			return false
		}
	}
	return true
}

// termTokens returns the maximal runs of letters and digits in term: the
// fragments a word tokenizer indexes, with the punctuation it breaks on removed.
// Lengths are counted in runes, so three multi-byte letters are three characters,
// which is how both engines count them.
func termTokens(term string) []string {
	var tokens []string
	var current []rune
	for _, r := range term {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current = append(current, r)
			continue
		}
		if len(current) > 0 {
			tokens = append(tokens, string(current))
			current = current[:0]
		}
	}
	if len(current) > 0 {
		tokens = append(tokens, string(current))
	}
	return tokens
}

// ShortTermPredicate returns the SQL predicate that matches a note whose title or
// content contains term literally, and the bind arguments it expects, in order.
// Both dialects share the text because it is identical in both; "n" is the alias
// both of them give the notes table.
//
// It exists for the terms the full-text index cannot answer for (see
// MinIndexedTerm), and it is a scan, not an optimisation: a LIKE with a leading
// wildcard cannot use an index, so it costs two substring comparisons per row of
// the caller's notes. The row set is bounded by the caller — the predicate is
// always ANDed into a query that also carries user_id = ? — and a short term is
// the only thing that pays for it. That cost is the price of the term being
// findable at all, and it is stated here rather than hidden behind the word
// "fallback".
//
// '%' and '_' in the term are escaped to literals, as is the escape character
// itself, so a query for "50%" cannot match "500" and a query for "a_b" cannot
// match "axb". The escape character is likeEscape, shared with the subtree
// predicate in pathscope.go: '!' needs no quoting in either dialect, whereas a
// backslash has to be doubled in a MySQL string literal and means two under
// NO_BACKSLASH_ESCAPES, so the same SQL text would decode differently per
// engine.
//
// The term is matched as the user typed it, punctuation included: "go" matches a
// note containing "go" wherever it sits in a word, and "go," matches the literal
// "go," rather than the word "go". That is narrower than the index's word matching
// for a term ending in punctuation, and it is deliberate — one rule, the same on
// both engines, is worth more here than guessing what the punctuation meant.
func ShortTermPredicate(term string) (string, []any) {
	pattern := "%" + likeEscaper.Replace(term) + "%"
	predicate := "(n.title LIKE ? ESCAPE '" + likeEscape + "'" +
		" OR n.content LIKE ? ESCAPE '" + likeEscape + "')"
	return predicate, []any{pattern, pattern}
}
