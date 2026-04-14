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
	"github.com/th0rn0/thornotes/internal/hub"
	"github.com/th0rn0/thornotes/internal/notes"
	"github.com/th0rn0/thornotes/internal/repository/sqlite"
	"github.com/th0rn0/thornotes/internal/router"
	"github.com/th0rn0/thornotes/internal/security"

	"html/template"
)

type stubDBHealth struct{}

func (s *stubDBHealth) HealthCheck() map[string]string {
	return map[string]string{"db_read": "ok", "db_write": "ok"}
}

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
	apiTokenRepo := sqlite.NewAPITokenRepo(pool.ReadDB, pool.WriteDB)

	authSvc := auth.NewServiceForTest(userRepo, sessionRepo, true)
	notesSvc := notes.NewService(noteRepo, folderRepo, searchRepo, sqlite.NewJournalRepo(pool.ReadDB, pool.WriteDB), fs)
	rateLimiter := security.NewAuthRateLimiter(nil)
	t.Cleanup(rateLimiter.Stop)

	tmpl, err := template.ParseFS(thornotes.TemplatesFS, "web/templates/*.html")
	require.NoError(t, err)

	staticSub, err := iofs.Sub(thornotes.StaticFS, "web/static")
	require.NoError(t, err)

	h := router.New(authSvc, notesSvc, apiTokenRepo, userRepo, rateLimiter, tmpl, http.FS(staticSub), hub.New(), false, false, &stubDBHealth{})
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
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
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

	// Non-API paths serve the app shell (SPA deep linking), so use an API path
	// that doesn't exist to get a real 404.
	resp, err := c.httpClient.Get(c.server.URL + "/api/v1/nonexistent-endpoint")
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

	// Registration creates a Getting Started note; delete it so root is empty.
	rootResp := c.do(t, http.MethodGet, "/api/v1/notes/root", nil)
	var items []map[string]interface{}
	require.NoError(t, json.NewDecoder(rootResp.Body).Decode(&items))
	rootResp.Body.Close()
	for _, item := range items {
		id := int64(item["id"].(float64))
		delResp := c.do(t, http.MethodDelete, "/api/v1/notes/"+i64str(id), nil)
		delResp.Body.Close()
	}

	resp := c.do(t, http.MethodGet, "/api/v1/notes/root", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var empty []interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&empty))
	assert.NotNil(t, empty)
	assert.Empty(t, empty)
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

// ─── Journal handler tests ────────────────────────────────────────────────────

func TestHandler_CreateJournal(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPost, "/api/v1/journals", map[string]interface{}{
		"name": "Personal",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var j map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&j))
	assert.Equal(t, "Personal", j["name"])
	assert.NotZero(t, j["id"])
}

func TestHandler_CreateJournal_BadJSON(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	req, _ := http.NewRequest(http.MethodPost, c.server.URL+"/api/v1/journals", bytes.NewReader([]byte("{bad")))
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

func TestHandler_ListJournals_Empty(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodGet, "/api/v1/journals", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var journals []interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&journals))
	assert.NotNil(t, journals)
	assert.Empty(t, journals)
}

func TestHandler_ListJournals_AfterCreate(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	createResp := c.do(t, http.MethodPost, "/api/v1/journals", map[string]interface{}{"name": "Work"})
	createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)

	resp := c.do(t, http.MethodGet, "/api/v1/journals", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var journals []interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&journals))
	assert.Len(t, journals, 1)
}

func TestHandler_DeleteJournal(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	createResp := c.do(t, http.MethodPost, "/api/v1/journals", map[string]interface{}{"name": "Temp"})
	var j map[string]interface{}
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&j))
	createResp.Body.Close()

	id := int64(j["id"].(float64))
	delResp := c.do(t, http.MethodDelete, "/api/v1/journals/"+i64str(id), nil)
	defer delResp.Body.Close()
	assert.Equal(t, http.StatusNoContent, delResp.StatusCode)
}

