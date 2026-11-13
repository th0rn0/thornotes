package notes_test

// Ticket 633: a title that moves must take the links that named it with it.
//
// Link resolution matches on the title (internal/wikilinks/resolve.go), so a
// note whose title changes stops being the target of every [[old title]]. The
// repair decided for this is to rewrite the links, not to leave a redirect or
// alias behind: an alias would keep two names for one note alive forever, and
// every later read would have to agree with both.
//
// These tests state the rule on both backends:
//
//   - a rename through UpdateNoteMetadata rewrites every link that resolved to
//     the note, keeping the anchor and the display text exactly as written;
//   - a title converged from the file's "# H1" (the 467 path) does the same;
//   - an old title that named more than one note is left alone — that link was
//     already ambiguous and rewriting it would silently pick a winner;
//   - a title that does not resolve back to the note is left alone for the same
//     reason;
//   - links inside fenced blocks and inline code are documentation, not links,
//     and are never touched;
//   - the pass terminates: a second reconcile immediately afterwards makes no
//     change at all.

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/th0rn0/thornotes/internal/notes"
)

func linkRewriteCases() []titleRepairCase {
	return []titleRepairCase{
		{"a rename moves the link and keeps the anchor and display text", func(t *testing.T, st *titleStack) {
			ctx := context.Background()
			target, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Old Title", nil)
			require.NoError(t, err)
			source, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Source Page", nil)
			require.NoError(t, err)

			const body = "See [[Old Title#Details|the old page]], [[Old Title]] and [[  old   title  ]]."
			_, err = st.svc.UpdateNoteContent(ctx, st.userID, source.ID, body, source.ContentHash)
			require.NoError(t, err)

			require.NoError(t, st.svc.UpdateNoteMetadata(ctx, st.userID, st.user.UUID, target.ID, "New Title", nil))

			want := "See [[New Title#Details|the old page]], [[New Title]] and [[  New Title  ]]."
			got, err := st.svc.GetNote(ctx, st.userID, source.ID)
			require.NoError(t, err)
			assert.Equal(t, want, got.Content,
				"only the target moves: the anchor, the display text and the author's spacing are theirs")
			assert.Equal(t, want, readNoteFile(t, st, got.DiskPath),
				"the rewritten body is on disk, not only in the row")

			// The edge still resolves to the note that moved, carrying the
			// author's anchor and display text.
			edges, err := st.svc.OutgoingLinks(ctx, st.userID, source.ID)
			require.NoError(t, err)
			require.Len(t, edges, 1, "one edge per distinct target, as the index counts them")
			require.NotNil(t, edges[0].ToNoteID)
			assert.Equal(t, target.ID, *edges[0].ToNoteID)
			assert.Equal(t, "New Title", edges[0].RawTarget)
			assert.Equal(t, "Details", edges[0].Anchor)
			assert.Equal(t, "the old page", edges[0].LinkText)

			back, err := st.svc.Backlinks(ctx, st.userID, target.ID)
			require.NoError(t, err)
			require.Len(t, back, 1, "the backlink survives the rename")
			assert.Equal(t, source.ID, back[0].FromNoteID)
		}},

		{"a converged title from the file heading moves the link too", func(t *testing.T, st *titleStack) {
			ctx := context.Background()
			target, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Placeholder", nil)
			require.NoError(t, err)
			source, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Source Page", nil)
			require.NoError(t, err)
			_, err = st.svc.UpdateNoteContent(ctx, st.userID, source.ID,
				"see [[Placeholder#Notes|the notes]]", source.ContentHash)
			require.NoError(t, err)

			// An external editor states a different H1: reconcile converges the
			// row's title onto it (ticket 467), which moves the name the link
			// was written against.
			writeNoteFile(t, st, target.DiskPath, "# Actual Title\n\ntarget body")

			_, err = st.svc.Reconcile(ctx, st.user)
			require.NoError(t, err)

			converged, err := st.svc.GetNote(ctx, st.userID, target.ID)
			require.NoError(t, err)
			require.Equal(t, "Actual Title", converged.Title)

			got, err := st.svc.GetNote(ctx, st.userID, source.ID)
			require.NoError(t, err)
			assert.Equal(t, "see [[Actual Title#Notes|the notes]]", got.Content)
			assert.Equal(t, "see [[Actual Title#Notes|the notes]]", readNoteFile(t, st, got.DiskPath))

			edges, err := st.svc.OutgoingLinks(ctx, st.userID, source.ID)
			require.NoError(t, err)
			require.Len(t, edges, 1)
			require.NotNil(t, edges[0].ToNoteID)
			assert.Equal(t, target.ID, *edges[0].ToNoteID)
			assert.Equal(t, "Notes", edges[0].Anchor)
			assert.Equal(t, "the notes", edges[0].LinkText)
		}},

		{"a second pass immediately afterwards changes nothing", func(t *testing.T, st *titleStack) {
			ctx := context.Background()
			target, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Before", nil)
			require.NoError(t, err)
			source, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Source Page", nil)
			require.NoError(t, err)
			_, err = st.svc.UpdateNoteContent(ctx, st.userID, source.ID, "see [[Before]]", source.ContentHash)
			require.NoError(t, err)

			writeNoteFile(t, st, target.DiskPath, "# After\n")

			changed, err := st.svc.Reconcile(ctx, st.user)
			require.NoError(t, err)
			require.Greater(t, changed, 0, "the pass that retitles and rewrites reports its changes")

			first, err := st.svc.GetNote(ctx, st.userID, source.ID)
			require.NoError(t, err)
			require.Equal(t, "see [[After]]", first.Content)

			// Rewriting a body changes its hash, but never a title, so the pass
			// can never feed itself. This is the same property ticket 467's
			// tests assert for convergence alone.
			changed, err = st.svc.Reconcile(ctx, st.user)
			require.NoError(t, err)
			assert.Zero(t, changed, "a steady-state pass must write nothing")

			second, err := st.svc.GetNote(ctx, st.userID, source.ID)
			require.NoError(t, err)
			assert.Equal(t, first.Content, second.Content)
			assert.Equal(t, first.ContentHash, second.ContentHash)
			assert.Equal(t, first.UpdatedAt.UTC(), second.UpdatedAt.UTC(), "no write means updated_at must not move")
		}},

		{"an old title shared by two notes is skipped, not guessed", func(t *testing.T, st *titleStack) {
			ctx := context.Background()
			left, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Left")
			require.NoError(t, err)
			right, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Right")
			require.NoError(t, err)

			moved, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &left.ID, "Duplicate", nil)
			require.NoError(t, err)
			other, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &right.ID, "Duplicate", nil)
			require.NoError(t, err)
			source, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &left.ID, "Source Page", nil)
			require.NoError(t, err)
			_, err = st.svc.UpdateNoteContent(ctx, st.userID, source.ID, "see [[Duplicate]]", source.ContentHash)
			require.NoError(t, err)

			// The link resolved to the same-folder note; the name is shared, so
			// it was only unambiguous because of where it was written.
			edges, err := st.svc.OutgoingLinks(ctx, st.userID, source.ID)
			require.NoError(t, err)
			require.Len(t, edges, 1)
			require.NotNil(t, edges[0].ToNoteID)
			require.Equal(t, moved.ID, *edges[0].ToNoteID)

			require.NoError(t, st.svc.UpdateNoteMetadata(ctx, st.userID, st.user.UUID, moved.ID, "Renamed", nil))

			got, err := st.svc.GetNote(ctx, st.userID, source.ID)
			require.NoError(t, err)
			assert.Equal(t, "see [[Duplicate]]", got.Content,
				"a link on a shared old title is left exactly as written")

			// And it is not silently repointed at the note that moved: the
			// graph re-resolves the text as it stands (here, to the other
			// note that still answers to it), which is the resolver's rule,
			// not a rename picking a winner.
			edges, err = st.svc.OutgoingLinks(ctx, st.userID, source.ID)
			require.NoError(t, err)
			require.Len(t, edges, 1)
			assert.Equal(t, "Duplicate", edges[0].RawTarget)
			if edges[0].ToNoteID != nil {
				assert.NotEqual(t, moved.ID, *edges[0].ToNoteID)
				assert.Equal(t, other.ID, *edges[0].ToNoteID)
			}

			still, err := st.svc.GetNote(ctx, st.userID, other.ID)
			require.NoError(t, err)
			assert.Equal(t, "Duplicate", still.Title, "the other note keeps the name it had")
		}},

		{"a new title that resolves to a different note is skipped", func(t *testing.T, st *titleStack) {
			ctx := context.Background()
			folder, err := st.svc.CreateFolder(ctx, st.userID, st.user.UUID, nil, "Area")
			require.NoError(t, err)
			target, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Alpha", nil)
			require.NoError(t, err)
			_, err = st.svc.CreateNote(ctx, st.userID, st.user.UUID, &folder.ID, "Beta", nil)
			require.NoError(t, err)
			source, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, &folder.ID, "Source Page", nil)
			require.NoError(t, err)
			_, err = st.svc.UpdateNoteContent(ctx, st.userID, source.ID, "see [[Alpha]]", source.ContentHash)
			require.NoError(t, err)

			// From the source's folder, "Beta" is the note next door. Rewriting
			// the link to it would be a silent guess at what the author meant.
			require.NoError(t, st.svc.UpdateNoteMetadata(ctx, st.userID, st.user.UUID, target.ID, "Beta", nil))

			got, err := st.svc.GetNote(ctx, st.userID, source.ID)
			require.NoError(t, err)
			assert.Equal(t, "see [[Alpha]]", got.Content)
		}},

		{"a link written in the slug form moves with the title", func(t *testing.T, st *titleStack) {
			ctx := context.Background()
			target, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Old Title", nil)
			require.NoError(t, err)
			source, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Source Page", nil)
			require.NoError(t, err)
			// The filename form resolves through the same resolver, and the
			// rename moves the file too, so this link dies with the title.
			_, err = st.svc.UpdateNoteContent(ctx, st.userID, source.ID, "see [[old-title]]", source.ContentHash)
			require.NoError(t, err)

			require.NoError(t, st.svc.UpdateNoteMetadata(ctx, st.userID, st.user.UUID, target.ID, "New Title", nil))

			got, err := st.svc.GetNote(ctx, st.userID, source.ID)
			require.NoError(t, err)
			assert.Equal(t, "see [[New Title]]", got.Content)

			edges, err := st.svc.OutgoingLinks(ctx, st.userID, source.ID)
			require.NoError(t, err)
			require.Len(t, edges, 1)
			require.NotNil(t, edges[0].ToNoteID)
			assert.Equal(t, target.ID, *edges[0].ToNoteID)
		}},

		{"links inside code are documentation, not links", func(t *testing.T, st *titleStack) {
			ctx := context.Background()
			target, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Old Title", nil)
			require.NoError(t, err)
			source, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Source Page", nil)
			require.NoError(t, err)

			const body = "prose [[Old Title]]\n\n" +
				"```markdown\n[[Old Title]]\n```\n\n" +
				"~~~\n[[Old Title]]\n~~~\n\n" +
				"inline `[[Old Title]]` stays\n"
			_, err = st.svc.UpdateNoteContent(ctx, st.userID, source.ID, body, source.ContentHash)
			require.NoError(t, err)

			require.NoError(t, st.svc.UpdateNoteMetadata(ctx, st.userID, st.user.UUID, target.ID, "New Title", nil))

			got, err := st.svc.GetNote(ctx, st.userID, source.ID)
			require.NoError(t, err)
			want := "prose [[New Title]]\n\n" +
				"```markdown\n[[Old Title]]\n```\n\n" +
				"~~~\n[[Old Title]]\n~~~\n\n" +
				"inline `[[Old Title]]` stays\n"
			assert.Equal(t, want, got.Content,
				"a [[link]] inside a fence or an inline code span documents the syntax and is never rewritten")
			assert.Equal(t, want, readNoteFile(t, st, got.DiskPath))
		}},
	}
}

