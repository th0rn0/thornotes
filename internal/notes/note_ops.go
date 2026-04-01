package notes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

// CreateNote creates a new note, writes the file, then saves to DB.
func (s *Service) CreateNote(ctx context.Context, userID int64, folderID *int64, title string, tags []string) (*model.Note, error) {
	if len(title) == 0 || len(title) > 500 {
		return nil, apperror.BadRequest("title must be 1–500 characters")
	}
	if tags == nil {
		tags = []string{}
	}

	slug := slugify(title)

	// Determine disk path.
	var folderPath string
	if folderID != nil {
		folder, err := s.folders.GetByID(ctx, userID, *folderID)
		if err != nil {
			return nil, err
		}
		folderPath = folder.DiskPath
	}
	diskPath := notesDiskPath(userID, folderPath, slug)

	content := ""
	hash := HashContent(content)

	// Write file first (file-as-canonical protocol).
	if err := s.fs.Write(diskPath, content); err != nil {
		return nil, apperror.Internal("write note file", err)
	}

	n := &model.Note{
		UserID:      userID,
		FolderID:    folderID,
		Title:       title,
		Slug:        slug,
		DiskPath:    diskPath,
		Content:     content,
		ContentHash: hash,
		Tags:        tags,
	}

	created, err := s.notes.Create(ctx, n)
	if err != nil {
		// Best-effort cleanup of the file if DB insert fails.
		if cleanupErr := s.fs.Delete(diskPath); cleanupErr != nil {
			slog.Warn("cleanup file after failed db insert", "path", diskPath, "err", cleanupErr)
		}
		return nil, err
	}

	return created, nil
}

// GetNote returns a note by ID, verifying it belongs to userID.
func (s *Service) GetNote(ctx context.Context, userID, noteID int64) (*model.Note, error) {
	return s.notes.GetByID(ctx, userID, noteID)
}

// GetNoteByShareToken returns a note by its public share token.
func (s *Service) GetNoteByShareToken(ctx context.Context, token string) (*model.Note, error) {
	return s.notes.GetByShareToken(ctx, token)
}

// ListNotes returns note metadata for a given folder (nil = root/unsorted).
func (s *Service) ListNotes(ctx context.Context, userID int64, folderID *int64) ([]*model.NoteListItem, error) {
	return s.notes.ListByFolder(ctx, userID, folderID)
}

// ListAllNotes returns note metadata for all notes owned by userID, across all folders.
func (s *Service) ListAllNotes(ctx context.Context, userID int64) ([]*model.NoteListItem, error) {
	return s.notes.ListAll(ctx, userID)
}

// UpdateNoteContent saves new content using optimistic concurrency.
// expectedHash must match the current content_hash in the DB.
func (s *Service) UpdateNoteContent(ctx context.Context, userID, noteID int64, content, expectedHash string) (string, error) {
	if int64(len(content)) > 1<<20 {
		return "", &apperror.AppError{Code: 413, Message: "content exceeds 1 MB limit"}
	}

	newHash := HashContent(content)

	// Write file first.
	note, err := s.notes.GetByID(ctx, userID, noteID)
	if err != nil {
		return "", err
	}

	if err := s.fs.Write(note.DiskPath, content); err != nil {
		return "", apperror.Internal("write note file", err)
	}

	// Optimistic concurrency update.
	if err := s.notes.UpdateContent(ctx, userID, noteID, content, newHash, expectedHash); err != nil {
		return "", err
	}

	return newHash, nil
}

// UpdateNoteMetadata updates the title and tags of a note.
func (s *Service) UpdateNoteMetadata(ctx context.Context, userID, noteID int64, title string, tags []string) error {
	note, err := s.notes.GetByID(ctx, userID, noteID)
	if err != nil {
		return err
	}
	if title != "" {
		note.Title = title
	}
	if tags != nil {
		note.Tags = tags
	}
	return s.notes.Update(ctx, note)
}

// DeleteNote removes a note from DB and disk.
func (s *Service) DeleteNote(ctx context.Context, userID, noteID int64) error {
	note, err := s.notes.GetByID(ctx, userID, noteID)
	if err != nil {
		return err
	}
	diskPath := note.DiskPath

	if err := s.notes.Delete(ctx, userID, noteID); err != nil {
		return err
	}

	// Delete file after DB row is gone.
	_ = s.fs.Delete(diskPath)
	return nil
}

// SetShareToken generates a new share token or clears it (pass clear=true).
func (s *Service) SetShareToken(ctx context.Context, userID, noteID int64, clear bool) (*string, error) {
	if clear {
		return nil, s.notes.SetShareToken(ctx, userID, noteID, nil)
	}

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, apperror.Internal("generate share token", err)
	}
	token := hex.EncodeToString(b)

	if err := s.notes.SetShareToken(ctx, userID, noteID, &token); err != nil {
		return nil, fmt.Errorf("set share token: %w", err)
	}
	return &token, nil
}
