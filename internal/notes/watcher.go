package notes

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/th0rn0/thornotes/internal/hub"
	"github.com/th0rn0/thornotes/internal/repository"
)

// Watch polls the filesystem every interval and reconciles all notes for all
// users. When a user's on-disk tree has drifted from the DB (content changed,
// files added, files removed, folders renamed), the hub is notified so
// connected SSE clients refresh.
//
// This is the runtime counterpart to the startup Reconcile call: Reconcile
// runs once at boot, Watch runs continuously while the server is up. Both
// share the same disk-walk + diff implementation in reconcile.go.
func Watch(ctx context.Context, interval time.Duration, notesSvc *Service, userRepo repository.UserRepository, h *hub.Hub) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcileAllUsers(ctx, notesSvc, userRepo, h)
		}
	}
}

func reconcileAllUsers(ctx context.Context, notesSvc *Service, userRepo repository.UserRepository, h *hub.Hub) {
	ids, err := userRepo.IDs(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("watcher: list user ids")
		return
	}
	for _, userID := range ids {
		user, err := userRepo.GetByID(ctx, userID)
		if err != nil {
			log.Warn().Err(err).Int64("user_id", userID).Msg("watcher: get user")
			continue
		}
		changed, err := notesSvc.Reconcile(ctx, user)
		if err != nil {
			log.Warn().Err(err).Int64("user_id", userID).Msg("watcher: reconcile user")
			continue
		}
		if changed > 0 {
			log.Info().Int64("user_id", userID).Int("changes", changed).Msg("watcher: disk changes detected")
			h.Notify(userID, "notes_changed")
		}
	}
}