func TestLinkRewrite_TitleMoveRewritesInboundLinks(t *testing.T) {
	for _, backend := range titleRepairBackends() {
		t.Run(backend.name, func(t *testing.T) {
			for _, tc := range linkRewriteCases() {
				t.Run(tc.name, func(t *testing.T) {
					tc.run(t, backend.newStack(t))
				})
			}
		})
	}
}

// TestLinkRewrite_LeavesOtherNotesAlone is the "do not touch what you were not
// asked to touch" half: a rename must not rewrite a link that resolves to a
// different note, even when the target text looks identical.
func TestLinkRewrite_LeavesOtherNotesAlone(t *testing.T) {
	for _, backend := range titleRepairBackends() {
		t.Run(backend.name, func(t *testing.T) {
			st := backend.newStack(t)
			ctx := context.Background()

			moved, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Renamed Note", nil)
			require.NoError(t, err)
			untouched, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Some Other Note", nil)
			require.NoError(t, err)
			source, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Source Page", nil)
			require.NoError(t, err)
			const body = "keep [[Some Other Note]]"
			_, err = st.svc.UpdateNoteContent(ctx, st.userID, source.ID, body, source.ContentHash)
			require.NoError(t, err)

			require.NoError(t, st.svc.UpdateNoteMetadata(ctx, st.userID, st.user.UUID, moved.ID, "Migrated Note", nil))

			got, err := st.svc.GetNote(ctx, st.userID, source.ID)
			require.NoError(t, err)
			assert.Equal(t, body, got.Content)

			edges, err := st.svc.OutgoingLinks(ctx, st.userID, source.ID)
			require.NoError(t, err)
			require.Len(t, edges, 1)
			require.NotNil(t, edges[0].ToNoteID)
			assert.Equal(t, untouched.ID, *edges[0].ToNoteID)
		})
	}
}

