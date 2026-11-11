package notes

import (
	"context"
	"fmt"
	"strings"

	"github.com/th0rn0/thornotes/internal/model"
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
//
// The read stops at the cap. Notes are streamed from the repository rather than
// listed first, and each body arrives clipped to contextCharLimit+1 characters,
// so peak memory is bounded by the cap instead of by the size of the corpus
// (ticket N9). A corpus that fits under the cap still produces exactly the
// response it produced before: same order, same blocks, same counts.
func (s *Service) NoteContext(ctx context.Context, userID int64, folderID *int64) (*NoteContextResult, error) {
	var sb strings.Builder
	included := 0
	truncated := false

	// One character past the cap is all the reader needs: a note whose body is
	// longer than the cap can never fit, so a clipped body settles the fit
	// question without loading the rest of it.
	clamp := contextCharLimit + 1

	err := s.notes.IterateForContext(ctx, userID, folderID, clamp, func(note *model.Note) (bool, error) {
		// Format each note as: # Title\n\ncontent\n\n---\n\n
		blockLen := len("# ") + len(note.Title) + len("\n\n") +
			len(note.Content) + len("\n\n---\n\n")
		if sb.Len()+blockLen > contextCharLimit {
			truncated = true
			return false, nil
		}
		sb.WriteString("# ")
		sb.WriteString(note.Title)
		sb.WriteString("\n\n")
		sb.WriteString(note.Content)
		sb.WriteString("\n\n---\n\n")
		included++
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list notes for context: %w", err)
	}

	return &NoteContextResult{
		Context:   sb.String(),
		NoteCount: included,
		Truncated: truncated,
		CharLimit: contextCharLimit,
	}, nil
}
