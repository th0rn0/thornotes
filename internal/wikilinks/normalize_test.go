package wikilinks

import "testing"

func TestNormalizeKey_EntityDecode(t *testing.T) {
	if got := NormalizeKey("A &amp; B"); got != "a & b" {
		t.Fatalf("NormalizeKey(%q) = %q, want %q", "A &amp; B", got, "a & b")
	}
}

func TestNormalizeKey_NFC(t *testing.T) {
	// 'e' + combining acute (U+0301) must NFC-combine to é (U+00E9).
	if got := NormalizeKey("e\u0301"); got != "\u00e9" {
		t.Fatalf("NFC combine: got %q want %q", got, "\u00e9")
	}
}

func TestNormalizeKey_DashFold(t *testing.T) {
	variants := []rune{'\u2012', '\u2013', '\u2014', '\u2015', '\u2212', '\ufe58', '\ufe63', '\uff0d'}
	for _, d := range variants {
		if got := NormalizeKey("a" + string(d) + "b"); got != "a-b" {
			t.Fatalf("dash U+%04X folded to %q, want %q", d, got, "a-b")
		}
	}
}

func TestNormalizeKey_CaseFold(t *testing.T) {
	if got := NormalizeKey("Hello WORLD"); got != "hello world" {
		t.Fatalf("got %q", got)
	}
	// Full Unicode case fold: ß -> ss (matches Python str.casefold()).
	if got := NormalizeKey("Straße"); got != "strasse" {
		t.Fatalf("full case fold: got %q, want %q", got, "strasse")
	}
}

func TestNormalizeKey_Whitespace(t *testing.T) {
	if got := NormalizeKey("  foo\t\n bar  "); got != "foo bar" {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeKey_Combined(t *testing.T) {
	if got := NormalizeKey("  Foo &amp; Bar — baz  "); got != "foo & bar - baz" {
		t.Fatalf("got %q", got)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Session 2026-04-21 — v1.5.11.2 ship (find_folders)": "session-2026-04-21-v1-5-11-2-ship-find-folders",
		"Hello, World!": "hello-world",
		"  Foo  Bar ":   "foo-bar",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Fatalf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEdgeKey(t *testing.T) {
	want := "bd445c462b7eebbc242e27f08c7d981e97fb28faf17ea73862bf95119b60e0f7" // sha256("overview")
	if got := EdgeKey("Overview"); got != want {
		t.Fatalf("EdgeKey(Overview) = %q, want %q", got, want)
	}
	if got := EdgeKey("anything"); len(got) != 64 {
		t.Fatalf("edge key must be 64 hex chars, got %d", len(got))
	}
	// Normalisation differences must not change the key.
	if EdgeKey(" overview ") != EdgeKey("Overview") {
		t.Fatalf("edge key must be normalisation-invariant")
	}
}
