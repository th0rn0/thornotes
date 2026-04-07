package notes_test

import (
	"archive/zip"
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── FindFoldersByName ────────────────────────────────────────────────────────

func TestService_FindFoldersByName_EmptyQuery(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Work")
	require.NoError(t, err)
	_, err = svc.CreateFolder(ctx, userID, "test-uuid", nil, "Personal")
	require.NoError(t, err)

	all, err := svc.FindFoldersByName(ctx, userID, "")
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

func TestService_FindFoldersByName_Matching(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Work Projects")
	require.NoError(t, err)
	_, err = svc.CreateFolder(ctx, userID, "test-uuid", nil, "Personal")
	require.NoError(t, err)

	found, err := svc.FindFoldersByName(ctx, userID, "work")
	require.NoError(t, err)
	assert.Len(t, found, 1)
	assert.Equal(t, "Work Projects", found[0].Name)
}

func TestService_FindFoldersByName_CaseInsensitive(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Budget")
	require.NoError(t, err)

	found, err := svc.FindFoldersByName(ctx, userID, "BUDGET")
	require.NoError(t, err)
	assert.Len(t, found, 1)
}

func TestService_FindFoldersByName_NoMatch(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.CreateFolder(ctx, userID, "test-uuid", nil, "Archives")
	require.NoError(t, err)

	found, err := svc.FindFoldersByName(ctx, userID, "xyz-not-found")
	require.NoError(t, err)
	assert.Empty(t, found)
}

// ─── FindNotesByTag ───────────────────────────────────────────────────────────

func TestService_FindNotesByTag_EmptyTags(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Note A", nil)
	require.NoError(t, err)

	all, err := svc.FindNotesByTag(ctx, userID, []string{})
	require.NoError(t, err)
	assert.Len(t, all, 1)
}

func TestService_FindNotesByTag_Matching(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	tags := []string{"go", "backend"}
	note, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Note A", tags)
	require.NoError(t, err)
	_ = note

	_, err = svc.CreateNote(ctx, userID, "test-uuid", nil, "Note B", []string{"frontend"})
	require.NoError(t, err)

	found, err := svc.FindNotesByTag(ctx, userID, []string{"go"})
	require.NoError(t, err)
	assert.Len(t, found, 1)
	assert.Equal(t, "Note A", found[0].Title)
}

func TestService_FindNotesByTag_AllTagsMustMatch(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Tagged Both", []string{"a", "b"})
	require.NoError(t, err)
	_, err = svc.CreateNote(ctx, userID, "test-uuid", nil, "Tagged A Only", []string{"a"})
	require.NoError(t, err)

	found, err := svc.FindNotesByTag(ctx, userID, []string{"a", "b"})
	require.NoError(t, err)
	assert.Len(t, found, 1)
	assert.Equal(t, "Tagged Both", found[0].Title)
}

func TestService_FindNotesByTag_NoMatch(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Untagged", nil)
	require.NoError(t, err)

	found, err := svc.FindNotesByTag(ctx, userID, []string{"nope"})
	require.NoError(t, err)
	assert.Empty(t, found)
}

// ─── ListAllTags ──────────────────────────────────────────────────────────────

func TestService_ListAllTags_Empty(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "No Tags", nil)
	require.NoError(t, err)

	tags, err := svc.ListAllTags(ctx, userID)
	require.NoError(t, err)
	assert.Empty(t, tags)
}

func TestService_ListAllTags_Sorted(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.CreateNote(ctx, userID, "test-uuid", nil, "Note Z", []string{"zebra", "apple"})
	require.NoError(t, err)
	_, err = svc.CreateNote(ctx, userID, "test-uuid", nil, "Note M", []string{"mango", "apple"})
	require.NoError(t, err)

	tags, err := svc.ListAllTags(ctx, userID)
	require.NoError(t, err)
	// Deduplication and sorting.
	assert.Contains(t, tags, "apple")
	assert.Contains(t, tags, "zebra")
	assert.Contains(t, tags, "mango")
	assert.Len(t, tags, 3)
	for i := 1; i < len(tags); i++ {
		assert.LessOrEqual(t, tags[i-1], tags[i], "tags should be sorted")
	}
}

// ─── ImportMarkdown ───────────────────────────────────────────────────────────

func TestService_ImportMarkdown_Simple(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	result, err := svc.ImportMarkdown(ctx, userID, "test-uuid", "hello.md", "# Hello\nWorld")
	require.NoError(t, err)
	assert.Equal(t, 1, result.NotesCreated)
}

func TestService_ImportMarkdown_EmptyTitle(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	// filename without meaningful name → fallback title
	result, err := svc.ImportMarkdown(ctx, userID, "test-uuid", ".md", "content")
	require.NoError(t, err)
	assert.Equal(t, 1, result.NotesCreated)
}

func TestService_ImportMarkdown_EmptyContent(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	result, err := svc.ImportMarkdown(ctx, userID, "test-uuid", "empty.md", "")
	require.NoError(t, err)
	assert.Equal(t, 1, result.NotesCreated)
}

// ─── ImportZip ────────────────────────────────────────────────────────────────

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := w.Create(name)
		require.NoError(t, err)
		_, err = f.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func TestService_ImportZip_SingleFile(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	data := makeZip(t, map[string]string{"note.md": "# Hello"})
	result, err := svc.ImportZip(ctx, userID, "test-uuid", data)
	require.NoError(t, err)
	assert.Equal(t, 1, result.NotesCreated)
	assert.Equal(t, 0, result.FoldersCreated)
}

func TestService_ImportZip_WithFolder(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	data := makeZip(t, map[string]string{
		"Work/todo.md":     "# Todo",
		"Work/meeting.md":  "# Meeting",
		"personal.md":      "# Personal",
	})
	result, err := svc.ImportZip(ctx, userID, "test-uuid", data)
	require.NoError(t, err)
	assert.Equal(t, 3, result.NotesCreated)
	assert.Equal(t, 1, result.FoldersCreated)
}

func TestService_ImportZip_NestedFolders(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	data := makeZip(t, map[string]string{
		"A/B/deep.md": "# Deep",
	})
	result, err := svc.ImportZip(ctx, userID, "test-uuid", data)
	require.NoError(t, err)
	assert.Equal(t, 1, result.NotesCreated)
	assert.Equal(t, 2, result.FoldersCreated)
}

func TestService_ImportZip_SkipsNonMarkdown(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	data := makeZip(t, map[string]string{
		"note.md":  "# Note",
		"img.png":  "\x89PNG",
		"data.txt": "text data",
	})
	result, err := svc.ImportZip(ctx, userID, "test-uuid", data)
	require.NoError(t, err)
	assert.Equal(t, 1, result.NotesCreated)
}

func TestService_ImportZip_SkipsDirectoryEntries(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	// Create an explicit directory entry.
	h := &zip.FileHeader{Name: "emptydir/", Method: zip.Deflate}
	h.SetMode(0755 | 1<<31) // set directory mode bit
	_, err := w.CreateHeader(h)
	require.NoError(t, err)
	f2, err := w.Create("emptydir/note.md")
	require.NoError(t, err)
	_, err = f2.Write([]byte("# Note"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	result, err := svc.ImportZip(ctx, userID, "test-uuid", buf.Bytes())
	require.NoError(t, err)
	assert.Equal(t, 1, result.NotesCreated)
}

func TestService_ImportZip_InvalidData(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	_, err := svc.ImportZip(ctx, userID, "test-uuid", []byte("not a zip"))
	require.Error(t, err)
}

func TestService_ImportZip_DuplicateNoteSkipped(t *testing.T) {
	svc, userID := newTestStack(t)
	ctx := context.Background()

	// First import creates the note.
	data1 := makeZip(t, map[string]string{"Work/todo.md": "# v1"})
	result1, err := svc.ImportZip(ctx, userID, "test-uuid", data1)
	require.NoError(t, err)
	assert.Equal(t, 1, result1.NotesCreated)

	// Second import with same folder/name → conflict, note skipped (not error).
	data2 := makeZip(t, map[string]string{"Work/todo.md": "# v2"})
	result2, err := svc.ImportZip(ctx, userID, "test-uuid", data2)
	require.NoError(t, err)
	assert.Equal(t, 0, result2.NotesCreated)
	assert.Equal(t, 0, result2.FoldersCreated) // folder already existed
}
