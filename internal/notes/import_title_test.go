package notes_test

// 631: import titled a note from its filename and then wrote the body, so an
// imported file whose "# H1" disagreed with its name started out drifted and
// the first reconcile pass retitled it. The user saw one title for a moment
// and a different one a pass later.
//
// Import now derives the title the way disk discovery already does (step 8 of
// reconcile, deriveTitle): the heading the body states, with the filename as
// the fallback and no opinion when the body states no heading. The filename
// keeps deciding the slug, and therefore the path, because the zip round trip
// depends on it — entry names are sanitised titles so a re-import restores the
// same layout (ticket 615). Deriving the title must not move a file.
//
// The zero-retitle assertion is the point of this file: it is what proves the
// wobble is gone, as opposed to the title merely ending up right a pass later.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/model"
)

// imported notes at root, as full notes (ListNotes gives no disk_path).
func rootNotes(t *testing.T, st *serviceStack) []*model.Note {
	t.Helper()
	ctx := context.Background()
	items, err := st.svc.ListNotes(ctx, st.userID, nil)
	require.NoError(t, err)
	out := make([]*model.Note, 0, len(items))
	for _, it := range items {
		n, err := st.svc.GetNote(ctx, st.userID, it.ID)
		require.NoError(t, err)
		out = append(out, n)
	}
	return out
}

// notesInFolder returns the notes of one folder by name, as full notes.
func notesInFolder(t *testing.T, st *serviceStack, folderName string) []*model.Note {
	t.Helper()
	ctx := context.Background()
	tree, err := st.svc.FolderTree(ctx, st.userID)
	require.NoError(t, err)
	var folderID int64
	found := false
	for _, f := range tree {
		if f.Name == folderName {
			folderID, found = f.ID, true
			break
		}
	}
	require.True(t, found, "folder %q should exist after import", folderName)

	items, err := st.svc.FolderNotes(ctx, st.userID, folderID)
	require.NoError(t, err)
	out := make([]*model.Note, 0, len(items))
	for _, it := range items {
		n, err := st.svc.GetNote(ctx, st.userID, it.ID)
		require.NoError(t, err)
		out = append(out, n)
	}
	return out
}

// requireNoReconcileWork asserts the import left nothing for the first pass.
func requireNoReconcileWork(t *testing.T, st *serviceStack) {
	t.Helper()
	changed, err := st.svc.Reconcile(context.Background(), st.user)
	require.NoError(t, err)
	assert.Zero(t, changed,
		"the first reconcile pass after an import must converge nothing (no retitle, no content rewrite)")
}

// assertOnDisk proves the note file is where the filename put it, and that
// nothing on the import or reconcile path renamed it.
func assertOnDisk(t *testing.T, st *serviceStack, relPath string) {
	t.Helper()
	_, err := os.Stat(filepath.Join(st.notesDir, filepath.FromSlash(relPath)))
	assert.NoError(t, err, "expected the note file at %s", relPath)
}

// TestImportMarkdown_TitleFromHeading_SlugFromFilename is the head-on case: the
// file is named for its date, the body states its own title.
func TestImportMarkdown_TitleFromHeading_SlugFromFilename(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	res, err := st.svc.ImportMarkdown(ctx, st.userID, "test-uuid", "2026-11-10-retro.md",
		"# Sprint 19 retro\n\nWe shipped the import work.\n")
	require.NoError(t, err)
	require.Equal(t, 1, res.NotesCreated)

	notes := rootNotes(t, st)
	require.Len(t, notes, 1)
	n := notes[0]

	assert.Equal(t, "Sprint 19 retro", n.Title, "the title is the heading the body states")
	assert.Equal(t, "2026-11-10-retro", n.Slug, "the slug still comes from the filename")
	assert.Equal(t, "test-uuid/2026-11-10-retro.md", n.DiskPath, "the path still comes from the filename")
	assertOnDisk(t, st, n.DiskPath)

	requireNoReconcileWork(t, st)

	// The pass must not have retitled or moved anything either.
	after := rootNotes(t, st)
	require.Len(t, after, 1)
	assert.Equal(t, "Sprint 19 retro", after[0].Title)
	assert.Equal(t, "2026-11-10-retro", after[0].Slug)
	assert.Equal(t, "test-uuid/2026-11-10-retro.md", after[0].DiskPath)
}