func TestHandler_DeleteJournal_InvalidID(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodDelete, "/api/v1/journals/notanid", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_DeleteJournal_NotFound(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodDelete, "/api/v1/journals/99999", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandler_JournalToday(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	createResp := c.do(t, http.MethodPost, "/api/v1/journals", map[string]interface{}{"name": "Daily"})
	var j map[string]interface{}
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&j))
	createResp.Body.Close()

	id := int64(j["id"].(float64))
	resp := c.do(t, http.MethodGet, "/api/v1/journals/"+i64str(id)+"/today", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var note map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&note))
	assert.NotZero(t, note["id"])

	// Idempotent — second call returns same note.
	resp2 := c.do(t, http.MethodGet, "/api/v1/journals/"+i64str(id)+"/today", nil)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusOK, resp2.StatusCode)
	var note2 map[string]interface{}
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&note2))
	assert.Equal(t, note["id"], note2["id"])
}

func TestHandler_JournalToday_InvalidID(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodGet, "/api/v1/journals/notanid/today", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_JournalToday_NotFound(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodGet, "/api/v1/journals/99999/today", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestHandler_JournalToday_WithTimezone(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	createResp := c.do(t, http.MethodPost, "/api/v1/journals", map[string]interface{}{"name": "TZTest"})
	var j map[string]interface{}
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&j))
	createResp.Body.Close()

	id := int64(j["id"].(float64))
	resp := c.do(t, http.MethodGet, "/api/v1/journals/"+i64str(id)+"/today?tz=America%2FNew_York", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHandler_JournalToday_InvalidTimezone(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	createResp := c.do(t, http.MethodPost, "/api/v1/journals", map[string]interface{}{"name": "TZBad"})
	var j map[string]interface{}
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&j))
	createResp.Body.Close()

	id := int64(j["id"].(float64))
	resp := c.do(t, http.MethodGet, "/api/v1/journals/"+i64str(id)+"/today?tz=Not%2FA%2FReal%2FZone", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// ─── ListAll handler tests ────────────────────────────────────────────────────

func TestHandler_ListAll_AcrossFolders(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	// Create a root note and a folder note.
	createNoteHelper(t, c, "Root Note A")

	folderResp := c.do(t, http.MethodPost, "/api/v1/folders", map[string]interface{}{"name": "MyFolder"})
	var folder map[string]interface{}
	require.NoError(t, json.NewDecoder(folderResp.Body).Decode(&folder))
	folderResp.Body.Close()
	folderID := int64(folder["id"].(float64))

	folderNoteResp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{
		"title":     "Folder Note B",
		"folder_id": folderID,
	})
	folderNoteResp.Body.Close()

	resp := c.do(t, http.MethodGet, "/api/v1/notes/all", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var items []map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&items))

	// Should contain the Getting Started note + Root Note A + Folder Note B.
	assert.GreaterOrEqual(t, len(items), 2)

	titles := make(map[string]bool)
	for _, item := range items {
		titles[item["title"].(string)] = true
		// folder_id field must be present (may be null).
		_, hasFolderID := item["folder_id"]
		assert.True(t, hasFolderID, "folder_id field should be present")
	}
	assert.True(t, titles["Root Note A"])
	assert.True(t, titles["Folder Note B"])
}

func TestHandler_ListAll_DBError(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)
	c.pool.ReadDB.Close()

	resp := c.do(t, http.MethodGet, "/api/v1/notes/all", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

// ─── Context endpoint tests ───────────────────────────────────────────────────

func TestHandler_Context_Empty(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	// Delete the auto-created Getting Started note so we have a clean slate.
	rootResp := c.do(t, http.MethodGet, "/api/v1/notes/root", nil)
	var items []map[string]interface{}
	require.NoError(t, json.NewDecoder(rootResp.Body).Decode(&items))
	rootResp.Body.Close()
	for _, item := range items {
		id := int64(item["id"].(float64))
		delResp := c.do(t, http.MethodDelete, "/api/v1/notes/"+i64str(id), nil)
		delResp.Body.Close()
	}

	resp := c.do(t, http.MethodGet, "/api/v1/notes/context", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "", result["context"])
	assert.Equal(t, float64(0), result["note_count"])
	assert.Equal(t, false, result["truncated"])
	assert.Equal(t, float64(200000), result["char_limit"])
}

func TestHandler_Context_IncludesNoteContent(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	// Create a note with known content.
	noteID := createNoteHelper(t, c, "My Context Note")

	// Patch in some content.
	getResp := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(noteID), nil)
	var noteData map[string]interface{}
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&noteData))
	getResp.Body.Close()
	currentHash := noteData["content_hash"].(string)

	patchResp := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(noteID), map[string]interface{}{
		"content":      "Hello from context test",
		"content_hash": currentHash,
	})
	patchResp.Body.Close()

	resp := c.do(t, http.MethodGet, "/api/v1/notes/context", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

	context := result["context"].(string)
	assert.Contains(t, context, "# My Context Note")
	assert.Contains(t, context, "Hello from context test")
	assert.GreaterOrEqual(t, result["note_count"].(float64), float64(1))
	assert.Equal(t, false, result["truncated"])
}

