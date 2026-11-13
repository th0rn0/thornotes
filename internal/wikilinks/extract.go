package wikilinks

import (
	"regexp"
	"strings"
)

// linkRe matches a wikilink body `[[...]]` with no nested brackets.
var linkRe = regexp.MustCompile(`\[\[([^\[\]]+)\]\]`)

// codeSpanRe matches a single-backtick inline code span (no newline, no
// internal backtick), the same shape as the reference CODE_SPAN_RE.
var codeSpanRe = regexp.MustCompile("`[^`\n]*`")

// ExtractLinks returns every parsed [[...]] link in body, excluding links that
// appear inside inline code spans or fenced code blocks. Table cells are plain
// text to the extractor, so links in tables are returned like any other inline
// link.
func ExtractLinks(body string) []LinkRef {
	body = stripFencedCode(body)
	body = codeSpanRe.ReplaceAllString(body, "")
	matches := linkRe.FindAllStringSubmatch(body, -1)
	refs := make([]LinkRef, 0, len(matches))
	for _, m := range matches {
		refs = append(refs, parseLinkRef(m[1]))
	}
	return refs
}

// parseLinkRef splits an inner `[[...]]` body into target / anchor / link_text
// per note 323 §4:
//
//	[[Overview]]                  -> Target "Overview", Anchor "", LinkText ""
//	[[Overview#Trust boundaries]] -> Target "Overview", Anchor "Trust boundaries"
//	[[Overview|the map]]          -> Target "Overview", LinkText "the map"
func parseLinkRef(raw string) LinkRef {
	ref := LinkRef{Raw: raw}
	body := raw
	if i := strings.Index(body, "|"); i >= 0 {
		ref.LinkText = strings.TrimSpace(body[i+1:])
		body = body[:i]
	}
	if i := strings.Index(body, "#"); i >= 0 {
		ref.Target = strings.TrimSpace(body[:i])
		ref.Anchor = strings.TrimSpace(body[i+1:])
	} else {
		ref.Target = strings.TrimSpace(body)
	}
	return ref
}

// stripFencedCode removes ``` and ~~~ fenced code blocks: the opening fence
// line, the closing fence line, and every line between them. It shares
// walkLines with maskCode so the two views of "inside a fence" are the same
// view.
func stripFencedCode(body string) string {
	lines := make([]string, 0, strings.Count(body, "\n")+1)
	walkLines(body, func(line string, inCode bool) {
		if !inCode {
			lines = append(lines, line)
		}
	})
	return strings.Join(lines, "\n")
}

// fenceMarker reports whether trimmed starts a code fence (3+ backticks or
// tildes) and returns the fence character.
func fenceMarker(trimmed string) (byte, bool) {
	if len(trimmed) < 3 {
		return 0, false
	}
	c := trimmed[0]
	if c != '`' && c != '~' {
		return 0, false
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == c {
		n++
	}
	if n < 3 {
		return 0, false
	}
	return c, true
}
