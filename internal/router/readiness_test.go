package router_test

import (
	"bytes"
	"context"
	"encoding/json"
	htmltemplate "html/template"
	iofs "io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	thornotes "github.com/th0rn0/thornotes"
	"github.com/th0rn0/thornotes/internal/auth"
	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/hub"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/notes"
	"github.com/th0rn0/thornotes/internal/repository/sqlite"
	"github.com/th0rn0/thornotes/internal/router"
	"github.com/th0rn0/thornotes/internal/security"
)

// startupStack is a full running instance (real database, real notes root, real
// router) whose startup reconcile the test drives by hand, so the ordering that
// main.go performs can be reproduced without racing a real boot.
type startupStack struct {
	svc      *notes.Service
	user     *model.User
	notesDir string
	server   *httptest.Server
	client   *http.Client
}

// newStartupStack boots an instance whose database has a user and an *empty*
// note index while the user's notes root already holds their notes: a restored
// notes directory, a rebuilt index, or a fresh deploy over an existing tree.
// Nothing on disk is created through the API, so no row exists for it until the
// startup reconcile inserts one — which is the window this test is about.
func newStartupStack(t *testing.T, noteFiles int) *startupStack {
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
	svc := notes.NewService(noteRepo, folderRepo, searchRepo,
		sqlite.NewJournalRepo(pool.ReadDB, pool.WriteDB),
		sqlite.NewLinkRepo(pool.ReadDB, pool.WriteDB), fs)

	rateLimiter := security.NewAuthRateLimiter(nil)
	t.Cleanup(rateLimiter.Stop)

	tmpl, err := htmltemplate.ParseFS(thornotes.TemplatesFS, "web/templates/*.html")
	require.NoError(t, err)
	staticSub, err := iofs.Sub(thornotes.StaticFS, "web/static")
	require.NoError(t, err)

	h := router.New(authSvc, svc, apiTokenRepo, userRepo, rateLimiter, tmpl,
		http.FS(staticSub), hub.New(), false, false, &stubDBHealth{}, context.Background(), "test")
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{Jar: jar}

	// A real register, so the instance has a user with a UUID and its directory.
	resp := post(t, client, server.URL+"/api/v1/auth/register",
		map[string]string{"username": "startup", "password": "startup-password-123"})
	require.Equal(t, http.StatusCreated, resp)
	// Log in for a session: register deliberately does not open one.
	resp = post(t, client, server.URL+"/api/v1/auth/login",
		map[string]string{"username": "startup", "password": "startup-password-123"})
	require.Equal(t, http.StatusOK, resp)

	// Read the user back for its id and UUID, then write the notes straight to
	// disk — never through the API — so the index starts out empty.
	user, err := userRepo.GetByUsername(context.Background(), "startup")
	require.NoError(t, err)
	require.NotEmpty(t, user.UUID)

	writeNotesOnDisk(t, filepath.Join(notesDir, user.UUID), noteFiles)

	return &startupStack{svc: svc, user: user, notesDir: notesDir, server: server, client: client}
}

// writeNotesOnDisk lays down n note files in two folders: the test's expectation
// is always "whatever is on disk must be served".
func writeNotesOnDisk(t *testing.T, userDir string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		folder := "Inbox"
		if i%2 == 1 {
			folder = "Work/Projects"
		}
		dir := filepath.Join(userDir, folder)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		body := "# Note " + strconv.Itoa(i) + "\n\nThe quokka lives in note " + strconv.Itoa(i) + ".\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "note-"+strconv.Itoa(i)+".md"), []byte(body), 0o600))
	}
}

func post(t *testing.T, c *http.Client, url string, body any) int {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	return resp.StatusCode
}

func getRaw(t *testing.T, c *http.Client, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := c.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, err = buf.ReadFrom(resp.Body)
	require.NoError(t, err)
	return resp, buf.Bytes()
}