func TestHandler_Context_FolderFilter(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	folderID := createFolderHelper(t, c, "FilterFolder")

	// Create a note in the folder.
	folderNoteResp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{
		"title":     "In Folder",
		"folder_id": folderID,
	})
	var folderNote map[string]interface{}
	require.NoError(t, json.NewDecoder(folderNoteResp.Body).Decode(&folderNote))
	folderNoteResp.Body.Close()

	// Create a root note.
	createNoteHelper(t, c, "Root Only Note")

	// Context with folder_id filter: should only include the folder note.
	resp := c.do(t, http.MethodGet, "/api/v1/notes/context?folder_id="+i64str(folderID), nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

	context := result["context"].(string)
	assert.Contains(t, context, "# In Folder")
	assert.NotContains(t, context, "# Root Only Note")
	assert.Equal(t, float64(1), result["note_count"])
}

func TestHandler_Context_AllNotesAcrossFolders(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	folderID := createFolderHelper(t, c, "AFolder")

	folderNoteResp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{
		"title":     "Folder Note",
		"folder_id": folderID,
	})
	folderNoteResp.Body.Close()

	createNoteHelper(t, c, "Root Note")

	// Context without folder_id: should include both.
	resp := c.do(t, http.MethodGet, "/api/v1/notes/context", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

	context := result["context"].(string)
	assert.Contains(t, context, "# Folder Note")
	assert.Contains(t, context, "# Root Note")
	assert.GreaterOrEqual(t, result["note_count"].(float64), float64(2))
}

func TestHandler_Context_InvalidFolderID(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodGet, "/api/v1/notes/context?folder_id=notanumber", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Contains(t, body["error"].(string), "folder_id")
}

