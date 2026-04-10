package handler_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── multipart helper ──────────────────────────────────────────────────────────

func (c *testClient) doMultipart(t *testing.T, path, fieldname, filename string, content []byte) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile(fieldname, filename)
	require.NoError(t, err)
	_, err = fw.Write(content)
	require.NoError(t, err)
	w.Close()

	req, err := http.NewRequest(http.MethodPost, c.server.URL+path, &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", w.FormDataContentType())
	for _, cookie := range c.cookies {
		req.AddCookie(cookie)
	}
	if c.csrfToken != "" {
		req.Header.Set("X-CSRF-Token", c.csrfToken)
	}
	resp, err := c.httpClient.Do(req)
	require.NoError(t, err)
	return resp
}

func buildZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		f, err := zw.Create(name)
		require.NoError(t, err)
		_, err = f.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// ── Move note ─────────────────────────────────────────────────────────────────

func TestHandler_MoveNote(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	noteID := createNoteHelper(t, c, "Nomadic Note")
	folderID := createFolderHelper(t, c, "Destination")

	resp := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(noteID)+"/move",
		map[string]interface{}{"folder_id": folderID})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify the note now lives in the destination folder.
	listResp := c.do(t, http.MethodGet, "/api/v1/folders/"+i64str(folderID)+"/notes", nil)
	defer listResp.Body.Close()
	var items []map[string]interface{}
	require.NoError(t, json.NewDecoder(listResp.Body).Decode(&items))
	found := false
	for _, item := range items {
		if int64(item["id"].(float64)) == noteID {
			found = true
		}
	}
	assert.True(t, found, "moved note should appear in destination folder")
}

func TestHandler_MoveNote_ToRoot(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	folderID := createFolderHelper(t, c, "Origin")
	// Create the note inside the folder.
	resp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{
		"title":     "Back to Root",
		"folder_id": folderID,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var note map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&note))
	noteID := int64(note["id"].(float64))

	// Move to root by sending null folder_id.
	moveResp := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(noteID)+"/move",
		map[string]interface{}{"folder_id": nil})
	defer moveResp.Body.Close()
	assert.Equal(t, http.StatusOK, moveResp.StatusCode)

	// Verify it appears in root notes.
	rootResp := c.do(t, http.MethodGet, "/api/v1/notes/root", nil)
	defer rootResp.Body.Close()
	var rootItems []map[string]interface{}
	require.NoError(t, json.NewDecoder(rootResp.Body).Decode(&rootItems))
	found := false
	for _, item := range rootItems {
		if int64(item["id"].(float64)) == noteID {
			found = true
		}
	}
	assert.True(t, found, "note should appear in root after moving to root")
}