// TestStartupReadiness_NoRequestServedAgainstAHalfReconciledIndex is the
// regression test for N1 (ticket 466): the HTTP listener opens before the
// startup reconcile finishes, so a request arriving during that pass used to be
// answered from an index that did not contain the notes yet.
//
// On this build a 401-note vault served a 64-note list for 2.7 seconds and
// /healthz called the instance "ok" throughout. The wrong answer is not a slow
// answer: a short list, a search that misses, and a 404 for a note that is
// plainly on disk are all indistinguishable from real deletions.
//
// The test drives the same three steps main.go drives — open the window, serve,
// run the pass and close the window — with the window held open instead of raced
// against, so it is deterministic on a fast machine and a slow one.
func TestStartupReadiness_NoRequestServedAgainstAHalfReconciledIndex(t *testing.T) {
	// 40 is under the search endpoint's 50-row limit, so "search returns every
	// note that states the term" is an exact expectation rather than a floor.
	const noteFiles = 40
	st := newStartupStack(t, noteFiles)

	// Files on disk: noteFiles, plus the welcome note register writes.
	onDisk := noteFiles + 1

	// --- the window main.go opens before the listener accepts a request ---
	st.svc.BeginStartup()

	t.Run("index-derived requests are refused, not answered from a partial index", func(t *testing.T) {
		for _, path := range []string{
			"/api/v1/notes/all",
			"/api/v1/notes?q=quokka",
			"/api/v1/folders",
			"/api/v1/journals",
		} {
			resp, body := getRaw(t, st.client, st.server.URL+path)
			assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode,
				"%s answered %s while the index was being reconciled: %s", path, resp.Status, body)
			assert.Equal(t, "1", resp.Header.Get("Retry-After"), "%s must tell the caller to retry", path)
		}
	})

	t.Run("healthz reports reconciling rather than ok", func(t *testing.T) {
		resp, body := getRaw(t, st.client, st.server.URL+"/healthz")
		require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode,
			"a booting instance must not be reported ready to a load balancer: %s", body)

		var parsed map[string]any
		require.NoError(t, json.Unmarshal(body, &parsed))
		assert.Equal(t, "reconciling", parsed["status"])
		startup, ok := parsed["startup"].(map[string]any)
		require.True(t, ok, "healthz must distinguish reconciling from ready, got %s", body)
		assert.Equal(t, "reconciling", startup["phase"])
	})

	t.Run("paths that need no index keep working", func(t *testing.T) {
		// The gate is not a blackout: the browser must still be able to load the
		// shell and the session must still be readable, or a booting instance
		// looks like a broken one.
		resp, _ := getRaw(t, st.client, st.server.URL+"/api/v1/auth/me")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		resp, _ = getRaw(t, st.client, st.server.URL+"/")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		// A deep link is a bookmark, not an index read: it serves the shell, which
		// then retries the API. Answering it with a JSON 503 would be a regression
		// this gate would otherwise introduce.
		resp, _ = getRaw(t, st.client, st.server.URL+"/folder/some-note")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	// --- the startup pass, then the window closes ---
	_, err := st.svc.Reconcile(context.Background(), st.user)
	require.NoError(t, err)
	st.svc.EndStartup()

	t.Run("every note on disk is listed, and none of them answers 404", func(t *testing.T) {
		resp, body := getRaw(t, st.client, st.server.URL+"/api/v1/notes/all")
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)

		var listed []map[string]any
		require.NoError(t, json.Unmarshal(body, &listed))
		require.Len(t, listed, onDisk,
			"the list must hold every note that is on disk after the startup pass")

		for _, item := range listed {
			id := int64(item["id"].(float64))
			noteResp, noteBody := getRaw(t, st.client,
				st.server.URL+"/api/v1/notes/"+strconv.FormatInt(id, 10))
			assert.Equal(t, http.StatusOK, noteResp.StatusCode,
				"note %v is on disk and in the list but answered %s: %s", id, noteResp.Status, noteBody)
		}
	})

	t.Run("search finds the note that states the term", func(t *testing.T) {
		_, body := getRaw(t, st.client, st.server.URL+"/api/v1/notes?q=quokka")
		var hits []map[string]any
		require.NoError(t, json.Unmarshal(body, &hits))
		assert.Len(t, hits, noteFiles, "every written note states the term, so all must be found")
	})

	t.Run("healthz reports ready once the pass is done", func(t *testing.T) {
		resp, body := getRaw(t, st.client, st.server.URL+"/healthz")
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)

		var parsed map[string]any
		require.NoError(t, json.Unmarshal(body, &parsed))
		assert.Equal(t, "ok", parsed["status"])
		startup, ok := parsed["startup"].(map[string]any)
		require.True(t, ok, "got %s", body)
		assert.Equal(t, "ready", startup["phase"])
	})
}

// TestStartupReadiness_PublicShareViewIsGated: the public /s/:token view reads a
// note, so it is part of the same index and must not be served mid-pass either.
// It is the one gated route a caller can reach without a session, which is why it
// gets its own case: a "not found" there would look like a burned share link.
func TestStartupReadiness_PublicShareViewIsGated(t *testing.T) {
	st := newStartupStack(t, 4)
	st.svc.BeginStartup()

	resp, _ := getRaw(t, st.client, st.server.URL+"/s/some-token")
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode,
		"a share link must not answer 404 while the index is being reconciled")

	st.svc.EndStartup()
}
