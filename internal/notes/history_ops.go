package notes

import (
	"context"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

// GitHistoryEnabled reports whether the underlying file store has git history active.
func (s *Service) GitHistoryEnabled() bool {
	return s.fs.GitHistoryEnabled()
}

// NoteHistory returns the git commit log for a note, newest first.
// limit ≤ 0 returns all entries (up to go-git's internal maximum).
// Returns apperror.NotImplemented when git history is not enabled.
func (s *Service) NoteHistory(ctx context.Context, userID, noteID int64, limit int) ([]model.HistoryEntry, error) {
	if !s.fs.GitHistoryEnabled() {
		return nil, apperror.NotImplemented("git history is not enabled")
	}

	note, err := s.notes.GetByID(ctx, userID, noteID)
	if err != nil {
		return nil, err
	}

	entries, err := s.fs.GitLogFile(note.DiskPath, limit)
	if err != nil {
		return nil, apperror.Internal("git log", err)
	}
	if entries == nil {
		entries = []model.HistoryEntry{}
	}
	return entries, nil
}

// NoteContentAt returns the content of a note as it was at the given git commit SHA.
// Returns apperror.NotImplemented when git history is not enabled.
func (s *Service) NoteContentAt(ctx context.Context, userID, noteID int64, sha string) (*model.HistoryEntryContent, error) {
	if !s.fs.GitHistoryEnabled() {
		return nil, apperror.NotImplemented("git history is not enabled")
	}

	note, err := s.notes.GetByID(ctx, userID, noteID)
	if err != nil {
		return nil, err
	}

	content, ts, err := s.fs.GitFileAt(sha, note.DiskPath)
	if err != nil {
		return nil, apperror.NotFound("note not found at this commit")
	}

	return &model.HistoryEntryContent{
		SHA:       sha,
		Content:   content,
		Timestamp: ts,
	}, nil
}

// NoteRestoreAt restores a note's content to what it was at the given commit SHA.
// It performs a normal save so the restoration itself is committed as a new entry.
// Returns apperror.NotImplemented when git history is not enabled.
func (s *Service) NoteRestoreAt(ctx context.Context, userID, noteID int64, sha, currentHash string) (string, error) {
	if !s.fs.GitHistoryEnabled() {
		return "", apperror.NotImplemented("git history is not enabled")
	}

	note, err := s.notes.GetByID(ctx, userID, noteID)
	if err != nil {
		return "", err
	}

	content, _, err := s.fs.GitFileAt(sha, note.DiskPath)
	if err != nil {
		return "", apperror.NotFound("note not found at this commit")
	}

	newHash, err := s.UpdateNoteContent(ctx, userID, noteID, content, currentHash)
	if err != nil {
		return "", err
	}
	return newHash, nil
}

