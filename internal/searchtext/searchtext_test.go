package searchtext

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTerms(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"empty", "", nil},
		{"spaces", "   \t\n ", nil},
		{"one", "boiler", []string{"boiler"}},
		{"two", "boiler service", []string{"boiler", "service"}},
		{"collapses whitespace", "  boiler \t service  ", []string{"boiler", "service"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Terms(tc.query)
			if len(tc.want) == 0 {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// The stopword list is pinned word for word, because it is a claim about someone
// else's database rather than a preference: InnoDB's default stopword list holds
// 36 rows (35 distinct — MariaDB 11.8.9 lists "the" twice) and exactly these 18
// are at least three characters long, so they are the only ones that would reach
// the full-text index and be refused by it. Editing Stopwords in either direction
// without editing this list must fail the build: shortening it puts the zero-row
// defect back for whichever word is removed, and lengthening it makes a word that
// is findable today unsearchable.
func TestStopwords_FrozenList(t *testing.T) {
	want := []string{
		"about", "are", "com", "for", "from",
		"how", "that", "the", "this", "und",
		"was", "what", "when", "where", "who",
		"will", "with", "www",
	}

	require.Equal(t, want, Stopwords, "InnoDB's default stopwords at or above the index floor")
	require.Len(t, Stopwords, 18)
}

// TestWithoutStopwords is the drop rule itself: which words go, which stay, and
// the fact that removing one never reorders or rewrites the rest.
func TestWithoutStopwords(t *testing.T) {
	tests := []struct {
		name  string
		terms []string
		want  []string
	}{
		{
			name:  "empty query",
			terms: nil,
		},
		{
			name:  "no stopword, nothing to do",
			terms: []string{"boiler", "service"},
			want:  []string{"boiler", "service"},
		},
		{
			name:  "a stopword before the term",
			terms: []string{"the", "quick"},
			want:  []string{"quick"},
		},
		{
			name:  "a stopword after the term",
			terms: []string{"quick", "the"},
			want:  []string{"quick"},
		},
		{
			name:  "a stopword between two terms",
			terms: []string{"quick", "the", "brown"},
			want:  []string{"quick", "brown"},
		},
		{
			// The list is matched on the token, so the case it is typed in
			// cannot save a stopword — "The" is the same word to the index.
			name:  "a stopword in capitals",
			terms: []string{"The", "quick"},
			want:  []string{"quick"},
		},
		{
			name:  "a stopword in mixed case",
			terms: []string{"WhEn", "quick"},
			want:  []string{"quick"},
		},
		{
			// ... and neither can the punctuation a person types around it:
			// "the," is the token "the" to every tokenizer involved.
			name:  "a stopword with punctuation",
			terms: []string{"the,", "quick."},
			want:  []string{"quick."},
		},
		{
			name:  "a stopword in brackets",
			terms: []string{"(the)", "quick"},
			want:  []string{"quick"},
		},
		{
			// Keep what is not the refused word. "theatre" and "without" contain
			// a stopword but are not one, and dropping them would narrow the
			// query to nothing — the opposite failure.
			name:  "words that merely contain a stopword are kept",
			terms: []string{"theatre", "without", "within", "theme"},
			want:  []string{"theatre", "without", "within", "theme"},
		},
		{
			// A multi-token term is kept whole: it is a phrase, not the word the
			// index refuses, and dropping it would widen the query.
			name:  "a hyphenated term is a phrase, not a stopword",
			terms: []string{"the-quick"},
			want:  []string{"the-quick"},
		},
		{
			// Below the index floor the word never reaches the index — 619's
			// LIKE scan finds it — so it is not this rule's business. Dropping
			// these would take away a search that works today.
			name:  "short stopwords are left to the short-term scan",
			terms: []string{"a", "of", "to", "in", "is", "at", "an", "as", "be", "by", "de", "en", "i", "it", "la", "on", "or"},
			want:  []string{"a", "of", "to", "in", "is", "at", "an", "as", "be", "by", "de", "en", "i", "it", "la", "on", "or"},
		},
		{
			name:  "every term a stopword leaves none",
			terms: []string{"the", "what", "when"},
		},
		{
			name:  "order is preserved",
			terms: []string{"alpha", "the", "beta", "who", "gamma"},
			want:  []string{"alpha", "beta", "gamma"},
		},
		{
			name:  "duplicates of a kept term are kept",
			terms: []string{"quick", "the", "quick"},
			want:  []string{"quick", "quick"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := WithoutStopwords(tc.terms)
			if len(tc.want) == 0 {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestWithoutStopwords_EveryListedWordDrops walks the list itself, so a word added
// to Stopwords is dropped without the table above being extended by hand.
func TestWithoutStopwords_EveryListedWordDrops(t *testing.T) {
	for _, word := range Stopwords {
		for _, variant := range []string{word, strings.ToUpper(word), strings.ToUpper(word[:1]) + word[1:], word + ","} {
			assert.True(t, isStopword(variant), "%q must be recognised as the stopword %q", variant, word)
		}
		assert.Equal(t, []string{"quick"}, WithoutStopwords([]string{word, "quick"}), "%q", word)
	}
}

// TestWithoutStopwords_DoesNotMutateTheInput guards the callers: the term list is
// also handed to searchtext.Snippet after this, and both repositories pass the
// same slice they were given.
func TestWithoutStopwords_DoesNotMutateTheInput(t *testing.T) {
	terms := []string{"the", "quick", "who"}

	kept := WithoutStopwords(terms)

	assert.Equal(t, []string{"the", "quick", "who"}, terms)
	assert.Equal(t, []string{"quick"}, kept)
}

// TestTermTokens pins the shared tokenizer, which two rules measure terms with:
// this package's stopword match and repository.MinIndexedTerm's floor.
func TestTermTokens(t *testing.T) {
	tests := []struct {
		term string
		want []string
	}{
		{"", nil},
		{"!!!", nil},
		{"go", []string{"go"}},
		{"go.", []string{"go"}},
		{"(ip)", []string{"ip"}},
		{"the,", []string{"the"}},
		{"the-quick", []string{"the", "quick"}},
		{"50%", []string{"50"}},
		{"a_b", []string{"a", "b"}},
		{"wifi2", []string{"wifi2"}},
		{"äöü", []string{"äöü"}},
	}

	for _, tc := range tests {
		t.Run(tc.term, func(t *testing.T) {
			assert.Equal(t, tc.want, TermTokens(tc.term))
		})
	}
}

func TestFTSAnd(t *testing.T) {
	tests := []struct {
		name  string
		terms []string
		want  string
	}{
		{"empty", nil, ""},
		{"one", []string{"boiler"}, `"boiler"`},
		// AND, not OR: ticket 452 fixed the text terms to the same rule as tags.
		{"two", []string{"boiler", "service"}, `"boiler" AND "service"`},
		{"quotes are escaped", []string{`quo"te`}, `"quo""te"`},
		{"fts syntax is quoted", []string{"*", "a:b"}, `"*" AND "a:b"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, FTSAnd(tc.terms))
		})
	}
}

func TestBooleanAnd(t *testing.T) {
	tests := []struct {
		name  string
		terms []string
		want  string
	}{
		{"empty", nil, ""},
		{"one", []string{"boiler"}, `+"boiler"`},
		// Boolean mode ANDs space-separated terms; every term is mandatory.
		{"two", []string{"boiler", "service"}, `+"boiler" +"service"`},
		{"operators are stripped", []string{"+boiler", "-service"}, `+"boiler" +"service"`},
		{"pure operators are dropped", []string{"+", "-", "*"}, ""},
		{"kept terms survive a dropped neighbour", []string{"+", "boiler"}, `+"boiler"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, BooleanAnd(tc.terms))
		})
	}
}

// The snippet cases are pinned as literal expectations: this table is the
// specification of the excerpt, not a description of the implementation.
func TestSnippet(t *testing.T) {
	const (
		startContent  = "Snippetstart marks the very first word of this note, so the excerpt needs no leading ellipsis at all."
		middleContent = "The first sentence of this note is plain filler text before the match. middleword appears here and the note continues afterwards with more plain filler text."
		tailContent   = "The opening of this note is a long run of plain filler words used only to push the interesting part of the text towards the end of the note so that a bounded excerpt has to be trimmed somewhere. tailword"
	)

	tests := []struct {
		name    string
		content string
		terms   []string
		want    string
	}{
		{
			name:    "match at the start is not trimmed",
			content: startContent,
			terms:   []string{"snippetstart"},
			want:    "<mark>Snippetstart</mark> marks the very first word of this note, so the excerpt needs no leading ellipsis at all.",
		},
		{
			name:    "match in the middle gets a lead and no trailing ellipsis",
			content: middleContent,
			terms:   []string{"middleword"},
			want:    "…is plain filler text before the match. <mark>middleword</mark> appears here and the note continues afterwards with more plain filler text.",
		},
		{
			name:    "match near the end keeps the whole tail",
			content: tailContent,
			terms:   []string{"tailword"},
			want:    "…excerpt has to be trimmed somewhere. <mark>tailword</mark>",
		},
		{
			name:    "no match in content yields the opening, unmarked",
			content: "Nothing relevant in the prose here.",
			terms:   []string{"titlesearch"},
			want:    "Nothing relevant in the prose here.",
		},
		{
			name:    "empty content yields empty snippet",
			content: "",
			terms:   []string{"anything"},
			want:    "",
		},
		{
			name:    "no terms yields the opening",
			content: "Short note.",
			terms:   nil,
			want:    "Short note.",
		},
		{
			name:    "every term is marked",
			content: "alpha beta appears first, then gamma appears later in the same note.",
			terms:   []string{"beta", "gamma"},
			want:    "alpha <mark>beta</mark> appears first, then <mark>gamma</mark> appears later in the same note.",
		},
		{
			name:    "repeated occurrences are all marked",
			content: "boiler now, boiler again, and boiler once more.",
			terms:   []string{"boiler"},
			want:    "<mark>boiler</mark> now, <mark>boiler</mark> again, and <mark>boiler</mark> once more.",
		},
		{
			name:    "matching is case-insensitive and keeps the original casing",
			content: "The Boiler needs a service.",
			terms:   []string{"boiler"},
			want:    "The <mark>Boiler</mark> needs a service.",
		},
		{
			// A byte-based window would cut inside a multi-byte rune here.
			name:    "unicode is measured in runes",
			content: strings.Repeat("äöü ", 30) + "Wasser" + strings.Repeat(" äöü", 40),
			terms:   []string{"wasser"},
			want: "…" + strings.Repeat("äöü ", 9) + "<mark>Wasser</mark>" +
				strings.Repeat(" äöü", 38) + "…",
		},
		{
			name:    "a very long note is bounded on both sides",
			content: strings.Repeat("plain filler words to make this note long enough. ", 30) + "longword" + strings.Repeat(" and more filler words after the match", 20),
			terms:   []string{"longword"},
			want: "…words to make this note long enough. <mark>longword</mark>" +
				strings.Repeat(" and more filler words after the match", 3) +
				" and more filler words after the…",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Snippet(tc.content, tc.terms)
			assert.Equal(t, tc.want, got)

			// The bound holds for every input: the excerpt body (markers and
			// ellipses excluded) never exceeds SnippetMaxLen runes.
			body := strings.TrimSuffix(strings.TrimPrefix(got, "…"), "…")
			body = strings.ReplaceAll(strings.ReplaceAll(body, markOpen, ""), markClose, "")
			assert.LessOrEqual(t, len([]rune(body)), SnippetMaxLen)
		})
	}
}
