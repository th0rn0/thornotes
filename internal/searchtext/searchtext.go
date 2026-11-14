// Package searchtext holds every part of note search that must behave the same
// on both storage backends.
//
// # The search contract
//
// Thornotes runs on two engines with different query languages and different
// relevance maths: SQLite with FTS5 (development, single-file deployments) and
// MySQL/MariaDB with an InnoDB FULLTEXT index (production). Everything about a
// search that is not scoring is fixed here, so the two repositories cannot
// drift apart again:
//
//  1. Tags are AND, always. A note is returned only if it carries every
//     requested tag. Each tag is a separate correlated EXISTS over the note's
//     tags array; a single unnest of the array can only ever hold one tag value
//     at a time, so a conjunction across unnest rows is unsatisfiable by
//     construction (that was ticket 452). Duplicate tags are harmless — the
//     same predicate twice is the same predicate. Tag matching is exact and
//     case-sensitive: "%" and "_" are values, not patterns, and a request for
//     "Home" does not match a note tagged "home". Exact matching is what makes
//     the two engines agree — MySQL's default collation is case-insensitive
//     while SQLite's "=" is binary, and neither engine can fold Unicode case in
//     a way the other matches, so case folding would be a contract we could not
//     keep.
//
//  2. Text terms are AND, always. A query of two words returns only notes that
//     contain both. Terms is the single splitter ("which words did the user
//     type"), and FTSAnd/BooleanAnd turn that one list into each engine's
//     syntax — FTS5 joins with AND, MySQL boolean mode ANDs by prefixing every
//     term with "+". A term the dialect cannot express is dropped rather than
//     searched for; the engines differ on what that means (FTS5 accepts almost
//     any quoted literal, MySQL strips boolean operators), so a query made only
//     of such terms returns no results rather than all of them.
//
//  3. The snippet is computed once, in Go, by Snippet — never by SQL. It is a
//     bounded excerpt of the note's content centred on the first occurrence of
//     any search term, with every occurrence inside the excerpt wrapped in
//     literal <mark>...</mark> markers. The markers are deliberately not
//     escaped here: the UI escapes the snippet itself when it renders it.
//
//  4. Ordering and limits are fixed: relevance descending, then title, then id,
//     at most 50 rows. The two engines score differently, so only the
//     tie-breakers can be identical; both repositories order by title with a
//     binary comparison (MySQL spells it COLLATE utf8mb4_bin) and then by id.
//
//  5. Term length is the application's rule, not the engine's. A full-text index
//     can decline to hold a token at all — InnoDB's innodb_ft_min_token_size
//     drops tokens shorter than three characters, and a MATCH for one of them is
//     a valid query that matches nothing — so a term the index cannot answer is
//     matched by a scan of the caller's own notes instead, on both backends, with
//     the same floor (repository.MinIndexedTerm) and the same escaped LIKE
//     (repository.ShortTermPredicate). That makes the two engines return the same
//     rows for every term length. It does not make them identical: stemming is
//     the tokenizer's, so SQLite's porter stemmer finds "meetings" for "meeting"
//     and MySQL does not. Closing that needs terms derived in Go and stored in a
//     table of their own — a migration, tracked separately — and until then a
//     query that depends on stemming still divides the two backends.
//
//  6. A stopword is dropped from the query, not required. InnoDB's FULLTEXT index
//     refuses to hold the words in its stopword list, so a MATCH for one of them
//     matches nothing however many notes contain it — and boolean mode makes
//     every term mandatory, so one such word empties the whole result set (measured
//     on MariaDB 11.8.9, reproduced through this package's repositories). WithoutStopwords
//     removes them from the term list on both backends:
//     dropping a word cannot narrow a query, and dropping it on one engine only
//     would put the two backends back in disagreement in the other direction. See
//     Stopwords for the list and for why it must not be edited.
package searchtext

import (
	"strings"
	"unicode"
)

// Snippet bounds. The excerpt body is at most SnippetMaxLen runes and starts at
// most SnippetLead runes before the first match, so the match is always visible.
const (
	// SnippetMaxLen is the maximum length of the excerpt, in runes, excluding
	// the <mark> markers and the ellipses.
	SnippetMaxLen = 200
	// SnippetLead is how many runes of context precede the first match.
	SnippetLead = 40
)

// Snippet markers and the trimming marker. The ellipsis is a single rune so a
// trimmed excerpt costs one character, not three.
const (
	markOpen  = "<mark>"
	markClose = "</mark>"
	ellipsis  = "…"
)

// Terms splits a user query into its search terms. It is the only place the
// query is tokenized, so both dialects search for exactly the same words.
func Terms(query string) []string {
	return strings.Fields(query)
}

