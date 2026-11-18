package router

import (
	"context"
	"html/template"
	"net/http"
	"regexp"
	"strings"
	"time"

	"crypto/sha256"
	"encoding/hex"
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
	shareLimiter *security.ShareRateLimiter,
	tmpl *template.Template,
	staticFS http.FileSystem,
	h *hub.Hub,
	secureCookies bool,
	enableGitHistory bool,
	dbHealth handler.DBHealthChecker,
	sseShutdown context.Context,
	buildVersion string,
) http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(zerologAccessMiddleware())
	r.Use(security.SecureHeadersMiddleware())
	r.Use(readinessGate(notesSvc))

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
		// A share URL that does not match a route — /f/ with no token, /F/<token>,
		// /f//<token> — used to fall through to the shell with the shell's own
		// cache headers and no robots header, so "no-store and noindex on every
		// outcome" was true of routes rather than of URLs. The shell is not
		// secret, but the promise in the changelog is about the surface, and
		// these are exactly the shapes a rewritten link arrives as.
		if isSharePath(p) {
			c.Header("Cache-Control", "no-store")
			c.Header("X-Robots-Tag", "noindex")
		}
		shell(c)
	})

	authH := handler.NewAuthHandler(authSvc, notesSvc, secureCookies)
	foldersH := handler.NewFoldersHandler(notesSvc)
	notesH := handler.NewNotesHandler(notesSvc)
	shareH := handler.NewShareHandler(notesSvc, tmpl)
	accountH := handler.NewAccountHandler(apiTokenRepo)
	mcpH := handler.NewMCPHandler(notesSvc)
	eventsH := handler.NewEventsHandler(h, sseShutdown)
	journalsH := handler.NewJournalsHandler(notesSvc)
	historyH := handler.NewHistoryHandler(notesSvc)
	importH := handler.NewImportHandler(notesSvc)
	exportH := handler.NewExportHandler(notesSvc)

	bearerMW := auth.BearerMiddleware(apiTokenRepo, userRepo)
	sessionMW := authSvc.SessionMiddleware()
	csrfMW := security.CSRFGinMiddleware()
	rateMW := rateLimiter.GinMiddleware()

	// The anonymous share surfaces get their own limiter — per (IP, token), with
	// read-sized budgets. The auth bucket (10 per 15 min per IP) is unusable
	// here: one visit to a ten-note folder would trip it.
	//
	// A nil limiter is refused rather than allowed: an instance that mounted the
	// public surface with no throttle is the defect this wiring exists to close,
	// so fall back to the defaults instead of falling open.
	if shareLimiter == nil {
		log.Warn().Msg("share rate limiter not provided; using default budgets")
		shareLimiter = security.NewShareRateLimiter(nil, security.ShareRateLimits{
			PerMinute: security.DefaultShareRatePerMinute,
			PerHour:   security.DefaultShareRatePerHour,
		})
	}
	shareMW := handler.ShareThrottleMiddleware(shareLimiter)

	// Health check — unauthenticated, used by load balancers and monitoring.
	// The notes-root check reports whether the reconciler trusts the volume; the
	// schema block reports the applied migration version against this build's
	// floor, which is present whenever the pool implements SchemaReporter.
	healthOpts := []handler.HealthOption{
		handler.WithNotesRootChecker(notesSvc),
		handler.WithBuildVersion(buildVersion),
		handler.WithStartupProbe(notesSvc),
	}
	if schemaReporter, ok := dbHealth.(handler.SchemaReporter); ok {
		healthOpts = append(healthOpts, handler.WithSchemaReporter(schemaReporter))
	}
	healthH := handler.NewHealthHandler(dbHealth, healthOpts...)
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
	r.GET("/s/:token", shareMW, shareH.View)

	// Public shared folder views. Same capability model as /s/:token — the
	// segment after the prefix is the credential, read with no session — and
	// the same two constraints follow: these are HTML only (no anonymous JSON,
	// which would be a second public API to keep stable) and every failure is
	// one 404 at the handler.
	//
	// Two path prefixes, d/ and n/, rather than one route that guesses: a folder
	// called "design" and a note "design.md" can sit in the same parent, and one
	// route that tries both is one route that can render the wrong thing.
	r.GET("/f/:token", shareMW, shareH.ViewFolder)
	r.GET("/f/:token/d/*path", shareMW, shareH.ViewFolder)
	r.GET("/f/:token/n/*path", shareMW, shareH.ViewFolderNote)

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
		// Folder shares (ticket 616, slice 1): issue, rotate and revoke the
		// anonymous link for one folder. CSRF-protected like every other write in
		// this group, because a link that publishes a folder must not be issuable
		// from another origin.
		folders.POST("/:id/share", csrfMW, foldersH.Share)
		folders.GET("/:id/zip", exportH.Folder)
		// HEAD is the UI's cheap preflight; Gin does not answer HEAD for a GET
		// route, and without it the preflight 404s on rows that exist.
		folders.HEAD("/:id/zip", exportH.HeadFolder)
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
		notesGroup.GET("/:id/zip", exportH.Note)
		notesGroup.HEAD("/:id/zip", exportH.HeadNote)
		notesGroup.PATCH("/:id", csrfMW, notesH.Patch)
		notesGroup.PATCH("/:id/move", csrfMW, notesH.Move)
		notesGroup.DELETE("/:id", csrfMW, notesH.Delete)
		notesGroup.POST("/:id/share", csrfMW, notesH.Share)
		notesGroup.PATCH("/:id/share", csrfMW, notesH.PatchShareExpiry)
	}

	// The caller's own share links. Read-only and session-scoped; the response
	// carries a redacted token prefix, never a usable token, so this endpoint
	// cannot be used to harvest capabilities.
	shares := r.Group("/api/v1/shares", sessionMW)
	{
		shares.GET("", shareH.List)
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

	// Links — the read-only dangling-link report (ticket 633).
	links := r.Group("/api/v1/links", sessionMW)
	{
		links.GET("/broken", notesH.BrokenLinks)
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

// The public share routes. The segment after each prefix is a bearer
// capability: whoever holds it reads the note (or the folder), with no session
// and no account.
const (
	sharePathPrefix       = "/s/"
	folderSharePathPrefix = "/f/"
)

// redactLogPath replaces the token in a share URL with a short, stable hash
// before it reaches the access log.
//
// The access log is the one place a working capability would otherwise be
// written down in plaintext, and logs are copied, shipped and retained far more
// casually than the tokens they describe. The hash keeps the log useful — every
// request for the same link still carries the same handle, so an operator can
// count and trace a single share — while giving a reader of the log nothing they
// can replay. Hashing rather than dropping the segment is deliberate: "/s/…" on
// every line would make a share indistinguishable from a 404 sweep.
//
// Both public share surfaces are covered (/s/ for a note, /f/ for a folder),
// because a second prefix added later without this branch would be a token
// written to the log in full — which is exactly how ticket 616 found it.
//
// Only the token segment is hashed. A deeper path is kept, so
// /f/<token>/n/projects/plan logs as /f/t_ab12cd34/n/projects/plan: a note read
// stays attributable to the link it happened on, and to a log reader nothing is
// lost that was not already public in the address bar. Everything that is not a
// share URL is returned untouched.
func redactLogPath(path string) string {
	if !isSharePath(path) {
		return path
	}
	// Redact by shape, not by position.
	//
	// The previous version keyed on "the first segment after a case-sensitive
	// /f/ prefix" and returned the path untouched when that segment was empty.
	// So /F/<token>, /f//<token>, /f/<token>/../<token> and /f/<token>/n/<token>
	// all reached the access log with the live token in cleartext — while the
	// shipped test, which drove only canonical URLs, stayed green. A redaction
	// that fails open on a mangled URL is worse than none, because the log is
	// where a recursive folder capability ends up when a mail scanner or a
	// double-paste rewrites the link.
	//
	// Matching the token's shape anywhere under a share prefix closes every
	// current and future shape in one rule. The cost: a note legitimately named
	// as 32 hex characters under a share path is hashed in the log too —
	// cosmetic, log output only, no behaviour change.
	segments := strings.Split(path, "/")
	changed := false
	for i, seg := range segments {
		if seg == "" || !strings.Contains(seg, "%") && !shareTokenSegment.MatchString(seg) {
			continue
		}
		// A percent-encoded separator (/f/%2F<token>) keeps the token in the
		// same segment as the encoding, so matching the whole segment is not
		// enough — replace every 32-hex run inside it.
		redacted := shareTokenRun.ReplaceAllStringFunc(seg, func(run string) string {
			sum := sha256.Sum256([]byte(strings.ToLower(run)))
			return "t_" + hex.EncodeToString(sum[:4])
		})
		if redacted != seg {
			segments[i] = redacted
			changed = true
		}
	}
	if !changed {
		return path
	}
	return strings.Join(segments, "/")
}

// shareTokenSegment matches the shape of a public share token or its uppercase
// form: 32 hex characters and nothing else.
var shareTokenSegment = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

// shareTokenRun matches a token-shaped run anywhere inside a path segment, which
// is what percent-encoded separators produce.
var shareTokenRun = regexp.MustCompile(`[0-9a-fA-F]{32}`)

// isSharePath reports whether path is under a public share prefix, matched
// case-insensitively and including the bare prefix itself — /F/<token> and /f
// are share URLs too, and they were the shapes that escaped redaction.
func isSharePath(path string) bool {
	lower := strings.ToLower(path)
	for _, prefix := range []string{sharePathPrefix, folderSharePathPrefix} {
		if lower == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func zerologAccessMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Info().
			Str("method", c.Request.Method).
			Str("path", redactLogPath(c.Request.URL.Path)).
			Int("status", c.Writer.Status()).
			Dur("latency", time.Since(start)).
			Str("ip", c.ClientIP()).
			Msg("request")
	}
}

// readinessGate refuses requests that would be answered from a note index the
// startup reconcile is still rebuilding (see internal/notes/readiness.go).
//
// The listener is deliberately open while that pass runs — the port has to
// answer, and a bind conflict should fail at t=0 rather than thirty seconds in
// — so the index cannot be trusted yet: the list is short, search misses notes
// that are on disk, and a note the caller already has an id for can answer 404.
// Those are wrong answers, and the honest response during the window is "not
// yet" rather than a partially populated list.
func readinessGate(svc *notes.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !svc.StartupReconciling() || !indexDerivedPath(c.Request.URL.Path) {
			c.Next()
			return
		}
		// One second is a hint, not a promise: the pass is bounded by the size of
		// the vault (33s for 5,000 notes on SQLite), so a client that honours
		// Retry-After exactly once will retry into another 503. It is the right
		// header to send anyway — it is what a retrying proxy reads.
		c.Header("Retry-After", "1")
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "starting up: the note index is still being reconciled, retry shortly",
			"phase": "reconciling",
		})
	}
}

