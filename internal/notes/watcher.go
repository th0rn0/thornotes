package notes

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/th0rn0/thornotes/internal/hub"
	"github.com/th0rn0/thornotes/internal/repository"
)

// Watch polls the filesystem every interval and reconciles all notes for all users.
// When a user's notes have changed on disk, the hub is notified so connected SSE
// clients can refresh. Exits when ctx is cancelled.
//
// This is the runtime counterpart to the startup Reconcile call: Reconcile runs
// once at boot, Watch runs continuously while the server is up.
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
		changed, err := reconcileUserForWatch(ctx, notesSvc, userID)
		if err != nil {
			log.Warn().Err(err).Int64("user_id", userID).Msg("watcher: reconcile user")
			continue
		}
		if changed > 0 {
			log.Info().Int64("user_id", userID).Int("notes_updated", changed).Msg("watcher: disk changes detected")
			h.Notify(userID, "notes_changed")
		}
	}
}

// reconcileUserForWatch reads all of a user's notes from disk, compares content
// hashes, and updates the DB for any that have changed. Returns the count updated.
func reconcileUserForWatch(ctx context.Context, svc *Service, userID int64) (int, error) {
	records, err := svc.notes.ListAllForWatch(ctx, userID)
	if err != nil {
		return 0, err
	}

	updated := 0
	for _, rec := range records {
		fileContent, err := svc.fs.Read(rec.DiskPath)
		if err != nil {
			// File missing from disk is not an error we want to surface — note may
			// have been deleted via the API between list and read.
			continue
		}

		fileHash := HashContent(fileContent)
		if fileHash == rec.ContentHash {
			continue
		}

		log.Info().Int64("id", rec.ID).Str("disk_path", rec.DiskPath).Msg("watcher: updating changed note")
		if err := svc.notes.UpdateContent(ctx, userID, rec.ID, fileContent, fileHash, rec.ContentHash); err != nil {
			log.Warn().Err(err).Int64("id", rec.ID).Msg("watcher: update content")
			continue
		}
		updated++
	}
	return updated, nil
}
