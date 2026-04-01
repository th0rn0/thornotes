package router

import (
	"html/template"
	"net/http"
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
) http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(zerologAccessMiddleware())
	r.Use(security.SecureHeadersMiddleware())

	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	})

	authH := handler.NewAuthHandler(authSvc, notesSvc, secureCookies)
	foldersH := handler.NewFoldersHandler(notesSvc)
	notesH := handler.NewNotesHandler(notesSvc)
	shareH := handler.NewShareHandler(notesSvc, tmpl)
	accountH := handler.NewAccountHandler(apiTokenRepo)
	mcpH := handler.NewMCPHandler(notesSvc)
	eventsH := handler.NewEventsHandler(h)
	journalsH := handler.NewJournalsHandler(notesSvc)

	bearerMW := auth.BearerMiddleware(apiTokenRepo, userRepo)
	sessionMW := authSvc.SessionMiddleware()
	csrfMW := security.CSRFGinMiddleware()
	rateMW := rateLimiter.GinMiddleware()

	// Static files.
	r.StaticFS("/static", staticFS)

	// Service worker must be served at root scope (not under /static/).
	r.GET("/sw.js", func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
		c.Header("Service-Worker-Allowed", "/")
		c.FileFromFS("sw.js", staticFS)
	})

	// App shell.
	r.GET("/", func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.ExecuteTemplate(c.Writer, "app.html", nil); err != nil {
			log.Error().Err(err).Msg("execute app template")
		}
	})

	// Public shared note view.
	r.GET("/s/:token", shareH.View)

	// Auth endpoints.
	authGroup := r.Group("/api/v1/auth")
	{
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
		notesGroup.GET("/:id", notesH.Get)
		notesGroup.PATCH("/:id", csrfMW, notesH.Patch)
		notesGroup.DELETE("/:id", csrfMW, notesH.Delete)
		notesGroup.POST("/:id/share", csrfMW, notesH.Share)
	}

	// Account — API token management.
	account := r.Group("/api/v1/account", sessionMW)
	{
		account.GET("/tokens", accountH.ListTokens)
		account.POST("/tokens", csrfMW, accountH.CreateToken)
		account.DELETE("/tokens/:id", csrfMW, accountH.DeleteToken)
	}

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

	// MCP — bearer token auth, no CSRF (token-authenticated API).
	r.POST("/mcp", bearerMW, mcpH.Handle)

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
