package handler_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── N10: an ordinary write while the FTS re-sync runs ───────────────────────
//
// Search re-indexes every note with fts_synced_at IS NULL, and every one of
// those statements needs the MaxOpenConns(1) write connection
// (internal/repository/sqlite/search.go). The defect (ticket 468) is not a
// queue: no request waits seconds for the whole backlog to finish
// (max_during/backlog was ~0.01 measured before the fix). It is a window — the
// re-sync occupies the write connection for the run's whole duration, so an
// ordinary POST /api/v1/notes issued inside that window is 3-13x slower with a
// tail near a second. Batching closes the connection between batches and
// shrinks the window 7-20x; each batch's commit is the irreducible residual on a
// durable single-writer database.
//
// What that means for this file: the numbers are evidence, so they live in
// BenchmarkN10_WriteDuringResync, which `go test ./...` never runs. The gate is
// structural and lives at the layer that owns the connection
// (TestSearchRepo_StaleBacklog_ReleasesWriteConnBetweenBatches, which proves the
// release from inside a batch boundary); the always-on test here guards the
// end-to-end plumbing.

const n10NoteChars = 2_000

var n10Words = []string{
	"reconcile", "folder", "wikilink", "journal", "synced", "markdown",
	"heading", "notebook", "search", "index", "context", "prompt", "draft",
	"ticket", "measure", "memory", "database", "sqlite", "single", "writer",
}

func n10Content(i, chars int) string {
	buf := make([]byte, 0, chars+64)
	buf = append(buf, fmt.Sprintf("note %d\n\n", i)...)
	for w := 0; len(buf) < chars; w++ {
		buf = append(buf, n10Words[(i+w)%len(n10Words)]...)
		buf = append(buf, ' ')
		if w%12 == 11 {
			buf = append(buf, '\n', '\n')
		}
	}
	return string(buf)
}

