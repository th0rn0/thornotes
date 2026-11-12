package notes_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// H-11: git history records only what changed.
//
// The notes tree is a git repository and the store commits to it after every
// write, delete and rename. Two defects lived in that layer: a delete or rename
// staged the caller's entire working tree (so an unrelated note the user had open
// in an editor was committed under someone else's message), and a folder delete
// never staged the removals it made at all — the files left the live tree and the
// working tree stayed dirty until the next unrelated commit swept them in.
//
// These tests assert on `git show --name-only`, i.e. on what the commit actually
// contains, not on what the code intended to stage.

const h11UserEdit = "\nuser's own unsaved work\n"

// h11Git runs a git command in dir and returns its combined output.
func h11Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	return string(out)
}

// h11HeadPaths returns the sorted paths the HEAD commit touched.
func h11HeadPaths(t *testing.T, dir string) []string {
	t.Helper()
	out := h11Git(t, dir, "show", "--name-only", "--format=", "HEAD")
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	sort.Strings(paths)
	return paths
}

// h11HeadPathsNoRenames is h11HeadPaths with rename detection off, so both ends
// of a move appear instead of a single collapsed entry.
func h11HeadPathsNoRenames(t *testing.T, dir string) []string {
	t.Helper()
	out := h11Git(t, dir, "show", "--name-only", "--no-renames", "--format=", "HEAD")
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	sort.Strings(paths)
	return paths
}

// h11Status returns the sorted `git status --short` lines.
func h11Status(t *testing.T, dir string) []string {
	t.Helper()
	out := strings.TrimSpace(h11Git(t, dir, "status", "--short"))
	if out == "" {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	sort.Strings(lines)
	return lines
}

// h11Ignored returns the .gitignore rule that ignores path, or "" when git does
// not ignore it.
func h11Ignored(t *testing.T, dir, path string) string {
	t.Helper()
	cmd := exec.Command("git", "check-ignore", "-v", path)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// h11EditOutsideAPI simulates a user editing a note with their own editor: the
// file changes on disk, but nothing is staged or committed.
func h11EditOutsideAPI(t *testing.T, path string) {
	t.Helper()
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(content, []byte(h11UserEdit)...), 0600))
}

func h11NewGitStack(t *testing.T) *serviceStack {
	t.Helper()
	st := newTestStackFull(t)
	require.NoError(t, st.svc.FileStore().EnableGitHistory())
	return st
}

// A delete must commit only the deleted note's path. An unrelated modification
// sitting in the caller's working tree (a user with an editor open) must stay
// uncommitted and must not appear in the delete commit's tree at all.
func TestGitHistory_DeleteStagesOnlyItsOwnPath(t *testing.T) {
	st := h11NewGitStack(t)
	ctx := context.Background()

	a, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Alpha", nil)
	require.NoError(t, err)
	b, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Bravo", nil)
	require.NoError(t, err)
	bPath := noteFilePath(t, st, b.ID)
	h11EditOutsideAPI(t, bPath)

	require.NoError(t, st.svc.DeleteNote(ctx, st.userID, a.ID))

	t.Logf("git log -1 --stat:\n%s", h11Git(t, st.notesDir, "log", "-1", "--stat"))
	t.Logf("git status --short:\n%s", h11Git(t, st.notesDir, "status", "--short"))

	assert.Equal(t, []string{a.DiskPath}, h11HeadPaths(t, st.notesDir),
		"the delete commit must contain only the deleted note")

	// The user's edit is neither committed nor discarded.
	assert.Equal(t, []string{"M " + b.DiskPath}, h11Status(t, st.notesDir),
		"the user's own edit must still be an uncommitted change")
	committed := h11Git(t, st.notesDir, "show", "HEAD:"+b.DiskPath)
	assert.NotContains(t, committed, "user's own unsaved work",
		"the delete commit must not carry the other note's new content")
}

// A folder delete must commit the removals it made — the same delete path the
// single-note case uses, applied to the removed subtree — and must not sweep in
// an unrelated edit that was in the working tree.
func TestGitHistory_DeleteFolderCommitsRemovals(t *testing.T) {
	st := h11NewGitStack(t)
	ctx := context.Background()

	folder, err := st.svc.CreateFolder(ctx, st.userID, "test-uuid", nil, "Projects")
	require.NoError(t, err)
	inside, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", &folder.ID, "Inside", nil)
	require.NoError(t, err)

	outside, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Untouched", nil)
	require.NoError(t, err)
	h11EditOutsideAPI(t, noteFilePath(t, st, outside.ID))

	require.NoError(t, st.svc.DeleteFolder(ctx, st.userID, folder.ID))

	t.Logf("git log -1 --stat:\n%s", h11Git(t, st.notesDir, "log", "-1", "--stat"))
	t.Logf("git status --short:\n%s", h11Git(t, st.notesDir, "status", "--short"))

	assert.Equal(t, []string{inside.DiskPath}, h11HeadPaths(t, st.notesDir),
		"folder deletion must commit the removal of the notes it deleted, and nothing else")
	assert.Equal(t, []string{"M " + outside.DiskPath}, h11Status(t, st.notesDir),
		"the deleted folder's files must not be left dirty, and the user's edit must survive")
}

