package notes

import "strings"

// The heading rules live in one place because two writers share them: the
// rename path (UpdateNoteMetadata) writes the note's title into the file's
// "# H1", and the reconciler reads that heading back as the title the file
// states (step 9b, ticket N7). If the two disagreed about which line is the
// note's heading, a rename would write a heading the repair pass cannot find
// and the title would drift back on the next pass — the exact failure this
// pair of functions exists to prevent.
// headingScanLines bounds how far into a note its H1 is looked for. A heading
// is a document title: past the opening lines it is prose, and a "# " line 40
// lines down is far more likely to be a quoted or pasted heading than the
// note's own.
const headingScanLines = 16

// headingTitle returns the first markdown H1 ("# Heading") line in content,
// truncated to the title limit.
//
// ok is false when the content states no heading at all, which is not the same
// as stating an empty one: a caller that treats "no heading" as "the title is
// the filename" would overwrite the human title of every note that has no body
// yet.
func headingTitle(content string) (string, bool) {
	title, ok := statedHeading(content)
	if !ok {
		return "", false
	}
	if len(title) > maxTitleLen {
		title = title[:maxTitleLen]
	}
	return title, true
}

// statedHeading returns the first markdown H1 line's text exactly as the
// content states it, with no truncation.
//
// It exists for the caller that must tell "a heading I can use" from "a heading
// too long to be a title": import falls back to the filename rather than
// truncating, and it cannot make that call from headingTitle's output because
// that output is already cut to the limit. Which lines count as the heading is
// still decided here, once, for every reader of the rule.
func statedHeading(content string) (string, bool) {
	for _, line := range strings.SplitN(content, "\n", headingScanLines) {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "# ") {
			continue
		}
		title := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
		if title == "" {
			continue
		}
		return title, true
	}
	return "", false
}

// RetitleHeading returns content with the first markdown H1 it states replaced
// by "# <title>".
//
// It is the write half of the rule headingTitle reads: a person renaming a note
// states one fact — what the note is called — and the app owns both places it
// is written down (the row's title and the file's heading). Writing only the
// row is what let a rename be undone by the next reconcile pass, which sees a
// heading that still states the old title and adopts it (ticket 467).
//
// Content that states no heading is returned unchanged, and no heading is
// added: a note created with a title and no body yet states nothing, and the
// repair pass has no opinion about it either, so inventing a heading here would
// be the app writing a document title the user never typed. Everything outside
// the heading line — the rest of the body, the line ending style, the trailing
// newline — is preserved byte for byte.
func RetitleHeading(content, title string) string {
	offset := 0
	for i := 0; i < headingScanLines && offset <= len(content); i++ {
		var lineEnd int
		if nl := strings.IndexByte(content[offset:], '\n'); nl >= 0 {
			lineEnd = offset + nl
		} else {
			lineEnd = len(content)
		}
		line := content[offset:lineEnd]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			if stated := strings.TrimSpace(strings.TrimPrefix(trimmed, "#")); stated != "" {
				newLine := "# " + title
				if strings.HasSuffix(line, "\r") {
					newLine += "\r" // keep a CRLF file CRLF
				}
				return content[:offset] + newLine + content[lineEnd:]
			}
		}
		if lineEnd == len(content) {
			break
		}
		offset = lineEnd + 1
	}
	return content
}
