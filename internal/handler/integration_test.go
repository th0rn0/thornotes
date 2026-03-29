package handler_test

import (
	"bytes"
	"encoding/json"
	iofs "io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	thornotes "github.com/th0rn0/thornotes"
	"github.com/th0rn0/thornotes/internal/auth"
	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/notes"
	"github.com/th0rn0/thornotes/internal/repository/sqlite"
	"github.com/th0rn0/thornotes/internal/router"
	"github.com/th0rn0/thornotes/internal/security"

	"html/template"
)

type testClient struct {
	server     *httptest.Server
	httpClient *http.Client
	cookies    []*http.Cookie
	csrfToken  string
	pool       *db.Pool
}

func newTestClient(t *testing.T) *testClient {
	t.Helper()
	dir := t.TempDir()
	pool, err := db.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	notesDir := filepath.Join(dir, "notes")
	fs, err := notes.NewFileStore(notesDir)
	require.NoError(t, err)

	userRepo := sqlite.NewUserRepo(pool.WriteDB)
	sessionRepo := sqlite.NewSessionRepo(pool.WriteDB)
	folderRepo := sqlite.NewFolderRepo(pool.ReadDB, pool.WriteDB)
	noteRepo := sqlite.NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := sqlite.NewSearchRepo(pool.ReadDB, pool.WriteDB)

	authSvc := auth.NewService(userRepo, sessionRepo, true)
	notesSvc := notes.NewService(noteRepo, folderRepo, searchRepo, fs)
	rateLimiter := security.NewAuthRateLimiter(nil)

	tmpl, err := template.ParseFS(thornotes.TemplatesFS, "web/templates/*.html")
	require.NoError(t, err)

	staticSub, err := iofs.Sub(thornotes.StaticFS, "web/static")
	require.NoError(t, err)

	h := router.New(authSvc, notesSvc, rateLimiter, tmpl, http.FS(staticSub))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)

	return &testClient{
		server:     srv,
		httpClient: &http.Client{Jar: jar},
		pool:       pool,
	}
}

func (c *testClient) do(t *testing.T, method, path string, body any) *http.Response {
	t.Helper()
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		require.NoError(t, err)
	}

	req, err := http.NewRequest(method, c.server.URL+path, bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, cookie := range c.cookies {
		req.AddCookie(cookie)
	}
	if c.csrfToken != "" && method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		req.Header.Set("X-CSRF-Token", c.csrfToken)
	}
	resp, err := c.httpClient.Do(req)
	require.NoError(t, err)
	return resp
}

func (c *testClient) register(t *testing.T, username, password string) {
	t.Helper()
	resp := c.do(t, http.MethodPost, "/api/v1/auth/register", map[string]string{"username": username, "password": password})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
}

func (c *testClient) login(t *testing.T, username, password string) {
	t.Helper()
	resp := c.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": username, "password": password})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	for _, cookie := range resp.Cookies() {
		if cookie.Name == "session" {
			c.cookies = []*http.Cookie{cookie}
		}
	}

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	c.csrfToken = result["csrf_token"]
}

func (c *testClient) registerAndLogin(t *testing.T) {
	t.Helper()
	c.register(t, "testuser", "securepassword123!")
	c.login(t, "testuser", "securepassword123!")
}

func i64str(id int64) string {
	return strconv.FormatInt(id, 10)
}

// ─── Auth handler tests ───────────────────────────────────────────────────────

