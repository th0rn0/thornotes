package notes_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/notes"
)

// ─── N9: GET /api/v1/notes/context memory ────────────────────────────────────
//
// The endpoint concatenates notes newest-first until the response would exceed
// contextCharLimit (200,000 characters). The claim under test (ticket 468) is
// that the *read* is unbounded even though the *response* is: every note's full
// content is loaded into memory before the cap is applied, so peak allocation
// tracks corpus size rather than the cap.

const contextSeedNoteChars = 2_000

// proseWords gives the seeded corpus a realistic token mix. Repeated natural
// words compress and tokenise differently from one repeated byte, so no dedup
// or index effect can make the corpus look smaller than it is.
var proseWords = []string{
	"reconcile", "folder", "wikilink", "journal", "synced", "markdown",
	"heading", "notebook", "search", "index", "context", "prompt", "draft",
	"ticket", "measure", "memory", "database", "sqlite", "single", "writer",
}

func corpusContent(i, chars int) string {
	var sb strings.Builder
	sb.Grow(chars + 32)
	fmt.Fprintf(&sb, "note %d\n\n", i)
	for w := 0; sb.Len() < chars; w++ {
		sb.WriteString(proseWords[(i+w)%len(proseWords)])
		sb.WriteByte(' ')
		if w%12 == 11 {
			sb.WriteString("\n\n")
		}
	}
	return sb.String()
}

