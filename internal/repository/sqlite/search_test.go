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

// The re-sync must not hold the single write connection across the backlog.
// That is proved here structurally, not measured: the hook runs between two
// batches — after one transaction has committed, before the next one opens —
// and performs an ordinary write on the same MaxOpenConns(1) write pool. If the
// previous batch's transaction were still open, that write could not get the
// connection: it would block until the hook's deadline and come back with an
// error. With one transaction per batch it simply completes.
//
// There is deliberately no stopwatch in this gate. An earlier version of this
// test compared batched and per-note wall-clock cost and passed at 11.70x on an
// idle machine, then inverted on CI where the same ratio was 0.81x; see
// BenchmarkSearchRepo_StaleBacklog for the numbers, which are evidence rather
// than an assertion.
//
// On 26488a6 Search called SyncNote in a loop, so there were no batch
// boundaries at all: the hook never fires and the count assertion below fails.
func TestSearchRepo_StaleBacklog_ReleasesWriteConnBetweenBatches(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := NewSearchRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	// More than one batch at the production limit, so there is a boundary
	// between two transactions for the hook to fire on. The time budget may
	// split these further; that only gives the hook more boundaries to prove.
	const notes = 2*syncBatchSize + 50
	for i := 0; i < notes; i++ {
		_, err := noteRepo.Create(ctx, newNote(user.ID, "Release note",
			"release-note-"+strconv.Itoa(i), "releasemark body "+strconv.Itoa(i)))
		require.NoError(t, err)
	}
	_, err := pool.WriteDB.ExecContext(ctx, `UPDATE notes SET fts_synced_at = NULL`)
	require.NoError(t, err)

	var (
		batches   int
		writeErrs []error
	)
	searchRepo.syncBatchHook = func(batch int) {
		batches++
		// An ordinary write, on the write pool the re-sync is using. The
		// deadline is a liveness bound, not a latency comparison: an
		// uncontended create takes milliseconds on any machine, so it fires
		// only when the connection really is held by a transaction.
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		_, err := noteRepo.Create(wctx, newNote(user.ID, "Probe note",
			"probe-note-"+strconv.Itoa(batch), "probemark body "+strconv.Itoa(batch)))
		writeErrs = append(writeErrs, err)
	}

	_, err = searchRepo.Search(ctx, user.ID, "releasemark", nil)
	require.NoError(t, err)

	require.GreaterOrEqual(t, batches, 2,
		"%d stale notes were re-indexed without a single batch boundary: the connection is held for the whole run", notes)
	require.Len(t, writeErrs, batches)
	for batch, err := range writeErrs {
		require.NoErrorf(t, err,
			"a write issued after batch %d committed could not be served: that transaction was still holding the write connection", batch)
	}

	// The probes the hook wrote are the only notes that should still be stale:
	// they were created after the pass took its snapshot. Everything the pass
	// took on must be stamped, boundary included.
	var stale int
	require.NoError(t, pool.ReadDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notes WHERE fts_synced_at IS NULL`).Scan(&stale))
	assert.Equal(t, len(writeErrs), stale,
		"only the %d notes written from inside the pass should be left stale; the backlog itself must be fully stamped", len(writeErrs))
}

// The batch boundary is what bounds the connection hold, so the number of
// transactions is part of the contract: ceil(stale/limit) for a backlog of N.
// A regression to one transaction per note — the shape on 26488a6, where Search
// called SyncNote in a loop — takes N transactions and fails here immediately.
// That is the property the wall-clock comparison was trying to express and
// could not: the ratio moved with machine load, the count does not.
//
// The time budget is put out of the way so the count is decided by the limit
// alone. With the 5 ms budget in play a loaded machine legitimately commits
// smaller batches — on CI the per-note cost was above the budget, so batches
// degenerated to a note or two — and the assertion would be a race between the
// batch size and commit latency instead of a statement about the code.
func TestSearchRepo_StaleBacklog_CommitsOneTransactionPerBatch(t *testing.T) {
	pool := openTestDB(t)
	user := createUser(t, pool)
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := NewSearchRepo(pool.ReadDB, pool.WriteDB)
	searchRepo.batchBudget = time.Hour
	ctx := context.Background()

	const notes = 3*syncBatchSize + 37
	for i := 0; i < notes; i++ {
		_, err := noteRepo.Create(ctx, newNote(user.ID, "Batch count note",
			"batch-count-note-"+strconv.Itoa(i), "countmark body "+strconv.Itoa(i)))
		require.NoError(t, err)
	}
	_, err := pool.WriteDB.ExecContext(ctx, `UPDATE notes SET fts_synced_at = NULL`)
	require.NoError(t, err)

	var batches int
	searchRepo.syncBatchHook = func(int) { batches++ }

	_, err = searchRepo.Search(ctx, user.ID, "countmark", nil)
	require.NoError(t, err)

	want := (notes + syncBatchSize - 1) / syncBatchSize
	require.Equal(t, want, batches,
		"re-indexing %d stale notes took %d transactions; it must take ceil(%d/%d)=%d, one per batch (one per note would be %d)",
		notes, batches, notes, syncBatchSize, want, notes)

	// Nothing skipped at a boundary: the count above would also be satisfied by
	// batches that dropped notes.
	var stale int
	require.NoError(t, pool.ReadDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notes WHERE fts_synced_at IS NULL`).Scan(&stale))
	assert.Zero(t, stale, "every note in the backlog must be stamped, not just the ones inside the counted batches")
}

// BenchmarkSearchRepo_StaleBacklog records what batching bought, as evidence
// rather than as a gate: `go test ./...` never runs a Benchmark, and on CI the
// same numbers are a coin toss — the batched/per-note ratio was 11.70x on an
// idle machine and inverted on a loaded runner.
//
//	go test -run '^$' -bench BenchmarkSearchRepo_StaleBacklog -benchtime 3x ./internal/repository/sqlite/
//
// Each iteration re-marks the whole corpus stale and re-indexes it once, once
// through the backlog path (one transaction per batch) and once through the
// one-at-a-time path (one transaction per note).
func BenchmarkSearchRepo_StaleBacklog(b *testing.B) {
	const notes = 200
	pool := openTestDB(b)
	user := createUser(b, pool)
	noteRepo := NewNoteRepo(pool.ReadDB, pool.WriteDB)
	searchRepo := NewSearchRepo(pool.ReadDB, pool.WriteDB)
	ctx := context.Background()

	ids := make([]int64, 0, notes)
	for i := 0; i < notes; i++ {
		created, err := noteRepo.Create(ctx, newNote(user.ID, "Batch cost note",
			"batch-cost-note-"+strconv.Itoa(i), "costmark body "+strconv.Itoa(i)))
		require.NoError(b, err)
		ids = append(ids, created.ID)
	}

	markAllStale := func(b *testing.B) {
		b.Helper()
		_, err := pool.WriteDB.ExecContext(ctx, `UPDATE notes SET fts_synced_at = NULL`)
		require.NoError(b, err)
	}
	perNote := func(b *testing.B) {
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*notes), "ns/note")
	}

	b.Run("batched", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			markAllStale(b)
			if _, err := searchRepo.Search(ctx, user.ID, "costmark", nil); err != nil {
				b.Fatal(err)
			}
		}
		perNote(b)
	})

	b.Run("per-note", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			markAllStale(b)
			for _, id := range ids {
				if err := searchRepo.SyncNote(ctx, id); err != nil {
					b.Fatal(err)
				}
			}
		}
		perNote(b)
	})
}
