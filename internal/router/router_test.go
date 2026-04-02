package router_test

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

	iofs "io/fs"
)

func buildHandler(t *testing.T) http.Handler {
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

	return router.New(authSvc, notesSvc, apiTokenRepo, userRepo, rateLimiter, tmpl, http.FS(staticSub), hub.New(), false, false)
}

func TestRouter_New(t *testing.T) {
	h := buildHandler(t)
	assert.NotNil(t, h)
}

// API paths that don't exist still return 404 JSON.
func TestRouter_API_Serves404(t *testing.T) {
	h := buildHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nonexistent", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), "json")
}

// Unknown non-API paths serve the app shell for SPA deep linking.
func TestRouter_DeepLink_ServesAppShell(t *testing.T) {
	h := buildHandler(t)

	for _, path := range []string{
		"/my-folder/my-note",
		"/my-note",
		"/a/b/c/note-slug",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			assert.Equal(t, http.StatusOK, rr.Code)
			assert.Contains(t, rr.Header().Get("Content-Type"), "text/html")
			assert.Contains(t, rr.Body.String(), "thornotes")
		})
	}
}

// MCP endpoints still return 404 for unknown sub-paths.
func TestRouter_MCP_Unknown_Serves404(t *testing.T) {
	h := buildHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/mcp/unknown", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestRouter_ServesServiceWorker(t *testing.T) {
	h := buildHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/sw.js", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), "javascript")
	assert.Equal(t, "no-cache", rr.Header().Get("Cache-Control"))
	assert.Equal(t, "/", rr.Header().Get("Service-Worker-Allowed"))
}

func TestRouter_ServesManifest(t *testing.T) {
	h := buildHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/static/manifest.json", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), "json")
	assert.Contains(t, rr.Body.String(), "thornotes")
}