func TestHandler_MoveNote_InvalidID(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPatch, "/api/v1/notes/notanumber/move",
		map[string]interface{}{"folder_id": nil})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_MoveNote_NotFound(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPatch, "/api/v1/notes/99999/move",
		map[string]interface{}{"folder_id": nil})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandler_MoveNote_BadJSON(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	noteID := createNoteHelper(t, c, "Bad JSON Move")
	req, _ := http.NewRequest(http.MethodPatch, c.server.URL+"/api/v1/notes/"+i64str(noteID)+"/move",
		bytes.NewReader([]byte("{bad")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", c.csrfToken)
	for _, cookie := range c.cookies {
		req.AddCookie(cookie)
	}
	resp, err := c.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// ── Move folder ───────────────────────────────────────────────────────────────

func TestHandler_MoveFolder(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	srcID := createFolderHelper(t, c, "Source")
	dstID := createFolderHelper(t, c, "Destination")

	resp := c.do(t, http.MethodPatch, "/api/v1/folders/"+i64str(srcID)+"/move",
		map[string]interface{}{"parent_id": dstID})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify Source is now a child of Destination (parent_id matches dstID).
	treeResp := c.do(t, http.MethodGet, "/api/v1/folders", nil)
	defer treeResp.Body.Close()
	var tree []map[string]interface{}
	require.NoError(t, json.NewDecoder(treeResp.Body).Decode(&tree))

	foundAsChild := false
	for _, f := range tree {
		if int64(f["id"].(float64)) == srcID {
			if pid, ok := f["parent_id"].(float64); ok && int64(pid) == dstID {
				foundAsChild = true
			}
		}
	}
	assert.True(t, foundAsChild, "Source should have Destination as parent_id after move")
}

func TestHandler_MoveFolder_ToRoot(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	parentID := createFolderHelper(t, c, "Parent")
	childID := createFolderHelper(t, c, "Child")

	// Move Child under Parent.
	c.do(t, http.MethodPatch, "/api/v1/folders/"+i64str(childID)+"/move",
		map[string]interface{}{"parent_id": parentID})

	// Move Child back to root.
	resp := c.do(t, http.MethodPatch, "/api/v1/folders/"+i64str(childID)+"/move",
		map[string]interface{}{"parent_id": nil})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHandler_MoveFolder_Circular(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	parentID := createFolderHelper(t, c, "Parent")
	childID := createFolderHelper(t, c, "Child")

	// Nest child under parent.
	c.do(t, http.MethodPatch, "/api/v1/folders/"+i64str(childID)+"/move",
		map[string]interface{}{"parent_id": parentID})

	// Attempt to move parent under child → circular.
	resp := c.do(t, http.MethodPatch, "/api/v1/folders/"+i64str(parentID)+"/move",
		map[string]interface{}{"parent_id": childID})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_MoveFolder_InvalidID(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPatch, "/api/v1/folders/notanumber/move",
		map[string]interface{}{"parent_id": nil})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_MoveFolder_NotFound(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPatch, "/api/v1/folders/99999/move",
		map[string]interface{}{"parent_id": nil})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandler_MoveFolder_BadJSON(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	folderID := createFolderHelper(t, c, "Folder")
	req, _ := http.NewRequest(http.MethodPatch, c.server.URL+"/api/v1/folders/"+i64str(folderID)+"/move",
		bytes.NewReader([]byte("{bad")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", c.csrfToken)
	for _, cookie := range c.cookies {
		req.AddCookie(cookie)
	}
	resp, err := c.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// ── Import ────────────────────────────────────────────────────────────────────

func TestHandler_Import_Markdown(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	mdContent := []byte("# Hello\n\nThis is an imported note.")
	resp := c.doMultipart(t, "/api/v1/import", "file", "hello.md", mdContent)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, float64(1), result["notes_created"])
}

func TestHandler_Import_Zip_SingleNote(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	zipData := buildZip(t, map[string]string{
		"my-note.md": "# ZIP Note\n\nImported from zip.",
	})
	resp := c.doMultipart(t, "/api/v1/import", "file", "archive.zip", zipData)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, float64(1), result["notes_created"])
}

func TestHandler_Import_Zip_WithFolders(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	zipData := buildZip(t, map[string]string{
		"Work/project.md":   "# Project\n\nWork note.",
		"Work/meeting.md":   "# Meeting\n\nMeeting notes.",
		"Personal/diary.md": "# Diary\n\nPersonal note.",
	})
	resp := c.doMultipart(t, "/api/v1/import", "file", "export.zip", zipData)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, float64(3), result["notes_created"])
	assert.Equal(t, float64(2), result["folders_created"])
}

func TestHandler_Import_Zip_SkipsNonMarkdown(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	zipData := buildZip(t, map[string]string{
		"note.md":   "# Included",
		"image.png": "\x89PNG...",
		"readme":    "not a markdown file",
	})
	resp := c.doMultipart(t, "/api/v1/import", "file", "mixed.zip", zipData)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	// Only the .md file should be imported.
	assert.Equal(t, float64(1), result["notes_created"])
}

func TestHandler_Import_UnsupportedType(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.doMultipart(t, "/api/v1/import", "file", "notes.txt", []byte("plain text"))
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_Import_MissingFileField(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	// Send a valid multipart form but with the wrong field name.
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("wrong_field", "note.md")
	require.NoError(t, err)
	_, err = fw.Write([]byte("# Note"))
	require.NoError(t, err)
	w.Close()

	req, _ := http.NewRequest(http.MethodPost, c.server.URL+"/api/v1/import", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-CSRF-Token", c.csrfToken)
	for _, cookie := range c.cookies {
		req.AddCookie(cookie)
	}
	resp, err := c.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_Import_Unauthenticated(t *testing.T) {
	c := newTestClient(t)
	// Do not log in.
	resp := c.doMultipart(t, "/api/v1/import", "file", "note.md", []byte("# Note"))
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestHandler_Import_InvalidZip(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	// Send a .zip file with invalid/corrupt zip bytes — the service should return an error.
	resp := c.doMultipart(t, "/api/v1/import", "file", "corrupt.zip", []byte("not a zip file"))
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// ── Patch note returns slug + title ──────────────────────────────────────────

func TestHandler_PatchNote_Metadata_ReturnsSlug(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createNoteHelper(t, c, "Original Title")

	resp := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(id), map[string]interface{}{
		"title": "Updated Title",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "updated-title", result["slug"])
	assert.Equal(t, "Updated Title", result["title"])
}

// ── Account token integration tests ──────────────────────────────────────────

func TestHandler_Tokens_FullCRUD(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	// Initially empty.
	listResp := c.do(t, http.MethodGet, "/api/v1/account/tokens", nil)
	defer listResp.Body.Close()
	require.Equal(t, http.StatusOK, listResp.StatusCode)
	var empty []interface{}
	require.NoError(t, json.NewDecoder(listResp.Body).Decode(&empty))
	assert.Empty(t, empty)

	// Create a token.
	createResp := c.do(t, http.MethodPost, "/api/v1/account/tokens", map[string]interface{}{
		"name":  "CI Token",
		"scope": "readwrite",
	})
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)
	var created map[string]interface{}
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&created))
	assert.Equal(t, "CI Token", created["name"])
	assert.NotEmpty(t, created["token"], "raw token must be returned on creation")
	tokenID := int64(created["id"].(float64))

	// List — token appears but value is masked.
	listResp2 := c.do(t, http.MethodGet, "/api/v1/account/tokens", nil)
	defer listResp2.Body.Close()
	var tokens []map[string]interface{}
	require.NoError(t, json.NewDecoder(listResp2.Body).Decode(&tokens))
	require.Len(t, tokens, 1)
	assert.Equal(t, "CI Token", tokens[0]["name"])
	assert.Empty(t, tokens[0]["token"], "raw token must not appear in list")

	// Delete.
	delResp := c.do(t, http.MethodDelete, "/api/v1/account/tokens/"+i64str(tokenID), nil)
	defer delResp.Body.Close()
	assert.Equal(t, http.StatusNoContent, delResp.StatusCode)

	// List again — empty.
	listResp3 := c.do(t, http.MethodGet, "/api/v1/account/tokens", nil)
	defer listResp3.Body.Close()
	var afterDelete []interface{}
	require.NoError(t, json.NewDecoder(listResp3.Body).Decode(&afterDelete))
	assert.Empty(t, afterDelete)
}

func TestHandler_Tokens_CreateReadOnlyScope(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPost, "/api/v1/account/tokens", map[string]interface{}{
		"name":  "Read Token",
		"scope": "read",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "read", result["scope"])
}

func TestHandler_Tokens_DeleteNotFound(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodDelete, "/api/v1/account/tokens/99999", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandler_Tokens_Unauthenticated(t *testing.T) {
	c := newTestClient(t)
	// No login.
	resp := c.do(t, http.MethodGet, "/api/v1/account/tokens", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestHandler_Tokens_InvalidScope(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPost, "/api/v1/account/tokens", map[string]interface{}{
		"name":  "Bad Scope Token",
		"scope": "admin",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_Tokens_BadJSON(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	req, _ := http.NewRequest(http.MethodPost, c.server.URL+"/api/v1/account/tokens",
		bytes.NewReader([]byte("{bad json")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", c.csrfToken)
	for _, cookie := range c.cookies {
		req.AddCookie(cookie)
	}
	resp, err := c.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_Tokens_DeleteInvalidID(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodDelete, "/api/v1/account/tokens/notanumber", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestHandler_AccountModal_OpenReturnsJSON verifies that the token list endpoint
// used when the Account modal opens returns a JSON array immediately (no content
// that would cause a display delay). This is the API call that was blocking the
// modal from appearing — it must return 200+JSON for a fresh user with no tokens.
func TestHandler_AccountModal_OpenReturnsJSON(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodGet, "/api/v1/account/tokens", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Must return a JSON array (not null), so the frontend can display "No tokens yet."
	// without an error. This is what showAccountModal() calls before rendering.
	var tokens []interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tokens), "response must be a JSON array")
	assert.NotNil(t, tokens, "token list must be a non-null array even when empty")
	assert.Empty(t, tokens, "fresh account should have no tokens")
}

// TestHandler_AccountModal_TokensLoadedForExistingUser verifies that when a user
// already has tokens, the list endpoint returns them correctly — this is the same
// call the Account modal makes on open to populate the token list.
func TestHandler_AccountModal_TokensLoadedForExistingUser(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	// Create two tokens before opening the modal.
	r1 := c.do(t, http.MethodPost, "/api/v1/account/tokens", map[string]interface{}{"name": "Claude Desktop", "scope": "readwrite"})
	require.Equal(t, http.StatusCreated, r1.StatusCode, "token creation must succeed before testing list")
	r1.Body.Close()
	r2 := c.do(t, http.MethodPost, "/api/v1/account/tokens", map[string]interface{}{"name": "Read Token", "scope": "read"})
	require.Equal(t, http.StatusCreated, r2.StatusCode, "token creation must succeed before testing list")
	r2.Body.Close()

	resp := c.do(t, http.MethodGet, "/api/v1/account/tokens", nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var tokens []map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tokens))
	assert.Len(t, tokens, 2, "both tokens must be visible in the Account modal")

	for _, tok := range tokens {
		assert.Empty(t, tok["token"], "raw token value must not be exposed in list")
		assert.NotEmpty(t, tok["name"])
		assert.NotEmpty(t, tok["prefix"])
		assert.NotEmpty(t, tok["scope"])
	}
}
