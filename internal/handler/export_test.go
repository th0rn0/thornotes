package handler_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/model"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func zipExportCreateFolder(t *testing.T, c *testClient, parentID *int64, name string) int64 {
	t.Helper()
	resp := c.do(t, http.MethodPost, "/api/v1/folders", map[string]any{
		"parent_id": parentID,
		"name":      name,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var folder model.Folder
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&folder))
	return folder.ID
}

// zipExportCreateNote creates a note with body, then re-reads it so the returned
// row carries the content_hash and updated_at the export must agree with.
func zipExportCreateNote(t *testing.T, c *testClient, folderID *int64, title, content string) model.Note {
	t.Helper()
	resp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]any{
		"folder_id": folderID,
		"title":     title,
		"tags":      []string{},
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var note model.Note
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&note))

	patch := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(note.ID), map[string]any{
		"content":      content,
		"content_hash": note.ContentHash,
	})
	defer patch.Body.Close()
	require.Equal(t, http.StatusOK, patch.StatusCode)

	get := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(note.ID), nil)
	defer get.Body.Close()
	require.Equal(t, http.StatusOK, get.StatusCode)
	require.NoError(t, json.NewDecoder(get.Body).Decode(&note))
	return note
}

type zipArchive struct {
	names   []string
	content map[string][]byte
	mods    map[string]time.Time
	raw     []byte
}

// zipExportGet fetches an archive and asserts the response contract before
// parsing it: 200, application/zip, an attachment filename, and a body that is
// actually a valid zip.
func zipExportGet(t *testing.T, c *testClient, path string) zipArchive {
	t.Helper()
	resp := c.do(t, http.MethodGet, path, nil)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/zip", resp.Header.Get("Content-Type"))
	assert.Contains(t, resp.Header.Get("Content-Disposition"), "attachment; filename=")

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NotEmpty(t, raw)

	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	require.NoError(t, err, "the response body must be a readable zip archive")

	archive := zipArchive{content: map[string][]byte{}, mods: map[string]time.Time{}, raw: raw}
	for _, f := range zr.File {
		archive.names = append(archive.names, f.Name)
		archive.mods[f.Name] = f.Modified
		rc, err := f.Open()
		require.NoError(t, err)
		data, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		archive.content[f.Name] = data
	}
	return archive
}

func zipExportDisposition(resp *http.Response) string {
	return resp.Header.Get("Content-Disposition")
}

// zipExportTree snapshots a user's notes as folder-path → title → content, using
// only the public API. Folder paths are relative to the user's UUID directory so
// two instances — which have different UUIDs — can be compared directly.
func zipExportTree(t *testing.T, c *testClient) (map[string]map[string]string, []string) {
	t.Helper()

	resp := c.do(t, http.MethodGet, "/api/v1/folders", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var folders []model.FolderTreeItem
	decodeInto(t, resp, &folders)

	folderPaths := map[int64]string{}
	for _, f := range folders {
		folderPaths[f.ID] = strings.Join(strings.Split(f.DiskPath, "/")[1:], "/")
	}

	tree := map[string]map[string]string{}
	var paths []string
	for _, f := range folders {
		paths = append(paths, folderPaths[f.ID])
	}

	add := func(folderPath string, notes []model.NoteListItem) {
		if len(notes) == 0 {
			return
		}
		if tree[folderPath] == nil {
			tree[folderPath] = map[string]string{}
		}
		for _, item := range notes {
			resp := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(item.ID), nil)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			var note model.Note
			decodeInto(t, resp, &note)
			tree[folderPath][note.Title] = note.Content
		}
	}

	rootResp := c.do(t, http.MethodGet, "/api/v1/notes/root", nil)
	require.Equal(t, http.StatusOK, rootResp.StatusCode)
	var rootNotes []model.NoteListItem
	decodeInto(t, rootResp, &rootNotes)
	add("", rootNotes)

	for _, f := range folders {
		listResp := c.do(t, http.MethodGet, "/api/v1/folders/"+i64str(f.ID)+"/notes", nil)
		require.Equal(t, http.StatusOK, listResp.StatusCode)
		var notes []model.NoteListItem
		decodeInto(t, listResp, &notes)
		add(folderPaths[f.ID], notes)
	}

	sort.Strings(paths)
	return tree, paths
}

// ── folder export: entries, names, order, empty folder ────────────────────────

