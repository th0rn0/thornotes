package notes

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

// JournalsRootFolderName is the name of the top-level folder that contains all
// journals for a user. Every journal becomes a subfolder of this root, so the
// folder tree never has journals scattered next to ordinary top-level folders.
const JournalsRootFolderName = "Journals"

// CreateJournal creates a new journal record and ensures its on-disk folder
// hierarchy exists at "Journals/{name}".
//
// Journal names must be valid folder names (1–255 chars, no path separators).
func (s *Service) CreateJournal(ctx context.Context, userID int64, userUUID string, name string) (*model.Journal, error) {
	if len(name) == 0 || len(name) > 255 {
		return nil, apperror.BadRequest("journal name must be 1–255 characters")
	}

	root, err := s.ensureFolder(ctx, userID, userUUID, nil, JournalsRootFolderName)
	if err != nil {
		return nil, fmt.Errorf("ensure journals root folder: %w", err)
	}

	rootID := root.ID
	if _, err := s.ensureFolder(ctx, userID, userUUID, &rootID, name); err != nil {
		return nil, fmt.Errorf("ensure journal folder: %w", err)
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
//	Journals/{journalName}/{YYYY}/{MM - Month}/{DD - Weekday}.md
//
// e.g. "Journals/Personal/2025/01 - January/06 - Tuesday.md".
//
// The note is auto-tagged with "journal entry" and the journal name.
// loc determines which calendar day counts as "today"; pass time.UTC if unknown.
func (s *Service) TodayEntry(ctx context.Context, userID int64, userUUID string, journalID int64, loc *time.Location) (*model.Note, error) {
	j, err := s.journals.GetByID(ctx, userID, journalID)
	if err != nil {
		return nil, err
	}

	today := time.Now().In(loc)
	year, monthFolder, dayFile := journalPathSegments(today)

	root, err := s.ensureFolder(ctx, userID, userUUID, nil, JournalsRootFolderName)
	if err != nil {
		return nil, fmt.Errorf("ensure journals root folder: %w", err)
	}

	rootID := root.ID
	journalFolder, err := s.ensureFolder(ctx, userID, userUUID, &rootID, j.Name)
	if err != nil {
		return nil, fmt.Errorf("ensure journal folder: %w", err)
	}

	journalFolderID := journalFolder.ID
	yearFolder, err := s.ensureFolder(ctx, userID, userUUID, &journalFolderID, year)
	if err != nil {
		return nil, fmt.Errorf("ensure journal year folder: %w", err)
	}

	yearID := yearFolder.ID
	monthFolderRow, err := s.ensureFolder(ctx, userID, userUUID, &yearID, monthFolder)
	if err != nil {
		return nil, fmt.Errorf("ensure journal month folder: %w", err)
	}

	monthID := monthFolderRow.ID
	existing, err := s.notes.GetByFolderAndSlug(ctx, userID, &monthID, dayFile)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, apperror.ErrNotFound) {
		return nil, err
	}

	tags := []string{"journal entry", j.Name}
	return s.createNoteWithSlug(ctx, userID, userUUID, &monthID, dayFile, dayFile, tags)
}

// journalPathSegments returns the year folder, month folder, and day file
// names for a given timestamp. The format is:
//
//	year:  "2025"
//	month: "01 - January"
//	day:   "06 - Tuesday"
//
// Splitting this out keeps formatting in one place and makes the format
// trivially testable.
func journalPathSegments(t time.Time) (year, month, day string) {
	year = t.Format("2006")
	month = fmt.Sprintf("%02d - %s", int(t.Month()), t.Month().String())
	day = fmt.Sprintf("%02d - %s", t.Day(), t.Weekday().String())
	return year, month, day
}

// ensureFolder finds or creates a folder. It first looks up the expected disk_path;
// if not found it creates the folder.
func (s *Service) ensureFolder(ctx context.Context, userID int64, userUUID string, parentID *int64, name string) (*model.Folder, error) {
	var parentPath string
	if parentID != nil {
		parent, err := s.folders.GetByID(ctx, userID, *parentID)
		if err != nil {
			return nil, err
		}
		parentPath = parent.DiskPath
	}
	diskPath := folderDiskPath(userUUID, parentPath, name)

	existing, err := s.folders.GetByDiskPath(ctx, diskPath)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, apperror.ErrNotFound) {
		return nil, err
	}

	folder, err := s.CreateFolder(ctx, userID, userUUID, parentID, name)
	if err != nil {
		// A concurrent creator may have inserted the folder between our
		// lookup and create; fall back to the now-existing row.
		if errors.Is(err, apperror.ErrConflict) {
			return s.folders.GetByDiskPath(ctx, diskPath)
		}
		return nil, err
	}
	return folder, nil
}