func TestHandler_Context_NoSession(t *testing.T) {
	c := newTestClient(t)
	// No login.
	resp := c.do(t, http.MethodGet, "/api/v1/notes/context", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestHandler_Context_DBError(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)
	c.pool.ReadDB.Close()

	resp := c.do(t, http.MethodGet, "/api/v1/notes/context", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

func TestHandler_Context_ResponseFormat(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodGet, "/api/v1/notes/context", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// All four fields must be present.
	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	_, hasContext := result["context"]
	_, hasNoteCount := result["note_count"]
	_, hasTruncated := result["truncated"]
	_, hasCharLimit := result["char_limit"]
	assert.True(t, hasContext, "missing context field")
	assert.True(t, hasNoteCount, "missing note_count field")
	assert.True(t, hasTruncated, "missing truncated field")
	assert.True(t, hasCharLimit, "missing char_limit field")
	assert.Equal(t, float64(200000), result["char_limit"])
}

func TestHandler_Context_MarkdownFormat(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	// Delete auto-created notes for clean state.
	allResp := c.do(t, http.MethodGet, "/api/v1/notes/all", nil)
	var items []map[string]interface{}
	require.NoError(t, json.NewDecoder(allResp.Body).Decode(&items))
	allResp.Body.Close()
	for _, item := range items {
		id := int64(item["id"].(float64))
		delResp := c.do(t, http.MethodDelete, "/api/v1/notes/"+i64str(id), nil)
		delResp.Body.Close()
	}

	noteID := createNoteHelper(t, c, "Format Test")

	getResp := c.do(t, http.MethodGet, "/api/v1/notes/"+i64str(noteID), nil)
	var noteData map[string]interface{}
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&noteData))
	getResp.Body.Close()

	patchResp := c.do(t, http.MethodPatch, "/api/v1/notes/"+i64str(noteID), map[string]interface{}{
		"content":      "Some content here",
		"content_hash": noteData["content_hash"].(string),
	})
	patchResp.Body.Close()

	resp := c.do(t, http.MethodGet, "/api/v1/notes/context", nil)
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

	context := result["context"].(string)
	// Each note should be formatted as: # Title\n\ncontent\n\n---\n\n
	assert.Contains(t, context, "# Format Test\n\nSome content here\n\n---\n\n")
}

// ─── Git history handler tests ────────────────────────────────────────────────

// newTestClientGit creates a test client with a notes service that has git
// history enabled.
func newTestClientGit(t *testing.T) *testClient {
	t.Helper()
	dir := t.TempDir()
	pool, err := db.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	notesDir := filepath.Join(dir, "notes")
	fs, err := notes.NewFileStore(notesDir)
	require.NoError(t, err)
	require.NoError(t, fs.EnableGitHistory())

	userRepo := sqlite.NewUserRepo(pool.WriteDB)
	sessionRepo := sqlite.NewSessionRepo(pool.WriteDB)
	folderRepo := sqlite.NewFolderRepo(pool.ReadDB, pool.WriteDB)
	noteRepo := sqlite.NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := sqlite.NewSearchRepo(pool.ReadDB, pool.WriteDB)
	apiTokenRepo := sqlite.NewAPITokenRepo(pool.ReadDB, pool.WriteDB)

	authSvc := auth.NewServiceForTest(userRepo, sessionRepo, true)
	notesSvc := notes.NewService(noteRepo, folderRepo, searchRepo, sqlite.NewJournalRepo(pool.ReadDB, pool.WriteDB), fs)
	rateLimiter := security.NewAuthRateLimiter(nil)
	t.Cleanup(rateLimiter.Stop)

	tmpl, err := template.ParseFS(thornotes.TemplatesFS, "web/templates/*.html")
	require.NoError(t, err)

	staticSub, err := iofs.Sub(thornotes.StaticFS, "web/static")
	require.NoError(t, err)

	h := router.New(authSvc, notesSvc, apiTokenRepo, userRepo, rateLimiter, tmpl, http.FS(staticSub), hub.New(), false, true, &stubDBHealth{})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	return &testClient{server: srv, httpClient: &http.Client{Jar: jar}, pool: pool}
}

func TestHandler_History_DisabledReturns501(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	// Create a note.
	resp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{"title": "hist"})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var note map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&note))
	noteID := i64str(int64(note["id"].(float64)))

	resp2 := c.do(t, http.MethodGet, "/api/v1/notes/"+noteID+"/history", nil)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusNotImplemented, resp2.StatusCode)
}

func TestHandler_History_ListReturnsEntries(t *testing.T) {
	c := newTestClientGit(t)
	c.registerAndLogin(t)

	// Create a note.
	resp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{"title": "githistory"})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var note map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&note))
	noteID := i64str(int64(note["id"].(float64)))
	contentHash := note["content_hash"].(string)

	// Save content.
	patchResp := c.do(t, http.MethodPatch, "/api/v1/notes/"+noteID, map[string]interface{}{
		"content":      "# Hello world",
		"content_hash": contentHash,
	})
	defer patchResp.Body.Close()
	require.Equal(t, http.StatusOK, patchResp.StatusCode)

	// List history.
	histResp := c.do(t, http.MethodGet, "/api/v1/notes/"+noteID+"/history", nil)
	defer histResp.Body.Close()
	assert.Equal(t, http.StatusOK, histResp.StatusCode)

	var entries []map[string]interface{}
	require.NoError(t, json.NewDecoder(histResp.Body).Decode(&entries))
	assert.GreaterOrEqual(t, len(entries), 2, "expect at least create + save commit")
	assert.NotEmpty(t, entries[0]["sha"])
	assert.NotEmpty(t, entries[0]["timestamp"])
}