func TestHandler_ExportFolderZip_ThreeLevelTree(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	work := zipExportCreateFolder(t, c, nil, "work")
	projects := zipExportCreateFolder(t, c, &work, "Projects")
	q3 := zipExportCreateFolder(t, c, &projects, "Q3")
	zipExportCreateFolder(t, c, &work, "Empty") // no notes, no children

	daily := zipExportCreateNote(t, c, &work, "Daily log", "# Daily log\n\nStandup at nine.\n")
	roadmap := zipExportCreateNote(t, c, &projects, "Roadmap", "# Roadmap\n\nQ3 and Q4.\n")
	plan := zipExportCreateNote(t, c, &q3, "Plan", "# Plan\n\nShip the export.\n")

	resp := c.do(t, http.MethodGet, "/api/v1/folders/"+i64str(work)+"/zip", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, `attachment; filename="work.zip"`, zipExportDisposition(resp))
	resp.Body.Close()

	archive := zipExportGet(t, c, "/api/v1/folders/"+i64str(work)+"/zip")

	// The root folder is the archive's top-level directory, one entry per folder
	// (including the empty one), and entries sort by path — "work/" before
	// "work/Daily log.md" before "work/Empty/" before "work/Projects/…".
	want := []string{
		"work/",
		"work/Daily log.md",
		"work/Empty/",
		"work/Projects/",
		"work/Projects/Q3/",
		"work/Projects/Q3/Plan.md",
		"work/Projects/Roadmap.md",
	}
	assert.Equal(t, want, archive.names, "entries and their order are part of the contract")

	// Each note carries exactly the bytes on disk.
	assert.Equal(t, "# Daily log\n\nStandup at nine.\n", string(archive.content["work/Daily log.md"]))
	assert.Equal(t, "# Roadmap\n\nQ3 and Q4.\n", string(archive.content["work/Projects/Roadmap.md"]))
	assert.Equal(t, "# Plan\n\nShip the export.\n", string(archive.content["work/Projects/Q3/Plan.md"]))

	// Each entry's modified time comes from the note's updated_at.
	for entry, note := range map[string]model.Note{
		"work/Daily log.md":        daily,
		"work/Projects/Roadmap.md": roadmap,
		"work/Projects/Q3/Plan.md": plan,
	} {
		assert.Equal(t, note.UpdatedAt.Unix(), archive.mods[entry].Unix(),
			"entry %q must carry the note's updated_at", entry)
	}

	// Exporting the same tree twice produces the same entries in the same order.
	again := zipExportGet(t, c, "/api/v1/folders/"+i64str(work)+"/zip")
	assert.Equal(t, archive.names, again.names, "a second export must be identical in entries and order")
	for name, want := range archive.content {
		assert.Equal(t, want, again.content[name], "entry %q content changed between exports", name)
	}

	// An empty folder is a folder entry, not an error.
	empty := zipExportGet(t, c, "/api/v1/folders/"+i64str(q3)+"/zip")
	assert.Equal(t, []string{"Q3/", "Q3/Plan.md"}, empty.names)
}

func TestHandler_ExportFolderZip_EmptyFolderIsJustItsDirectoryEntry(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	empty := zipExportCreateFolder(t, c, nil, "Empty")

	archive := zipExportGet(t, c, "/api/v1/folders/"+i64str(empty)+"/zip")
	assert.Equal(t, []string{"Empty/"}, archive.names)
}

// ── note export: one entry, sanitised name, content from disk ────────────────

func TestHandler_ExportNoteZip_OneEntryByteIdenticalToDisk(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	content := "# Ünïcode café\n\nLinks to [[Some Other Note]] stay literal.\n\n```go\nfunc main() {\n\tprintln(\"hi\")\n}\n```\n"
	note := zipExportCreateNote(t, c, nil, "Kickoff", content)

	diskBytes, err := os.ReadFile(filepath.Join(c.notesDir, note.DiskPath))
	require.NoError(t, err)
	require.Equal(t, content, string(diskBytes), "the note file on disk is the source of the archive")

	archive := zipExportGet(t, c, "/api/v1/notes/"+i64str(note.ID)+"/zip")
	assert.Equal(t, []string{"Kickoff.md"}, archive.names, "a note export is one entry")
	assert.Equal(t, diskBytes, archive.content["Kickoff.md"],
		"the archive must carry the bytes on disk — unicode, wikilink and code fence included")
	assert.Equal(t, note.UpdatedAt.Unix(), archive.mods["Kickoff.md"].Unix())
}

func TestHandler_ExportNoteZip_TitleWithSeparatorStaysOneEntry(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	note := zipExportCreateNote(t, c, nil, "Q3/Q4 plan", "one note, not a directory\n")

	archive := zipExportGet(t, c, "/api/v1/notes/"+i64str(note.ID)+"/zip")
	assert.Equal(t, []string{"Q3 Q4 plan.md"}, archive.names,
		"a title must never introduce a path separator")
	assert.Len(t, archive.names, 1)
}

