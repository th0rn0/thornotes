package main

import (
	"context"
	"html/template"
	iofs "io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	thornotes "github.com/th0rn0/thornotes"
	"github.com/th0rn0/thornotes/internal/auth"
	"github.com/th0rn0/thornotes/internal/config"
	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/hub"
	"github.com/th0rn0/thornotes/internal/notes"
	"github.com/th0rn0/thornotes/internal/repository/sqlite"
	"github.com/th0rn0/thornotes/internal/router"
	"github.com/th0rn0/thornotes/internal/security"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg, err := config.Parse()
	if err != nil {
		slog.Error("parse config", "err", err)
		os.Exit(1)
	}

	// Open database.
	pool, err := db.Open(cfg.DBPath)
	if err != nil {
		slog.Error("open database", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Build repositories.
	userRepo := sqlite.NewUserRepo(pool.WriteDB)
	sessionRepo := sqlite.NewSessionRepo(pool.WriteDB)
	folderRepo := sqlite.NewFolderRepo(pool.ReadDB, pool.WriteDB)
	noteRepo := sqlite.NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := sqlite.NewSearchRepo(pool.ReadDB, pool.WriteDB)
	apiTokenRepo := sqlite.NewAPITokenRepo(pool.ReadDB, pool.WriteDB)
	journalRepo := sqlite.NewJournalRepo(pool.ReadDB, pool.WriteDB)

	// Build file store.
	fs, err := notes.NewFileStore(cfg.NotesRoot)
	if err != nil {
		slog.Error("init file store", "err", err)
		os.Exit(1)
	}

	// Build services.
	authSvc := auth.NewService(userRepo, sessionRepo, cfg.AllowRegistration)
	notesSvc := notes.NewService(noteRepo, folderRepo, searchRepo, journalRepo, fs)

	// Build SSE hub.
	notifyHub := hub.New()

	// Parse templates from embedded FS.
	tmpl, err := template.ParseFS(thornotes.TemplatesFS, "web/templates/*.html")
	if err != nil {
		slog.Error("parse templates", "err", err)
		os.Exit(1)
	}

	// Rate limiter.
	rateLimiter := security.NewAuthRateLimiter(cfg.TrustedProxy)

	// Sub the embedded FS to "web/static" so FileServer sees it as the root.
	staticSub, err := iofs.Sub(thornotes.StaticFS, "web/static")
	if err != nil {
		slog.Error("sub static fs", "err", err)
		os.Exit(1)
	}
	h := router.New(authSvc, notesSvc, apiTokenRepo, userRepo, rateLimiter, tmpl, http.FS(staticSub), notifyHub, cfg.SecureCookies)

	srv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      h,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()

	go func() {
		slog.Info("thornotes starting", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	// Cleanup expired sessions periodically.
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := sessionRepo.DeleteExpired(context.Background()); err != nil {
				slog.Warn("delete expired sessions", "err", err)
			}
		}
	}()

	// Disk watcher: poll for file changes and push SSE notifications.
	if cfg.WatchInterval > 0 {
		slog.Info("disk watcher enabled", "interval", cfg.WatchInterval)
		go notes.Watch(watchCtx, cfg.WatchInterval, notesSvc, userRepo, notifyHub)
	}

	<-stop
	slog.Info("shutting down...")
	watchCancel()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("shutdown error", "err", err)
	}

	// Wait for in-flight file writes to complete.
	fs.Wait()
	slog.Info("shutdown complete")
}
