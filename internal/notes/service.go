package notes

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"path/filepath"

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

	reconciled := 0
	for _, rec := range records {
		fileContent, err := s.fs.Read(rec.DiskPath)
		if err != nil {
			slog.Warn("reconcile: note file missing", "disk_path", rec.DiskPath, "id", rec.ID)
			continue
		}

		fileHash := HashContent(fileContent)
		if fileHash != rec.ContentHash {
			slog.Info("reconcile: updating stale note", "id", rec.ID, "disk_path", rec.DiskPath)
			if err := s.notes.UpdateContent(ctx, userID, rec.ID, fileContent, fileHash, rec.ContentHash); err != nil {
				slog.Warn("reconcile: update content", "id", rec.ID, "err", err)
			} else {
				reconciled++
			}
		}
	}

	if reconciled > 0 {
		slog.Info("reconcile: updated notes from disk", "count", reconciled)
	}
	return nil
}

// notesDiskPath returns the relative disk path for a note.
// e.g. "{userID}/Work/my-note.md"
func notesDiskPath(userID int64, folderDiskPath, slug string) string {
	if folderDiskPath == "" {
		return filepath.Join(fmt.Sprintf("%d", userID), slug+".md")
	}
	return filepath.Join(folderDiskPath, slug+".md")
}

// folderDiskPath returns the relative disk path for a folder.
func folderDiskPath(userID int64, parentDiskPath, name string) string {
	if parentDiskPath == "" {
		return filepath.Join(fmt.Sprintf("%d", userID), name)
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

// FolderTree returns the folder tree for a user.
func (s *Service) FolderTree(ctx context.Context, userID int64) ([]*model.FolderTreeItem, error) {
	return s.folders.Tree(ctx, userID)
}
