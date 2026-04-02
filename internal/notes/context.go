package notes

import (
	"context"
	"fmt"
	"strings"
)

const contextCharLimit = 200_000

// NoteContextResult is the response from NoteContext.
type NoteContextResult struct {
	Context   string `json:"context"`
	NoteCount int    `json:"note_count"`
	Truncated bool   `json:"truncated"`
	CharLimit int    `json:"char_limit"`
}

// NoteContext assembles a concatenated markdown string from the user's notes,
// ready to paste into an LLM prompt.
//
// Notes are ordered newest-first. If the total content exceeds CharLimit,
// the oldest notes are omitted and Truncated is set to true.
//
// Pass nil folderID to include all notes; pass a non-nil folderID to restrict
// to notes within that folder.
func (s *Service) NoteContext(ctx context.Context, userID int64, folderID *int64) (*NoteContextResult, error) {
	allNotes, err := s.notes.ListForContext(ctx, userID, folderID)
	if err != nil {
		return nil, fmt.Errorf("list notes for context: %w", err)
	}

	var sb strings.Builder
	included := 0
	truncated := false

	for _, note := range allNotes {
		// Format each note as: # Title\n\ncontent\n\n---\n\n
		block := fmt.Sprintf("# %s\n\n%s\n\n---\n\n", note.Title, note.Content)
		if sb.Len()+len(block) > contextCharLimit {
			truncated = true
			break
		}
		sb.WriteString(block)
		included++
	}

	return &NoteContextResult{
		Context:   sb.String(),
		NoteCount: included,
		Truncated: truncated,
		CharLimit: contextCharLimit,
	}, nil
}
