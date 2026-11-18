package handler_test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/security"
)

// The anonymous share surfaces had no throttling at all before this suite: the
// security review of #85 measured 10,686 unauthenticated requests in 20 seconds
// against one link, zero 429s. The risk is not token guessing (the tokens are
// 128-bit crypto/rand) — it is load on the same process that serves the
// authenticated app, on hardware a self-hoster actually runs.
//
// Three properties are the point here, and only the first is a status code:
//
//   - a peer cannot exceed its budget on either prefix, and an honest reader
//     never notices the limiter exists;
//   - the refusal is the surface's uniform empty 404 — byte for byte what an
//     unknown token gets — because a 429 (or a Retry-After) would hand the holder
//     of a burned URL a token-validity oracle, which is the one thing this
//     surface is designed not to be;
//   - the budget is per (IP, token), so a NAT neighbour and a second link are
//     both unaffected by one noisy reader.

// ─── fixture ─────────────────────────────────────────────────────────────────

// shareThrottleFixture is one instance with tight share budgets and two live
// capabilities: a folder link (with a subfolder and two notes under it) and a
// note link on the /s/ prefix.
type shareThrottleFixture struct {
	client *testClient
	// folderURL is /f/<token>, the shared folder's root listing.
	folderURL string
	// noteURL is /f/<folder token>/n/design/colour-notes, a note read inside the
	// shared folder.
	noteURL string
	// noteToken is the note's own /s/ capability, and notePath is its URL.
	noteToken string
	notePath  string
}

func newShareThrottleFixture(t *testing.T, limits security.ShareRateLimits) *shareThrottleFixture {
	return newShareThrottleFixtureWithProxy(t, limits, nil)
}

// newShareThrottleFixtureWithProxy also configures a trusted proxy, so a test can
// address the instance as several distinct clients through X-Forwarded-For.
func newShareThrottleFixtureWithProxy(t *testing.T, limits security.ShareRateLimits, trustedProxy *net.IPNet) *shareThrottleFixture {
	t.Helper()
	c := newTestClientWithOptions(t, true, limits, trustedProxy)
	isolationLogin(t, c, "alice")

	rootID := isolationCreateFolder(t, c, "handbook")
	subID := createSubfolder(t, c, "design", rootID)
	isolationCreateNote(t, c, &rootID, "Delivery plan")
	noteID := isolationCreateNote(t, c, &subID, "Colour notes")

	folderToken := shareFolder(t, c, rootID, nil)

	resp := c.do(t, http.MethodPost, "/api/v1/notes/"+i64str(noteID)+"/share", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var shared map[string]any
	decodeInto(t, resp, &shared)
	noteToken, ok := shared["share_token"].(string)
	require.True(t, ok, "sharing a note must return a token")
	require.NotEmpty(t, noteToken)

	return &shareThrottleFixture{
		client:    c,
		folderURL: "/f/" + folderToken,
		noteURL:   "/f/" + folderToken + "/n/design/colour-notes",
		noteToken: noteToken,
		notePath:  "/s/" + noteToken,
	}
}

// shareRead is one anonymous response, kept whole so a test can compare a
// throttled refusal with an unknown-token refusal rather than with a status
// constant.
type shareRead struct {
	status int
	header http.Header
	body   string
}

// readShareSurface performs one anonymous read and refuses, on the spot, any
// response a reader could use as a throttle oracle: the surface must never
// answer 429, and must never send Retry-After.
func readShareSurface(t *testing.T, c *testClient, path string) shareRead {
	t.Helper()
	return readShareSurfaceFrom(t, c, path, "")
}

// readShareSurfaceFrom is readShareSurface as one client behind a trusted proxy:
// xff is the address the proxy reports, which is what the limiters key on when a
// trusted-proxy CIDR is configured.
func readShareSurfaceFrom(t *testing.T, c *testClient, path, xff string) shareRead {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, c.server.URL+path, nil)
	require.NoError(t, err)
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.NotEqual(t, http.StatusTooManyRequests, resp.StatusCode,
		"%s answered 429: a status only a throttled read carries is a token-validity oracle", path)
	require.Empty(t, resp.Header.Get("Retry-After"),
		"%s sent Retry-After: same oracle, one header down", path)

	return shareRead{status: resp.StatusCode, header: resp.Header.Clone(), body: string(raw)}
}

