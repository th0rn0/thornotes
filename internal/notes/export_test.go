package notes

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSanitizeExportName pins the name rule: an archive entry is one path
// component, the importer derives the title back from it, and nothing a title
// contains may turn one note into a directory or a hidden file.
func TestSanitizeExportName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Kickoff", "Kickoff"},
		{"allowed punctuation", "a_b-c.d e", "a_b-c.d e"},
		{"path separator becomes one space", "Q3/Q4 plan", "Q3 Q4 plan"},
		{"backslash too", `a\b`, "a b"},
		{"runs of disallowed characters collapse", "a /// b", "a b"},
		{"leading dot and space trimmed", "  .hidden stuff  ", "hidden stuff"},
		{"only dots is not a name", "...", "untitled"},
		{"only spaces is not a name", "   ", "untitled"},
		{"unicode is dropped, not passed through", "Café", "Caf"},
		{"no ascii at all falls back", "日本語", "untitled"},
		{"internal spaces collapse", "hello     world", "hello world"},
		{"colon and quotes", `He said: "hi"`, "He said hi"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeExportName(tc.in)
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, "/")
			assert.NotContains(t, got, `\`)
			assert.False(t, strings.HasPrefix(got, "."))
			assert.LessOrEqual(t, len(got), exportNameMaxBytes)
		})
	}
}

func TestSanitizeExportName_CapsLength(t *testing.T) {
	got := sanitizeExportName(strings.Repeat("a", 500))
	assert.Len(t, got, exportNameMaxBytes)
	assert.NotEmpty(t, got)
}

// TestUniqueExportName pins the collision rule: -2, -3, never a silent overwrite,
// and case-insensitively, because the archive may be restored on a filesystem
// where "Notes" and "notes" are the same file.
func TestUniqueExportName(t *testing.T) {
	taken := map[string]map[string]bool{}

	assert.Equal(t, "Notes.md", uniqueExportName(taken, "work", "Notes", ".md"))
	assert.Equal(t, "Notes-2.md", uniqueExportName(taken, "work", "Notes", ".md"))
	assert.Equal(t, "Notes-3.md", uniqueExportName(taken, "work", "Notes", ".md"))

	// A different directory has its own namespace.
	assert.Equal(t, "Notes.md", uniqueExportName(taken, "work/sub", "Notes", ".md"))

	// Case-insensitive dedupe.
	assert.Equal(t, "Plan", uniqueExportName(taken, "work", "Plan", ""))
	assert.Equal(t, "plan-2", uniqueExportName(taken, "work", "plan", ""))
	assert.Equal(t, "PLAN-3", uniqueExportName(taken, "work", "PLAN", ""))
}

func TestRenderExportReport_NamesEverySkippedPath(t *testing.T) {
	report := string(renderExportReport([]ExportEntry{
		{Path: "work/Gone.md", DiskPath: "uuid-1/work/gone.md"},
		{Path: "work/sub/Also gone.md", DiskPath: "uuid-1/work/sub/also-gone.md"},
	}))

	assert.Contains(t, report, "2 note file(s) could not be exported")
	assert.Contains(t, report, "work/Gone.md")
	assert.Contains(t, report, "uuid-1/work/gone.md")
	assert.Contains(t, report, "work/sub/Also gone.md")
	assert.Contains(t, report, "uuid-1/work/sub/also-gone.md")
}

func TestSplitArchivePath(t *testing.T) {
	parent, base := splitArchivePath("work/sub/Note.md")
	assert.Equal(t, "work/sub", parent)
	assert.Equal(t, "Note.md", base)

	parent, base = splitArchivePath("work/")
	assert.Equal(t, "", parent)
	assert.Equal(t, "work", base)
}
