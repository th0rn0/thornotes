package notes_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ticket 598: a folder rename cascaded with an unscoped prefix match
// (`disk_path LIKE oldPrefix || '%'`), so renaming a folder whose name is a
// prefix of a sibling's rewrote the sibling's rows too. Each test below pins one
// of the reproductions from the security review (F-2), asserting the disk_path
// column and the filesystem, and — the property that actually matters — that the
// next reconcile finds nothing to change.

// requirePathExists is the counter-assertion to "the row was rewritten": a
// rewritten row points at a path that does not exist, and that is what makes the
// reconcile delete the row (dropping its share token) instead of repairing it.
func requirePathExists(t *testing.T, notesDir, relPath string) {
	t.Helper()
	_, err := os.Stat(filepath.Join(notesDir, relPath))
	require.NoErrorf(t, err, "row points at a path that must exist on disk: %s", relPath)
}

// TestRenameFolder_SiblingSharingPrefixUntouched covers reproduction 1: a note
// inside sibling folder "Notes2" became "<uuid>/Renamed2/..." when "Notes" was
// renamed to "Renamed2".
func TestRenameFolder_SiblingSharingPrefixUntouched(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	source, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Notes")
	require.NoError(t, err)
	sibling, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Notes2")
	require.NoError(t, err)
	inner, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &source.ID, "Inner", nil)
	require.NoError(t, err)
	siblingNote, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &sibling.ID, "Sibling", nil)
	require.NoError(t, err)

	require.NoError(t, st.svc.RenameFolder(ctx, st.userID, st.user.UUID, source.ID, "Renamed2"))

	gotInner, err := st.svc.GetNote(ctx, st.userID, inner.ID)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(st.user.UUID, "Renamed2", "inner.md"), gotInner.DiskPath)

	gotSibling, err := st.svc.GetNote(ctx, st.userID, siblingNote.ID)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(st.user.UUID, "Notes2", "sibling.md"), gotSibling.DiskPath,
		"a folder that merely shares a name prefix is not a descendant")

	requirePathExists(t, st.notesDir, gotInner.DiskPath)
	requirePathExists(t, st.notesDir, gotSibling.DiskPath)

	changes, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	assert.Zero(t, changes, "reconcile must find nothing to change after the rename")
}

// TestRenameFolder_DoesNotDoubleApplyOwnName covers reproduction 2: renaming
// "Proj" to "Proj-old" produced the folder's own path "Proj-old-old".
func TestRenameFolder_DoesNotDoubleApplyOwnName(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	proj, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Proj")
	require.NoError(t, err)

	require.NoError(t, st.svc.RenameFolder(ctx, st.userID, st.user.UUID, proj.ID, "Proj-old"))

	tree, err := st.svc.FolderTree(ctx, st.userID)
	require.NoError(t, err)
	require.Len(t, tree, 1)
	assert.Equal(t, filepath.Join(st.user.UUID, "Proj-old"), tree[0].DiskPath, "the cascade must not re-match the folder's own, already-renamed row")
	requirePathExists(t, st.notesDir, tree[0].DiskPath)

	changes, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	assert.Zero(t, changes)
}

// TestRenameFolder_ExistingSiblingIsNotDragged covers reproduction 3 with "Proj"
// present: renaming "Projects" to "Proj-oldects" must leave "Proj" alone, and —
// the red half under the old code — a later rename of "Proj" must leave the
// now-sibling "Proj-oldects" alone, because its path starts with "Proj" but is
// not underneath it.
func TestRenameFolder_ExistingSiblingIsNotDragged(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	proj, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Proj")
	require.NoError(t, err)
	projects, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Projects")
	require.NoError(t, err)
	projNote, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &proj.ID, "In Proj", nil)
	require.NoError(t, err)
	projectsNote, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &projects.ID, "In Projects", nil)
	require.NoError(t, err)

	require.NoError(t, st.svc.RenameFolder(ctx, st.userID, st.user.UUID, projects.ID, "Proj-oldects"))

	gotProj, err := st.svc.GetNote(ctx, st.userID, projNote.ID)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(st.user.UUID, "Proj", "in-proj.md"), gotProj.DiskPath)

	gotProjects, err := st.svc.GetNote(ctx, st.userID, projectsNote.ID)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(st.user.UUID, "Proj-oldects", "in-projects.md"), gotProjects.DiskPath)

	// Now the direction that used to corrupt: "Proj" is a string prefix of
	// "Proj-oldects" but not a path prefix of it.
	require.NoError(t, st.svc.RenameFolder(ctx, st.userID, st.user.UUID, proj.ID, "Proj2"))

	gotProj, err = st.svc.GetNote(ctx, st.userID, projNote.ID)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(st.user.UUID, "Proj2", "in-proj.md"), gotProj.DiskPath)

	gotProjects, err = st.svc.GetNote(ctx, st.userID, projectsNote.ID)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(st.user.UUID, "Proj-oldects", "in-projects.md"), gotProjects.DiskPath,
		"renaming Proj must not touch the sibling whose name starts with Proj-")

	requirePathExists(t, st.notesDir, gotProj.DiskPath)
	requirePathExists(t, st.notesDir, gotProjects.DiskPath)

	changes, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	assert.Zero(t, changes)
}