func TestHandler_ExportNoteZip_LeadingDotsAndUnicodeNames(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	hidden := zipExportCreateNote(t, c, nil, "  .hidden stuff  ", "dot names are trimmed\n")
	archive := zipExportGet(t, c, "/api/v1/notes/"+i64str(hidden.ID)+"/zip")
	assert.Equal(t, []string{"hidden stuff.md"}, archive.names)

	unicode := zipExportCreateNote(t, c, nil, "日本語", "no ascii survives\n")
	archive = zipExportGet(t, c, "/api/v1/notes/"+i64str(unicode.ID)+"/zip")
	assert.Equal(t, []string{"untitled.md"}, archive.names,
		"a title with no usable characters still yields a usable entry name")
}

// ── 404 equality: not yours and not there are the same answer ────────────────

func TestHandler_Export_NotFoundIsIdenticalForForeignAndMissing(t *testing.T) {
	f := newIsolationFixture(t) // fixture leaves the session logged in as bob; resources are alice's

	f.asAlice(t)
	noteID := f.noteID
	folderID := f.folderID

	// Bob asks for alice's note and folder, and for ids that do not exist.
	f.client.login(t, "bob", isolationBobPassword)

	fetch := func(path string) (int, string, string) {
		resp := f.client.do(t, http.MethodGet, path, nil)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(body)
	}

	for _, route := range []struct {
		name    string
		foreign string
		missing string
	}{
		{"note", "/api/v1/notes/" + i64str(noteID) + "/zip", "/api/v1/notes/999999/zip"},
		{"folder", "/api/v1/folders/" + i64str(folderID) + "/zip", "/api/v1/folders/999999/zip"},
	} {
		foreignStatus, foreignType, foreignBody := fetch(route.foreign)
		missingStatus, missingType, missingBody := fetch(route.missing)

		assert.Equal(t, http.StatusNotFound, foreignStatus, "%s: another user's id must answer 404", route.name)
		assert.Equal(t, http.StatusNotFound, missingStatus, "%s: a missing id must answer 404", route.name)
		assert.Equal(t, missingStatus, foreignStatus)
		assert.Equal(t, missingType, foreignType)
		assert.Equal(t, missingBody, foreignBody,
			"%s: not-yours and not-there must be byte-identical responses", route.name)
		assert.NotContains(t, foreignBody, "zip", "a 404 must not look like a download")
	}
}

// ── missing files: skip the entry, never silently ────────────────────────────

func TestHandler_Export_ReportsMissingFiles(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	work := zipExportCreateFolder(t, c, nil, "work")
	present := zipExportCreateNote(t, c, &work, "Present", "still here\n")
	gone := zipExportCreateNote(t, c, &work, "Gone", "row survives, file does not\n")
	require.NotEqual(t, present.DiskPath, gone.DiskPath)

	// A half-finished delete: the row is there, the file is not.
	require.NoError(t, os.Remove(filepath.Join(c.notesDir, gone.DiskPath)))

	archive := zipExportGet(t, c, "/api/v1/folders/"+i64str(work)+"/zip")
	assert.Equal(t, []string{"_export-report.txt", "work/", "work/Present.md"}, archive.names,
		"the missing note is left out and the report takes its sorted position")

	report := string(archive.content["_export-report.txt"])
	assert.Contains(t, report, "work/Gone.md", "the report must name the entry that was skipped")
	assert.Contains(t, report, gone.DiskPath, "the report must name the file that is missing")

	// A single-note export of the same row reports instead of erroring.
	single := zipExportGet(t, c, "/api/v1/notes/"+i64str(gone.ID)+"/zip")
	require.Equal(t, []string{"_export-report.txt"}, single.names)
	assert.Contains(t, string(single.content["_export-report.txt"]), "Gone.md")
}

// ── names: sanitising collisions get -2, -3 ──────────────────────────────────

func TestHandler_ExportFolderZip_SanitisedNameCollisionsGetSuffixes(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	root := zipExportCreateFolder(t, c, nil, "Notes")
	zipExportCreateFolder(t, c, &root, "Caf?") // sanitises to "Caf"
	zipExportCreateFolder(t, c, &root, "Café") // also sanitises to "Caf"

	archive := zipExportGet(t, c, "/api/v1/folders/"+i64str(root)+"/zip")
	assert.Equal(t, []string{"Notes/", "Notes/Caf-2/", "Notes/Caf/"}, archive.names,
		"two folders that sanitise to one name must both survive, deterministically")
}

