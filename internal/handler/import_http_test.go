package handler_test

// /api/v1/import at the HTTP boundary (ticket 472, bullet 2).
//
// The service-level tests in internal/notes pin what ImportMarkdown and
// ImportZip do with a name and a body. These pin what the *endpoint* does, which
// is a different set of promises and the ones a client actually depends on:
//
//   - the title a note ends up with is the "# H1" the upload's body states
//     (ticket 631) while the filename still decides the slug and the file that
//     is written — asserted through the API, not through the service;
//   - a zip recreates its directory tree as folders, and each note lands in the
//     folder that corresponds to its directory;
//   - a body over the 10 MB cap is refused with 413 before anything reaches the
//     notes root, so a rejected upload leaves no half-written file and no row;
//   - the endpoint is behind the session and CSRF middleware, so neither an
//     anonymous request nor a session without a token can write.
//
// Every test reads the file tree on disk as well as the API: the endpoint's job
// is to produce files, and an API-only assertion cannot tell where a note really
// went. The baseline is whatever registering the user left behind (a new account
// starts with a "Getting Started" welcome note), so assertions are on the delta
// this upload caused, never on an absolute count.

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noteSummary is the subset of GET /api/v1/notes/all this file asserts on.
type noteSummary struct {
	ID       int64    `json:"id"`
	FolderID *int64   `json:"folder_id"`
	Title    string   `json:"title"`
	Slug     string   `json:"slug"`
	Tags     []string `json:"tags"`
}

// folderSummary is GET /api/v1/folders' element shape.
type folderSummary struct {
	ID       int64  `json:"id"`
	ParentID *int64 `json:"parent_id"`
	Name     string `json:"name"`
	DiskPath string `json:"disk_path"`
}

func (c *testClient) listNotes(t *testing.T) []noteSummary {
	t.Helper()
	resp := c.do(t, http.MethodGet, "/api/v1/notes/all", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var notes []noteSummary
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&notes))
	return notes
}

func (c *testClient) listFolders(t *testing.T) []folderSummary {
	t.Helper()
	resp := c.do(t, http.MethodGet, "/api/v1/folders", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var folders []folderSummary
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&folders))
	return folders
}

// importResult is the documented response body of a successful import.
type importResult struct {
	NotesCreated   int `json:"notes_created"`
	FoldersCreated int `json:"folders_created"`
}

func decodeImportResult(t *testing.T, resp *http.Response) importResult {
	t.Helper()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out importResult
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}

// treeFiles lists every regular file under the notes root, relative and sorted —
// the disk's own answer to "what did this upload write?", which is what the
// "nothing was written" assertion needs.
func treeFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		out = append(out, rel)
		return nil
	})
	require.NoError(t, err)
	sort.Strings(out)
	return out
}

// newSince returns the notes that were not present in baseline, so a test can
// assert on what the upload it made created without caring that a new account
// arrives with a welcome note.
func newSince(all, baseline []noteSummary) []noteSummary {
	seen := map[int64]bool{}
	for _, n := range baseline {
		seen[n.ID] = true
	}
	var out []noteSummary
	for _, n := range all {
		if !seen[n.ID] {
			out = append(out, n)
		}
	}
	return out
}

// diffFiles returns the entries of after that are not in before.
func diffFiles(before, after []string) []string {
	seen := map[string]bool{}
	for _, b := range before {
		seen[b] = true
	}
	var out []string
	for _, a := range after {
		if !seen[a] {
			out = append(out, a)
		}
	}
	return out
}

