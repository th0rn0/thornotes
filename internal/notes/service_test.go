package notes

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHashContent(t *testing.T) {
	// SHA-256 of "" is e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
	hash := HashContent("")
	assert.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", hash)

	// Deterministic: same input → same hash
	h1 := HashContent("hello world")
	h2 := HashContent("hello world")
	assert.Equal(t, h1, h2)

	// Different input → different hash
	h3 := HashContent("hello world!")
	assert.NotEqual(t, h1, h3)
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Hello World", "hello-world"},
		{"  --foo--  ", "foo"},
		{"", "untitled"},
		{"Hello   World", "hello-world"},
		{"foo_bar", "foo-bar"},
		{"foo-bar", "foo-bar"},
		{"FOO BAR", "foo-bar"},
		{"123 abc", "123-abc"},
		{"unicode: 日本語", "unicode"},
		{"trailing-", "trailing"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result := slugify(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestSlugify_LongStringTruncated(t *testing.T) {
	// Build a long string > 100 chars.
	long := strings.Repeat("a", 120)
	result := slugify(long)
	assert.Equal(t, 100, len(result))
}

func TestNotesDiskPath_WithFolder(t *testing.T) {
	path := notesDiskPath(42, "42/Work", "my-note")
	assert.Equal(t, "42/Work/my-note.md", path)
}

func TestNotesDiskPath_NoFolder(t *testing.T) {
	path := notesDiskPath(42, "", "my-note")
	assert.Equal(t, fmt.Sprintf("42%cmy-note.md", '/'), path)
	assert.Equal(t, "42/my-note.md", path)
}

func TestFolderDiskPath_WithParent(t *testing.T) {
	path := folderDiskPath(42, "42/Projects", "Sub")
	assert.Equal(t, "42/Projects/Sub", path)
}

func TestFolderDiskPath_NoParent(t *testing.T) {
	path := folderDiskPath(42, "", "Work")
	assert.Equal(t, "42/Work", path)
}