// seedCorpus inserts n notes owned by stack's user, each with about noteChars
// characters of content, and returns the corpus's total character count.
//
// It writes straight to SQLite rather than through Service.CreateNote: the
// measurement is of the read path, and creating 8,000 notes through the service
// (filesystem + journal + link writes) would take minutes and measure the wrong
// thing. The rows are the shape the service writes.
func seedCorpus(t *testing.T, stack *serviceStack, n, noteChars int) int {
	t.Helper()
	ctx := context.Background()
	tx, err := stack.pool.WriteDB.BeginTx(ctx, nil)
	require.NoError(t, err)

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO notes (user_id, folder_id, title, slug, title_key, disk_path, content, content_hash, tags, fts_synced_at, updated_at)
		VALUES (?, NULL, ?, ?, ?, ?, ?, ?, '[]', datetime('now'), ?)`)
	require.NoError(t, err)

	// Distinct updated_at values make the newest-first order a total order, so
	// two builds of this package read the same corpus in the same order and the
	// response fingerprint below is comparable between them.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	total := 0
	for i := 0; i < n; i++ {
		content := corpusContent(i, noteChars)
		title := fmt.Sprintf("Seeded note %05d", i)
		slug := fmt.Sprintf("seeded-note-%05d", i)
		_, err := stmt.ExecContext(ctx, stack.userID, title, slug, strings.ToLower(title),
			slug+".md", content, fmt.Sprintf("hash-%05d", i),
			base.Add(time.Duration(i)*time.Second).Format("2006-01-02 15:04:05"))
		require.NoError(t, err)
		total += len(content)
	}
	require.NoError(t, stmt.Close())
	require.NoError(t, tx.Commit())
	return total
}

// measureContext runs NoteContext and reports the bytes the call allocated,
// plus the highest live heap observed while it ran.
//
// TotalAlloc is the honest metric: it counts every byte the call touched, so a
// lucky GC cycle cannot mask it, and unlike a live-heap sample it does not
// depend on catching the peak between two instructions. The sampled peak is
// reported next to it because "peak memory" in the ticket means the high-water
// mark — for the after-fix version the two agree, which is the point.
func measureContext(t *testing.T, svc *notes.Service, userID int64) (res *notes.NoteContextResult, alloc, sampledPeak uint64) {
	t.Helper()
	ctx := context.Background()

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	var (
		mu    sync.Mutex
		peak  uint64
		done  = make(chan struct{})
		wg    sync.WaitGroup
		mstat runtime.MemStats
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(250 * time.Microsecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				runtime.ReadMemStats(&mstat)
				mu.Lock()
				if mstat.HeapAlloc > peak {
					peak = mstat.HeapAlloc
				}
				mu.Unlock()
			}
		}
	}()

	res, err := svc.NoteContext(ctx, userID, nil)
	close(done)
	wg.Wait()
	require.NoError(t, err)

	runtime.ReadMemStats(&after)
	mu.Lock()
	sampled := peak
	mu.Unlock()
	return res, after.TotalAlloc - before.TotalAlloc, sampled
}

// fingerprint identifies a response by its bytes, so two builds can be compared
// by eye: the same corpus must produce the same context string before and after
// the read path changed.
func fingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// TestMeasure_N9_ContextMemory is the harness behind the ticket's N9 numbers.
// It is opt-in (THORNOTES_MEASURE=1) because seeding 8,000 notes is far too
// slow for `go test ./...`; TestService_NoteContext_MemoryBounded is the
// always-on regression guard.
func TestMeasure_N9_ContextMemory(t *testing.T) {
	if os.Getenv("THORNOTES_MEASURE") == "" {
		t.Skip("measurement harness: set THORNOTES_MEASURE=1 to run")
	}
	for _, n := range []int{500, 2000, 8000} {
		t.Run(fmt.Sprintf("notes=%d", n), func(t *testing.T) {
			stack := newTestStackFull(t)
			corpusChars := seedCorpus(t, stack, n, contextSeedNoteChars)

			res, alloc, peak := measureContext(t, stack.svc, stack.userID)

			t.Logf("notes=%d corpus_chars=%d cap=%d | response_chars=%d fingerprint=%s included=%d truncated=%v | allocated=%.2f MiB sampled_heap_peak=%.2f MiB | alloc/corpus=%.3f alloc/cap=%.1fx",
				n, corpusChars, res.CharLimit, len(res.Context), fingerprint(res.Context), res.NoteCount, res.Truncated,
				float64(alloc)/(1<<20), float64(peak)/(1<<20),
				float64(alloc)/float64(corpusChars), float64(alloc)/float64(res.CharLimit))
		})
	}
}

// TestService_NoteContext_MemoryBounded is the always-on guard for ticket N9:
// the endpoint's allocation must depend on the cap, not on how much text the
// user has written.
//
// 3,000 seeded notes of ~2,000 characters is a 6 MB corpus behind a 200,000
// character cap. Listing it first (the pre-N9 shape) allocated ~18 MiB here,
// because every body was materialised before the cap was applied; streaming
// with the cap stops after the 98th note and allocates ~1.4 MiB, which is the
// response buffer plus one note. 4 MiB is the line between the two: 2.8x above
// the streaming cost, 4.5x below the listing cost.
func TestService_NoteContext_MemoryBounded(t *testing.T) {
	stack := newTestStackFull(t)
	corpusChars := seedCorpus(t, stack, 3000, contextSeedNoteChars)

	// The premise: the corpus really is far larger than the bound being
	// asserted, so a bounded allocation means the read stopped early.
	require.Greater(t, corpusChars, 5<<20, "corpus must be large enough for the bound to mean something")

	res, alloc, _ := measureContext(t, stack.svc, stack.userID)

	// The response itself is unchanged: the cap still clips at the same note,
	// and the block format is the same as it has always been.
	require.True(t, res.Truncated, "a 6 MB corpus under a 200k cap must truncate")
	assert.LessOrEqual(t, len(res.Context), res.CharLimit)
	assert.Greater(t, res.NoteCount, 0)
	assert.True(t, strings.HasPrefix(res.Context, "# "), "each note is still formatted as a markdown heading block")

	const bound = 4 << 20 // 4 MiB
	assert.Less(t, alloc, uint64(bound),
		"NoteContext allocated %.2f MiB for a %d-char corpus under a %d-char cap; the read must stop at the cap",
		float64(alloc)/(1<<20), corpusChars, res.CharLimit)
}