func TestHandler_Register(t *testing.T) {
	c := newTestClient(t)
	resp := c.do(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"username": "alice",
		"password": "longenoughpassword123!",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
}

func TestHandler_Register_BadJSON(t *testing.T) {
	c := newTestClient(t)
	req, _ := http.NewRequest(http.MethodPost, c.server.URL+"/api/v1/auth/register", bytes.NewReader([]byte("{invalid json")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_Register_ShortPassword(t *testing.T) {
	c := newTestClient(t)
	resp := c.do(t, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"username": "bob",
		"password": "short",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_Login(t *testing.T) {
	c := newTestClient(t)
	c.register(t, "alice", "longenoughpassword123!")

	resp := c.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "alice",
		"password": "longenoughpassword123!",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.NotEmpty(t, result["csrf_token"])

	var sessionCookie *http.Cookie
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "session" {
			sessionCookie = cookie
		}
	}
	require.NotNil(t, sessionCookie)
}

func TestHandler_Login_BadCreds(t *testing.T) {
	c := newTestClient(t)
	resp := c.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "nobody",
		"password": "wrongpassword123!",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestHandler_Login_BadJSON(t *testing.T) {
	c := newTestClient(t)
	req, _ := http.NewRequest(http.MethodPost, c.server.URL+"/api/v1/auth/login", bytes.NewReader([]byte("{bad")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_Logout(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPost, "/api/v1/auth/logout", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHandler_Logout_NoCookie(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	// Create a new client with no cookies.
	c2 := &testClient{server: c.server, httpClient: &http.Client{}}
	resp := c2.do(t, http.MethodPost, "/api/v1/auth/logout", nil)
	defer resp.Body.Close()
	// Logout requires session (SessionMiddleware wraps it).
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestHandler_Me(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodGet, "/api/v1/auth/me", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "testuser", result["username"])
}

func TestHandler_Me_NoSession(t *testing.T) {
	c := newTestClient(t)

	resp := c.do(t, http.MethodGet, "/api/v1/auth/me", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestHandler_CSRF(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodGet, "/api/v1/csrf", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.NotEmpty(t, result["csrf_token"])
}

func TestHandler_CSRF_NoSession(t *testing.T) {
	c := newTestClient(t)

	resp := c.do(t, http.MethodGet, "/api/v1/csrf", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// ─── Notes handler tests ──────────────────────────────────────────────────────

func TestHandler_CreateNote(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{
		"title": "My Test Note",
		"tags":  []string{},
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
}

func TestHandler_CreateNote_BadJSON(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	req, _ := http.NewRequest(http.MethodPost, c.server.URL+"/api/v1/notes", bytes.NewReader([]byte("{bad")))
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range c.cookies {
		req.AddCookie(cookie)
	}
	req.Header.Set("X-CSRF-Token", c.csrfToken)
	resp, err := c.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func createNoteHelper(t *testing.T, c *testClient, title string) int64 {
	t.Helper()
	resp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{
		"title": title,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	return int64(result["id"].(float64))
}

func TestHandler_GetNote(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createNoteHelper(t, c, "Fetch Me")

	resp := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(id), nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHandler_GetNote_InvalidID(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodGet, "/api/v1/notes/abc", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_GetNote_NotFound(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodGet, "/api/v1/notes/99999", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandler_PatchNote_Content(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createNoteHelper(t, c, "Patch Content Note")

	// Get the current hash.
	resp := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(id), nil)
	var noteData map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&noteData))
	resp.Body.Close()
	currentHash := noteData["content_hash"].(string)

	patchResp := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(id), map[string]interface{}{
		"content":      "new content",
		"content_hash": currentHash,
	})
	defer patchResp.Body.Close()
	assert.Equal(t, http.StatusOK, patchResp.StatusCode)

	var patchResult map[string]string
	require.NoError(t, json.NewDecoder(patchResp.Body).Decode(&patchResult))
	assert.NotEmpty(t, patchResult["content_hash"])
}

func TestHandler_PatchNote_ContentMissingHash(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createNoteHelper(t, c, "Hash Required Note")

	resp := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(id), map[string]interface{}{
		"content": "new content without hash",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_PatchNote_Metadata(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createNoteHelper(t, c, "Old Title")

	resp := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(id), map[string]interface{}{
		"title": "New Title",
		"tags":  []string{"tag1", "tag2"},
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHandler_PatchNote_InvalidID(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPatch, "/api/v1/notes/notanumber", map[string]interface{}{
		"title": "Whatever",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_DeleteNote(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createNoteHelper(t, c, "Delete This Note")

	resp := c.do(t, http.MethodDelete, "/api/v1/notes/"+i64str(id), nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestHandler_DeleteNote_InvalidID(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodDelete, "/api/v1/notes/xyz", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_ShareNote(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createNoteHelper(t, c, "Shareable Note")

	resp := c.do(t, http.MethodPost, "/api/v1/notes/"+i64str(id)+"/share", map[string]interface{}{})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.NotEmpty(t, result["share_token"])
}

func TestHandler_ShareNote_InvalidID(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPost, "/api/v1/notes/badid/share", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_ShareNote_Clear(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createNoteHelper(t, c, "Note To Unshare")

	// First set a token.
	shareResp := c.do(t, http.MethodPost, "/api/v1/notes/"+i64str(id)+"/share", map[string]interface{}{})
	shareResp.Body.Close()

	// Now clear it.
	clearResp := c.do(t, http.MethodPost, "/api/v1/notes/"+i64str(id)+"/share", map[string]interface{}{
		"clear": true,
	})
	defer clearResp.Body.Close()
	assert.Equal(t, http.StatusOK, clearResp.StatusCode)
}

func TestHandler_ListRoot(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	createNoteHelper(t, c, "Root Note 1")

	resp := c.do(t, http.MethodGet, "/api/v1/notes/root", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var items []interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&items))
	assert.NotEmpty(t, items)
}

func TestHandler_Search(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createNoteHelper(t, c, "Search Test Note")

	// Get hash and update content.
	resp := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(id), nil)
	var noteData map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&noteData))
	resp.Body.Close()
	hash := noteData["content_hash"].(string)

	patchResp := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(id), map[string]interface{}{
		"content":      "unique search term xyzzy",
		"content_hash": hash,
	})
	patchResp.Body.Close()

	searchResp := c.do(t, http.MethodGet, "/api/v1/notes?q=xyzzy", nil)
	defer searchResp.Body.Close()
	assert.Equal(t, http.StatusOK, searchResp.StatusCode)

	var results []interface{}
	require.NoError(t, json.NewDecoder(searchResp.Body).Decode(&results))
	assert.NotEmpty(t, results)
}

func TestHandler_Search_NoQuery(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodGet, "/api/v1/notes?q=", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// ─── Folders handler tests ────────────────────────────────────────────────────

func TestHandler_CreateFolder(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPost, "/api/v1/folders", map[string]interface{}{
		"name": "My Folder",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
}

func TestHandler_CreateFolder_BadJSON(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	req, _ := http.NewRequest(http.MethodPost, c.server.URL+"/api/v1/folders", bytes.NewReader([]byte("{bad")))
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range c.cookies {
		req.AddCookie(cookie)
	}
	req.Header.Set("X-CSRF-Token", c.csrfToken)
	resp, err := c.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func createFolderHelper(t *testing.T, c *testClient, name string) int64 {
	t.Helper()
	resp := c.do(t, http.MethodPost, "/api/v1/folders", map[string]interface{}{
		"name": name,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	return int64(result["id"].(float64))
}

func TestHandler_ListFolders(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	createFolderHelper(t, c, "Projects")

	resp := c.do(t, http.MethodGet, "/api/v1/folders", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var items []interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&items))
	assert.NotEmpty(t, items)
}

func TestHandler_RenameFolder(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createFolderHelper(t, c, "OldFolder")

	resp := c.do(t, http.MethodPatch, "/api/v1/folders/"+i64str(id), map[string]interface{}{
		"name": "NewFolder",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHandler_RenameFolder_InvalidID(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPatch, "/api/v1/folders/badid", map[string]interface{}{
		"name": "whatever",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_RenameFolder_BadJSON(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createFolderHelper(t, c, "FolderForBadJSON")

	req, _ := http.NewRequest(http.MethodPatch, c.server.URL+"/api/v1/folders/"+i64str(id), bytes.NewReader([]byte("{bad")))
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range c.cookies {
		req.AddCookie(cookie)
	}
	req.Header.Set("X-CSRF-Token", c.csrfToken)
	resp, err := c.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_DeleteFolder(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createFolderHelper(t, c, "DeleteMe")

	resp := c.do(t, http.MethodDelete, "/api/v1/folders/"+i64str(id), nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestHandler_DeleteFolder_InvalidID(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodDelete, "/api/v1/folders/notanid", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_ListFolderNotes(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	folderID := createFolderHelper(t, c, "FolderWithNotes")

	// Create a note in that folder.
	noteResp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{
		"title":     "Note In Folder",
		"folder_id": folderID,
	})
	noteResp.Body.Close()

	listResp := c.do(t, http.MethodGet, "/api/v1/folders/"+i64str(folderID)+"/notes", nil)
	defer listResp.Body.Close()
	assert.Equal(t, http.StatusOK, listResp.StatusCode)

	var items []interface{}
	require.NoError(t, json.NewDecoder(listResp.Body).Decode(&items))
	assert.NotEmpty(t, items)
}

func TestHandler_ListFolderNotes_InvalidID(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodGet, "/api/v1/folders/notanid/notes", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// ─── Share handler tests ──────────────────────────────────────────────────────

func TestHandler_ShareView(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createNoteHelper(t, c, "Public Note")

	// Generate share token.
	shareResp := c.do(t, http.MethodPost, "/api/v1/notes/"+i64str(id)+"/share", nil)
	var shareResult map[string]string
	require.NoError(t, json.NewDecoder(shareResp.Body).Decode(&shareResult))
	shareResp.Body.Close()
	token := shareResult["share_token"]
	require.NotEmpty(t, token)

	// Access the public share URL.
	resp, err := c.httpClient.Get(c.server.URL + "/s/" + token)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHandler_ShareView_NotFound(t *testing.T) {
	c := newTestClient(t)

	resp, err := c.httpClient.Get(c.server.URL + "/s/nonexistenttoken")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// ─── App/static tests ─────────────────────────────────────────────────────────

func TestHandler_AppPage(t *testing.T) {
	c := newTestClient(t)

	resp, err := c.httpClient.Get(c.server.URL + "/")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"))
}

func TestHandler_NotFound(t *testing.T) {
	c := newTestClient(t)

	resp, err := c.httpClient.Get(c.server.URL + "/nonexistent/path/that/does/not/exist")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandler_Static(t *testing.T) {
	c := newTestClient(t)

	resp, err := c.httpClient.Get(c.server.URL + "/static/css/font-awesome.min.css")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// ─── Middleware tests via handler ─────────────────────────────────────────────

func TestHandler_SessionRequired(t *testing.T) {
	c := newTestClient(t)

	// No session cookie.
	resp := c.do(t, http.MethodGet, "/api/v1/auth/me", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestHandler_CSRFRequired(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	// Remove CSRF token to trigger CSRF middleware.
	origCSRF := c.csrfToken
	c.csrfToken = ""
	defer func() { c.csrfToken = origCSRF }()

	resp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{
		"title": "Will fail",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// ─── Additional coverage tests ────────────────────────────────────────────────

func TestHandler_ListFolders_Empty(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	// No folders created — should return empty array, not null.
	resp := c.do(t, http.MethodGet, "/api/v1/folders", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var items []interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&items))
	assert.NotNil(t, items)
	assert.Empty(t, items)
}

func TestHandler_CreateNote_EmptyTitle(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{
		"title": "",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_PatchNote_BadJSON(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createNoteHelper(t, c, "Patch BadJSON Note")

	req, _ := http.NewRequest(http.MethodPatch, c.server.URL+"/api/v1/notes/"+i64str(id), bytes.NewReader([]byte("{bad json")))
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range c.cookies {
		req.AddCookie(cookie)
	}
	req.Header.Set("X-CSRF-Token", c.csrfToken)
	resp, err := c.httpClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_PatchNote_WrongHash(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createNoteHelper(t, c, "WrongHash Note")

	// Patch with content but wrong hash → conflict.
	resp := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(id), map[string]interface{}{
		"content":      "new content",
		"content_hash": "definitely-wrong-hash",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestHandler_DeleteFolder_NotFound(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodDelete, "/api/v1/folders/99999", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandler_RenameFolder_EmptyName(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	id := createFolderHelper(t, c, "RenameTarget")

	resp := c.do(t, http.MethodPatch, "/api/v1/folders/"+i64str(id), map[string]interface{}{
		"name": "",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_CreateFolder_EmptyName(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPost, "/api/v1/folders", map[string]interface{}{
		"name": "",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_ListFolderNotes_Empty(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	folderID := createFolderHelper(t, c, "EmptyFolder")

	resp := c.do(t, http.MethodGet, "/api/v1/folders/"+i64str(folderID)+"/notes", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var items []interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&items))
	assert.NotNil(t, items)
	assert.Empty(t, items)
}

func TestHandler_ListRoot_Empty(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	// No notes created — should return empty array.
	resp := c.do(t, http.MethodGet, "/api/v1/notes/root", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var items []interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&items))
	assert.NotNil(t, items)
	assert.Empty(t, items)
}

func TestHandler_Search_WithTag(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	// Create a note with tags.
	resp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{
		"title": "Tagged Note",
		"tags":  []string{"go", "testing"},
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// Search with tag filter (even with no matching content, should not error).
	searchResp := c.do(t, http.MethodGet, "/api/v1/notes?q=Tagged&tag=go", nil)
	defer searchResp.Body.Close()
	assert.Equal(t, http.StatusOK, searchResp.StatusCode)
}

func TestHandler_DeleteNote_NotFound(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodDelete, "/api/v1/notes/99999", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandler_PatchNote_Metadata_NotFound(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	title := "New Title"
	resp := c.do(t, http.MethodPatch, "/api/v1/notes/99999", map[string]interface{}{"title": title})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandler_RenameFolder_NotFound(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPatch, "/api/v1/folders/99999", map[string]interface{}{"name": "New Name"})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandler_ListFolders_DBError(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)
	c.pool.ReadDB.Close()

	resp := c.do(t, http.MethodGet, "/api/v1/folders", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

func TestHandler_ListRoot_DBError(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)
	c.pool.ReadDB.Close()

	resp := c.do(t, http.MethodGet, "/api/v1/notes/root", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

func TestHandler_Search_DBError(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)
	c.pool.ReadDB.Close()

	resp := c.do(t, http.MethodGet, "/api/v1/notes?q=test", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

func TestHandler_ListFolderNotes_DBError(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)
	c.pool.ReadDB.Close()

	resp := c.do(t, http.MethodGet, "/api/v1/folders/1/notes", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

