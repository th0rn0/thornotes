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
	path := notesDiskPath("abc-uuid", "abc-uuid/Work", "my-note")
	assert.Equal(t, "abc-uuid/Work/my-note.md", path)
}

func TestNotesDiskPath_NoFolder(t *testing.T) {
	path := notesDiskPath("abc-uuid", "", "my-note")
	assert.Equal(t, fmt.Sprintf("abc-uuid%cmy-note.md", '/'), path)
	assert.Equal(t, "abc-uuid/my-note.md", path)
}

func TestFolderDiskPath_WithParent(t *testing.T) {
	path := folderDiskPath("abc-uuid", "abc-uuid/Projects", "Sub")
	assert.Equal(t, "abc-uuid/Projects/Sub", path)
}

func TestFolderDiskPath_NoParent(t *testing.T) {
	path := folderDiskPath("abc-uuid", "", "Work")
	assert.Equal(t, "abc-uuid/Work", path)
}

func TestPtrEq_BothNil(t *testing.T) {
	assert.True(t, ptrEq(nil, nil))
}

func TestPtrEq_FirstNil(t *testing.T) {
	v := int64(1)
	assert.False(t, ptrEq(nil, &v))
}

func TestPtrEq_SecondNil(t *testing.T) {
	v := int64(1)
	assert.False(t, ptrEq(&v, nil))
}

func TestPtrEq_BothNonNilEqual(t *testing.T) {
	a, b := int64(42), int64(42)
	assert.True(t, ptrEq(&a, &b))
}

func TestPtrEq_BothNonNilDifferent(t *testing.T) {
	a, b := int64(1), int64(2)
	assert.False(t, ptrEq(&a, &b))
}