// seedStaleNotes inserts n notes owned by username with fts_synced_at NULL, so
// the next search has n notes to re-index — the backlog the ticket describes.
func seedStaleNotes(t testing.TB, c *testClient, username string, n, noteChars int) {
	t.Helper()
	ctx := context.Background()

	var userID int64
	require.NoError(t, c.pool.ReadDB.QueryRowContext(ctx,
		`SELECT id FROM users WHERE username = ?`, username).Scan(&userID))

	tx, err := c.pool.WriteDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO notes (user_id, folder_id, title, slug, title_key, disk_path, content, content_hash, tags, fts_synced_at)
		VALUES (?, NULL, ?, ?, ?, ?, ?, ?, '[]', NULL)`)
	require.NoError(t, err)
	for i := 0; i < n; i++ {
		content := n10Content(i, noteChars)
		title := fmt.Sprintf("Stale note %05d", i)
		slug := fmt.Sprintf("stale-note-%05d", i)
		_, err := stmt.ExecContext(ctx, userID, title, slug, fmt.Sprintf("stale note %05d", i),
			slug+".md", content, fmt.Sprintf("stale-hash-%05d", i))
		require.NoError(t, err)
	}
	require.NoError(t, stmt.Close())
	require.NoError(t, tx.Commit())
}

// postNote issues one POST /api/v1/notes and returns its wall-clock latency.
// It is safe to call from the test goroutine only, which is where the writes in
// this file are issued: the search is the goroutine.
func postNote(c *testClient) (time.Duration, error) {
	req, err := newJSONRequest(c, http.MethodPost, "/api/v1/notes",
		fmt.Sprintf(`{"title":"probe %d"}`, time.Now().UnixNano()))
	if err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := c.httpClient.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return elapsed, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return elapsed, fmt.Errorf("POST /api/v1/notes answered %d", resp.StatusCode)
	}
	return elapsed, nil
}

// searchNotes runs GET /api/v1/notes?q=... — the request whose handler drives
// the stale-notes re-sync — and returns how long it took.
func searchNotes(ctx context.Context, c *testClient, q string) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.server.URL+"/api/v1/notes?q="+url.QueryEscape(q), nil)
	if err != nil {
		return 0, err
	}
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	start := time.Now()
	resp, err := c.httpClient.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return elapsed, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return elapsed, fmt.Errorf("GET /api/v1/notes answered %d", resp.StatusCode)
	}
	return elapsed, nil
}

func newJSONRequest(c *testClient, method, path, body string) (*http.Request, error) {
	req, err := http.NewRequest(method, c.server.URL+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	if c.csrfToken != "" {
		req.Header.Set("X-CSRF-Token", c.csrfToken)
	}
	return req, nil
}

func latencyStats(lat []time.Duration) (median, p95, max time.Duration) {
	if len(lat) == 0 {
		return 0, 0, 0
	}
	s := append([]time.Duration(nil), lat...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := len(s) * 95 / 100
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[len(s)/2], s[idx], s[len(s)-1]
}

// BenchmarkN10_WriteDuringResync is the harness behind the ticket's N10 table.
// It is a benchmark rather than a test because every number it produces is a
// measurement of this machine: as an assertion it was a stopwatch, and that is
// what failed CI.
//
//	go test -run '^$' -bench BenchmarkN10 -benchtime 1x ./internal/handler/
//
// One iteration is one pass: re-mark the seeded backlog stale, run the search
// that re-indexes it, and issue POSTs continuously for as long as it is in
// flight. Each iteration repeats that over the same corpus, so -benchtime 1x
// gives the table in the PR untouched.
func BenchmarkN10_WriteDuringResync(b *testing.B) {
	const baselineN = 50

	for _, n := range []int{500, 2000, 8000} {
		b.Run(fmt.Sprintf("stale=%d", n), func(b *testing.B) {
			ctx := context.Background()
			c := newTestClient(b)
			c.registerAndLogin(b)

			// Baseline: same endpoint, same instance, no re-sync in flight.
			var base []time.Duration
			for i := 0; i < baselineN; i++ {
				lat, err := postNote(c)
				require.NoError(b, err)
				base = append(base, lat)
			}
			bm, bp, bx := latencyStats(base)

			seedStaleNotes(b, c, "testuser", n, n10NoteChars)

			var (
				during    []time.Duration
				searchDur time.Duration
				searchErr error
			)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Every seeded note back to stale for this pass.
				_, err := c.pool.WriteDB.ExecContext(ctx, `UPDATE notes SET fts_synced_at = NULL`)
				require.NoError(b, err)

				searchDone := make(chan struct{})
				go func() {
					searchDur, searchErr = searchNotes(ctx, c, "reconcile")
					close(searchDone)
				}()

				during = during[:0]
			loop:
				for {
					select {
					case <-searchDone:
						break loop
					default:
					}
					lat, err := postNote(c)
					if err != nil {
						b.Fatal(err)
					}
					during = append(during, lat)
				}
				<-searchDone
				require.NoError(b, searchErr)
			}
			b.StopTimer()

			dm, dp, dx := latencyStats(during)
			// Milliseconds with the fraction kept: the interesting numbers here
			// are single-digit, and truncating them to whole ms hides the
			// difference the change is about.
			ms := func(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e6 }
			b.ReportMetric(ms(bm), "base_med_ms")
			b.ReportMetric(ms(bp), "base_p95_ms")
			b.ReportMetric(ms(bx), "base_max_ms")
			b.ReportMetric(ms(dm), "during_med_ms")
			b.ReportMetric(ms(dp), "during_p95_ms")
			b.ReportMetric(ms(dx), "during_max_ms")
			b.ReportMetric(ms(searchDur), "resync_ms")
			b.ReportMetric(float64(dx)/float64(searchDur), "during_max_over_resync")
			b.Logf("stale=%d | resync+search wall=%s | baseline POST n=%d median=%s p95=%s max=%s | during-resync POST n=%d median=%s p95=%s max=%s | max_during/backlog=%.2f",
				n, searchDur, len(base), bm, bp, bx, len(during), dm, dp, dx,
				float64(dx)/float64(searchDur))
		})
	}
}

// TestHandler_Notes_Create_CompletesDuringResync is the ticket's N10 check
// end to end, through the real router: a note creation issued while a search
// re-indexes a backlog must be served, and the pass must finish with the whole
// backlog indexed.
//
// It was called TestHandler_Notes_Create_IsNotQueuedBehindResync and asserted
// that the slowest write inside the window was under a quarter of the pass's
// duration. That name and that clause both rest on the premise the measurement
// refuted: there is no queue behind the backlog, there is a long window, and
// the write's latency inside it depends on commit latency and load. It passed
// by luck; the equivalent assertion in the repository test failed on CI.
//
// So no clock here. The property "the write connection is released between
// batches" is proved deterministically where it belongs, by
// TestSearchRepo_StaleBacklog_ReleasesWriteConnBetweenBatches, which issues a
// real write from inside a batch boundary with MaxOpenConns(1) and requires it
// to succeed. This test keeps the end-to-end shape honest: writes are served
// while the pass is in flight, every one of them succeeds, and the pass leaves
// nothing stale.
//
// Skipped under -short: it seeds thousands of notes.
func TestHandler_Notes_Create_CompletesDuringResync(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a large backlog: skipped under -short")
	}
	const (
		backlogNotes = 3000
		backlogChars = 200
		baselineN    = 20
	)

	c := newTestClient(t)
	c.registerAndLogin(t)

	// Recorded for the log line only. A baseline is context for the numbers in
	// the benchmark, never a threshold.
	var base []time.Duration
	for i := 0; i < baselineN; i++ {
		lat, err := postNote(c)
		require.NoError(t, err)
		base = append(base, lat)
	}
	baseMedian, _, _ := latencyStats(base)

	seedStaleNotes(t, c, "testuser", backlogNotes, backlogChars)

	searchDone := make(chan struct{})
	var searchDur time.Duration
	var searchErr error
	go func() {
		searchDur, searchErr = searchNotes(context.Background(), c, "reconcile")
		close(searchDone)
	}()

	deadline := time.Now().Add(3 * time.Minute)
	var during []time.Duration
	for running := true; running && time.Now().Before(deadline); {
		select {
		case <-searchDone:
			running = false
		default:
		}
		if !running {
			break
		}
		// postNote requires 201: a write rejected or failed during the pass
		// fails the test here.
		lat, err := postNote(c)
		require.NoError(t, err)
		during = append(during, lat)
	}
	<-searchDone
	require.NoError(t, searchErr)

	// The pass was in flight long enough for writes to land inside it. Without
	// this the loop below proves nothing: it would be asserting about a window
	// that never existed.
	require.GreaterOrEqual(t, len(during), 10,
		"the re-sync finished before ten writes were issued during it; the backlog is too small to exercise the window")

	// The pass did the work it was asked to do, boundary included: no seeded
	// note may be left stale. The notes the loop above created are expected to
	// be stale — they arrived after the pass took its snapshot — so they are
	// excluded here and bounded below instead.
	var staleSeeded, staleTotal int
	require.NoError(t, c.pool.ReadDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM notes WHERE fts_synced_at IS NULL AND slug LIKE 'stale-note-%'`).Scan(&staleSeeded))
	require.NoError(t, c.pool.ReadDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM notes WHERE fts_synced_at IS NULL`).Scan(&staleTotal))
	assert.Zero(t, staleSeeded, "the pass must leave nothing of the backlog stale: a batch boundary must not drop notes")
	assert.LessOrEqual(t, staleTotal, len(during),
		"%d notes are stale but only %d were created during the pass: something older than the pass was skipped", staleTotal, len(during))

	duringMedian, duringP95, duringMax := latencyStats(during)
	t.Logf("baseline write median=%s | re-sync of %d stale notes took %s (%.2fms/note) | writes during it: n=%d median=%s p95=%s max=%s | max/backlog=%.3f",
		baseMedian, backlogNotes, searchDur, float64(searchDur/backlogNotes)/float64(time.Millisecond),
		len(during), duringMedian, duringP95, duringMax,
		float64(duringMax)/float64(searchDur))
}
