package notes

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

// CreateJournal creates a new journal and its root folder on disk.
// Journal names must be valid folder names (1–255 chars, no path separators).
func (s *Service) CreateJournal(ctx context.Context, userID int64, name string) (*model.Journal, error) {
	if len(name) == 0 || len(name) > 255 {
		return nil, apperror.BadRequest("journal name must be 1–255 characters")
	}

	// Create the root folder for this journal (e.g. "1/Personal Journal").
	_, err := s.CreateFolder(ctx, userID, nil, name)
	if err != nil {
		// Conflict on folder means a folder already exists with this name —
		// still try to create the journal record so users can re-attach.
		if !errors.Is(err, apperror.ErrConflict) {
			return nil, err
		}
	}

	j, err := s.journals.Create(ctx, userID, name)
	if err != nil {
		return nil, err
	}
	return j, nil
}

// ListJournals returns all journals for a user.
func (s *Service) ListJournals(ctx context.Context, userID int64) ([]*model.Journal, error) {
	return s.journals.ListByUser(ctx, userID)
}

// DeleteJournal removes the journal record. The underlying folder and notes
// are preserved — the user can still access them through the normal folder tree.
func (s *Service) DeleteJournal(ctx context.Context, userID, journalID int64) error {
	return s.journals.Delete(ctx, userID, journalID)
}

// TodayEntry returns today's journal entry (creating it if it doesn't exist).
//
// The entry is stored at:
//
//	{journalName}/{year}/{month}/YYYY-MM-DD.md
//
// e.g. "Personal Journal/2026/04/2026-04-01.md" for user 1.
//
// The note is auto-tagged with "journal entry" and the journal name.
func (s *Service) TodayEntry(ctx context.Context, userID, journalID int64) (*model.Note, error) {
	j, err := s.journals.GetByID(ctx, userID, journalID)
	if err != nil {
		return nil, err
	}

	today := time.Now().UTC()
	year := today.Format("2006")
	month := today.Format("01")
	dateSlug := today.Format("2006-01-02")

	// Ensure root journal folder exists.
	rootFolder, err := s.ensureFolder(ctx, userID, nil, j.Name)
	if err != nil {
		return nil, fmt.Errorf("ensure journal root folder: %w", err)
	}

	// Ensure year subfolder.
	rootID := rootFolder.ID
	yearFolder, err := s.ensureFolder(ctx, userID, &rootID, year)
	if err != nil {
		return nil, fmt.Errorf("ensure journal year folder: %w", err)
	}

	// Ensure month subfolder.
	yearID := yearFolder.ID
	monthFolder, err := s.ensureFolder(ctx, userID, &yearID, month)
	if err != nil {
		return nil, fmt.Errorf("ensure journal month folder: %w", err)
	}

	// Return existing entry if already created today.
	monthID := monthFolder.ID
	existing, err := s.notes.GetByFolderAndSlug(ctx, userID, &monthID, dateSlug)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, apperror.ErrNotFound) {
		return nil, err
	}

	// Create today's entry.
	tags := []string{"journal entry", j.Name}
	return s.CreateNote(ctx, userID, &monthID, dateSlug, tags)
}

// ensureFolder finds or creates a folder. It first looks up the expected disk_path;
// if not found it creates the folder.
func (s *Service) ensureFolder(ctx context.Context, userID int64, parentID *int64, name string) (*model.Folder, error) {
	var parentPath string
	if parentID != nil {
		parent, err := s.folders.GetByID(ctx, userID, *parentID)
		if err != nil {
			return nil, err
		}
		parentPath = parent.DiskPath
	}
	diskPath := folderDiskPath(userID, parentPath, name)

	existing, err := s.folders.GetByDiskPath(ctx, diskPath)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, apperror.ErrNotFound) {
		return nil, err
	}

	return s.CreateFolder(ctx, userID, parentID, name)
}
