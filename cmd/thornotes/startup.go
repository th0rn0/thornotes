package main

import (
	"context"

	"github.com/rs/zerolog/log"

	"github.com/th0rn0/thornotes/internal/notes"
	"github.com/th0rn0/thornotes/internal/repository"
)

// startupReconcile runs the startup pass over every user and closes the startup
// readiness window on every exit path out of it.
//
// The window is opened by the caller (main opens it before the listener comes
// up), so by the time this runs the instance is already answering 503 for
// anything that reads the note index. Closing it is the half that must not be
// forgotten: the skip flag, a failure to list users, and a user whose pass
// returns an error all leave here through the same defer, so none of them can
// leave a live process permanently unready. That is the failure mode worth
// naming, because it is silent — the process is up, the port is open, the logs
// say "reconcile error", and every request answers 503 until someone restarts
// it. (Same shape as the 467 trap: whoever writes a fact must write it on every
// path out.)
//
// A failed pass does make the instance ready, and that is intended: readiness
// here means "the startup repair is over, stop refusing requests", not "the
// index is perfect". An aborted pass is already reported on its own terms — the
// notes-root check in /healthz answers 503 with the refusal reason — and an
// instance serving a stale but intact index is exactly what the reconciler's
// fail-closed design is for.
func startupReconcile(ctx context.Context, svc *notes.Service, users repository.UserRepository, skip bool) {
	defer svc.EndStartup()

	if skip {
		log.Info().Msg("startup reconciliation skipped (--skip-reconciliation)")
		return
	}

	userIDs, err := users.IDs(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("startup reconcile: list users")
		return
	}
	for _, uid := range userIDs {
		user, err := users.GetByID(ctx, uid)
		if err != nil {
			log.Warn().Err(err).Int64("user_id", uid).Msg("startup reconcile: get user")
			continue
		}
		if _, err := svc.Reconcile(ctx, user); err != nil {
			log.Warn().Err(err).Int64("user_id", uid).Msg("startup reconcile error")
		}
	}
}
