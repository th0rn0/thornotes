package repository_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/repository"
)

// TestDescendantPathPredicate_BoundsAndEscapes pins the two things the predicate
// has to get right, because both are things the SQL it replaced got wrong
// (ticket 598) and neither is visible by reading a query that "looks scoped".
func TestDescendantPathPredicate_BoundsAndEscapes(t *testing.T) {
	tests := []struct {
		name    string
		prefix  string
		pattern string
	}{
		{
			name:    "plain prefix gets a boundary, not a bare wildcard",
			prefix:  "uuid/Notes",
			pattern: "uuid/Notes" + string(filepath.Separator) + "%",
		},
		{
			name:    "percent is escaped so it cannot match anything",
			prefix:  "uuid/100%_done",
			pattern: "uuid/100!%!_done" + string(filepath.Separator) + "%",
		},
		{
			name:    "underscore is escaped so it cannot match one character",
			prefix:  "uuid/a_b",
			pattern: "uuid/a!_b" + string(filepath.Separator) + "%",
		},
		{
			name:    "the escape character itself is escaped",
			prefix:  "uuid/hi!",
			pattern: "uuid/hi!!" + string(filepath.Separator) + "%",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			predicate, args := repository.DescendantPathPredicate(tc.prefix)

			assert.Contains(t, predicate, "disk_path = ?", "the folder's own path must be matched exactly")
			assert.Contains(t, predicate, "LIKE ? ESCAPE", "the subtree match must use LIKE with an explicit escape")
			require.Len(t, args, 2)
			assert.Equal(t, tc.prefix, args[0])
			assert.Equal(t, tc.pattern, args[1])
		})
	}
}

// TestDescendantPathPredicate_UsesPlatformSeparator is the guard against a second,
// hard-coded '/' creeping back in: the scope must be built from the same
// separator the path builders use.
func TestDescendantPathPredicate_UsesPlatformSeparator(t *testing.T) {
	_, args := repository.DescendantPathPredicate("uuid/Notes")
	pattern, ok := args[1].(string)
	require.True(t, ok)
	assert.Equal(t, "uuid/Notes"+string(filepath.Separator)+"%", pattern)
}