// With no third-party edits in play, a folder delete must leave the working tree
// clean: a dirty tree is what let the next unrelated commit sweep the removals in.
// A real instance takes the cross-process owner lock at the notes root before it
// serves anything, so this runs with that lock file on disk too — it is the
// condition "clean" is actually about, and a test root alone never creates it.
func TestGitHistory_DeleteFolderLeavesCleanWorktree(t *testing.T) {
	st := h11NewGitStack(t)
	ctx := context.Background()

	fs := st.svc.FileStore()
	require.NoError(t, fs.AcquireOwnerLock())
	t.Cleanup(fs.ReleaseOwnerLock)
	// Spelled out rather than taken from the package constant, so the test proves
	// the on-disk name and cannot silently stop covering the condition.
	lockPath := filepath.Join(st.notesDir, ".thornotes-owner.lock")
	require.FileExists(t, lockPath, "the test must run with the owner lock, as production does")

	folder, err := st.svc.CreateFolder(ctx, st.userID, "test-uuid", nil, "Archive")
	require.NoError(t, err)
	child, err := st.svc.CreateFolder(ctx, st.userID, "test-uuid", &folder.ID, "Nested")
	require.NoError(t, err)
	deep, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", &child.ID, "Deep", nil)
	require.NoError(t, err)

	require.NoError(t, st.svc.DeleteFolder(ctx, st.userID, folder.ID))

	t.Logf("git log -1 --stat:\n%s", h11Git(t, st.notesDir, "log", "-1", "--stat"))
	t.Logf("git status --short:\n%s", h11Git(t, st.notesDir, "status", "--short"))

	// The store's own lock file must be invisible to git, not merely uncommitted.
	ignored := h11Ignored(t, st.notesDir, ".thornotes-owner.lock")
	t.Logf("git check-ignore -v .thornotes-owner.lock: %s", ignored)
	assert.NotEmpty(t, ignored,
		".thornotes-owner.lock must be ignored by the generated .gitignore, not just untracked")

	// A nested note is still one file of the subtree, removed by the one commit.
	assert.Equal(t, []string{deep.DiskPath}, h11HeadPaths(t, st.notesDir))
	assert.Empty(t, h11Status(t, st.notesDir),
		"folder deletion must leave a clean working tree, owner lock included")
}

// A rename must commit both ends of the move and nothing else.
func TestGitHistory_RenameStagesOnlyItsOwnPaths(t *testing.T) {
	st := h11NewGitStack(t)
	ctx := context.Background()

	folder, err := st.svc.CreateFolder(ctx, st.userID, "test-uuid", nil, "Before")
	require.NoError(t, err)
	inside, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", &folder.ID, "Inside", nil)
	require.NoError(t, err)

	outside, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Untouched", nil)
	require.NoError(t, err)
	h11EditOutsideAPI(t, noteFilePath(t, st, outside.ID))

	require.NoError(t, st.svc.RenameFolder(ctx, st.userID, "test-uuid", folder.ID, "After"))

	t.Logf("git log -1 --stat:\n%s", h11Git(t, st.notesDir, "log", "-1", "--stat"))
	t.Logf("git status --short:\n%s", h11Git(t, st.notesDir, "status", "--short"))

	moved := strings.Replace(inside.DiskPath, "/Before/", "/After/", 1)
	require.NotEqual(t, inside.DiskPath, moved, "the note must have moved")
	assert.Equal(t, []string{moved, inside.DiskPath}, h11HeadPathsNoRenames(t, st.notesDir),
		"the rename commit must contain both ends of the move, and nothing else")
	assert.Equal(t, []string{"M " + outside.DiskPath}, h11Status(t, st.notesDir),
		"the user's own edit must still be an uncommitted change")
}

// An operation that changes nothing must not create a commit. Deleting a note
// whose file is already gone is a DB-only change by H-09's semantics.
func TestGitHistory_NoCommitWhenNothingChanged(t *testing.T) {
	st := h11NewGitStack(t)
	ctx := context.Background()

	note, err := st.svc.CreateNote(ctx, st.userID, "test-uuid", nil, "Vanished", nil)
	require.NoError(t, err)

	// The file leaves the tree outside the API: there is nothing left for the
	// delete to stage.
	require.NoError(t, os.Remove(noteFilePath(t, st, note.ID)))
	before := strings.TrimSpace(h11Git(t, st.notesDir, "rev-parse", "HEAD"))

	require.NoError(t, st.svc.DeleteNote(ctx, st.userID, note.ID))

	after := strings.TrimSpace(h11Git(t, st.notesDir, "rev-parse", "HEAD"))
	assert.Equal(t, before, after, "an operation that changed nothing must not commit")
}
