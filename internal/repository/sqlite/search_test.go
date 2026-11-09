package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/model"
)

// newNote is a small fixture helper for the search tests.
func newNote(userID int64, title, slug, content string, tags ...string) *model.Note {
	return &model.Note{
		UserID:      userID,
		Title:       title,
		Slug:        slug,
		DiskPath:    "1/" + slug + ".md",
		Content:     content,
		ContentHash: "hash-" + slug,
		Tags:        tags,
	}
}

// Relevance must decide the order before the title tie-breaker does. The note
// that mentions the term twice carries the alphabetically later title on
// purpose: if rank is not being computed at all (it returns NULL when the FTS
// index statistics have been poisoned), the query falls back to title order and
// this test fails.
func TestSearchRepo_Search_RanksByRelevance(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := NewSearchRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	// Equal token counts, so the relevance difference is term frequency alone.
	for _, n := range []*model.Note{
		newNote(user.ID, "Zulu Note", "zulu-note", "The body and the body copy here."),
		newNote(user.ID, "Alpha Note", "alpha-note", "The single body copy is right here."),
	} {
		_, err := noteRepo.Create(ctx, n)
		require.NoError(t, err)
	}

	results, err := searchRepo.Search(ctx, user.ID, "body", nil)
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "Zulu Note", results[0].Title, "the note with two occurrences must rank first")
	assert.Equal(t, "Alpha Note", results[1].Title)
}

// The pre-452 SyncNote issued FTS5's 'delete' command for notes that had never
// been indexed, and ignored the error it raised. That error leaves the index
// statistics unusable, so rank/bm25() return NULL for every row from then on
// and relevance ordering silently degrades to the title tie-breaker. Syncing
// twice must therefore still leave relevance ordering working.
func TestSearchRepo_SyncNote_RepeatedSyncKeepsRank(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := NewSearchRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	zulu, err := noteRepo.Create(ctx, newNote(user.ID, "Zulu Note", "zulu-note", "The body and the body copy here."))
	require.NoError(t, err)
	_, err = noteRepo.Create(ctx, newNote(user.ID, "Alpha Note", "alpha-note", "The single body copy is right here."))
	require.NoError(t, err)

	for i := 0; i < 2; i++ {
		require.NoError(t, searchRepo.SyncNote(ctx, zulu.ID))
	}

	results, err := searchRepo.Search(ctx, user.ID, "body", nil)
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "Zulu Note", results[0].Title)
}

// Equal relevance falls back to title, then id: two identical queries must
// return the same order, and the order must not depend on insertion order.
func TestSearchRepo_Search_OrdersTiesByTitleThenID(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := NewSearchRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	for _, n := range []*model.Note{
		newNote(user.ID, "Bravo Note", "bravo-note", "One body only, in this note."),
		newNote(user.ID, "Alpha Note", "alpha-note", "One body only, in that note."),
	} {
		_, err := noteRepo.Create(ctx, n)
		require.NoError(t, err)
	}

	first, err := searchRepo.Search(ctx, user.ID, "body", nil)
	require.NoError(t, err)
	require.Len(t, first, 2)
	assert.Equal(t, []string{"alpha-note", "bravo-note"}, []string{first[0].Slug, first[1].Slug})

	second, err := searchRepo.Search(ctx, user.ID, "body", nil)
	require.NoError(t, err)
	assert.Equal(t, first[0].Snippet, second[0].Snippet)
	assert.Equal(t, first[0].Slug, second[0].Slug)
}