func TestHandler_History_LimitQueryParam(t *testing.T) {
	c := newTestClientGit(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{"title": "limit-test"})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var note map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&note))
	noteID := i64str(int64(note["id"].(float64)))
	h := note["content_hash"].(string)

	for i := range 4 {
		_ = i
		pR := c.do(t, http.MethodPatch, "/api/v1/notes/"+noteID, map[string]interface{}{"content": "# v", "content_hash": h})
		var pResult map[string]interface{}
		require.NoError(t, json.NewDecoder(pR.Body).Decode(&pResult))
		pR.Body.Close()
		h = pResult["content_hash"].(string)
	}

	histResp := c.do(t, http.MethodGet, "/api/v1/notes/"+noteID+"/history?limit=2", nil)
	defer histResp.Body.Close()
	assert.Equal(t, http.StatusOK, histResp.StatusCode)

	var entries []map[string]interface{}
	require.NoError(t, json.NewDecoder(histResp.Body).Decode(&entries))
	assert.Equal(t, 2, len(entries))
}

func TestHandler_History_AtReturnsContent(t *testing.T) {
	c := newTestClientGit(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{"title": "at-test"})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var note map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&note))
	noteID := i64str(int64(note["id"].(float64)))
	contentHash := note["content_hash"].(string)

	patchResp := c.do(t, http.MethodPatch, "/api/v1/notes/"+noteID, map[string]interface{}{
		"content":      "# at version",
		"content_hash": contentHash,
	})
	defer patchResp.Body.Close()
	require.Equal(t, http.StatusOK, patchResp.StatusCode)

	// Get history.
	histResp := c.do(t, http.MethodGet, "/api/v1/notes/"+noteID+"/history", nil)
	defer histResp.Body.Close()
	var entries []map[string]interface{}
	require.NoError(t, json.NewDecoder(histResp.Body).Decode(&entries))
	require.NotEmpty(t, entries)

	sha := entries[0]["sha"].(string)

	// Fetch content at that SHA.
	atResp := c.do(t, http.MethodGet, "/api/v1/notes/"+noteID+"/history/"+sha, nil)
	defer atResp.Body.Close()
	assert.Equal(t, http.StatusOK, atResp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(atResp.Body).Decode(&result))
	assert.Equal(t, sha, result["sha"])
	assert.Equal(t, "# at version", result["content"])
}

func TestHandler_History_AtInvalidSHA(t *testing.T) {
	c := newTestClientGit(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{"title": "bad-sha"})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var note map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&note))
	noteID := i64str(int64(note["id"].(float64)))

	// SHA shorter than 7 chars → 400 validation error.
	atResp := c.do(t, http.MethodGet, "/api/v1/notes/"+noteID+"/history/ab", nil)
	defer atResp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, atResp.StatusCode)
}

