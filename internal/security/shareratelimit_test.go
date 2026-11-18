package security

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock drives the limiter's windows so a test can cross a boundary without
// sleeping. Every decision the limiter makes is a function of time.Now, and a
// test that sleeps for a minute is a test nobody runs.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestShareLimiter(t *testing.T, limits ShareRateLimits) (*ShareRateLimiter, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	rl := NewShareRateLimiter(nil, limits)
	t.Cleanup(rl.Stop)
	rl.now = clock.Now
	return rl, clock
}

// TestShareRateLimiter_AllowsTheBurstThenRefuses pins the basic contract: the
// reads inside the budget are allowed and the first read over it is refused with
// the window that refused it named, so the refusal can be logged with a reason.
func TestShareRateLimiter_AllowsTheBurstThenRefuses(t *testing.T) {
	rl, _ := newTestShareLimiter(t, ShareRateLimits{PerMinute: 5, PerHour: 1000})

	for i := 0; i < 5; i++ {
		allowed, budget := rl.Allow("203.0.113.7", strings.Repeat("a", 32))
		assert.True(t, allowed, "read %d must be inside the budget", i+1)
		assert.Empty(t, budget)
	}

	allowed, budget := rl.Allow("203.0.113.7", strings.Repeat("a", 32))
	assert.False(t, allowed, "the sixth read in a five-read minute must be refused")
	assert.Equal(t, ShareBudgetMinute, budget, "the refusal must name the window that ran out")

	// A refused read does not consume budget: the next one is still refused, and
	// still for the same reason.
	allowed, budget = rl.Allow("203.0.113.7", strings.Repeat("a", 32))
	assert.False(t, allowed)
	assert.Equal(t, ShareBudgetMinute, budget)
}

// TestShareRateLimiter_MinuteWindowRollsOver is what makes the budget a rate and
// not a lifetime cap: the same reader gets its burst back when the minute ends.
func TestShareRateLimiter_MinuteWindowRollsOver(t *testing.T) {
	rl, clock := newTestShareLimiter(t, ShareRateLimits{PerMinute: 2, PerHour: 1000})
	token := strings.Repeat("b", 32)

	require.True(t, mustAllow(t, rl, "198.51.100.4", token))
	require.True(t, mustAllow(t, rl, "198.51.100.4", token))
	require.False(t, mustAllow(t, rl, "198.51.100.4", token))

	clock.Advance(59 * time.Second)
	assert.False(t, mustAllow(t, rl, "198.51.100.4", token),
		"59s into a 60s window the budget is still spent")

	clock.Advance(2 * time.Second)
	assert.True(t, mustAllow(t, rl, "198.51.100.4", token),
		"the burst must come back when the minute window rolls over")
}

// TestShareRateLimiter_HourlyCeilingBoundsASlowTrickle is the reason there are
// two windows: a per-minute cap alone lets a client sustain that rate forever,
// so the hourly ceiling is the real bound on what one peer can cost.
func TestShareRateLimiter_HourlyCeilingBoundsASlowTrickle(t *testing.T) {
	rl, clock := newTestShareLimiter(t, ShareRateLimits{PerMinute: 1000, PerHour: 6})
	token := strings.Repeat("c", 32)

	for i := 0; i < 6; i++ {
		require.True(t, mustAllow(t, rl, "198.51.100.5", token))
		clock.Advance(time.Minute) // a slow trickle that never trips the minute cap
	}

	allowed, budget := rl.Allow("198.51.100.5", token)
	assert.False(t, allowed, "6 reads an hour must be the ceiling even at one a minute")
	assert.Equal(t, ShareBudgetHour, budget)

	clock.Advance(30 * time.Minute)
	assert.False(t, mustAllow(t, rl, "198.51.100.5", token),
		"the hour is not over, so the hourly budget is still spent")

	clock.Advance(31 * time.Minute)
	assert.True(t, mustAllow(t, rl, "198.51.100.5", token),
		"the hourly budget must reset when the hour window rolls over")
}

