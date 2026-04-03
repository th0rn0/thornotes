package notes_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Path-traversal rejection — folder names
// ---------------------------------------------------------------------------

func TestService_CreateFolder_RejectsPathTraversal(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	cases := []string{
		"../2",
		"../../etc/passwd",
		"foo/bar",
		"./sneaky",
		"a/b",
		"../",
		"..",
	}
	for _, name := range cases {
		_, err := svc.CreateFolder(ctx, userID, nil, name)
		assert.Error(t, err, "expected error for folder name %q", name)
	}
}

func TestService_RenameFolder_RejectsPathTraversal(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	folder, err := svc.CreateFolder(ctx, userID, nil, "Legit")
	require.NoError(t, err)

	cases := []string{
		"../2",
		"../../etc/passwd",
		"foo/bar",
		"..",
	}
	for _, name := range cases {
		err := svc.RenameFolder(ctx, userID, folder.ID, name)
		assert.Error(t, err, "expected error renaming to %q", name)
	}
}

func TestService_CreateFolder_AcceptsNormalNames(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	cases := []string{
		"Work",
		"Personal Notes",
		"2024 - Projects",
		"hello_world",
		"Test (1)",
	}
	for _, name := range cases {
		_, err := svc.CreateFolder(ctx, userID, nil, name)
		assert.NoError(t, err, "expected no error for valid folder name %q", name)
	}
}