// TestImportHTTP_MarkdownTitledFromHeading: the endpoint creates the note the
// body describes. Per 631 the note's title is the "# H1" its content states,
// while the filename decides the slug and the file that is written — so a
// client that renames a file on the way in does not lose the note's real title.
func TestImportHTTP_MarkdownTitledFromHeading(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)
	baselineNotes := c.listNotes(t)
	baselineFiles := treeFiles(t, c.notesDir)

	body := []byte("# The Quokka Papers\n\nField notes from the island.\n")
	resp := c.doMultipart(t, "/api/v1/import", "file", "clint-notes.md", body)
	defer resp.Body.Close()

	got := decodeImportResult(t, resp)
	assert.Equal(t, 1, got.NotesCreated, "one .md file is one note")
	assert.Equal(t, 0, got.FoldersCreated, "a root-level .md file creates no folder")

	created := newSince(c.listNotes(t), baselineNotes)
	require.Len(t, created, 1, "the upload must create exactly one note")
	assert.Equal(t, "The Quokka Papers", created[0].Title,
		"the title comes from the body's # H1, not the filename (ticket 631)")
	assert.Equal(t, "clint-notes", created[0].Slug,
		"the slug still comes from the filename — the file keeps the name it arrived with")
	assert.Nil(t, created[0].FolderID, "a single .md import is a root-level note")

	// The file is on disk, with the name and the bytes it arrived with.
	after := treeFiles(t, c.notesDir)
	require.Len(t, after, len(baselineFiles)+1,
		"the import must write exactly one new file: before %v, after %v", baselineFiles, after)
	written := diffFiles(baselineFiles, after)
	require.Len(t, written, 1)
	assert.Equal(t, "clint-notes.md", filepath.Base(written[0]))
	onDisk, err := os.ReadFile(filepath.Join(c.notesDir, written[0]))
	require.NoError(t, err)
	assert.Equal(t, string(body), string(onDisk), "the import must preserve the content exactly")
}

// TestImportHTTP_ZipRoundTripsTree: a small exported tree comes back as the same
// tree — the documented "exporting and re-importing is the supported way to move
// a folder between instances". Counts alone would not catch a nested note landing
// in the root or in its grandparent, so this asserts the shape: every folder with
// its parent, and every note in the folder its directory maps to.
func TestImportHTTP_ZipRoundTripsTree(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)
	baselineNotes := c.listNotes(t)

	zipData := buildZip(t, map[string]string{
		"Work/Projects/quokka.md": "# Quokka Survey\n\nIsland counts.",
		"Work/notes.md":           "# Work Notes\n\nWeekly.",
		"Personal/trips/tapir.md": "# Tapir Trip\n\nHighlands.",
	})
	resp := c.doMultipart(t, "/api/v1/import", "file", "export.zip", zipData)
	defer resp.Body.Close()

	got := decodeImportResult(t, resp)
	assert.Equal(t, 3, got.NotesCreated, "one note per .md file in the archive")
	assert.Equal(t, 4, got.FoldersCreated, "Work, Work/Projects, Personal, Personal/trips")

	folders := c.listFolders(t)
	require.Len(t, folders, 4, "the archive's two top directories and their two subdirectories")

	byName := map[string]folderSummary{}
	for _, f := range folders {
		byName[f.Name] = f
	}
	for _, want := range []string{"Work", "Projects", "Personal", "trips"} {
		_, ok := byName[want]
		assert.True(t, ok, "folder %q must exist; got %v", want, folders)
	}
	// The parent links are the tree: Projects under Work, trips under Personal.
	require.NotNil(t, byName["Projects"].ParentID)
	require.NotNil(t, byName["trips"].ParentID)
	assert.Equal(t, byName["Work"].ID, *byName["Projects"].ParentID,
		"Work/Projects must be a child of Work, not a root folder")
	assert.Equal(t, byName["Personal"].ID, *byName["trips"].ParentID,
		"Personal/trips must be a child of Personal, not a root folder")

	created := newSince(c.listNotes(t), baselineNotes)
	require.Len(t, created, 3)
	byTitle := map[string]noteSummary{}
	for _, n := range created {
		byTitle[n.Title] = n
	}
	for title, folder := range map[string]string{
		"Quokka Survey": "Projects",
		"Work Notes":    "Work",
		"Tapir Trip":    "trips",
	} {
		note, ok := byTitle[title]
		require.True(t, ok, "note %q must exist; got %v", title, byTitle)
		require.NotNil(t, note.FolderID, "note %q must live in a folder", title)
		assert.Equal(t, byName[folder].ID, *note.FolderID,
			"note %q must live in folder %q", title, folder)
	}
}

