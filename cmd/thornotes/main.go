package main

import (
	"context"
	"fmt"
	"html/template"
	iofs "io/fs"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	thornotes "github.com/th0rn0/thornotes"
	"github.com/th0rn0/thornotes/internal/auth"
	"github.com/th0rn0/thornotes/internal/config"
	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/hub"
	"github.com/th0rn0/thornotes/internal/notes"
	"github.com/th0rn0/thornotes/internal/repository"
	mysqlrepo "github.com/th0rn0/thornotes/internal/repository/mysql"
	"github.com/th0rn0/thornotes/internal/repository/sqlite"
	"github.com/th0rn0/thornotes/internal/router"
	"github.com/th0rn0/thornotes/internal/security"
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	cfg, err := config.Parse()
	if err != nil {
		log.Error().Err(err).Msg("parse config")
		os.Exit(1)
	}

	// Open database.
	var pool *db.Pool
	switch strings.ToLower(cfg.DBDriver) {
	case "mysql":
		if cfg.DBUser == "" {
			log.Error().Msg("--db-user / THORNOTES_DB_USER is required when using mysql driver")
			os.Exit(1)
		}
		dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?parseTime=true", cfg.DBUser, cfg.DBPassword, cfg.DBHost, cfg.DBName)
		pool, err = db.OpenMySQL(dsn)
	default:
		pool, err = db.Open(cfg.DBPath)
	}
	if err != nil {
		log.Error().Err(err).Str("driver", cfg.DBDriver).Msg("open database")
		os.Exit(1)
	}
	defer pool.Close()

	// Build repositories — one set per driver.
	var (
		userRepo     repository.UserRepository
		sessionRepo  repository.SessionRepository
		folderRepo   repository.FolderRepository
		noteRepo     repository.NoteRepository
		searchRepo   repository.SearchRepository
		apiTokenRepo repository.APITokenRepository
		journalRepo  repository.JournalRepository
	)

	switch strings.ToLower(cfg.DBDriver) {
	case "mysql":
		userRepo = mysqlrepo.NewUserRepo(pool.WriteDB)
		sessionRepo = mysqlrepo.NewSessionRepo(pool.WriteDB)
		folderRepo = mysqlrepo.NewFolderRepo(pool.WriteDB)
		noteRepo = mysqlrepo.NewNoteRepo(pool.WriteDB)
		searchRepo = mysqlrepo.NewSearchRepo(pool.WriteDB)
		apiTokenRepo = mysqlrepo.NewAPITokenRepo(pool.WriteDB)
		journalRepo = mysqlrepo.NewJournalRepo(pool.WriteDB)
	default:
		userRepo = sqlite.NewUserRepo(pool.WriteDB)
		sessionRepo = sqlite.NewSessionRepo(pool.WriteDB)
		folderRepo = sqlite.NewFolderRepo(pool.ReadDB, pool.WriteDB)
		noteRepo = sqlite.NewNoteRepo(pool.ReadDB, pool.WriteDB)
		searchRepo = sqlite.NewSearchRepo(pool.ReadDB, pool.WriteDB)
		apiTokenRepo = sqlite.NewAPITokenRepo(pool.ReadDB, pool.WriteDB)
		journalRepo = sqlite.NewJournalRepo(pool.ReadDB, pool.WriteDB)
	}

	// Backfill UUIDs for any users created before v1.4.0.0.
	if err := db.EnsureUserUUIDs(context.Background(), pool.WriteDB, cfg.NotesRoot); err != nil {
		log.Error().Err(err).Msg("uuid migration")
		os.Exit(1)
	}

	// Build file store.
	fs, err := notes.NewFileStore(cfg.NotesRoot)
	if err != nil {
		log.Error().Err(err).Msg("init file store")
		os.Exit(1)
	}

	// Optionally enable git history tracking.
	if cfg.EnableGitHistory {
		if err := fs.EnableGitHistory(); err != nil {
			log.Error().Err(err).Msg("init git history")
			os.Exit(1)
		}
		log.Info().Str("notes_root", cfg.NotesRoot).Msg("git history enabled")
	}

	// Build services.
	authSvc := auth.NewService(userRepo, sessionRepo, cfg.AllowRegistration)
	notesSvc := notes.NewService(noteRepo, folderRepo, searchRepo, journalRepo, fs)

	// Build SSE hub.
	notifyHub := hub.New()

	// Parse templates from embedded FS.
	tmpl, err := template.ParseFS(thornotes.TemplatesFS, "web/templates/*.html")
	if err != nil {
		log.Error().Err(err).Msg("parse templates")
		os.Exit(1)
	}

	// Rate limiter.
	rateLimiter := security.NewAuthRateLimiter(cfg.TrustedProxy)

	// Sub the embedded FS to "web/static" so FileServer sees it as the root.
	staticSub, err := iofs.Sub(thornotes.StaticFS, "web/static")
	if err != nil {
		log.Error().Err(err).Msg("sub static fs")
		os.Exit(1)
	}
	h := router.New(authSvc, notesSvc, apiTokenRepo, userRepo, rateLimiter, tmpl, http.FS(staticSub), notifyHub, cfg.SecureCookies, cfg.EnableGitHistory)

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
		log.Info().Str("addr", cfg.Addr).Str("driver", cfg.DBDriver).Msg("thornotes starting")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("server error")
			os.Exit(1)
		}
	}()

	// Cleanup expired sessions periodically.
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := sessionRepo.DeleteExpired(context.Background()); err != nil {
				log.Warn().Err(err).Msg("delete expired sessions")
			}
		}
	}()

	// Startup reconciliation: compare every note's on-disk hash against the DB.
	// Skippable via --skip-reconciliation on trusted restarts with large corpora.
	if cfg.SkipReconciliation {
		log.Info().Msg("startup reconciliation skipped (--skip-reconciliation)")
	} else {
		userIDs, reconcileErr := userRepo.IDs(context.Background())
		if reconcileErr != nil {
			log.Warn().Err(reconcileErr).Msg("startup reconcile: list users")
		} else {
			for _, uid := range userIDs {
				if err := notesSvc.Reconcile(context.Background(), uid); err != nil {
					log.Warn().Err(err).Int64("user_id", uid).Msg("startup reconcile error")
				}
			}
		}
	}

	// Disk watcher: poll for file changes and push SSE notifications.
	if cfg.WatchInterval > 0 {
		log.Info().Dur("interval", cfg.WatchInterval).Msg("disk watcher enabled")
		go notes.Watch(watchCtx, cfg.WatchInterval, notesSvc, userRepo, notifyHub)
	}

	<-stop
	log.Info().Msg("shutting down...")
	watchCancel()
	rateLimiter.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("shutdown error")
	}

	// Wait for in-flight file writes to complete.
	fs.Wait()
	log.Info().Msg("shutdown complete")
}