// TestShareRateLimiter_KeysOnIPAndToken is the shape the card asks for, in both
// directions:
//
//   - two links read from one IP are separate budgets, so a NAT/CGNAT office is
//     not throttled by whichever link its noisiest neighbour opened;
//   - one link read from two IPs is two budgets, so a hot link cannot spend
//     somebody else's allowance.
func TestShareRateLimiter_KeysOnIPAndToken(t *testing.T) {
	rl, _ := newTestShareLimiter(t, ShareRateLimits{PerMinute: 2, PerHour: 1000})
	linkA := strings.Repeat("1", 32)
	linkB := strings.Repeat("2", 32)

	// One IP, link A: budget spent.
	require.True(t, mustAllow(t, rl, "203.0.113.10", linkA))
	require.True(t, mustAllow(t, rl, "203.0.113.10", linkA))
	require.False(t, mustAllow(t, rl, "203.0.113.10", linkA))

	// The same IP on link B still has its own full budget: this is the NAT case.
	assert.True(t, mustAllow(t, rl, "203.0.113.10", linkB),
		"a second link from the same IP must not inherit the first link's spent budget")
	assert.True(t, mustAllow(t, rl, "203.0.113.10", linkB))
	assert.False(t, mustAllow(t, rl, "203.0.113.10", linkB))

	// A different IP on link A has its own budget.
	assert.True(t, mustAllow(t, rl, "203.0.113.11", linkA),
		"a second peer reading the same link must get its own budget")
}

// TestShareRateLimiter_TokenCaseIsOneBucket: the URL is case-sensitive, the link
// is not. Folding the token means an upper-cased paste cannot buy a fresh
// bucket, which is exactly what a limiter is for.
func TestShareRateLimiter_TokenCaseIsOneBucket(t *testing.T) {
	rl, _ := newTestShareLimiter(t, ShareRateLimits{PerMinute: 2, PerHour: 1000})
	lower := strings.Repeat("ab", 16)

	require.True(t, mustAllow(t, rl, "203.0.113.12", lower))
	require.True(t, mustAllow(t, rl, "203.0.113.12", strings.ToUpper(lower)))
	assert.False(t, mustAllow(t, rl, "203.0.113.12", lower),
		"an upper-cased token is the same link and must share its budget")
}

// TestShareRateLimiter_DropsIdleEntries: a bucket nobody has touched for longer
// than the longer window can be dropped without changing any decision.
func TestShareRateLimiter_DropsIdleEntries(t *testing.T) {
	rl, clock := newTestShareLimiter(t, ShareRateLimits{PerMinute: 10, PerHour: 10})
	require.True(t, mustAllow(t, rl, "203.0.113.13", strings.Repeat("d", 32)))
	require.Equal(t, 1, rl.size())

	clock.Advance(61 * time.Minute)
	rl.sweep()

	assert.Equal(t, 0, rl.size(), "an entry idle past the longer window must be reclaimed")
}

// TestShareRateLimiter_BoundsMemory is the guard on the limiter itself: buckets
// are created by unauthenticated requests, so a flood of random tokens must not
// be able to grow the map without limit. That would be a worse bug than the
// unthrottled surface.
func TestShareRateLimiter_BoundsMemory(t *testing.T) {
	rl, _ := newTestShareLimiter(t, ShareRateLimits{PerMinute: 10, PerHour: 10})
	rl.maxEntries = 64

	for i := 0; i < 1000; i++ {
		// A fresh token per request, as an attacker sweeping the token space
		// would: every request is a new bucket.
		require.True(t, mustAllow(t, rl, "203.0.113.14", strings.Repeat("e", 28)+pad4(i)))
		require.LessOrEqual(t, rl.size(), rl.maxEntries,
			"the limiter must never hold more than its ceiling of buckets")
	}

	// The bound must not have cost the limiter its job: a live budget is still
	// enforced after the flood.
	for i := 0; i < 10; i++ {
		require.True(t, mustAllow(t, rl, "203.0.113.14", strings.Repeat("f", 32)))
	}
	assert.False(t, mustAllow(t, rl, "203.0.113.14", strings.Repeat("f", 32)),
		"evicting entries to stay in bounds must not disable the budget")
}

