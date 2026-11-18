package security

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Read budgets for the anonymous share surfaces, per (client IP, token).
//
// Why these routes need their own limiter rather than the auth one: a share URL
// is a bearer capability read with no session, and a folder link is a page a
// human browses — dozens of reads in a minute, not ten in fifteen. The auth
// bucket (10 per 15 minutes, see AuthRateLimiter) would close a shared folder on
// its first visit, so it is unusable here and must not be reused.
//
// The failure this guards against is not token guessing (128-bit crypto/rand
// tokens are not guessable); it is unauthenticated load on the same process that
// serves the authenticated app, and one peer saturating a self-hoster's box
// through a link they were legitimately given.
const (
	// DefaultShareRatePerMinute is the short window: a burst of page loads, a
	// handful of notes, the "More" pages behind them.
	DefaultShareRatePerMinute = 60
	// DefaultShareRatePerHour is the long window. It is what stops a sustained
	// trickle that stays under the per-minute cap from being unbounded load:
	// 60/min sustained would be 3,600 reads an hour, so the hourly ceiling — not
	// the per-minute one — is the real bound on one peer's cost.
	DefaultShareRatePerHour = 600

	// shareRateMaxEntries bounds the limiter's own memory. Entries are created
	// by unauthenticated requests, one per (IP, token), so without a ceiling a
	// flood of random tokens would be a memory-exhaustion vector — a worse bug
	// than the one this limiter fixes.
	shareRateMaxEntries = 4096
	// shareRateSweepInterval and shareRateEntryTTL: an entry idle for longer
	// than the longer window can be dropped without changing any decision.
	shareRateSweepInterval = 10 * time.Minute
	shareRateEntryTTL      = time.Hour
)

// Budget names reported by Allow when a read is refused.
const (
	ShareBudgetMinute = "per-minute"
	ShareBudgetHour   = "per-hour"
)

// ShareRateLimits is one share link's read budget for one client IP. A
// non-positive value disables that window; both non-positive disables the
// limiter, which is the operator's explicit opt-out (a deployment whose own
// front door already does this) and never an accident of the defaults.
type ShareRateLimits struct {
	PerMinute int
	PerHour   int
}

// Enabled reports whether any window is enforced.
func (l ShareRateLimits) Enabled() bool { return l.PerMinute > 0 || l.PerHour > 0 }

// shareRateKey is the bucket identity: one reader on one link.
//
// IP alone is wrong in both directions: a single hot link could drown every
// other link the same reader holds, and a NAT/CGNAT peer would spend an office's
// budget. Token alone is worse — a random token per request is one bucket per
// request.
type shareRateKey struct {
	ip    string
	token string
}

// shareRateWindow is a fixed-window counter per budget, which makes the decision
// trivially explainable ("60 reads since this minute began") and testable
// without sleeping. The cost is a boundary effect: a reader can spend its minute
// budget at the end of one window and again at the start of the next. For a
// load guard that is acceptable; for a billing quota it would not be.
type shareRateWindow struct {
	minuteStart time.Time
	minuteCount int
	hourStart   time.Time
	hourCount   int
	lastSeen    time.Time
}