// TestBrokenLinks_Report is the read-only half of ticket 633 §2: what a vault
// with already-orphaned links looks like from the outside. It writes nothing,
// and the near-match column is a hint — the link is never repaired on a guess.
func TestBrokenLinks_Report(t *testing.T) {
	for _, backend := range titleRepairBackends() {
		t.Run(backend.name, func(t *testing.T) {
			st := backend.newStack(t)
			ctx := context.Background()

			renamed, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Renaming Notes", nil)
			require.NoError(t, err)
			source, err := st.svc.CreateNote(ctx, st.userID, st.user.UUID, nil, "Source Page", nil)
			require.NoError(t, err)

			// The shape a pre-633 convergence left behind: the row's title has
			// moved on, the link still names the old one, and nothing rewrites
			// it because the old title is simply gone.
			const body = "see [[Renaming Note#Why|the old page]] and [[Nothing At All]]"
			_, err = st.svc.UpdateNoteContent(ctx, st.userID, source.ID, body, source.ContentHash)
			require.NoError(t, err)
			writeNoteFile(t, st, renamed.DiskPath, "# Renaming Notes\n")
			_, err = st.svc.Reconcile(ctx, st.user)
			require.NoError(t, err)

			inv, err := st.svc.BrokenLinks(ctx, st.userID)
			require.NoError(t, err)
			assert.Equal(t, 2, inv.Notes)
			assert.Zero(t, inv.Links)
			require.Len(t, inv.Broken, 2)

			broken := func(target string) notes.BrokenLink {
				t.Helper()
				for _, b := range inv.Broken {
					if b.Target == target {
						return b
					}
				}
				t.Fatalf("no broken link for target %q", target)
				return notes.BrokenLink{}
			}

			near := broken("Renaming Note")
			assert.Equal(t, source.ID, near.NoteID)
			assert.Equal(t, "Source Page", near.NoteTitle)
			assert.Equal(t, "Why", near.Anchor)
			assert.Equal(t, "the old page", near.LinkText)
			assert.False(t, near.Ambiguous)
			assert.Equal(t, `"Renaming Notes" (note `+strconv.FormatInt(renamed.ID, 10)+`, 1 edit(s))`, near.NearMatch,
				"the nearest title is offered as a hint for a human")

			far := broken("Nothing At All")
			assert.Empty(t, far.NearMatch, "an unrelated title is not a hint")

			// Read-only: nothing was repaired, and the report is stable.
			got, err := st.svc.GetNote(ctx, st.userID, source.ID)
			require.NoError(t, err)
			assert.Equal(t, body, got.Content)
			again, err := st.svc.BrokenLinks(ctx, st.userID)
			require.NoError(t, err)
			assert.Equal(t, inv, again)
		})
	}
}