// indexDerivedPath reports whether a request to path can only be answered from
// the note index, and so must be refused while the startup pass is running.
//
// The gate is expressed as "inside the API, MCP or the public share view, unless
// it is one of the two API routes that touch only sessions and users" rather
// than as a list of index-reading routes. The API is one namespace, so a route
// added later under it is covered by default — the failure mode of a per-route
// list is that the index quietly stops being protected the first time somebody
// forgets to extend it. The exception list is two entries long and does not grow
// with the product:
//
//   - /api/v1/auth/* (register, login, logout, me, config) and /api/v1/csrf read
//     users and sessions only. Blocking login for the length of the pass would be
//     a worse outage than the one being fixed, and it is the one window in which
//     nobody can act on the notes anyway.
//   - everything outside /api, /mcp, /s and /f is the app shell, a static asset
//     or the service worker: embedded bytes or a template render, no index.
//     Serving the shell is what lets a browser sitting on a booting instance
//     (including one arriving on a deep link to /folder/note-slug) show the app
//     and retry, instead of a bare JSON error. /s/:token is the one HTML view
//     that reads a note and /f/:token the one that reads a folder listing, which
//     is why both are named here: a listing answered mid-reconcile would be
//     short, and a wrong answer on an anonymous link is worse than a 503.
func indexDerivedPath(path string) bool {
	switch {
	case path == "/api/v1/csrf", strings.HasPrefix(path, "/api/v1/auth/"):
		return false
	case strings.HasPrefix(path, "/api/"),
		strings.HasPrefix(path, "/mcp"),
		strings.HasPrefix(path, sharePathPrefix),
		strings.HasPrefix(path, folderSharePathPrefix):
		return true
	}
	return false
}
