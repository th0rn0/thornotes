package handler_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
// (internal/repository/sqlite/search.go). The claim under test (ticket 468) is
// that an ordinary POST /api/v1/notes issued while that runs is serialised
// behind the re-sync.
//
// Everything here goes through the real router over HTTP: the claim is about
// request latency, and a direct repository call would measure a different
// thing.

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
func seedStaleNotes(t *testing.T, c *testClient, username string, n, noteChars int) {
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

// TestMeasure_N10_WriteDuringResync is the harness behind the ticket's N10
// numbers. Opt-in because it seeds thousands of notes; the always-on guard is
// TestHandler_Notes_Create_IsNotQueuedBehindResync.
func TestMeasure_N10_WriteDuringResync(t *testing.T) {
	if os.Getenv("THORNOTES_MEASURE") == "" {
		t.Skip("measurement harness: set THORNOTES_MEASURE=1 to run")
	}

	for _, n := range []int{500, 2000, 8000} {
		t.Run(fmt.Sprintf("stale=%d", n), func(t *testing.T) {
			c := newTestClient(t)
			c.registerAndLogin(t)

			// Baseline: same endpoint, same instance, no re-sync in flight.
			var base []time.Duration
			for i := 0; i < 50; i++ {
				lat, err := postNote(c)
				require.NoError(t, err)
				base = append(base, lat)
			}
			bm, bp, bx := latencyStats(base)

			seedStaleNotes(t, c, "testuser", n, n10NoteChars)

			// Kick off the search that re-indexes the whole backlog.
			searchDone := make(chan struct{})
			var searchDur time.Duration
			var searchErr error
			go func() {
				searchDur, searchErr = searchNotes(context.Background(), c, "reconcile")
				close(searchDone)
			}()

			// Write throughout it.
			var during []time.Duration
			var writeErr error
		loop:
			for {
				select {
				case <-searchDone:
					break loop
				default:
				}
				lat, err := postNote(c)
				if err != nil {
					writeErr = err
					break loop
				}
				during = append(during, lat)
			}
			<-searchDone
			require.NoError(t, searchErr)
			require.NoError(t, writeErr)

			dm, dp, dx := latencyStats(during)
			t.Logf("stale=%d | resync+search wall=%s | baseline POST n=%d median=%s p95=%s max=%s | during-resync POST n=%d median=%s p95=%s max=%s | max_during/backlog=%.2f",
				n, searchDur, len(base), bm, bp, bx, len(during), dm, dp, dx,
				float64(dx)/float64(searchDur))
		})
	}
}

// TestHandler_Notes_Create_IsNotQueuedBehindResync is the ticket's named check
// for N10: a note creation issued while a search re-indexes a backlog must
// complete, and must not wait for the backlog to finish.
//
// The measurement that fails on 26488a6 lives at the repository layer
// (TestSearchRepo_StaleBacklog_BatchedSyncBeatPerNoteTransactions), because that
// is where batching changed the cost of the re-sync. What this test adds is the
// end-to-end shape the ticket describes — the re-sync running through the real
// router while ordinary writes are served — and it is written so a slow machine
// moves both numbers together rather than tripping the assertion.
//
// Skipped under -short: it is a timing test, and `go test ./...` is where the
// gate runs it.
func TestHandler_Notes_Create_IsNotQueuedBehindResync(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test: skipped under -short")
	}
	const (
		backlogNotes = 3000
		backlogChars = 200
		baselineN    = 20
	)

	c := newTestClient(t)
	c.registerAndLogin(t)

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
		lat, err := postNote(c)
		require.NoError(t, err)
		during = append(during, lat)
	}
	<-searchDone
	require.NoError(t, searchErr)
	require.GreaterOrEqual(t, len(during), 10,
		"the re-sync finished before ten writes were issued during it; the backlog is too small to measure")

	duringMedian, duringP95, duringMax := latencyStats(during)
	t.Logf("baseline write median=%s | re-sync of %d stale notes took %s (%.2fms/note) | writes during it: n=%d median=%s p95=%s max=%s | max/backlog=%.3f",
		baseMedian, backlogNotes, searchDur, float64(searchDur/backlogNotes)/float64(time.Millisecond),
		len(during), duringMedian, duringP95, duringMax,
		float64(duringMax)/float64(searchDur))

	// The writes were served while the re-sync ran — none of them queued behind
	// it. A quarter of the backlog is a generous line: before batching, the
	// worst write during a 3,000-note re-sync was 0.6% of its duration, so this
	// clause was already true and must stay true.
	assert.Less(t, duringMax, searchDur/4,
		"a write took %s while the re-sync ran for %s — it was queued behind the backlog", duringMax, searchDur)
}
