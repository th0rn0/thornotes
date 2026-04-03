package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// EnsureUserUUIDs backfills UUIDs for any users that don't have one yet,
// renames their notes directory from the old int-based path to the UUID path,
// and updates all disk_path values in the folders and notes tables.
//
// This runs at startup after DB migrations, before the HTTP server starts.
// It is idempotent — users that already have a UUID are skipped.
func EnsureUserUUIDs(ctx context.Context, db *sql.DB, notesRoot string) error {
	rows, err := db.QueryContext(ctx, `SELECT id FROM users WHERE uuid IS NULL OR uuid = ''`)
	if err != nil {
		return fmt.Errorf("list users without uuid: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if len(ids) == 0 {
		return nil
	}

	log.Info().Int("count", len(ids)).Msg("uuid migration: backfilling user UUIDs")

	for _, userID := range ids {
		newUUID := uuid.New().String()
		if err := migrateUserToUUID(ctx, db, notesRoot, userID, newUUID); err != nil {
			log.Warn().Err(err).Int64("user_id", userID).Msg("uuid migration: failed for user")
			// Continue — don't block startup for a single user failure.
		}
	}

	return nil
}

func migrateUserToUUID(ctx context.Context, db *sql.DB, notesRoot string, userID int64, newUUID string) error {
	oldPrefix := fmt.Sprintf("%d", userID)
	newPrefix := newUUID

	oldDir := filepath.Join(notesRoot, oldPrefix)
	newDir := filepath.Join(notesRoot, newPrefix)

	// Rename the on-disk directory if it exists.
	if _, err := os.Stat(oldDir); err == nil {
		if err := os.Rename(oldDir, newDir); err != nil {
			return fmt.Errorf("rename notes dir %s → %s: %w", oldDir, newDir, err)
		}
		log.Info().Int64("user_id", userID).Str("uuid", newUUID).Msg("uuid migration: renamed notes directory")
	}

	// Update disk_path for all folders owned by this user.
	folderRows, err := db.QueryContext(ctx, `SELECT id, disk_path FROM folders WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("list folders: %w", err)
	}
	type pathUpdate struct {
		id      int64
		newPath string
	}
	var folderUpdates []pathUpdate
	for folderRows.Next() {
		var id int64
		var diskPath string
		if err := folderRows.Scan(&id, &diskPath); err != nil {
			folderRows.Close()
			return err
		}
		newPath := rebasePath(diskPath, oldPrefix, newPrefix)
		if newPath != diskPath {
			folderUpdates = append(folderUpdates, pathUpdate{id, newPath})
		}
	}
	folderRows.Close()
	if err := folderRows.Err(); err != nil {
		return err
	}

	for _, u := range folderUpdates {
		if _, err := db.ExecContext(ctx, `UPDATE folders SET disk_path = ? WHERE id = ?`, u.newPath, u.id); err != nil {
			return fmt.Errorf("update folder %d disk_path: %w", u.id, err)
		}
	}

	// Update disk_path for all notes owned by this user.
	noteRows, err := db.QueryContext(ctx, `SELECT id, disk_path FROM notes WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("list notes: %w", err)
	}
	var noteUpdates []pathUpdate
	for noteRows.Next() {
		var id int64
		var diskPath string
		if err := noteRows.Scan(&id, &diskPath); err != nil {
			noteRows.Close()
			return err
		}
		newPath := rebasePath(diskPath, oldPrefix, newPrefix)
		if newPath != diskPath {
			noteUpdates = append(noteUpdates, pathUpdate{id, newPath})
		}
	}
	noteRows.Close()
	if err := noteRows.Err(); err != nil {
		return err
	}

	for _, u := range noteUpdates {
		if _, err := db.ExecContext(ctx, `UPDATE notes SET disk_path = ? WHERE id = ?`, u.newPath, u.id); err != nil {
			return fmt.Errorf("update note %d disk_path: %w", u.id, err)
		}
	}

	// Persist the UUID on the user record.
	if _, err := db.ExecContext(ctx, `UPDATE users SET uuid = ? WHERE id = ?`, newUUID, userID); err != nil {
		return fmt.Errorf("set user uuid: %w", err)
	}

	log.Info().Int64("user_id", userID).Str("uuid", newUUID).
		Int("folders", len(folderUpdates)).Int("notes", len(noteUpdates)).
		Msg("uuid migration: complete")

	return nil
}

// rebasePath replaces the leading oldPrefix segment with newPrefix.
// e.g. rebasePath("42/Work/todo.md", "42", "abc-uuid") → "abc-uuid/Work/todo.md"
func rebasePath(path, oldPrefix, newPrefix string) string {
	// Normalise separators.
	clean := filepath.ToSlash(path)
	oldSlash := oldPrefix + "/"
	if strings.HasPrefix(clean, oldSlash) {
		return newPrefix + "/" + clean[len(oldSlash):]
	}
	if clean == oldPrefix {
		return newPrefix
	}
	return path
}
