package handler_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/model"
)

// Multi-user isolation (IDOR) tests.
//
// Every test creates resources as "alice", switches the session to "bob", and
// then attempts to read, modify, move, share or delete alice's resources, or to
// observe them through list/search/context endpoints.
//
// The contract under test: a session may only see or change rows belonging to
// its own user_id. A missing row and a row owned by somebody else are reported
// identically (404), so the API does not leak existence.
//
// Each test also performs a positive control as the owner, so a blanket 404
// regression (e.g. middleware that rejects everything) cannot make the suite
// pass.

const (
	isolationAlicePassword = "correct-horse-battery-alice"
	isolationBobPassword   = "correct-horse-battery-bob"
	isolationSecretTerm    = "alice-secret-term"
)

// isolationFixture is one server with two accounts and one resource of each kind
// owned by alice. After newIsolationFixture returns, the session is bob's.
type isolationFixture struct {
	client    *testClient
	noteID    int64
	folderID  int64
	journalID int64
}

func isolationLogin(t *testing.T, c *testClient, username string) {
	t.Helper()
	password := isolationAlicePassword
	if username != "alice" {
		password = isolationBobPassword
	}
	c.register(t, username, password)
	c.login(t, username, password)
}

func decodeInto[T any](t *testing.T, resp *http.Response, out *T) {
	t.Helper()
	defer resp.Body.Close()
	require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
}

func isolationCreateNote(t *testing.T, c *testClient, folderID *int64, title string) int64 {
	t.Helper()
	resp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]any{
		"folder_id": folderID,
		"title":     title,
		"tags":      []string{},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var note model.Note
	decodeInto(t, resp, &note)
	require.NotZero(t, note.ID)
	return note.ID
}

