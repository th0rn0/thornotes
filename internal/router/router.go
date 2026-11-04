package router

import (
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
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
	secureCookies bool,
	enableGitHistory bool,
	dbHealth handler.DBHealthChecker,
) http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(zerologAccessMiddleware())
	r.Use(security.SecureHeadersMiddleware())

	// Snapshot the static tree once so every asset URL the shell emits and
	// every cache header it is served with agree. A failure here would mean
	// the embed is broken; degrade to an empty version, which disables the
	// immutable path and leaves everything on no-cache rather than pinning
	// bytes that cannot be revalidated.
	assets, err := indexAssets(staticFS)
	if err != nil {
		log.Error().Err(err).Msg("index static assets")
		assets = &assetIndex{etags: map[string]string{}}
	}
	shell := assets.serveAppShell(tmpl)

	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		// API and MCP paths return JSON 404; everything else serves the app
		// shell so that deep-linked URLs (e.g. /folder/note-slug) work when
		// navigated to directly.
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/mcp") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		shell(c)
	})

	authH := handler.NewAuthHandler(authSvc, notesSvc, secureCookies)
	foldersH := handler.NewFoldersHandler(notesSvc)
	notesH := handler.NewNotesHandler(notesSvc)
	shareH := handler.NewShareHandler(notesSvc, tmpl)
	accountH := handler.NewAccountHandler(apiTokenRepo)
	mcpH := handler.NewMCPHandler(notesSvc)
	eventsH := handler.NewEventsHandler(h)
	journalsH := handler.NewJournalsHandler(notesSvc)
	historyH := handler.NewHistoryHandler(notesSvc)
	importH := handler.NewImportHandler(notesSvc)

	bearerMW := auth.BearerMiddleware(apiTokenRepo, userRepo)
	sessionMW := authSvc.SessionMiddleware()
	csrfMW := security.CSRFGinMiddleware()
	rateMW := rateLimiter.GinMiddleware()

	// Health check — unauthenticated, used by load balancers and monitoring.
	// The notes-root check reports whether the reconciler trusts the volume.
	healthH := handler.NewHealthHandler(dbHealth, handler.WithNotesRootChecker(notesSvc))
	r.GET("/healthz", healthH.Check)

	// Static files. Cache headers depend on the ?v= asset version: only the
	// current version is immutable (see serveStatic).
	r.GET("/static/*filepath", assets.serveStatic(staticFS))
	r.HEAD("/static/*filepath", assets.serveStatic(staticFS))

	// Service worker must be served at root scope (not under /static/), with
	// the asset version substituted so its bytes change on every release.
	r.GET("/sw.js", assets.serveServiceWorker(staticFS))
	r.HEAD("/sw.js", assets.serveServiceWorker(staticFS))

	// App shell — uncached, because it pins the asset URLs.
	r.GET("/", shell)

	// Public shared note view.
	r.GET("/s/:token", shareH.View)

	// Auth endpoints.
	authGroup := r.Group("/api/v1/auth")
	{
		// Unauth: lets the SPA decide whether to render the register form.
		// Intentionally NOT behind rateMW: the auth rate-limiter is per-IP
		// with a small burst, and /config is read by every unauth page load
		// alongside /me. Sharing the bucket with /login meant a noisy
		// neighbor on a NAT/CGNAT/corp-proxy IP could exhaust the limit and
		// fail-close the register form on the OPEN instance for every
		// honest visitor on that IP until the bucket refilled (90s). The
		// endpoint exposes a single config bool with no auth side effects,
		// so it doesn't need the per-IP burst protection. The same
		// predicate drives the route gate inside authH.Register below, so
		// a false here is guaranteed to be a 404 on the POST.
		authGroup.GET("/config", authH.Config)
		authGroup.POST("/register", rateMW, authH.Register)
		authGroup.POST("/login", rateMW, authH.Login)
		authGroup.POST("/logout", sessionMW, authH.Logout)
		authGroup.GET("/me", sessionMW, authH.Me)
	}

	// CSRF token.
	r.GET("/api/v1/csrf", sessionMW, authH.CSRF)

	// Folders.
	folders := r.Group("/api/v1/folders", sessionMW)
	{
		folders.GET("", foldersH.List)
		folders.POST("", csrfMW, foldersH.Create)
		folders.PATCH("/:id", csrfMW, foldersH.Rename)
		folders.PATCH("/:id/move", csrfMW, foldersH.Move)
		folders.DELETE("/:id", csrfMW, foldersH.Delete)
		folders.GET("/:id/notes", foldersH.ListNotes)
	}

	// Notes.
	notesGroup := r.Group("/api/v1/notes", sessionMW)
	{
		notesGroup.POST("", csrfMW, notesH.Create)
		notesGroup.GET("", notesH.Search)
		notesGroup.GET("/root", notesH.ListRoot)
		notesGroup.GET("/all", notesH.ListAll)
		notesGroup.GET("/context", notesH.Context)
		notesGroup.GET("/:id", notesH.Get)
		notesGroup.GET("/:id/links", notesH.Links)
		notesGroup.GET("/:id/backlinks", notesH.Backlinks)
		notesGroup.PATCH("/:id", csrfMW, notesH.Patch)
		notesGroup.PATCH("/:id/move", csrfMW, notesH.Move)
		notesGroup.DELETE("/:id", csrfMW, notesH.Delete)
		notesGroup.POST("/:id/share", csrfMW, notesH.Share)
	}

	// Note history (git-backed, requires --enable-git-history).
	// Routes are always registered; service methods return 501 when disabled.
	_ = enableGitHistory // surfaced via service.GitHistoryEnabled()
	history := r.Group("/api/v1/notes", sessionMW)
	{
		history.GET("/:id/history", historyH.List)
		history.GET("/:id/history/:sha", historyH.At)
		history.POST("/:id/history/:sha/restore", csrfMW, historyH.Restore)
	}

	// Account — API token management.
	account := r.Group("/api/v1/account", sessionMW)
	{
		account.GET("/tokens", accountH.ListTokens)
		account.POST("/tokens", csrfMW, accountH.CreateToken)
		account.DELETE("/tokens/:id", csrfMW, accountH.DeleteToken)
		account.PUT("/tokens/:id/permissions", csrfMW, accountH.UpdateTokenPermissions)
	}

	// Import.
	r.POST("/api/v1/import", sessionMW, csrfMW, importH.Import)

	// Journals.
	journals := r.Group("/api/v1/journals", sessionMW)
	{
		journals.GET("", journalsH.List)
		journals.POST("", csrfMW, journalsH.Create)
		journals.DELETE("/:id", csrfMW, journalsH.Delete)
		journals.GET("/:id/today", journalsH.Today)
	}

	// Server-Sent Events.
	r.GET("/api/v1/events", sessionMW, eventsH.Stream)

	// MCP — Streamable HTTP transport (2025-03-26), bearer token auth, no CSRF.
	r.POST("/mcp", bearerMW, mcpH.HandlePOST)
	r.GET("/mcp", bearerMW, mcpH.HandleGET)
	r.DELETE("/mcp", bearerMW, mcpH.HandleDELETE)

	return r
}

func zerologAccessMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Info().
			Str("method", c.Request.Method).
			Str("path", c.Request.URL.Path).
			Int("status", c.Writer.Status()).
			Dur("latency", time.Since(start)).
			Str("ip", c.ClientIP()).
			Msg("request")
	}
}
