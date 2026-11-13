package wikilinks

import "strings"

// walkLines visits every line of body together with whether that line belongs
// to a fenced code block — the opening fence line, its body and the closing
// fence line are all reported as code. This is the one place the fence rule
// lives, so extraction (which ignores code) and rewriting (which must leave it
// alone) cannot drift apart.
func walkLines(body string, fn func(line string, inCode bool)) {
	fence := byte(0)
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if fence == 0 {
			if c, ok := fenceMarker(trimmed); ok {
				fence = c
				fn(line, true) // opening fence line
				continue
			}
			fn(line, false)
			continue
		}
		if c, ok := fenceMarker(trimmed); ok && c == fence {
			fence = 0 // closing fence line (still reported as code)
		}
		fn(line, true)
	}
}

// maskCode returns body with every fenced code block and inline code span
// blanked to spaces, keeping the body's byte length and every newline exactly
// where it was. Offsets into the masked copy are therefore offsets into body,
// which is what lets ReplaceTargets rewrite prose without touching code.
func maskCode(body string) string {
	lines := make([]string, 0, strings.Count(body, "\n")+1)
	walkLines(body, func(line string, inCode bool) {
		if inCode {
			lines = append(lines, blankNonNewlines(line))
			return
		}
		lines = append(lines, line)
	})
	masked := strings.Join(lines, "\n")
	return codeSpanRe.ReplaceAllStringFunc(masked, blankNonNewlines)
}

// blankNonNewlines replaces every byte except '\n' with a space, preserving the
// input's byte length so the result can be overlaid on the original.
func blankNonNewlines(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	for i := range b {
		if b[i] != '\n' {
			b[i] = ' '
		}
	}
	return string(b)
}

// targetSpan returns the [start,end) range of the link target inside an inner
// `[[...]]` body — the part before any '#anchor' and before any '|display
// text', with surrounding spaces excluded. The offsets index inner itself, so a
// caller can substitute exactly the target and leave the author's spacing,
// anchor and display text alone.
func targetSpan(inner string) (int, int) {
	head := inner
	if i := strings.IndexByte(head, '|'); i >= 0 {
		head = head[:i]
	}
	if i := strings.IndexByte(head, '#'); i >= 0 {
		head = head[:i]
	}
	start, end := 0, len(head)
	for start < end && (head[start] == ' ' || head[start] == '\t') {
		start++
	}
	for end > start && (head[end-1] == ' ' || head[end-1] == '\t') {
		end--
	}
	return start, end
}

// ReplaceTargets rewrites the target of every `[[link]]` in body that match
// accepts, and leaves everything else — including the anchor and the display
// text, which are the author's words — exactly as written.
//
// Links inside code are not links: fenced blocks and inline code spans document
// the syntax, so they are skipped by the same rule ExtractLinks uses (maskCode).
//
// match decides whether a link target is one this caller may rename; rewrite
// returns its replacement, or "" to leave that link alone. It returns the new
// body and the number of links rewritten.
func ReplaceTargets(body string, match func(target string) bool, rewrite func(target string) string) (string, int) {
	matches := linkRe.FindAllStringSubmatchIndex(maskCode(body), -1)
	if len(matches) == 0 {
		return body, 0
	}

	var b strings.Builder
	b.Grow(len(body))
	last, rewritten := 0, 0
	for _, m := range matches {
		innerStart, innerEnd := m[2], m[3]
		inner := body[innerStart:innerEnd]
		start, end := targetSpan(inner)
		if start == end {
			continue // [[#Heading]] or [[|]]: no target to rename
		}
		target := inner[start:end]
		if !match(target) {
			continue
		}
		replacement := rewrite(target)
		if replacement == "" || replacement == target {
			continue
		}
		b.WriteString(body[last : innerStart+start])
		b.WriteString(replacement)
		last = innerStart + end
		rewritten++
	}
	if rewritten == 0 {
		return body, 0
	}
	b.WriteString(body[last:])
	return b.String(), rewritten
}