// headersWithoutDate is the response's header set minus the one header the
// server generates per response, so two refusals can be compared as equal.
func headersWithoutDate(h http.Header) http.Header {
	out := h.Clone()
	out.Del("Date")
	return out
}

// ─── an honest reader never notices ──────────────────────────────────────────

// TestShareThrottle_HonestReaderIsUnaffected: the limiter's budget must be sized
// for what a human does with a folder link — open it, walk into a subfolder,
// open a note, come back — with room to spare. A throttle that refuses an honest
// reader is a bug even though it is not a security bug.
func TestShareThrottle_HonestReaderIsUnaffected(t *testing.T) {
	f := newShareThrottleFixture(t, security.ShareRateLimits{PerMinute: 20, PerHour: 100})

	for _, path := range []string{
		f.folderURL,
		f.folderURL + "/d/design",
		f.folderURL + "/n/delivery-plan",
		f.noteURL,
		f.folderURL,
	} {
		read := readShareSurface(t, f.client, path)
		assert.Equal(t, http.StatusOK, read.status, "%s must be served to a reader inside the budget", path)
		assert.NotEmpty(t, read.body)
	}

	// The same listing read as a burst, still well inside the budget: no
	// unexpected refusals at all.
	for i := 0; i < 10; i++ {
		read := readShareSurface(t, f.client, f.folderURL)
		assert.Equal(t, http.StatusOK, read.status, "burst read %d must be inside the budget", i+1)
	}

	// The note sharing the same client IP is a different link, so it has its own
	// untouched budget.
	read := readShareSurface(t, f.client, f.notePath)
	assert.Equal(t, http.StatusOK, read.status)
}

// ─── a single peer is capped on both prefixes ────────────────────────────────

// TestShareThrottle_FolderLinkRefusesAtTheBudget: /f/:token stops at the
// configured rate. Every refusal after the budget is the surface's 404, never a
// 429 (readShareSurface fails the test on a 429).
func TestShareThrottle_FolderLinkRefusesAtTheBudget(t *testing.T) {
	const budget = 5
	f := newShareThrottleFixture(t, security.ShareRateLimits{PerMinute: budget, PerHour: 1000})

	for i := 0; i < budget; i++ {
		read := readShareSurface(t, f.client, f.folderURL)
		require.Equal(t, http.StatusOK, read.status, "read %d of %d must be served", i+1, budget)
	}

	for i := 0; i < 5; i++ {
		read := readShareSurface(t, f.client, f.folderURL)
		assert.Equal(t, http.StatusNotFound, read.status,
			"read %d over the budget must be refused", budget+i+1)
		assert.Empty(t, read.body, "a refusal carries no body")
	}
}

// TestShareThrottle_NoteLinkRefusesAtTheBudget does the same on /s/:token, so the
// note prefix cannot be the unthrottled half of the surface.
func TestShareThrottle_NoteLinkRefusesAtTheBudget(t *testing.T) {
	const budget = 4
	f := newShareThrottleFixture(t, security.ShareRateLimits{PerMinute: budget, PerHour: 1000})

	for i := 0; i < budget; i++ {
		read := readShareSurface(t, f.client, f.notePath)
		require.Equal(t, http.StatusOK, read.status, "read %d of %d must be served", i+1, budget)
	}

	read := readShareSurface(t, f.client, f.notePath)
	assert.Equal(t, http.StatusNotFound, read.status,
		"the read after the budget must be refused on the note prefix too")
	assert.Empty(t, read.body)
}

