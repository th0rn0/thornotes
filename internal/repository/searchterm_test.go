package repository_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/repository"
)

// TestMinIndexedTerm pins the number as well as the behaviour, because the number
// is a compatibility claim about someone else's database, not a tuning knob: 3 is
// InnoDB's default innodb_ft_min_token_size. Raising it makes terms at the old
// floor scan (correct, just slower); lowering it puts the zero-row bug back for
// every term between the two values, silently. Changing it is a contract change
// and needs the conformance table updated too.
func TestMinIndexedTerm(t *testing.T) {
	assert.Equal(t, 3, repository.MinIndexedTerm,
		"InnoDB's default innodb_ft_min_token_size; below this the index cannot answer")
}

// TestSplitSearchTerms pins the boundary: the index answers for a term only when
// every token in it is at least repository.MinIndexedTerm characters long, and
// everything else is scanned. The cases are written relative to the constant
// rather than in terms of "two characters", so raising the floor moves the
// boundary with the test instead of leaving the test asserting yesterday's value.
func TestSplitSearchTerms(t *testing.T) {
	atFloor := strings.Repeat("a", repository.MinIndexedTerm)
	belowFloor := strings.Repeat("a", repository.MinIndexedTerm-1)

	tests := []struct {
		name        string
		term        string
		wantIndexed bool
		reason      string
	}{
		{
			name: "a token exactly at the floor", term: atFloor, wantIndexed: true,
			reason: "the index holds a token of exactly the floor length",
		},
		{
			name: "digits are tokens too", term: strings.Repeat("7", repository.MinIndexedTerm),
			wantIndexed: true,
		},
		{
			name: "one character below the floor", term: belowFloor, wantIndexed: false,
			reason: "a token shorter than the floor is not in the index at all",
		},
		{
			name: "above the floor", term: atFloor + "a", wantIndexed: true,
		},
		{
			// Length is counted per token, not on the raw string: "go." is three
			// characters, but the token both engines would index is "go".
			name: "raw length at the floor, token below it", term: belowFloor + ".",
			wantIndexed: false,
			reason:      "only the tokens are indexed, and this one is too short",
		},
		{
			name: "surrounding punctuation", term: "(" + belowFloor + ")", wantIndexed: false,
		},
		{
			name: "no token at all", term: "!!!", wantIndexed: false,
			reason: "punctuation only — there is nothing for a tokenizer to index",
		},
		{
			name: "empty", term: "", wantIndexed: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			indexed, scanned := repository.SplitSearchTerms([]string{tc.term})

			if tc.wantIndexed {
				assert.Equal(t, []string{tc.term}, indexed, tc.reason)
				assert.Empty(t, scanned)
				return
			}
			assert.Equal(t, []string{tc.term}, scanned, tc.reason)
			assert.Empty(t, indexed)
		})
	}
}

// TestSplitSearchTerms_PreservesOrder guards the property the callers rely on: the
// split must not reorder the query, so each engine's expression is built from the
// words in the order they were typed.
func TestSplitSearchTerms_PreservesOrder(t *testing.T) {
	indexed, scanned := repository.SplitSearchTerms([]string{"alpha", "go", "beta", "ip", "gamma"})

	assert.Equal(t, []string{"alpha", "beta", "gamma"}, indexed)
	assert.Equal(t, []string{"go", "ip"}, scanned)

	indexed, scanned = repository.SplitSearchTerms(nil)
	assert.Empty(t, indexed, "no terms means neither list, not a nil to be appended to")
	assert.Empty(t, scanned)
}

// TestShortTermPredicate_EscapesLiterals pins what the predicate has to get right
// and a reader of the SQL cannot see: the scan covers title and content, and the
// LIKE metacharacters in a *query term* are characters. The tag cases in the
// conformance table cover the same characters in a tag, which is a different
// string and a different code path.
func TestShortTermPredicate_EscapesLiterals(t *testing.T) {
	tests := []struct {
		name    string
		term    string
		pattern string
	}{
		{name: "plain term", term: "go", pattern: "%go%"},
		{name: "percent is escaped", term: "50%", pattern: "%50!%%"},
		{name: "underscore is escaped", term: "a_b", pattern: "%a!_b%"},
		{name: "the escape character itself is escaped", term: "hi!", pattern: "%hi!!%"},
		{name: "backslash is an ordinary character", term: `c:\temp`, pattern: `%c:\temp%`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			predicate, args := repository.ShortTermPredicate(tc.term)

			assert.Contains(t, predicate, "n.title LIKE ? ESCAPE", "the title must be searched too")
			assert.Contains(t, predicate, "n.content LIKE ? ESCAPE", "the content must be searched too")
			require.Len(t, args, 2, "one bound pattern per column")
			assert.Equal(t, tc.pattern, args[0])
			assert.Equal(t, tc.pattern, args[1])
		})
	}
}