// Stopwords are the words the query drops instead of searching for: InnoDB's
// default stopword list, filtered to the entries long enough to reach the
// full-text index.
//
// # What it is
//
// InnoDB never stores a stopword in a FULLTEXT index, so `MATCH ... AGAINST` for
// one matches nothing at all — no error, no row, exactly like a word that appears
// in no note. MariaDB 11.8.9's INNODB_FT_DEFAULT_STOPWORD holds 36 rows ("the"
// twice, so 35 distinct words); 18 of them are at least three characters long and
// therefore above innodb_ft_min_token_size, so they would otherwise be sent to
// the index as ordinary terms. Those 18 are listed here.
//
// That single word is enough to empty a whole query. BooleanAnd prefixes *every*
// term with "+", which boolean mode reads as "this term is mandatory", so a
// query of "the quick" asks for notes that contain "the" — a term the index
// cannot answer for — and returns zero rows however many notes say "the quick
// brown fox". Measured on MariaDB 11.8.9: `+the +quick` 0 rows, `the quick` 2
// rows, `+quick` 2 rows. The blast radius is not the word, it is every query a
// person types: "who owns", "the meeting", "when will they".
//
// The entries below the index floor ("a", "of", "to", "in", "is", "at", "an",
// "as", "be", "by", "de", "en", "i", "it", "la", "on", "or") are deliberately
// absent. They never reach the index — repository.MinIndexedTerm routes them to
// the LIKE scan, which finds them as substrings of the caller's notes — so they
// are not the defect, and dropping them would take away a search that works.
//
// # Why it must not be edited to taste
//
// This is a fact about someone else's database, not a tuning knob: it is InnoDB's
// list, on both backends, because the two engines must answer the same query the
// same way. Shortening it puts the zero-row defect back for whichever word is
// removed, silently — a query returns nothing, which is indistinguishable from
// "no such note". Lengthening it takes a word that is findable today and makes it
// unsearchable, which is a behaviour change, not a fix. Either direction needs
// the conformance table in internal/repository updated in the same commit, and a
// note in docs/how-to.md.
//
// A deployment that points innodb_ft_server_stopword_table at a list of its own
// diverges again: no client protocol reports that setting, so the app cannot
// know. That residual is accepted and stated rather than guessed at, the same
// shape as MinIndexedTerm's on innodb_ft_min_token_size.
var Stopwords = []string{
	"about", "are", "com", "for", "from",
	"how", "that", "the", "this", "und",
	"was", "what", "when", "where", "who",
	"will", "with", "www",
}

// stopwordSet is Stopwords as a lookup table, built once at start-up.
var stopwordSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(Stopwords))
	for _, word := range Stopwords {
		set[word] = struct{}{}
	}
	return set
}()

// WithoutStopwords returns terms with every stopword removed, preserving the
// order of the terms it keeps. It is applied by both repositories immediately
// after Terms and before the terms are split by indexability, so the word list
// the two engines search for is decided here, once.
//
// A term is dropped when the only token a tokenizer would index is a stopword,
// matched case-insensitively: "the", "The", "the," and "(the)" all drop, because
// the same index that refuses "the" refuses "The" and reads "the," as the word
// "the". Anything else is kept. "theatre" and "without" are not the refused word,
// and a phrase such as "the-quick" is two tokens of which only one is a
// stopword — dropping the whole phrase would widen the query, and keeping it
// cannot.
//
// Dropping is the whole fix. The alternative — sending the term without its "+",
// so it is optional — would leave MySQL's relevance score depending on a word the
// index does not hold, and would have meant a second dialect-specific rule in
// BooleanAnd. Removing the word says what the user meant: a stopword does not
// narrow a search, so it does not narrow this one.
func WithoutStopwords(terms []string) []string {
	kept := make([]string, 0, len(terms))
	for _, term := range terms {
		if isStopword(term) {
			continue
		}
		kept = append(kept, term)
	}
	return kept
}

// isStopword reports whether term is a single token that Stopwords holds,
// compared case-insensitively.
func isStopword(term string) bool {
	tokens := TermTokens(term)
	if len(tokens) != 1 {
		return false
	}
	_, found := stopwordSet[strings.ToLower(tokens[0])]
	return found
}

