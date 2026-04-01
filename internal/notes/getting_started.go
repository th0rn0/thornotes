package notes

import (
	"context"
	"errors"

	"github.com/rs/zerolog/log"
	"github.com/th0rn0/thornotes/internal/apperror"
)

// gettingStartedContent is the default welcome note created for new users.
// Update this when new markdown-file features are added.
const gettingStartedContent = `# Welcome to thornotes

thornotes is a self-hosted markdown note-taking app. Every note is a real ` + "`" + `.md` + "`" + ` file on disk — your notes are always yours.

## Writing notes

Notes are written in **Markdown**. Use the editor toolbar or type Markdown directly.

- *italic* and **bold** text
- [Links](https://example.com)
- > Blockquotes
- Numbered and bulleted lists
- ` + "---" + ` horizontal rules

## Syntax highlighting

Wrap code in fenced code blocks and specify the language for colour highlighting:

` + "```go" + `
func hello() string {
    return "world"
}
` + "```" + `

Supported: ` + "`go`" + `, ` + "`yaml`" + `, ` + "`json`" + `, ` + "`python`" + `, ` + "`javascript`" + `, ` + "`bash`" + `, and [180+ more](https://highlightjs.org/).

## Organising notes

Use **folders** to organise your notes into a tree. Create a folder with the **+ Folder** button in the sidebar.

Notes without a folder appear in **Unsorted** at the bottom of the sidebar.

## Tags

Add comma-separated tags to any note using the tags field in the editor. Tags are searchable.

## Search

Type in the search bar to full-text search across all your notes. Filter by tag using the search results.

## Daily journal

Use the **Journal** section in the sidebar to keep a daily journal. Create a journal with **+ Journal** — each entry is automatically named with today's date (` + "`YYYY-MM-DD`" + `) and filed under ` + "`{journal name}/{year}/{month}/`" + `.

You can have multiple named journals (e.g. "Personal", "Work").

## Sharing notes

Click the **Share** button in the editor to generate a public read-only link for any note. Share links work without an account.

## Live sync

Edit ` + "`.md`" + ` files directly in any external editor (vim, VS Code, etc.) — thornotes detects changes on disk and updates open browser tabs automatically.

## Dark mode

Toggle dark mode with the **☾** button in the top bar.

## MCP integration

Connect AI assistants (Claude Desktop, Cursor) to your notes via the **Account** modal. Create an API token and paste the connection snippet into your assistant's MCP config.

Available tools: ` + "`list_notes`" + `, ` + "`get_note`" + `, ` + "`search_notes`" + `, ` + "`create_note`" + `, ` + "`update_note`" + `, ` + "`list_folders`" + `.
`

// CreateGettingStartedNote creates the welcome note for a new user.
// It is idempotent — if the note already exists the call is a no-op.
func (s *Service) CreateGettingStartedNote(ctx context.Context, userID int64) {
	_, err := s.CreateNote(ctx, userID, nil, "Getting Started", nil)
	if err != nil {
		if errors.Is(err, apperror.ErrConflict) {
			return // already exists, that's fine
		}
		log.Warn().Err(err).Int64("user_id", userID).Msg("create getting started note")
		return
	}

	// Write the content to the newly created note.
	note, err := s.notes.GetByFolderAndSlug(ctx, userID, nil, "getting-started")
	if err != nil {
		log.Warn().Err(err).Int64("user_id", userID).Msg("find getting started note after create")
		return
	}

	if _, err := s.UpdateNoteContent(ctx, userID, note.ID, gettingStartedContent, note.ContentHash); err != nil {
		log.Warn().Err(err).Int64("user_id", userID).Msg("write getting started content")
	}
}