// TestImportHTTP_OversizedBodyRejectedAndNothingWritten pins the 10 MB cap and
// its status code. The previous test in integration_extra_test.go accepted
// "413 or 400", which cannot fail: it would pass even if the cap started
// returning 500, or if a partial import had already written a note. Here the
// status is asserted exactly and both sides of "nothing was written" are checked
// — the filesystem and the API's own note list.
func TestImportHTTP_OversizedBodyRejectedAndNothingWritten(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)
	baselineNotes := c.listNotes(t)
	baselineFiles := treeFiles(t, c.notesDir)

	// 11 MiB of content: over the 10 MiB cap by a clear margin, so the limit
	// trips inside the multipart parse rather than at a boundary the parser
	// could legitimately round.
	oversized := bytes.Repeat([]byte("A"), 11<<20)
	resp := c.doMultipart(t, "/api/v1/import", "file", "too-big.md", oversized)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode,
		"a body over the cap must be rejected with 413, not parsed and rejected later")

	var errBody map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errBody))
	assert.Contains(t, errBody["error"], "10 MB", "the refusal must say what the limit is")

	assert.Equal(t, baselineFiles, treeFiles(t, c.notesDir),
		"a rejected upload must not leave a file in the notes root")
	assert.Len(t, newSince(c.listNotes(t), baselineNotes), 0,
		"a rejected upload must not create a note row")
}

// TestImportHTTP_RequiresSessionAndCSRF: the endpoint writes files, so it is
// behind both middlewares. A request with no session is 401 (the client is not
// logged in); a request with a session but no CSRF token is 403 (the request is
// not the app's). Neither may import anything.
func TestImportHTTP_RequiresSessionAndCSRF(t *testing.T) {
	c := newTestClient(t)

	// ── no session at all ────────────────────────────────────────────────────
	anonFiles := treeFiles(t, c.notesDir)
	anon := c.postMultipartRaw(t, "/api/v1/import", "file", "anon.md",
		[]byte("# Anonymous\n\nshould not be stored"), nil, "")
	defer anon.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, anon.StatusCode,
		"an unauthenticated import must be refused")
	assert.Equal(t, anonFiles, treeFiles(t, c.notesDir),
		"an unauthenticated import must write nothing")

	// ── a real session, no CSRF token ────────────────────────────────────────
	c.registerAndLogin(t)
	baselineNotes := c.listNotes(t)
	baselineFiles := treeFiles(t, c.notesDir)

	noCSRF := c.postMultipartRaw(t, "/api/v1/import", "file", "no-csrf.md",
		[]byte("# No CSRF\n\nshould not be stored"), c.cookies, "")
	defer noCSRF.Body.Close()
	assert.Equal(t, http.StatusForbidden, noCSRF.StatusCode,
		"a session without a CSRF token must be refused with 403")

	var errBody map[string]string
	require.NoError(t, json.NewDecoder(noCSRF.Body).Decode(&errBody))
	assert.Equal(t, "csrf_invalid", errBody["code"], "the refusal must be machine-readable")

	assert.Equal(t, baselineFiles, treeFiles(t, c.notesDir),
		"a CSRF-refused import must write nothing")
	assert.Len(t, newSince(c.listNotes(t), baselineNotes), 0,
		"a CSRF-refused import must create no note row")

	// ── the same request with the token is accepted ──────────────────────────
	// The negative cases above are only meaningful if the positive one works,
	// otherwise a broken route would look like correct enforcement.
	ok := c.postMultipartRaw(t, "/api/v1/import", "file", "with-csrf.md",
		[]byte("# With CSRF\n\nstored"), c.cookies, c.csrfToken)
	defer ok.Body.Close()
	assert.Equal(t, http.StatusOK, ok.StatusCode)
	created := newSince(c.listNotes(t), baselineNotes)
	require.Len(t, created, 1)
	assert.Equal(t, "With CSRF", created[0].Title)
}

// postMultipartRaw posts a multipart form with explicit control over the cookies
// and the CSRF header, which doMultipart fixes from the client's own state.
func (c *testClient) postMultipartRaw(t *testing.T, path, fieldname, filename string, content []byte, cookies []*http.Cookie, csrf string) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile(fieldname, filename)
	require.NoError(t, err)
	_, err = fw.Write(content)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	req, err := http.NewRequest(http.MethodPost, c.server.URL+path, &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", w.FormDataContentType())
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := c.httpClient.Do(req)
	require.NoError(t, err)
	return resp
}