// ShareRateLimiter throttles the anonymous share surfaces per (client IP, token).
//
// It is the read-surface sibling of AuthRateLimiter and deliberately separate:
// different key, different budgets, different refusal. AuthRateLimiter answers
// 429 with a Retry-After; this one must never do that (see the handler that
// renders its refusal), because the share surface's whole design is that every
// refusal is indistinguishable from "no such link".
type ShareRateLimiter struct {
	limits       ShareRateLimits
	trustedProxy *net.IPNet

	// Overridable for tests; the sweep is otherwise time-driven.
	maxEntries int
	sweepEvery time.Duration
	entryTTL   time.Duration
	now        func() time.Time

	mu      sync.Mutex
	entries map[shareRateKey]*shareRateWindow

	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewShareRateLimiter builds a limiter for the given budgets. trustedProxy is
// the same CIDR the auth limiter uses: X-Forwarded-For is only believed when the
// request came from inside it, so a client cannot choose its own bucket by
// inventing a header.
func NewShareRateLimiter(trustedProxy *net.IPNet, limits ShareRateLimits) *ShareRateLimiter {
	rl := &ShareRateLimiter{
		limits:       limits,
		trustedProxy: trustedProxy,
		maxEntries:   shareRateMaxEntries,
		sweepEvery:   shareRateSweepInterval,
		entryTTL:     shareRateEntryTTL,
		now:          time.Now,
		entries:      make(map[shareRateKey]*shareRateWindow),
		stopCh:       make(chan struct{}),
	}
	if limits.Enabled() {
		go rl.sweepLoop()
	}
	return rl
}

// Stop terminates the background sweep. It is safe to call more than once, and
// safe on a limiter that never started one.
func (rl *ShareRateLimiter) Stop() {
	rl.stopOnce.Do(func() { close(rl.stopCh) })
}

// Enabled reports whether this limiter enforces anything. A nil limiter
// enforces nothing, so a caller that failed to wire one cannot throttle by
// accident either.
func (rl *ShareRateLimiter) Enabled() bool {
	return rl != nil && rl.limits.Enabled()
}

// IP returns the client IP a request is keyed on, honouring the trusted-proxy
// CIDR exactly as the auth limiter does.
func (rl *ShareRateLimiter) IP(r *http.Request) string {
	if rl == nil {
		return ""
	}
	return clientIP(r, rl.trustedProxy)
}

// Allow reports whether one more read of token's link is within budget for
// client ip. When it is not, the second return names the window that refused it
// (ShareBudgetMinute or ShareBudgetHour) so the refusal can be logged with a
// reason; when it is, the second return is "".
//
// Tokens are compared case-insensitively: the URL is case-sensitive but the link
// a reader holds is not, and without folding, /f/<TOKEN> would be a second,
// unspent bucket for the same link.
func (rl *ShareRateLimiter) Allow(ip, token string) (bool, string) {
	if !rl.Enabled() {
		return true, ""
	}

	key := shareRateKey{ip: ip, token: strings.ToLower(token)}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	w, ok := rl.entries[key]
	if !ok {
		if len(rl.entries) >= rl.maxEntries {
			rl.evictLocked(now)
		}
		w = &shareRateWindow{minuteStart: now, hourStart: now}
		rl.entries[key] = w
	}

	if now.Sub(w.minuteStart) >= time.Minute {
		w.minuteStart, w.minuteCount = now, 0
	}
	if now.Sub(w.hourStart) >= time.Hour {
		w.hourStart, w.hourCount = now, 0
	}
	w.lastSeen = now

	if rl.limits.PerMinute > 0 && w.minuteCount >= rl.limits.PerMinute {
		return false, ShareBudgetMinute
	}
	if rl.limits.PerHour > 0 && w.hourCount >= rl.limits.PerHour {
		return false, ShareBudgetHour
	}

	w.minuteCount++
	w.hourCount++
	return true, ""
}

// evictLocked drops stale entries, then — if the map is still full — drops live
// ones at random until there is headroom. The second step only runs when entries
// are arriving faster than the TTL can reclaim them, which is what a flood of
// random tokens looks like. Random eviction is a cheap approximation of LRU and
// costs nothing on the request path; the price is that under such a flood an
// honest reader's counter can reset, which grants a read, never a refusal.
func (rl *ShareRateLimiter) evictLocked(now time.Time) {
	cutoff := now.Add(-rl.entryTTL)
	for key, w := range rl.entries {
		if w.lastSeen.Before(cutoff) {
			delete(rl.entries, key)
		}
	}

	target := rl.maxEntries * 3 / 4
	for key := range rl.entries {
		if len(rl.entries) <= target {
			return
		}
		delete(rl.entries, key)
	}
}

// sweep drops entries that have been idle for longer than the longer window.
func (rl *ShareRateLimiter) sweep() {
	now := rl.now()
	cutoff := now.Add(-rl.entryTTL)

	rl.mu.Lock()
	defer rl.mu.Unlock()
	for key, w := range rl.entries {
		if w.lastSeen.Before(cutoff) {
			delete(rl.entries, key)
		}
	}
}

func (rl *ShareRateLimiter) sweepLoop() {
	ticker := time.NewTicker(rl.sweepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-rl.stopCh:
			return
		case <-ticker.C:
			rl.sweep()
		}
	}
}

// size reports how many buckets are held, for tests that pin the memory bound.
func (rl *ShareRateLimiter) size() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return len(rl.entries)
}

// ShareTokenHandle renders a share token as the short, stable handle the access
// log uses: "t_" plus eight hex characters of the token's sha256, computed over
// the lowercased token so the handle does not change with the case of the URL.
//
// It is the same handle redactLogPath writes, on purpose: an operator counting a
// throttled link in the access log and in the throttle log must be counting the
// same thing, and neither log may carry a replayable capability.
func ShareTokenHandle(token string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(token)))
	return "t_" + hex.EncodeToString(sum[:4])
}
