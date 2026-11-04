package main

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	iofs "io/fs"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	thornotes "github.com/th0rn0/thornotes"
	"github.com/th0rn0/thornotes/internal/auth"
	"github.com/th0rn0/thornotes/internal/config"
	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/devseed"
	"github.com/th0rn0/thornotes/internal/hub"
	"github.com/th0rn0/thornotes/internal/notes"
	"github.com/th0rn0/thornotes/internal/repository"
	mysqlrepo "github.com/th0rn0/thornotes/internal/repository/mysql"
	"github.com/th0rn0/thornotes/internal/repository/sqlite"
	"github.com/th0rn0/thornotes/internal/router"
	"github.com/th0rn0/thornotes/internal/security"
)

// Version is the build version. It is injected at build time with
// -ldflags "-X main.Version=..." (see the Makefile and the Dockerfile) and
// reports "dev" for an unversioned local build.
var Version = "dev"

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
		logDatabaseOpenError(err, cfg.DBDriver)
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
		linkRepo     repository.LinkRepository
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
		linkRepo = mysqlrepo.NewLinkRepo(pool.WriteDB)
	default:
		userRepo = sqlite.NewUserRepo(pool.WriteDB)
		sessionRepo = sqlite.NewSessionRepo(pool.WriteDB)
		folderRepo = sqlite.NewFolderRepo(pool.ReadDB, pool.WriteDB)
		noteRepo = sqlite.NewNoteRepo(pool.ReadDB, pool.WriteDB)
		searchRepo = sqlite.NewSearchRepo(pool.ReadDB, pool.WriteDB)
		apiTokenRepo = sqlite.NewAPITokenRepo(pool.ReadDB, pool.WriteDB)
		journalRepo = sqlite.NewJournalRepo(pool.ReadDB, pool.WriteDB)
		linkRepo = sqlite.NewLinkRepo(pool.ReadDB, pool.WriteDB)
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

	// One process owns a notes root. Two reconcilers writing the same tree
	// corrupt it, so a second process fails here instead of racing silently.
	if err := fs.AcquireOwnerLock(); err != nil {
		log.Error().Err(err).Str("notes_root", cfg.NotesRoot).Msg("notes root ownership")
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
	notesSvc := notes.NewService(noteRepo, folderRepo, searchRepo, journalRepo, linkRepo, fs)
	applyReconcileGuardFromEnv(notesSvc)

	// Development-only: seed a dev user + folder tree + ~100 notes.
	// Idempotent — skipped entirely if the dev user already exists.
	if cfg.SeedDev {
		stats, err := devseed.Seed(context.Background(), authSvc, notesSvc, userRepo)
		if err != nil {
			log.Error().Err(err).Msg("seed: dev seeding failed")
			os.Exit(1)
		}
		if stats.Skipped {
			log.Info().Msg("seed: dev user already exists, skipping")
		} else {
			log.Info().Int("folders", stats.Folders).Int("notes", stats.Notes).Msgf("seed: created dev user + %d folders + %d notes", stats.Folders, stats.Notes)
		}
	}

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
	h := router.New(authSvc, notesSvc, apiTokenRepo, userRepo, rateLimiter, tmpl, http.FS(staticSub), notifyHub, cfg.SecureCookies, cfg.EnableGitHistory, pool)

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
		log.Info().Str("addr", cfg.Addr).Str("driver", cfg.DBDriver).Str("version", Version).Msg("thornotes starting")
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
			security.SweepExpiredCSRFTokens()
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
				user, err := userRepo.GetByID(context.Background(), uid)
				if err != nil {
					log.Warn().Err(err).Int64("user_id", uid).Msg("startup reconcile: get user")
					continue
				}
				if _, err := notesSvc.Reconcile(context.Background(), user); err != nil {
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
	fs.ReleaseOwnerLock()
	log.Info().Msg("shutdown complete")
}

// logDatabaseOpenError reports a failure to open the database.
//
// A database stamped with a newer migration version than this build carries, or
// one left dirty by an interrupted migration, is a one-line diagnosis, not a
// stack trace: those are the two states that would otherwise crash-loop a
// container on restart with a message that never says what is actually wrong.
// Everything else keeps the generic wrapped message.
func logDatabaseOpenError(err error, driver string) {
	var tooNew *db.ErrSchemaTooNew
	var dirty *db.ErrSchemaDirty
	switch {
	case errors.As(err, &tooNew):
		log.Error().
			Str("driver", driver).
			Int("db_schema_version", tooNew.DBVersion).
			Int("max_supported", tooNew.MaxSupported).
			Msg(tooNew.Error())
	case errors.As(err, &dirty):
		log.Error().
			Str("driver", driver).
			Int("dirty_version", dirty.Version).
			Msg(dirty.Error())
	default:
		log.Error().Err(err).Str("driver", driver).Msg("open database")
	}
}

// applyReconcileGuardFromEnv configures the reconcile deletion circuit breaker.
//
// The defaults (refuse a pass that would delete more than 25% of a user's notes,
// once at least 3 are at stake) are the safe ones: they turn a partial data-loss
// event into a refusal instead of a silent index wipe. An operator who genuinely
// intends a large deletion raises the ratio rather than the code being relaxed.
func applyReconcileGuardFromEnv(svc *notes.Service) {
	ratio := notes.DefaultReconcileMaxDeleteRatio
	minDeletes := notes.DefaultReconcileMinDeleteTrips

	if v := os.Getenv("THORNOTES_RECONCILE_MAX_DELETE_RATIO"); v != "" {
		parsed, err := strconv.ParseFloat(v, 64)
		if err != nil || parsed < 0 || parsed > 1 {
			log.Error().Str("value", v).
				Msg("THORNOTES_RECONCILE_MAX_DELETE_RATIO must be a number between 0 and 1")
			os.Exit(1)
		}
		ratio = parsed
	}
	if v := os.Getenv("THORNOTES_RECONCILE_MIN_DELETES"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 0 {
			log.Error().Str("value", v).Msg("THORNOTES_RECONCILE_MIN_DELETES must be a non-negative integer")
			os.Exit(1)
		}
		minDeletes = parsed
	}

	svc.SetReconcileDeleteGuard(ratio, minDeletes)
	log.Info().
		Float64("max_delete_ratio", ratio).
		Int("min_deletes_to_trip", minDeletes).
		Msg("reconcile: deletion circuit breaker configured")
}