// TestShareThrottle_ConcurrentBurstCannotExceedTheBudget is the review's burst
// test as an assertion rather than a measurement: many concurrent readers of one
// link, and exactly one budget's worth of 200s comes out.
func TestShareThrottle_ConcurrentBurstCannotExceedTheBudget(t *testing.T) {
	const (
		budget     = 5
		goroutines = 20
		readsEach  = 5
		totalReads = goroutines * readsEach
	)
	f := newShareThrottleFixture(t, security.ShareRateLimits{PerMinute: budget, PerHour: 1000})

	var (
		mu       sync.Mutex
		statuses = map[int]int{}
	)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < readsEach; j++ {
				resp, err := http.Get(f.client.server.URL + f.folderURL)
				if err != nil {
					t.Errorf("burst read: %v", err)
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()

				mu.Lock()
				statuses[resp.StatusCode]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	require.Equal(t, totalReads, statuses[http.StatusOK]+statuses[http.StatusNotFound],
		"every read must be either served or refused, got %v", statuses)
	assert.Equal(t, budget, statuses[http.StatusOK],
		"a burst of %d concurrent reads must yield exactly one budget of %d, got %v",
		totalReads, budget, statuses)
	assert.Zero(t, statuses[http.StatusTooManyRequests], "the surface must never answer 429, got %v", statuses)
	for status, n := range statuses {
		assert.Less(t, status, 500, "a throttled read must not become a server error (%d x %d)", status, n)
	}
}

// TestShareThrottle_SecondIPOnTheSameLinkHasItsOwnBudget is the other half of the
// key: the budget belongs to a reader on a link, so a second client reading the
// same link is not spending the first client's allowance — and, crucially, is not
// locked out by it. Both directions matter: keying on the token alone would let
// one peer starve every other reader of the link it is hammering.
//
// The instance is configured with the loopback range as its trusted proxy, so
// X-Forwarded-For is believed and the two "peers" are two addresses.
func TestShareThrottle_SecondIPOnTheSameLinkHasItsOwnBudget(t *testing.T) {
	_, proxyNet, err := net.ParseCIDR("127.0.0.0/8")
	require.NoError(t, err)
	const budget = 2
	f := newShareThrottleFixtureWithProxy(t, security.ShareRateLimits{PerMinute: budget, PerHour: 1000}, proxyNet)

	const peerA, peerB = "203.0.113.50", "203.0.113.51"

	for i := 0; i < budget; i++ {
		require.Equal(t, http.StatusOK, readShareSurfaceFrom(t, f.client, f.folderURL, peerA).status,
			"peer A read %d must be inside its own budget", i+1)
	}
	require.Equal(t, http.StatusNotFound, readShareSurfaceFrom(t, f.client, f.folderURL, peerA).status,
		"peer A must be refused once its budget is spent")

	for i := 0; i < budget; i++ {
		assert.Equal(t, http.StatusOK, readShareSurfaceFrom(t, f.client, f.folderURL, peerB).status,
			"peer B read %d on the same link must not be throttled by peer A", i+1)
	}
}

// ─── the refusal is the unknown-token 404, byte for byte ─────────────────────

// TestShareThrottle_RefusalIsTheUnknownToken404 is the security property, not a
// style preference. If a throttled read differed from an unknown-token read in
// status, headers or body, the difference itself would confirm that the token was
// real — which is exactly what an expired or burned link must never reveal.
func TestShareThrottle_RefusalIsTheUnknownToken404(t *testing.T) {
	f := newShareThrottleFixture(t, security.ShareRateLimits{PerMinute: 1, PerHour: 1000})

	first := readShareSurface(t, f.client, f.notePath)
	require.Equal(t, http.StatusOK, first.status, "the fixture's first read must be served")

	throttled := readShareSurface(t, f.client, f.notePath)
	require.Equal(t, http.StatusNotFound, throttled.status,
		"the second read of a one-read link must be refused")

	unknown := readShareSurface(t, f.client, "/f/"+strings.Repeat("0", 32))
	require.Equal(t, http.StatusNotFound, unknown.status, "the control token must be unknown")

	assert.Equal(t, unknown.status, throttled.status)
	assert.Empty(t, throttled.body, "a refusal carries no body")
	assert.Equal(t, unknown.body, throttled.body, "the throttled body must equal the unknown-token body")
	assert.Equal(t, headersWithoutDate(unknown.header), headersWithoutDate(throttled.header),
		"a throttled read must carry exactly the headers an unknown token carries")
	assert.Equal(t, "no-store", throttled.header.Get("Cache-Control"))
	assert.Equal(t, "noindex", throttled.header.Get("X-Robots-Tag"))
}

// ─── one limiter, one set of counters, two prefixes ──────────────────────────

// TestShareThrottle_PrefixesShareOneBudget pins "share the limiter and the
// counters": both prefixes are one surface, so reads under the same token spend
// the same budget whichever route served them. Two buckets would be two half-
// throttled surfaces, which is the defect the card opens with.
func TestShareThrottle_PrefixesShareOneBudget(t *testing.T) {
	const budget = 4
	f := newShareThrottleFixture(t, security.ShareRateLimits{PerMinute: budget, PerHour: 1000})

	// Spend three reads of the note's token on the folder prefix. A note token is
	// not a folder link, so each is the handler's 404 — but each is a read of the
	// same capability and must cost the same budget.
	for i := 0; i < budget-1; i++ {
		read := readShareSurface(t, f.client, "/f/"+f.noteToken)
		require.Equal(t, http.StatusNotFound, read.status,
			"a note token on the folder prefix is the handler's 404, read %d", i+1)
	}

	// The fourth read, on the note prefix, is the last one inside the budget: the
	// positive control that the token is live and the budget was not halved.
	inBudget := readShareSurface(t, f.client, f.notePath)
	require.Equal(t, http.StatusOK, inBudget.status,
		"the token's fourth read must still be served — the budget is shared, not split")

	// The fifth read is over budget on the note prefix even though the token is
	// valid: the folder-prefix reads spent it.
	overBudget := readShareSurface(t, f.client, f.notePath)
	assert.Equal(t, http.StatusNotFound, overBudget.status,
		"reads on /f/<token> must spend the same budget as reads on /s/<token>")

	// A different capability is untouched, so the refusal above is a budget and
	// not a global stop.
	other := readShareSurface(t, f.client, f.folderURL)
	assert.Equal(t, http.StatusOK, other.status, "another link must keep its own budget")
}

// TestShareThrottle_NATNeighboursDoNotShareABudget is the NAT/CGNAT case: two
// people behind one address, two links, and neither is throttled by the other's
// traffic. Keying on the IP alone would close a whole office's links because one
// person opened a shared folder in a loop.
//
// Both peers reach this harness from 127.0.0.1, which is exactly that shape. The
// mirror case — one link, two IPs, two budgets — is
// TestShareRateLimiter_KeysOnIPAndToken in internal/security, where the key can
// be set directly.
func TestShareThrottle_NATNeighboursDoNotShareABudget(t *testing.T) {
	const budget = 3
	f := newShareThrottleFixture(t, security.ShareRateLimits{PerMinute: budget, PerHour: 1000})

	for i := 0; i < budget; i++ {
		require.Equal(t, http.StatusOK, readShareSurface(t, f.client, f.folderURL).status,
			"peer A read %d must be inside its budget", i+1)
	}
	require.Equal(t, http.StatusNotFound, readShareSurface(t, f.client, f.folderURL).status,
		"peer A's link must be refused once its own budget is spent")

	for i := 0; i < budget; i++ {
		read := readShareSurface(t, f.client, f.notePath)
		assert.Equal(t, http.StatusOK, read.status,
			"peer B read %d must not be throttled by peer A's traffic on another link", i+1)
	}

	// The authenticated app on the same process is not behind this middleware at
	// all: a link flood must not lock its owner out of their own account.
	resp := f.client.do(t, http.MethodGet, "/api/v1/shares", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"throttling a public link must not throttle the authenticated API")
}

// ─── the "More" walk is bounded by the same bucket ───────────────────────────

// TestShareThrottle_CursorWalkCannotExceedTheBudget: a page of 200 entries is
// ~16 KiB, so a paging loop is a bandwidth amplifier unless each page is just
// another read of the same link. It is — same route, same token, same bucket —
// and this pins it, because the cheapest way to "fix" paging later would be a
// second route with its own budget.
func TestShareThrottle_CursorWalkCannotExceedTheBudget(t *testing.T) {
	const budget = 3
	f := newShareThrottleFixture(t, security.ShareRateLimits{PerMinute: budget, PerHour: 1000})

	require.Equal(t, http.StatusOK, readShareSurface(t, f.client, f.folderURL).status)

	// Two cursor reads spend the rest of the budget. They are the handler's 404
	// (the cursor does not exist in this fixture) but the budget is the
	// middleware's, and it does not look at the query string.
	for i := 0; i < budget-1; i++ {
		readShareSurface(t, f.client, fmt.Sprintf("%s?na=%d", f.folderURL, i))
	}

	over := readShareSurface(t, f.client, f.folderURL+"?na=99")
	assert.Equal(t, http.StatusNotFound, over.status,
		"a paging read must be refused once the budget is spent")

	// The control: the same first-page read on a fresh instance is served, so the
	// refusal above came from the cursor reads rather than from a broken fixture.
	fresh := newShareThrottleFixture(t, security.ShareRateLimits{PerMinute: budget, PerHour: 1000})
	assert.Equal(t, http.StatusOK, readShareSurface(t, fresh.client, fresh.folderURL).status)
}
