package sqlite

import (
	"context"
	"strconv"
	"testing"
	"time"

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

// A stale backlog is re-indexed in bounded batches, so the batch boundary is a
// place a note can be silently skipped: everything past the first batch must
// still end up in the index, and the note that sorts last must be findable by
// a word only it carries (ticket N10).
func TestSearchRepo_StaleBacklog_SyncsAcrossBatches(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := NewSearchRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	// Several batches' worth, whatever the batch size is: the budget ends a
	// batch early once it is spent, so the count is an upper bound and this
	// backlog spans at least three of them.
	const notes = 3*syncBatchSize + 7
	var tail *model.Note
	for i := 0; i < notes; i++ {
		n := newNote(user.ID, "Backlog note", "backlog-note-"+strconv.Itoa(i),
			"batchmark body "+strconv.Itoa(i))
		created, err := noteRepo.Create(ctx, n)
		require.NoError(t, err)
		tail = created
	}
	// A token only the last note carries, to prove the tail was indexed and not
	// just the head.
	_, err := pool.WriteDB.ExecContext(ctx,
		`UPDATE notes SET content = ? WHERE id = ?`, "batchmark tailtoken", tail.ID)
	require.NoError(t, err)

	// Every note is stale: this is the backlog a bulk import leaves behind.
	_, err = pool.WriteDB.ExecContext(ctx, `UPDATE notes SET fts_synced_at = NULL`)
	require.NoError(t, err)

	var stale int
	require.NoError(t, pool.ReadDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notes WHERE fts_synced_at IS NULL`).Scan(&stale))
	require.Equal(t, notes, stale, "the whole corpus must start stale")

	results, err := searchRepo.Search(ctx, user.ID, "batchmark", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, results)

	require.NoError(t, pool.ReadDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notes WHERE fts_synced_at IS NULL`).Scan(&stale))
	assert.Zero(t, stale, "every note in the backlog must be stamped, not just the first batches")

	tailResults, err := searchRepo.Search(ctx, user.ID, "tailtoken", nil)
	require.NoError(t, err)
	require.Len(t, tailResults, 1, "the last note in the backlog must be indexed")
	assert.Equal(t, tail.ID, tailResults[0].NoteID)
}

// The re-sync must cost less per note than re-indexing the same notes one at a
// time, and the comparison is made in this process so it does not depend on how
// fast the disk is: SyncNote is the one-at-a-time path (one transaction per
// note), syncStale is the backlog path (one transaction per batch).
//
// On 26488a6 both are the same loop — Search called SyncNote once per stale
// note — so the two numbers are equal by construction and this fails. That
// equality is the bug: the re-sync took the single write connection once per
// statement for the entire backlog.
func TestSearchRepo_StaleBacklog_BatchedSyncBeatPerNoteTransactions(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := NewSearchRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	const notes = 200
	ids := make([]int64, 0, notes)
	for i := 0; i < notes; i++ {
		n := newNote(user.ID, "Batch cost note", "batch-cost-note-"+strconv.Itoa(i),
			"costmark body "+strconv.Itoa(i))
		created, err := noteRepo.Create(ctx, n)
		require.NoError(t, err)
		ids = append(ids, created.ID)
	}

	markAllStale := func() {
		t.Helper()
		_, err := pool.WriteDB.ExecContext(ctx, `UPDATE notes SET fts_synced_at = NULL`)
		require.NoError(t, err)
	}

	// Batched: one search re-indexes the whole backlog.
	markAllStale()
	start := time.Now()
	_, err := searchRepo.Search(ctx, user.ID, "costmark", nil)
	require.NoError(t, err)
	batchedPerNote := time.Since(start) / notes

	// One at a time: the same notes, one transaction each.
	markAllStale()
	start = time.Now()
	for _, id := range ids {
		require.NoError(t, searchRepo.SyncNote(ctx, id))
	}
	singlePerNote := time.Since(start) / notes

	t.Logf("%d notes: batched re-sync %s/note, one-at-a-time %s/note (%.2fx)",
		notes, batchedPerNote, singlePerNote, float64(singlePerNote)/float64(batchedPerNote))

	assert.Less(t, batchedPerNote, singlePerNote/2,
		"re-indexing a backlog cost %s/note, against %s/note one note at a time: the batch is not amortising the commit",
		batchedPerNote, singlePerNote)
}