// TestImportMarkdown_NoHeading_KeepsFilename: no heading is not an opinion, so
// the filename supplies the title.
func TestImportMarkdown_NoHeading_KeepsFilename(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	_, err := st.svc.ImportMarkdown(ctx, st.userID, "test-uuid", "Meeting notes.md",
		"Just prose, no heading at all.\n")
	require.NoError(t, err)

	notes := rootNotes(t, st)
	require.Len(t, notes, 1)
	assert.Equal(t, "Meeting notes", notes[0].Title, "a body with no heading keeps the filename title")
	assert.Equal(t, "meeting-notes", notes[0].Slug)
	assert.Equal(t, "test-uuid/meeting-notes.md", notes[0].DiskPath)
	assertOnDisk(t, st, notes[0].DiskPath)

	requireNoReconcileWork(t, st)
}

// TestImportMarkdown_OverlongHeading_FallsBackToFilename: a heading longer than
// the title limit is not truncated into a title nobody wrote — the filename
// supplies the title instead.
func TestImportMarkdown_OverlongHeading_FallsBackToFilename(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	longHeading := strings.Repeat("very long heading ", 60) // 1080 chars, over the 500 limit
	_, err := st.svc.ImportMarkdown(ctx, st.userID, "test-uuid", "long.md", "# "+longHeading+"\n\nbody\n")
	require.NoError(t, err)

	notes := rootNotes(t, st)
	require.Len(t, notes, 1)
	assert.Equal(t, "long", notes[0].Title, "an over-limit heading falls back to the filename, not a truncation")
	assert.Equal(t, "long", notes[0].Slug)
}

// TestImportZip_TitlesFromHeadings_SlugsFromFilenames is the mixed archive:
// one entry whose heading matches its name, one that differs, one with no
// heading at all.
func TestImportZip_TitlesFromHeadings_SlugsFromFilenames(t *testing.T) {
	st := newTestStackFull(t)
	ctx := context.Background()

	data := makeZip(t, map[string]string{
		"Sprint/matching.md":      "# matching\n\nheading and filename agree\n",
		"Sprint/differing.md":     "# Sprint 19 retro\n\nheading differs from the filename\n",
		"Sprint/Meeting notes.md": "no heading here, just prose\n",
	})

	res, err := st.svc.ImportZip(ctx, st.userID, "test-uuid", data)
	require.NoError(t, err)
	require.Equal(t, 3, res.NotesCreated)
	require.Equal(t, 1, res.FoldersCreated)

	bySlug := map[string]*model.Note{}
	for _, n := range notesInFolder(t, st, "Sprint") {
		bySlug[n.Slug] = n
	}
	require.Len(t, bySlug, 3)

	// matching: title and filename agree.
	require.Contains(t, bySlug, "matching")
	assert.Equal(t, "matching", bySlug["matching"].Title)
	assert.Equal(t, "test-uuid/Sprint/matching.md", bySlug["matching"].DiskPath)

	// differing: title from the heading, path from the filename.
	require.Contains(t, bySlug, "differing")
	assert.Equal(t, "Sprint 19 retro", bySlug["differing"].Title)
	assert.Equal(t, "test-uuid/Sprint/differing.md", bySlug["differing"].DiskPath)

	// no heading: the filename supplies the title.
	require.Contains(t, bySlug, "meeting-notes")
	assert.Equal(t, "Meeting notes", bySlug["meeting-notes"].Title)
	assert.Equal(t, "test-uuid/Sprint/meeting-notes.md", bySlug["meeting-notes"].DiskPath)

	for _, n := range bySlug {
		assertOnDisk(t, st, n.DiskPath)
	}

	requireNoReconcileWork(t, st)

	// Nothing was retitled or renamed by the pass.
	after := map[string]*model.Note{}
	for _, n := range notesInFolder(t, st, "Sprint") {
		after[n.Slug] = n
	}
	require.Len(t, after, 3)
	assert.Equal(t, "Sprint 19 retro", after["differing"].Title)
	assert.Equal(t, "matching", after["matching"].Title)
	assert.Equal(t, "Meeting notes", after["meeting-notes"].Title)
	for _, n := range after {
		assertOnDisk(t, st, n.DiskPath)
	}
}
