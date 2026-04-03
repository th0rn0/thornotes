package notes

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"

	"github.com/rs/zerolog/log"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/repository"
)

// Service coordinates note and folder operations across the FileStore and repositories.
type Service struct {
	notes    repository.NoteRepository
	folders  repository.FolderRepository
	search   repository.SearchRepository
	journals repository.JournalRepository
	fs       *FileStore
}

func NewService(
	notes repository.NoteRepository,
	folders repository.FolderRepository,
	search repository.SearchRepository,
	journals repository.JournalRepository,
	fs *FileStore,
) *Service {
	return &Service{
		notes:    notes,
		folders:  folders,
		search:   search,
		journals: journals,
		fs:       fs,
	}
}

// HashContent returns the SHA-256 hex digest of content.
func HashContent(content string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
}

// Reconcile compares every note's content_hash against the file on disk.
// If they differ, the file is authoritative and the DB is updated.
// This runs at startup to recover from any partial DB writes.
func (s *Service) Reconcile(ctx context.Context, userID int64) error {
	// Use ListAllForWatch to cover all notes, not just root-level ones.
	records, err := s.notes.ListAllForWatch(ctx, userID)
	if err != nil {
		return err
	}

	total := len(records)
	if total == 0 {
		return nil
	}

	log.Info().Int64("user_id", userID).Int("total", total).Msg("reconcile: starting")

	reconciled := 0
	for i, rec := range records {
		if i > 0 && i%100 == 0 {
			log.Info().Int64("user_id", userID).Int("progress", i).Int("total", total).Msg("reconcile: progress")
		}

		fileContent, err := s.fs.Read(rec.DiskPath)
		if err != nil {
			log.Warn().Str("disk_path", rec.DiskPath).Int64("id", rec.ID).Msg("reconcile: note file missing")
			continue
		}

		fileHash := HashContent(fileContent)
		if fileHash != rec.ContentHash {
			log.Info().Int64("id", rec.ID).Str("disk_path", rec.DiskPath).Msg("reconcile: updating stale note")
			if err := s.notes.UpdateContent(ctx, userID, rec.ID, fileContent, fileHash, rec.ContentHash); err != nil {
				log.Warn().Err(err).Int64("id", rec.ID).Msg("reconcile: update content")
			} else {
				reconciled++
			}
		}
	}

	log.Info().Int64("user_id", userID).Int("total", total).Int("updated", reconciled).Msg("reconcile: complete")
	return nil
}

// notesDiskPath returns the relative disk path for a note.
// e.g. "{userUUID}/Work/my-note.md"
func notesDiskPath(userUUID string, folderDiskPath, slug string) string {
	if folderDiskPath == "" {
		return filepath.Join(userUUID, slug+".md")
	}
	return filepath.Join(folderDiskPath, slug+".md")
}

// folderDiskPath returns the relative disk path for a folder.
func folderDiskPath(userUUID string, parentDiskPath, name string) string {
	if parentDiskPath == "" {
		return filepath.Join(userUUID, name)
	}
	return filepath.Join(parentDiskPath, name)
}

// slugify converts a title to a safe filename slug.
func slugify(title string) string {
	slug := ""
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z':
			slug += string(r)
		case r >= 'A' && r <= 'Z':
			slug += string(r + 32) // toLower
		case r >= '0' && r <= '9':
			slug += string(r)
		case r == ' ' || r == '-' || r == '_':
			if len(slug) > 0 && slug[len(slug)-1] != '-' {
				slug += "-"
			}
		}
	}
	// Trim trailing dash.
	for len(slug) > 0 && slug[len(slug)-1] == '-' {
		slug = slug[:len(slug)-1]
	}
	if slug == "" {
		slug = "untitled"
	}
	if len(slug) > 100 {
		slug = slug[:100]
	}
	return slug
}

// FileStore returns the underlying FileStore. Used by tests and startup code
// to enable optional features like git history.
func (s *Service) FileStore() *FileStore {
	return s.fs
}

// ptrEq reports whether two *int64 pointers point to equal values (or are both nil).
func ptrEq(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// FolderTree returns the folder tree for a user.
func (s *Service) FolderTree(ctx context.Context, userID int64) ([]*model.FolderTreeItem, error) {
	return s.folders.Tree(ctx, userID)
}
