package router

import (
	"html/template"
	"log/slog"
	"net/http"

	"github.com/th0rn0/thornotes/internal/auth"
	"github.com/th0rn0/thornotes/internal/handler"
	"github.com/th0rn0/thornotes/internal/hub"
	"github.com/th0rn0/thornotes/internal/notes"
	"github.com/th0rn0/thornotes/internal/repository"
	"github.com/th0rn0/thornotes/internal/security"
)

func New(
	authSvc *auth.Service,
	notesSvc *notes.Service,
	apiTokenRepo repository.APITokenRepository,
	userRepo repository.UserRepository,
	rateLimiter *security.AuthRateLimiter,
	tmpl *template.Template,
	staticFS http.FileSystem,
	h *hub.Hub,
) http.Handler {
	mux := http.NewServeMux()

	authH := handler.NewAuthHandler(authSvc, notesSvc)
	foldersH := handler.NewFoldersHandler(notesSvc)
	notesH := handler.NewNotesHandler(notesSvc)
	shareH := handler.NewShareHandler(notesSvc, tmpl)
	accountH := handler.NewAccountHandler(apiTokenRepo)
	mcpH := handler.NewMCPHandler(notesSvc)
	eventsH := handler.NewEventsHandler(h)
	journalsH := handler.NewJournalsHandler(notesSvc)

	bearerMW := auth.BearerMiddleware(apiTokenRepo, userRepo)

	// Static files — the embedded FS has paths like "web/static/js/app.js".
	// Strip "/static/" prefix and serve from "web/static/" subtree.
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(staticFS)))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.ExecuteTemplate(w, "app.html", nil); err != nil {
			slog.Error("execute app template", "err", err)
		}
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
	mux.Handle("GET /api/v1/notes/root", authSvc.SessionMiddleware(http.HandlerFunc(notesH.ListRoot)))
	mux.Handle("GET /api/v1/notes/all", authSvc.SessionMiddleware(http.HandlerFunc(notesH.ListAll)))
	mux.Handle("GET /api/v1/notes/{id}", authSvc.SessionMiddleware(http.HandlerFunc(notesH.Get)))
	mux.Handle("PATCH /api/v1/notes/{id}", authSvc.SessionMiddleware(security.CSRFMiddleware(http.HandlerFunc(notesH.Patch))))
	mux.Handle("DELETE /api/v1/notes/{id}", authSvc.SessionMiddleware(security.CSRFMiddleware(http.HandlerFunc(notesH.Delete))))
	mux.Handle("POST /api/v1/notes/{id}/share", authSvc.SessionMiddleware(security.CSRFMiddleware(http.HandlerFunc(notesH.Share))))

	// Account — API token management (session + CSRF for mutations).
	mux.Handle("GET /api/v1/account/tokens", authSvc.SessionMiddleware(http.HandlerFunc(accountH.ListTokens)))
	mux.Handle("POST /api/v1/account/tokens", authSvc.SessionMiddleware(security.CSRFMiddleware(http.HandlerFunc(accountH.CreateToken))))
	mux.Handle("DELETE /api/v1/account/tokens/{id}", authSvc.SessionMiddleware(security.CSRFMiddleware(http.HandlerFunc(accountH.DeleteToken))))

	// Journals.
	mux.Handle("GET /api/v1/journals", authSvc.SessionMiddleware(http.HandlerFunc(journalsH.List)))
	mux.Handle("POST /api/v1/journals", authSvc.SessionMiddleware(security.CSRFMiddleware(http.HandlerFunc(journalsH.Create))))
	mux.Handle("DELETE /api/v1/journals/{id}", authSvc.SessionMiddleware(security.CSRFMiddleware(http.HandlerFunc(journalsH.Delete))))
	mux.Handle("GET /api/v1/journals/{id}/today", authSvc.SessionMiddleware(http.HandlerFunc(journalsH.Today)))

	// Server-Sent Events — session auth, long-lived connection for disk-change notifications.
	mux.Handle("GET /api/v1/events", authSvc.SessionMiddleware(http.HandlerFunc(eventsH.Stream)))

	// MCP — bearer token auth, no CSRF (token-authenticated API).
	mux.Handle("POST /mcp", bearerMW(http.HandlerFunc(mcpH.Handle)))

	return security.SecureHeaders(mux)
}
