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

	authSvc := auth.NewService(userRepo, sessionRepo, true)
	notesSvc := notes.NewService(noteRepo, folderRepo, searchRepo, fs)
	rateLimiter := security.NewAuthRateLimiter(nil)

	tmpl, err := template.ParseFS(thornotes.TemplatesFS, "web/templates/*.html")
	require.NoError(t, err)

	staticSub, err := iofs.Sub(thornotes.StaticFS, "web/static")
	require.NoError(t, err)

	return router.New(authSvc, notesSvc, apiTokenRepo, userRepo, rateLimiter, tmpl, http.FS(staticSub), hub.New())
}

func TestRouter_New(t *testing.T) {
	h := buildHandler(t)
	assert.NotNil(t, h)
}

func TestRouter_Serves404(t *testing.T) {
	h := buildHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}
