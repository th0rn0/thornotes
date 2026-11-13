package wikilinks

import (
	"strings"
	"testing"
)

// renameTarget rewrites any target equal to "old title" (normalized) and
// reports what it did, the way the notes service drives it.
func renameTarget(to string) (func(string) bool, func(string) string) {
	return func(target string) bool { return NormalizeKey(target) == "old title" },
		func(string) string { return to }
}
func TestReplaceTargets_PreservesAnchorAndLinkText(t *testing.T) {
	match, rewrite := renameTarget("New Title")
	cases := []struct {
		name string
		in   string
		want string
		n    int
	}{
		{"bare link", "see [[Old Title]] here", "see [[New Title]] here", 1},
		{"anchor survives", "see [[Old Title#Trust boundaries]]", "see [[New Title#Trust boundaries]]", 1},
		{"display text survives", "see [[Old Title|the map]]", "see [[New Title|the map]]", 1},
		{"anchor and display text survive", "see [[Old Title#Why|the reason]]", "see [[New Title#Why|the reason]]", 1},
		{"author spacing survives", "see [[  Old Title  ]]", "see [[  New Title  ]]", 1},
		{"normalised match (case and dashes)", "see [[OLD  TITLE]]", "see [[New Title]]", 1},
		{"several links", "[[Old Title]] and [[Old Title|two]]", "[[New Title]] and [[New Title|two]]", 2},
		{"unrelated target untouched", "see [[Other Page]]", "see [[Other Page]]", 0},
		{"heading-only link has no target", "see [[#Trust boundaries]]", "see [[#Trust boundaries]]", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n := ReplaceTargets(tc.in, match, rewrite)
			if got != tc.want || n != tc.n {
				t.Fatalf("ReplaceTargets(%q) = %q, %d; want %q, %d", tc.in, got, n, tc.want, tc.n)
			}
		})
	}
}

func TestReplaceTargets_LeavesCodeAlone(t *testing.T) {
	match, rewrite := renameTarget("New Title")

	in := "# Doc\n\n" +
		"prose [[Old Title]]\n" +
		"\n" +
		"```markdown\n" +
		"[[Old Title]]\n" +
		"```\n" +
		"\n" +
		"~~~\n" +
		"[[Old Title]]\n" +
		"~~~\n" +
		"\n" +
		"inline `[[Old Title]]` and another [[Old Title]]\n"

	want := "# Doc\n\n" +
		"prose [[New Title]]\n" +
		"\n" +
		"```markdown\n" +
		"[[Old Title]]\n" +
		"```\n" +
		"\n" +
		"~~~\n" +
		"[[Old Title]]\n" +
		"~~~\n" +
		"\n" +
		"inline `[[Old Title]]` and another [[New Title]]\n"

	got, n := ReplaceTargets(in, match, rewrite)
	if got != want {
		t.Fatalf("code was rewritten:\n got: %q\nwant: %q", got, want)
	}
	if n != 2 {
		t.Fatalf("rewrote %d links, want 2 (the two in prose)", n)
	}
}

// TestReplaceTargets_ExtractionAgrees is the property that keeps the two halves
// of the package honest: every link ExtractLinks reports is one ReplaceTargets
// can rewrite, and nothing else is. If they disagreed, the index could point at
// a link the rename never moved (or vice versa).
func TestReplaceTargets_ExtractionAgrees(t *testing.T) {
	body := "a [[Old Title]] b `[[Old Title]]` c\n```\n[[Old Title]]\n```\nd [[Old Title|d]]\n"

	refs := ExtractLinks(body)
	if len(refs) != 2 {
		t.Fatalf("ExtractLinks found %d links, want 2", len(refs))
	}

	var seen []string
	_, n := ReplaceTargets(body, func(target string) bool {
		seen = append(seen, target)
		return true
	}, func(target string) string { return target + "!" })
	if n != len(refs) {
		t.Fatalf("ReplaceTargets rewrote %d links, ExtractLinks found %d", n, len(refs))
	}
	for i, ref := range refs {
		if seen[i] != ref.Target {
			t.Fatalf("target %d: rewrite saw %q, extract saw %q", i, seen[i], ref.Target)
		}
	}
}

func TestReplaceTargets_MultibyteOffsets(t *testing.T) {
	match, rewrite := renameTarget("New Title")
	// A byte offset computed on the masked copy is only valid if masking
	// preserves byte length, so any multibyte prefix would corrupt the splice.
	in := "héllo — wörld [[Old Title#à|ç]] ünd more"
	want := "héllo — wörld [[New Title#à|ç]] ünd more"

	got, n := ReplaceTargets(in, match, rewrite)
	if got != want || n != 1 {
		t.Fatalf("ReplaceTargets(%q) = %q, %d", in, got, n)
	}
}

func TestReplaceTargets_Idempotent(t *testing.T) {
	match, rewrite := renameTarget("New Title")
	in := "see [[Old Title#A|b]] and [[Other]]"

	once, n := ReplaceTargets(in, match, rewrite)
	if n != 1 {
		t.Fatalf("first pass rewrote %d links, want 1", n)
	}
	twice, n := ReplaceTargets(once, match, rewrite)
	if n != 0 || twice != once {
		t.Fatalf("second pass changed %q (n=%d)", twice, n)
	}
	if strings.Contains(twice, "Old Title") {
		t.Fatalf("old target survived: %q", twice)
	}
}