// TestShareRateLimiter_ConcurrentRequestsCannotExceedBudget is the burst test in
// miniature: many goroutines hitting one link at once must still be capped at
// the budget, and the race detector runs over this in -race.
func TestShareRateLimiter_ConcurrentRequestsCannotExceedBudget(t *testing.T) {
	const budget = 10
	rl, _ := newTestShareLimiter(t, ShareRateLimits{PerMinute: budget, PerHour: 1000})
	token := strings.Repeat("9", 32)

	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if ok, _ := rl.Allow("203.0.113.15", token); ok {
					allowed.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(budget), allowed.Load(),
		"500 concurrent reads of one link must yield exactly one minute's budget")
}

// TestShareRateLimiter_DisabledLetsEverythingThrough documents the opt-out: an
// operator who sets both windows to 0 gets the pre-ticket behaviour, not a
// limiter that refuses everything.
func TestShareRateLimiter_DisabledLetsEverythingThrough(t *testing.T) {
	rl, _ := newTestShareLimiter(t, ShareRateLimits{})
	require.False(t, rl.Enabled())

	for i := 0; i < 100; i++ {
		assert.True(t, mustAllow(t, rl, "203.0.113.16", strings.Repeat("a", 32)))
	}
	assert.Equal(t, 0, rl.size(), "a disabled limiter must not track anything at all")

	// A nil limiter behaves the same way, so a caller that failed to wire one
	// cannot panic on the request path.
	var none *ShareRateLimiter
	allowed, budget := none.Allow("203.0.113.16", strings.Repeat("a", 32))
	assert.True(t, allowed)
	assert.Empty(t, budget)
}

// TestShareRateLimiter_IPHonoursTrustedProxy keeps the share limiter's idea of
// "whose request is this" identical to the auth limiter's: X-Forwarded-For only
// when the request came from inside the trusted CIDR, so a client cannot pick its
// own bucket by inventing a header.
func TestShareRateLimiter_IPHonoursTrustedProxy(t *testing.T) {
	_, proxyNet, err := net.ParseCIDR("10.0.0.0/8")
	require.NoError(t, err)

	withProxy := NewShareRateLimiter(proxyNet, ShareRateLimits{PerMinute: 1, PerHour: 1})
	t.Cleanup(withProxy.Stop)
	withoutProxy := NewShareRateLimiter(nil, ShareRateLimits{PerMinute: 1, PerHour: 1})
	t.Cleanup(withoutProxy.Stop)

	request := func(remote, xff string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/f/"+strings.Repeat("a", 32), nil)
		req.RemoteAddr = remote
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		return req
	}

	assert.Equal(t, "203.0.113.20", withProxy.IP(request("10.1.2.3:4444", "203.0.113.20")),
		"a trusted proxy's X-Forwarded-For is believed")
	assert.Equal(t, "10.1.2.3", withProxy.IP(request("10.1.2.3:4444", "")),
		"with no forwarded header the peer address is the client")
	assert.Equal(t, "10.1.2.3", withoutProxy.IP(request("10.1.2.3:4444", "203.0.113.20")),
		"an untrusted peer cannot choose its own bucket with a header")
	assert.Equal(t, "203.0.113.21", withoutProxy.IP(request("203.0.113.21:4444", "")))
}

// TestShareTokenHandle pins the throttle log's handle: it must be stable, short,
// case-insensitive, and must not contain the token.
func TestShareTokenHandle(t *testing.T) {
	token := strings.Repeat("a1", 16)
	handle := ShareTokenHandle(token)

	assert.Regexp(t, `^t_[0-9a-f]{8}$`, handle)
	assert.Equal(t, handle, ShareTokenHandle(strings.ToUpper(token)),
		"the handle must not change with the case of the URL")
	assert.NotEqual(t, handle, ShareTokenHandle(strings.Repeat("b2", 16)))
	assert.NotContains(t, handle, token, "the handle must not carry the capability")
}

func mustAllow(t *testing.T, rl *ShareRateLimiter, ip, token string) bool {
	t.Helper()
	allowed, _ := rl.Allow(ip, token)
	return allowed
}

// pad4 renders i as exactly four digits, so a generated token stays the 32
// characters a real one is.
func pad4(i int) string {
	const digits = "0123456789"
	return string([]byte{
		digits[(i/1000)%10],
		digits[(i/100)%10],
		digits[(i/10)%10],
		digits[i%10],
	})
}