// ── cycles: a looping parent chain must not make the walk infinite ───────────

func TestHandler_ExportFolderZip_CyclicParentChainTerminates(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	parent := zipExportCreateFolder(t, c, nil, "Parent")
	child := zipExportCreateFolder(t, c, &parent, "Child")
	zipExportCreateNote(t, c, &parent, "Outside", "body\n")
	zipExportCreateNote(t, c, &child, "Inside", "body\n")

	// Force the impossible state directly: a parent chain that loops. Rows can
	// end up like this from a half-applied move or a hand-edited database, and
	// the export has to terminate and emit each folder once.
	_, err := c.pool.WriteDB.Exec(`UPDATE folders SET parent_id = ? WHERE id = ?`, child, parent)
	require.NoError(t, err)

	archive := zipExportGet(t, c, "/api/v1/folders/"+i64str(parent)+"/zip")
	assert.Equal(t, []string{
		"Parent/",
		"Parent/Child/",
		"Parent/Child/Inside.md",
		"Parent/Outside.md",
	}, archive.names)
}

// ── the acceptance test that matters: export → import → compare ──────────────

// TestHandler_ExportFolderZip_RoundTrip exports a three-level tree, imports the
// archive into a fresh instance through the existing import path, and compares
// the restored tree to the original. A zip that opens is not a zip that
// restores: the export format is also the import format, so titles, structure
// and content all have to survive the trip.
//
// One caveat is pinned explicitly below: the importer recreates folders from the
// directories of .md entries and ignores directory entries, so an empty folder
// does not come back. That is existing import behaviour and out of scope here;
// the assertion documents it rather than hiding it.
func TestHandler_ExportFolderZip_RoundTrip(t *testing.T) {
	source := newTestClient(t)
	source.registerAndLogin(t)

	work := zipExportCreateFolder(t, source, nil, "work")
	projects := zipExportCreateFolder(t, source, &work, "Projects")
	q3 := zipExportCreateFolder(t, source, &projects, "Q3")
	zipExportCreateFolder(t, source, &work, "Empty")

	dailyContent := "# Daily log\n\nÜnïcode café ☕ and a [[wikilink]].\n\n```js\nconst x = 1;\n```\n"
	zipExportCreateNote(t, source, &work, "Daily log", dailyContent)
	zipExportCreateNote(t, source, &projects, "Roadmap", "roadmap body\n")
	zipExportCreateNote(t, source, &q3, "Plan", "plan body\n")

	origTree, origFolders := zipExportTree(t, source)
	// The instance's own "Getting Started" seed note lives at the root and is not
	// part of this export; compare the exported subtree only.
	delete(origTree, "")
	require.Equal(t, map[string]map[string]string{
		"work":             {"Daily log": dailyContent},
		"work/Projects":    {"Roadmap": "roadmap body\n"},
		"work/Projects/Q3": {"Plan": "plan body\n"},
	}, origTree)

	archive := zipExportGet(t, source, "/api/v1/folders/"+i64str(work)+"/zip")

	// Import into a brand-new instance through the existing endpoint.
	target := newTestClient(t)
	target.registerAndLogin(t)
	importResp := target.doMultipart(t, "/api/v1/import", "file", "work.zip", archive.raw)
	defer importResp.Body.Close()
	require.Equal(t, http.StatusOK, importResp.StatusCode)

	var result struct {
		NotesCreated   int `json:"notes_created"`
		FoldersCreated int `json:"folders_created"`
	}
	require.NoError(t, json.NewDecoder(importResp.Body).Decode(&result))
	assert.Equal(t, 3, result.NotesCreated, "every note in the archive must be recreated")
	assert.Equal(t, 3, result.FoldersCreated,
		"work, Projects and Q3 are recreated; the empty folder is not, because the importer creates folders from .md paths only")

	imported, importedFolders := zipExportTree(t, target)
	delete(imported, "") // the target's own seed note, not from the archive

	// Titles, structure and content all match for every folder that carries notes.
	assert.Equal(t, origTree, imported, "the restored tree must match the original note-for-note")

	// And the non-empty folder structure matches exactly.
	var wantFolders []string
	for _, p := range origFolders {
		if p != "work/Empty" {
			wantFolders = append(wantFolders, p)
		}
	}
	gotFolders := importedFolders
	assert.Equal(t, wantFolders, gotFolders,
		"folder structure round-trips apart from the empty folder, which the existing importer does not recreate")
	assert.NotContains(t, gotFolders, "work/Empty",
		"pinning current import behaviour: directory entries are ignored, so empty folders are lost")
}
