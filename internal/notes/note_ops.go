package notes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/rs/zerolog/log"
	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

// CreateNote creates a new note, writes the file, then saves to DB.
func (s *Service) CreateNote(ctx context.Context, userID int64, userUUID string, folderID *int64, title string, tags []string) (*model.Note, error) {
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
	diskPath := notesDiskPath(userUUID, folderPath, slug)

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
			log.Warn().Err(cleanupErr).Str("path", diskPath).Msg("cleanup file after failed db insert")
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

// ListNotes returns note metadata for a given folder (nil = root).
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

// MoveNote moves a note to newFolderID (nil = root). It:
// 1. Moves the file on disk.
// 2. Updates the note's folder_id and disk_path in DB.
func (s *Service) MoveNote(ctx context.Context, userID int64, userUUID string, noteID int64, newFolderID *int64) error {
	note, err := s.notes.GetByID(ctx, userID, noteID)
	if err != nil {
		return err
	}

	// No-op if already in the target folder.
	if ptrEq(note.FolderID, newFolderID) {
		return nil
	}

	var newFolderPath string
	if newFolderID != nil {
		folder, err := s.folders.GetByID(ctx, userID, *newFolderID)
		if err != nil {
			return err
		}
		newFolderPath = folder.DiskPath
	}

	oldDiskPath := note.DiskPath
	newDiskPath := notesDiskPath(userUUID, newFolderPath, note.Slug)

	if err := s.fs.RenameFile(oldDiskPath, newDiskPath); err != nil {
		return apperror.Internal("move note file", err)
	}

	if err := s.notes.Move(ctx, userID, noteID, newFolderID, newDiskPath); err != nil {
		_ = s.fs.RenameFile(newDiskPath, oldDiskPath)
		return err
	}

	return nil
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

// FindNotesByTag returns all notes that have every tag in the given list (AND semantics).
// An empty tags slice returns all notes.
func (s *Service) FindNotesByTag(ctx context.Context, userID int64, tags []string) ([]*model.NoteListItem, error) {
	all, err := s.notes.ListAll(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(tags) == 0 {
		return all, nil
	}
	var out []*model.NoteListItem
	for _, note := range all {
		if hasAllTags(note.Tags, tags) {
			out = append(out, note)
		}
	}
	return out, nil
}

// ListAllTags returns the sorted unique set of tags in use across all notes.
func (s *Service) ListAllTags(ctx context.Context, userID int64) ([]string, error) {
	all, err := s.notes.ListAll(ctx, userID)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, note := range all {
		for _, t := range note.Tags {
			seen[t] = struct{}{}
		}
	}
	tags := make([]string, 0, len(seen))
	for t := range seen {
		tags = append(tags, t)
	}
	sortStrings(tags)
	return tags, nil
}

// hasAllTags reports whether item contains every tag in want (case-sensitive).
func hasAllTags(have, want []string) bool {
	m := make(map[string]struct{}, len(have))
	for _, t := range have {
		m[t] = struct{}{}
	}
	for _, t := range want {
		if _, ok := m[t]; !ok {
			return false
		}
	}
	return true
}

// sortStrings sorts a string slice in place.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