func TestHandler_History_Restore(t *testing.T) {
	c := newTestClientGit(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{"title": "restore-test"})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var note map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&note))
	noteID := i64str(int64(note["id"].(float64)))
	h := note["content_hash"].(string)

	// Save "original".
	p1 := c.do(t, http.MethodPatch, "/api/v1/notes/"+noteID, map[string]interface{}{
		"content": "# original", "content_hash": h,
	})
	var p1r map[string]interface{}
	require.NoError(t, json.NewDecoder(p1.Body).Decode(&p1r))
	p1.Body.Close()
	h = p1r["content_hash"].(string)

	// Overwrite with "changed".
	p2 := c.do(t, http.MethodPatch, "/api/v1/notes/"+noteID, map[string]interface{}{
		"content": "# changed", "content_hash": h,
	})
	var p2r map[string]interface{}
	require.NoError(t, json.NewDecoder(p2.Body).Decode(&p2r))
	p2.Body.Close()
	h = p2r["content_hash"].(string)

	// Find SHA of "original" commit.
	histResp := c.do(t, http.MethodGet, "/api/v1/notes/"+noteID+"/history", nil)
	defer histResp.Body.Close()
	var entries []map[string]interface{}
	require.NoError(t, json.NewDecoder(histResp.Body).Decode(&entries))

	var originalSHA string
	for _, e := range entries {
		atResp := c.do(t, http.MethodGet, "/api/v1/notes/"+noteID+"/history/"+e["sha"].(string), nil)
		var atResult map[string]interface{}
		require.NoError(t, json.NewDecoder(atResp.Body).Decode(&atResult))
		atResp.Body.Close()
		if atResult["content"] == "# original" {
			originalSHA = e["sha"].(string)
			break
		}
	}
	require.NotEmpty(t, originalSHA)

	// Restore.
	restoreResp := c.do(t, http.MethodPost, "/api/v1/notes/"+noteID+"/history/"+originalSHA+"/restore",
		map[string]string{"content_hash": h})
	defer restoreResp.Body.Close()
	assert.Equal(t, http.StatusOK, restoreResp.StatusCode)

	// Verify the note content.
	noteResp := c.do(t, http.MethodGet, "/api/v1/notes/"+noteID, nil)
	defer noteResp.Body.Close()
	var restored map[string]interface{}
	require.NoError(t, json.NewDecoder(noteResp.Body).Decode(&restored))
	assert.Equal(t, "# original", restored["content"])
}

func TestHandler_History_Unauthenticated(t *testing.T) {
	c := newTestClientGit(t)
	// No login.
	resp := c.do(t, http.MethodGet, "/api/v1/notes/1/history", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestHandler_History_At_Disabled(t *testing.T) {
	// Without git history enabled, At must return 501.
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{"title": "no-git"})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var note map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&note))
	noteID := i64str(int64(note["id"].(float64)))

	atResp := c.do(t, http.MethodGet, "/api/v1/notes/"+noteID+"/history/abc1234", nil)
	defer atResp.Body.Close()
	assert.Equal(t, http.StatusNotImplemented, atResp.StatusCode)
}

func TestHandler_History_Restore_Disabled(t *testing.T) {
	// Without git history enabled, Restore must return 501.
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPost, "/api/v1/notes", map[string]interface{}{"title": "no-git-restore"})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var note map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&note))
	noteID := i64str(int64(note["id"].(float64)))
	hash := note["content_hash"].(string)

	restoreResp := c.do(t, http.MethodPost, "/api/v1/notes/"+noteID+"/history/abc1234/restore",
		map[string]string{"content_hash": hash})
	defer restoreResp.Body.Close()
	assert.Equal(t, http.StatusNotImplemented, restoreResp.StatusCode)
}

func TestHandler_Notes_ListAll_Returns200(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	createNoteHelper(t, c, "Note 1")
	createNoteHelper(t, c, "Note 2")

	resp := c.do(t, http.MethodGet, "/api/v1/notes/all", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var items []map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&items))
	assert.GreaterOrEqual(t, len(items), 2)
}

func TestHandler_Notes_Share_InvalidID(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodPost, "/api/v1/notes/notanumber/share", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_Notes_Share_Clear(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	noteID := createNoteHelper(t, c, "Shareable Note")

	// Share it first.
	shareResp := c.do(t, http.MethodPost, "/api/v1/notes/"+i64str(noteID)+"/share", nil)
	defer shareResp.Body.Close()
	require.Equal(t, http.StatusOK, shareResp.StatusCode)

	// Then clear the share token.
	clearResp := c.do(t, http.MethodPost, "/api/v1/notes/"+i64str(noteID)+"/share",
		map[string]bool{"clear": true})
	defer clearResp.Body.Close()
	require.Equal(t, http.StatusOK, clearResp.StatusCode)
	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(clearResp.Body).Decode(&result))
	assert.Nil(t, result["share_token"])
}

func TestHandler_Notes_Search_NoQuery(t *testing.T) {
	c := newTestClient(t)
	c.registerAndLogin(t)

	resp := c.do(t, http.MethodGet, "/api/v1/notes/search", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_Journals_List_Unauthenticated(t *testing.T) {
	c := newTestClient(t)
	// Not logged in.
	resp := c.do(t, http.MethodGet, "/api/v1/journals", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

