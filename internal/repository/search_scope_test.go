package repository_test

// The short-term scan is the only part of search that reads note text without an
// index in the way, so it is the only part where "scoped to the caller" is a
// property of a WHERE clause someone could forget. A scan without the user filter
// would return other people's notes — a data leak, not a ranking bug — and it
// would still look correct in a single-user fixture set. Hence a test with two
// users: the same term exists in both vaults, and each search must see only its
// own note.
//
// Ticket 619. Runs on both engines; the MySQL leg is where the short term is
// genuinely unanswerable by the index.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/model"
)

// seedScopedNote creates one note for userID and returns it.
func seedScopedNote(t *testing.T, b *backend, userID int64, slug, title, content string) {
	t.Helper()
	ctx := context.Background()

	_, err := b.notes.Create(ctx, &model.Note{
		UserID:      userID,
		Title:       title,
		Slug:        slug,
		DiskPath:    "scoped-user/" + slug + ".md",
		ContentHash: "hash-" + slug,
		Content:     content,
	})
	require.NoError(t, err, "%s: create note %s", b.name, slug)
}

// TestSearchShortTermIsScopedToCaller proves the scan cannot see another user's
// notes, with the same short term present in both vaults so a missing filter
// returns two rows instead of one.
func TestSearchShortTermIsScopedToCaller(t *testing.T) {
	for _, makeBackend := range []struct {
		name string
		new  func(*testing.T) *backend
	}{
		{name: "sqlite", new: newSQLiteBackend},
		{name: "mysql", new: newMySQLBackend},
	} {
		t.Run(makeBackend.name, func(t *testing.T) {
			ctx := context.Background()
			b := makeBackend.new(t)

			alice, err := b.users.Create(ctx, "alice", "hash", "alice-uuid")
			require.NoError(t, err)
			bob, err := b.users.Create(ctx, "bob", "hash", "bob-uuid")
			require.NoError(t, err)

			// The same two-character token in both vaults, plus a term only one
			// of them has.
			seedScopedNote(t, b, alice.ID, "alice-go", "Alice Place", "Meet me at the go kart track.")
			seedScopedNote(t, b, bob.ID, "bob-go", "Bob Place", "The go kart track is closed tomorrow.")
			seedScopedNote(t, b, bob.ID, "bob-only", "Bob Secret", "A private word: kumquat.")

			// Alice's short-term search returns her note and nothing of Bob's.
			aliceResults, err := b.search.Search(ctx, alice.ID, "go", nil)
			require.NoError(t, err)
			assert.Equal(t, []string{"alice-go"}, slugsOf(aliceResults),
				"%s: a short-term scan must not cross the user boundary", b.name)

			// ... and the other way round.
			bobResults, err := b.search.Search(ctx, bob.ID, "go", nil)
			require.NoError(t, err)
			assert.Equal(t, []string{"bob-go"}, slugsOf(bobResults),
				"%s: a short-term scan must not cross the user boundary", b.name)

			// Bob's long-term search is scoped by the same WHERE clause; it is
			// here so a future change that scopes one half and not the other
			// fails on the half it forgot.
			bobLong, err := b.search.Search(ctx, bob.ID, "kumquat", nil)
			require.NoError(t, err)
			assert.Equal(t, []string{"bob-only"}, slugsOf(bobLong), "%s: indexed search", b.name)

			aliceLong, err := b.search.Search(ctx, alice.ID, "kumquat", nil)
			require.NoError(t, err)
			assert.Empty(t, slugsOf(aliceLong), "%s: indexed search must not cross users", b.name)
		})
	}
}
