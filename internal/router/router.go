package router

import (
	"html/template"
	"net/http"

	"github.com/th0rn0/thornotes/internal/auth"
	"github.com/th0rn0/thornotes/internal/handler"
	"github.com/th0rn0/thornotes/internal/notes"
	"github.com/th0rn0/thornotes/internal/security"
)

func New(
	authSvc *auth.Service,
	notesSvc *notes.Service,
	rateLimiter *security.AuthRateLimiter,
	tmpl *template.Template,
	staticFS http.FileSystem,
) http.Handler {
	mux := http.NewServeMux()

	authH := handler.NewAuthHandler(authSvc)
	foldersH := handler.NewFoldersHandler(notesSvc)
	notesH := handler.NewNotesHandler(notesSvc)
	shareH := handler.NewShareHandler(notesSvc, tmpl)

	// Static files — the embedded FS has paths like "web/static/js/app.js".
	// Strip "/static/" prefix and serve from "web/static/" subtree.
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(staticFS)))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		tmpl.ExecuteTemplate(w, "app.html", nil)
	})

	// Public shared note view.
	mux.HandleFunc("GET /s/{token}", shareH.View)

	// Auth endpoints (rate-limited, no session required for login/register).
	mux.Handle("POST /api/v1/auth/register", rateLimiter.Middleware(http.HandlerFunc(authH.Register)))
	mux.Handle("POST /api/v1/auth/login", rateLimiter.Middleware(http.HandlerFunc(authH.Login)))

	// Session-required auth endpoints.
	mux.Handle("POST /api/v1/auth/logout", authSvc.SessionMiddleware(http.HandlerFunc(authH.Logout)))
	mux.Handle("GET /api/v1/auth/me", authSvc.SessionMiddleware(http.HandlerFunc(authH.Me)))
	mux.Handle("GET /api/v1/csrf", authSvc.SessionMiddleware(http.HandlerFunc(authH.CSRF)))

	// Folders (session + CSRF required for mutating methods).
	mux.Handle("GET /api/v1/folders", authSvc.SessionMiddleware(http.HandlerFunc(foldersH.List)))
	mux.Handle("POST /api/v1/folders", authSvc.SessionMiddleware(security.CSRFMiddleware(http.HandlerFunc(foldersH.Create))))
	mux.Handle("PATCH /api/v1/folders/{id}", authSvc.SessionMiddleware(security.CSRFMiddleware(http.HandlerFunc(foldersH.Rename))))
	mux.Handle("DELETE /api/v1/folders/{id}", authSvc.SessionMiddleware(security.CSRFMiddleware(http.HandlerFunc(foldersH.Delete))))
	mux.Handle("GET /api/v1/folders/{id}/notes", authSvc.SessionMiddleware(http.HandlerFunc(foldersH.ListNotes)))

	// Notes.
	mux.Handle("POST /api/v1/notes", authSvc.SessionMiddleware(security.CSRFMiddleware(http.HandlerFunc(notesH.Create))))
	mux.Handle("GET /api/v1/notes", authSvc.SessionMiddleware(http.HandlerFunc(notesH.Search)))
	mux.Handle("GET /api/v1/notes/{id}", authSvc.SessionMiddleware(http.HandlerFunc(notesH.Get)))
	mux.Handle("PATCH /api/v1/notes/{id}", authSvc.SessionMiddleware(security.CSRFMiddleware(http.HandlerFunc(notesH.Patch))))
	mux.Handle("DELETE /api/v1/notes/{id}", authSvc.SessionMiddleware(security.CSRFMiddleware(http.HandlerFunc(notesH.Delete))))
	mux.Handle("POST /api/v1/notes/{id}/share", authSvc.SessionMiddleware(security.CSRFMiddleware(http.HandlerFunc(notesH.Share))))

	return security.SecureHeaders(mux)
}