// isolationWriteContent replaces a note's body as its owner, so list/search
// assertions have a distinctive string to look for.
func isolationWriteContent(t *testing.T, c *testClient, noteID int64, content string) {
	t.Helper()
	resp := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(noteID), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var note model.Note
	decodeInto(t, resp, &note)

	resp = c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(noteID), map[string]any{
		"content":      content,
		"content_hash": note.ContentHash,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

func isolationCreateFolder(t *testing.T, c *testClient, name string) int64 {
	t.Helper()
	resp := c.do(t, http.MethodPost, "/api/v1/folders", map[string]any{"name": name})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var folder model.Folder
	decodeInto(t, resp, &folder)
	require.NotZero(t, folder.ID)
	return folder.ID
}

func isolationCreateJournal(t *testing.T, c *testClient, name string) int64 {
	t.Helper()
	resp := c.do(t, http.MethodPost, "/api/v1/journals", map[string]any{"name": name})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var journal model.Journal
	decodeInto(t, resp, &journal)
	require.NotZero(t, journal.ID)
	return journal.ID
}

func newIsolationFixture(t *testing.T) *isolationFixture {
	t.Helper()
	c := newTestClient(t)

	isolationLogin(t, c, "alice")
	folderID := isolationCreateFolder(t, c, "Alice folder")
	noteID := isolationCreateNote(t, c, &folderID, "Alice private note")
	isolationWriteContent(t, c, noteID, "# Alice\n\n"+isolationSecretTerm+"\n")
	journalID := isolationCreateJournal(t, c, "Alice journal")

	// Switch to the second account. Registration is open on this test server,
	// so bob gets an account of his own and a fresh session.
	isolationLogin(t, c, "bob")

	return &isolationFixture{
		client:    c,
		noteID:    noteID,
		folderID:  folderID,
		journalID: journalID,
	}
}

func (f *isolationFixture) asAlice(t *testing.T) {
	t.Helper()
	f.client.login(t, "alice", isolationAlicePassword)
}

// ─── Notes ───────────────────────────────────────────────────────────────────

func TestIsolation_GetNote_OtherUserDenied(t *testing.T) {
	f := newIsolationFixture(t)

	resp := f.client.do(t, http.MethodGet, "/api/v1/notes/"+i64str(f.noteID), nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "bob must not read alice's note")
	resp.Body.Close()

	// Positive control: the owner still can.
	f.asAlice(t)
	resp = f.client.do(t, http.MethodGet, "/api/v1/notes/"+i64str(f.noteID), nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "alice must still read her own note")
	resp.Body.Close()
}

func TestIsolation_PatchNoteContent_OtherUserDenied(t *testing.T) {
	f := newIsolationFixture(t)

	// bob supplies the real hash — ownership, not a stale hash, must reject him.
	f.asAlice(t)
	resp := f.client.do(t, http.MethodGet, "/api/v1/notes/"+i64str(f.noteID), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var note model.Note
	decodeInto(t, resp, &note)
	// login() as bob via the fixture helper would also re-register; use login.
	f.client.login(t, "bob", isolationBobPassword)

	resp = f.client.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(f.noteID), map[string]any{
		"content":      "hijacked by bob",
		"content_hash": note.ContentHash,
	})
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "bob must not write alice's note")
	resp.Body.Close()

	f.asAlice(t)
	resp = f.client.do(t, http.MethodGet, "/api/v1/notes/"+i64str(f.noteID), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	decodeInto(t, resp, &note)
	assert.NotContains(t, note.Content, "hijacked by bob", "alice's content must be unchanged")
	assert.Contains(t, note.Content, isolationSecretTerm)
}

func TestIsolation_PatchNoteMetadata_OtherUserDenied(t *testing.T) {
	f := newIsolationFixture(t)

	resp := f.client.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(f.noteID), map[string]any{
		"title": "Renamed by bob",
	})
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()

	f.asAlice(t)
	resp = f.client.do(t, http.MethodGet, "/api/v1/notes/"+i64str(f.noteID), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var note model.Note
	decodeInto(t, resp, &note)
	assert.Equal(t, "Alice private note", note.Title)
}

func TestIsolation_DeleteNote_OtherUserDenied(t *testing.T) {
	f := newIsolationFixture(t)

	resp := f.client.do(t, http.MethodDelete, "/api/v1/notes/"+i64str(f.noteID), nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "bob must not delete alice's note")
	resp.Body.Close()

	f.asAlice(t)
	resp = f.client.do(t, http.MethodGet, "/api/v1/notes/"+i64str(f.noteID), nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "alice's note must survive bob's delete")
	resp.Body.Close()
}

func TestIsolation_MoveNote_OtherUserDenied(t *testing.T) {
	f := newIsolationFixture(t)

	resp := f.client.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(f.noteID)+"/move", map[string]any{
		"folder_id": nil,
	})
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "bob must not move alice's note")
	resp.Body.Close()

	f.asAlice(t)
	resp = f.client.do(t, http.MethodGet, "/api/v1/notes/"+i64str(f.noteID), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var note model.Note
	decodeInto(t, resp, &note)
	require.NotNil(t, note.FolderID)
	assert.Equal(t, f.folderID, *note.FolderID, "note must still be in alice's folder")
}

func TestIsolation_ShareNote_OtherUserDoesNotPersist(t *testing.T) {
	f := newIsolationFixture(t)

	// The security property is that bob cannot publish alice's note. The
	// endpoint currently answers 200 even when the UPDATE matches no row
	// (repository/sqlite/notes.go SetShareToken ignores RowsAffected), so this
	// test asserts the persisted state that matters rather than the status code.
	// Recorded as a follow-up: it should answer 404.
	resp := f.client.do(t, http.MethodPost, "/api/v1/notes/"+i64str(f.noteID)+"/share", map[string]any{})
	assert.Contains(t, []int{http.StatusOK, http.StatusNotFound}, resp.StatusCode,
		"bob's share attempt must not fail with a server error")
	resp.Body.Close()

	f.asAlice(t)
	resp = f.client.do(t, http.MethodGet, "/api/v1/notes/"+i64str(f.noteID), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var note model.Note
	decodeInto(t, resp, &note)
	assert.Nil(t, note.ShareToken, "no share token may be persisted after bob's attempt")
}

// ─── Folders ─────────────────────────────────────────────────────────────────

func TestIsolation_RenameFolder_OtherUserDenied(t *testing.T) {
	f := newIsolationFixture(t)

	resp := f.client.do(t, http.MethodPatch, "/api/v1/folders/"+i64str(f.folderID), map[string]any{
		"name": "Renamed by bob",
	})
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()

	f.asAlice(t)
	resp = f.client.do(t, http.MethodGet, "/api/v1/folders", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	defer resp.Body.Close()
	var folders json.RawMessage
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&folders))
	assert.NotContains(t, string(folders), "Renamed by bob")
	assert.Contains(t, string(folders), "Alice folder")
}

func TestIsolation_DeleteFolder_OtherUserDenied(t *testing.T) {
	f := newIsolationFixture(t)

	resp := f.client.do(t, http.MethodDelete, "/api/v1/folders/"+i64str(f.folderID), nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()

	f.asAlice(t)
	resp = f.client.do(t, http.MethodGet, "/api/v1/folders/"+i64str(f.folderID)+"/notes", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "alice's folder must survive bob's delete")
	resp.Body.Close()
}

func TestIsolation_ListFolderNotes_DoesNotLeak(t *testing.T) {
	f := newIsolationFixture(t)

	// The handler currently answers 200 with an empty list for a folder that is
	// not the caller's (ListByFolder filters on user_id), so accept 404 or an
	// empty body — but never alice's note.
	resp := f.client.do(t, http.MethodGet, "/api/v1/folders/"+i64str(f.folderID)+"/notes", nil)
	if resp.StatusCode == http.StatusOK {
		var items []model.NoteListItem
		decodeInto(t, resp, &items)
		assert.Empty(t, items, "bob must not see notes inside alice's folder")
	} else {
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "bob must not list alice's folder")
		resp.Body.Close()
	}

	f.asAlice(t)
	resp = f.client.do(t, http.MethodGet, "/api/v1/folders/"+i64str(f.folderID)+"/notes", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var items []model.NoteListItem
	decodeInto(t, resp, &items)
	require.Len(t, items, 1, "alice must still see her own note in her folder")
	assert.Equal(t, "Alice private note", items[0].Title)
}

// ─── Read paths that must not leak ───────────────────────────────────────────

func TestIsolation_Search_DoesNotLeak(t *testing.T) {
	f := newIsolationFixture(t)

	resp := f.client.do(t, http.MethodGet, "/api/v1/notes?q="+isolationSecretTerm, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var results []model.SearchResult
	decodeInto(t, resp, &results)
	assert.Empty(t, results, "bob's search must not return alice's note")

	// Positive control: alice's own search does find it.
	f.asAlice(t)
	resp = f.client.do(t, http.MethodGet, "/api/v1/notes?q="+isolationSecretTerm, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	decodeInto(t, resp, &results)
	require.Len(t, results, 1)
	assert.Equal(t, f.noteID, results[0].NoteID)
}

func TestIsolation_ListAll_DoesNotLeak(t *testing.T) {
	f := newIsolationFixture(t)

	resp := f.client.do(t, http.MethodGet, "/api/v1/notes/all", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body json.RawMessage
	decodeInto(t, resp, &body)
	assert.NotContains(t, string(body), "Alice private note")

	f.asAlice(t)
	resp = f.client.do(t, http.MethodGet, "/api/v1/notes/all", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	decodeInto(t, resp, &body)
	assert.Contains(t, string(body), "Alice private note")
}

func TestIsolation_Context_DoesNotLeak(t *testing.T) {
	f := newIsolationFixture(t)

	resp := f.client.do(t, http.MethodGet, "/api/v1/notes/context?folder_id="+i64str(f.folderID), nil)
	// Either refused (404) or an empty document — but never alice's content.
	if resp.StatusCode == http.StatusOK {
		var body json.RawMessage
		decodeInto(t, resp, &body)
		assert.NotContains(t, string(body), isolationSecretTerm)
	} else {
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		resp.Body.Close()
	}

	f.asAlice(t)
	resp = f.client.do(t, http.MethodGet, "/api/v1/notes/context?folder_id="+i64str(f.folderID), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body json.RawMessage
	decodeInto(t, resp, &body)
	assert.Contains(t, string(body), isolationSecretTerm)
}

// ─── Journals ────────────────────────────────────────────────────────────────

func TestIsolation_DeleteJournal_OtherUserDenied(t *testing.T) {
	f := newIsolationFixture(t)

	resp := f.client.do(t, http.MethodDelete, "/api/v1/journals/"+i64str(f.journalID), nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()

	f.asAlice(t)
	resp = f.client.do(t, http.MethodGet, "/api/v1/journals", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body json.RawMessage
	decodeInto(t, resp, &body)
	assert.Contains(t, string(body), "Alice journal")
}

func TestIsolation_JournalToday_OtherUserDenied(t *testing.T) {
	f := newIsolationFixture(t)

	resp := f.client.do(t, http.MethodGet, "/api/v1/journals/"+i64str(f.journalID)+"/today", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "bob must not open alice's journal")
	resp.Body.Close()

	f.asAlice(t)
	resp = f.client.do(t, http.MethodGet, "/api/v1/journals/"+i64str(f.journalID)+"/today", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}
