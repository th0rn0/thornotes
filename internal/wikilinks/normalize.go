package wikilinks

import (
	"crypto/sha256"
	"encoding/hex"
	"html"
	"regexp"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// dashRunes are the dash variants folded to ASCII hyphen '-', per note 323 §4
// and the DASHES set in the reference implementation:
//
//	‒ (U+2012) figure dash      – (U+2013) en dash      — (U+2014) em dash
//	― (U+2015) horizontal bar   − (U+2212) minus        ﹘ (U+FE58) small em dash
//	﹣ (U+FE63) small hyphen-minus  － (U+FF0D) fullwidth hyphen-minus
var dashRunes = map[rune]struct{}{
	'\u2012': {},
	'\u2013': {},
	'\u2014': {},
	'\u2015': {},
	'\u2212': {},
	'\ufe58': {},
	'\ufe63': {},
	'\uff0d': {},
}

// nonAlnumRe matches runs of anything other than [a-z0-9], used by Slugify.
var nonAlnumRe = regexp.MustCompile(`[^a-z0-9]+`)

// NormalizeKey mirrors the reference normalize_key exactly:
//
//	entity-decode → NFC → fold dash variants to '-' → collapse whitespace → case fold
//
// It is applied to both sides of every title/slug comparison so client and
// server (and the export script) can never disagree on a key.
func NormalizeKey(s string) string {
	s = html.UnescapeString(s)
	s = norm.NFC.String(s)
	s = foldDashes(s)
	s = strings.Join(strings.Fields(s), " ")
	return cases.Fold().String(s)
}

func foldDashes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if _, ok := dashRunes[r]; ok {
			b.WriteByte('-')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Slugify applies NormalizeKey then replaces every run of non-[a-z0-9] with a
// single '-', trimming leading/trailing dashes. This is the "existing slug
// path" reused so a link like [[Session 2026-04-21 — v1]] matches the note
// whose slug is the hyphenated form.
func Slugify(s string) string {
	s = NormalizeKey(s)
	s = nonAlnumRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// EdgeKey is sha256(NormalizeKey(target)), hex-encoded. It is dialect-neutral,
// index-safe under InnoDB's 3072-byte limit, and answers "same target?" before
// any normalisation difference can leak in (note 406 decision 4, note 323 §3).
func EdgeKey(target string) string {
	sum := sha256.Sum256([]byte(NormalizeKey(target)))
	return hex.EncodeToString(sum[:])
}
