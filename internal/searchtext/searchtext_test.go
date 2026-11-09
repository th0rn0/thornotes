package searchtext

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