// TermTokens returns the maximal runs of letters and digits in term: the
// fragments a word tokenizer indexes, with the punctuation it breaks on removed.
// Lengths are counted in runes, so three multi-byte letters are three characters,
// which is how both engines count them.
//
// It is exported because two rules in two packages must agree on what a token is:
// isStopword above, and repository.MinIndexedTerm's floor, which measures a term
// token by token ("go." is three characters but the token is "go").
func TermTokens(term string) []string {
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

// FTSAnd renders terms as an SQLite FTS5 MATCH expression that requires every
// term. Each term is double-quoted so FTS5 treats its contents as literal text
// rather than as query syntax.
func FTSAnd(terms []string) string {
	parts := make([]string, 0, len(terms))
	for _, t := range terms {
		if t == "" {
			continue
		}
		parts = append(parts, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
	}
	return strings.Join(parts, " AND ")
}

// BooleanAnd renders terms as a MySQL FULLTEXT boolean-mode expression that
// requires every term: boolean mode ANDs space-separated terms, and the "+"
// prefix makes each one mandatory. Characters that are boolean-mode operators
// are stripped so a term can never be reinterpreted as syntax.
func BooleanAnd(terms []string) string {
	parts := make([]string, 0, len(terms))
	for _, t := range terms {
		t = strings.NewReplacer(
			`+`, ``, `-`, ``, `~`, ``, `*`, ``, `(`, `)`, `"`, ``, `<`, ``, `>`, ``,
		).Replace(t)
		if t == "" {
			continue
		}
		parts = append(parts, `+"`+t+`"`)
	}
	return strings.Join(parts, " ")
}

// Snippet returns a bounded excerpt of content centred on the first occurrence
// of any term, with each occurrence inside the excerpt wrapped in literal
// <mark> markers.
//
// The excerpt body is at most SnippetMaxLen runes. When the first match is more
// than SnippetLead runes in, the excerpt starts SnippetLead runes before it and
// an ellipsis is prepended; when the note continues past the excerpt, an
// ellipsis is appended. Both edges are moved to a word boundary when one is
// available, so an excerpt does not begin or end mid-word.
//
// Matching is a case-insensitive substring comparison over runes — the same
// notion of "contains" for both engines, and never a byte offset, so multi-byte
// text cannot be cut in half. If no term occurs in content (a note matched on
// its title alone, say) the excerpt is the opening of the content, unmarked.
func Snippet(content string, terms []string) string {
	runes := []rune(content)
	if len(runes) == 0 {
		return ""
	}
	lowered := lowerRunes(runes)

	first, matchLen := firstMatch(lowered, terms)

	start := 0
	if first > SnippetLead {
		start = first - SnippetLead
	}
	end := len(runes)
	if start+SnippetMaxLen < end {
		end = start + SnippetMaxLen
	}

	// Do not cut a word in half at either edge. Snapping may only move the
	// edges outwards from the match, never past it.
	if start > 0 {
		if i := nextWordStart(lowered, start); i > 0 && i <= first {
			start = i
		}
	}
	if end < len(runes) {
		if i := lastSpaceBefore(lowered, end); i >= first+matchLen && i > start {
			end = i
		}
	}

	var b strings.Builder
	if start > 0 {
		b.WriteString(ellipsis)
	}
	b.WriteString(markAll(runes[start:end], lowered[start:end], terms))
	if end < len(runes) {
		b.WriteString(ellipsis)
	}
	return b.String()
}

// firstMatch returns the index and rune length of the earliest occurrence of
// any term in lowered, or (-1, 0) when no term occurs.
func firstMatch(lowered []rune, terms []string) (int, int) {
	first, matchLen := -1, 0
	for _, term := range terms {
		needle := lowerRunes([]rune(term))
		if len(needle) == 0 {
			continue
		}
		if i := indexRunes(lowered, needle); i >= 0 && (first < 0 || i < first) {
			first, matchLen = i, len(needle)
		}
	}
	return first, matchLen
}

// markAll copies runes, wrapping each occurrence of any term in <mark> markers.
// Terms are tried in the order given, so the output is a function of the input
// alone; a match consumes its runes, so overlapping occurrences are marked once.
func markAll(runes, lowered []rune, terms []string) string {
	needles := make([][]rune, 0, len(terms))
	for _, term := range terms {
		if n := lowerRunes([]rune(term)); len(n) > 0 {
			needles = append(needles, n)
		}
	}

	var b strings.Builder
	b.Grow(len(runes))
	for i := 0; i < len(runes); {
		matched := 0
		for _, needle := range needles {
			if i+len(needle) <= len(runes) && equalRunes(lowered[i:i+len(needle)], needle) {
				matched = len(needle)
				break
			}
		}
		if matched == 0 {
			b.WriteRune(runes[i])
			i++
			continue
		}
		b.WriteString(markOpen)
		b.WriteString(string(runes[i : i+matched]))
		b.WriteString(markClose)
		i += matched
	}
	return b.String()
}

// nextWordStart returns the index just after the next space at or after i, or
// -1 when the text from i to the end holds no space.
func nextWordStart(lowered []rune, i int) int {
	for ; i < len(lowered); i++ {
		if unicode.IsSpace(lowered[i]) {
			return i + 1
		}
	}
	return -1
}

// lastSpaceBefore returns the index of the last space before i, or -1 when
// there is none.
func lastSpaceBefore(lowered []rune, i int) int {
	for j := i - 1; j >= 0; j-- {
		if unicode.IsSpace(lowered[j]) {
			return j
		}
	}
	return -1
}

// indexRunes returns the first index of needle in hay, or -1.
func indexRunes(hay, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(hay) {
		return -1
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if equalRunes(hay[i:i+len(needle)], needle) {
			return i
		}
	}
	return -1
}

func equalRunes(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// lowerRunes lower-cases runes one for one, so offsets into the result are
// offsets into the original text. (strings.ToLower can change length — "İ"
// becomes two runes — which would misalign every window.)
func lowerRunes(runes []rune) []rune {
	out := make([]rune, len(runes))
	for i, r := range runes {
		out[i] = unicode.ToLower(r)
	}
	return out
}