// TestRenameFolder_PercentUnderscoreName covers the LIKE-metacharacter case: a
// folder name may legally contain '%' and '_', and the old pattern passed them
// to LIKE as wildcards, so "100%_done" also matched the unrelated sibling
// "1000Xdone-notes".
func TestRenameFolder_PercentUnderscoreName(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	wildcard, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "100%_done")
	require.NoError(t, err)
	bystander, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "1000Xdone-notes")
	require.NoError(t, err)
	inner, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &wildcard.ID, "Child", nil)
	require.NoError(t, err)
	bystanderNote, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &bystander.ID, "Other", nil)
	require.NoError(t, err)

	require.NoError(t, st.svc.RenameFolder(ctx, st.userID, st.user.UUID, wildcard.ID, "done"))

	gotInner, err := st.svc.GetNote(ctx, st.userID, inner.ID)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(st.user.UUID, "done", "child.md"), gotInner.DiskPath)

	gotBystander, err := st.svc.GetNote(ctx, st.userID, bystanderNote.ID)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(st.user.UUID, "1000Xdone-notes", "other.md"), gotBystander.DiskPath,
		"'%' and '_' in the renamed folder's name must not act as wildcards")

	requirePathExists(t, st.notesDir, gotInner.DiskPath)
	requirePathExists(t, st.notesDir, gotBystander.DiskPath)

	changes, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	assert.Zero(t, changes)
}

// TestRenameFolder_NonASCIIFolderName pins the reconcile property for a folder
// name containing multi-byte characters: after the rename cascade the rows must
// point at files that exist, and reconcile must find nothing to change.
//
// It cannot see the MySQL byte-vs-character defect this ticket fixed — SQLite's
// LENGTH() counts characters, so the old MySQL expression happened to be right
// here. The regression test for that lives in
// internal/repository/mysql.TestMySQL_FolderRepo_UpdateDescendantPaths_NonASCIIFolderName;
// this one guards the service-level consequence (total_changes=0, so no
// delete/reimport and no lost share token) on the dialect the default suite runs.
func TestRenameFolder_NonASCIIFolderName(t *testing.T) {
	for _, folderName := range []string{"Café", "日本語"} {
		t.Run(folderName, func(t *testing.T) {
			st := newTestStackFull(t)
			ctx := context.Background()

			source, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, folderName)
			require.NoError(t, err)
			child, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, &source.ID, "Sub")
			require.NoError(t, err)
			inner, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &source.ID, "Inner", nil)
			require.NoError(t, err)
			deep, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &child.ID, "Deep", nil)
			require.NoError(t, err)

			require.NoError(t, st.svc.RenameFolder(ctx, st.userID, st.user.UUID, source.ID, "Renamed"))

			gotInner, err := st.svc.GetNote(ctx, st.userID, inner.ID)
			require.NoError(t, err)
			assert.Equal(t, filepath.Join(st.user.UUID, "Renamed", "inner.md"), gotInner.DiskPath)

			gotDeep, err := st.svc.GetNote(ctx, st.userID, deep.ID)
			require.NoError(t, err)
			assert.Equal(t, filepath.Join(st.user.UUID, "Renamed", "Sub", "deep.md"), gotDeep.DiskPath)

			requirePathExists(t, st.notesDir, gotInner.DiskPath)
			requirePathExists(t, st.notesDir, gotDeep.DiskPath)

			changes, err := st.svc.Reconcile(ctx, st.user)
			require.NoError(t, err)
			assert.Zero(t, changes, "total_changes must be 0 — a non-zero count here is the delete/reimport that drops share tokens")
		})
	}
}

// TestRenameFolder_ShareTokenSurvivesSiblingPrefixRename is the property the
// defect actually broke: a corrupted sibling path makes reconcile delete and
// reimport the row, and a reimported row is a new row with no share token, so a
// public link silently stops working. Assert both halves — total_changes=0 and
// the token still resolving.
func TestRenameFolder_ShareTokenSurvivesSiblingPrefixRename(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	source, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Notes")
	require.NoError(t, err)
	sibling, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Notes2")
	require.NoError(t, err)
	siblingNote, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &sibling.ID, "Shared", nil)
	require.NoError(t, err)

	token, err := st.svc.SetShareToken(ctx, st.userID, siblingNote.ID, false)
	require.NoError(t, err)
	require.NotNil(t, token)

	require.NoError(t, st.svc.RenameFolder(ctx, st.userID, st.user.UUID, source.ID, "Renamed2"))

	changes, err := st.svc.Reconcile(ctx, st.user)
	require.NoError(t, err)
	assert.Zero(t, changes, "total_changes must be 0 — a non-zero count here is the delete/reimport that drops share tokens")

	byToken, err := st.svc.GetNoteByShareToken(ctx, *token)
	require.NoError(t, err)
	assert.Equal(t, siblingNote.ID, byToken.ID, "the public link must still resolve to the same row")
}
