// Package hub provides a lightweight per-user pub/sub channel for SSE notifications.
// Each connected browser tab subscribes with its user ID and receives string events.
// Notification is best-effort: slow consumers are dropped rather than blocked.
package hub

import (
	"sync"
	"time"
)

// MaxStreamsPerUser caps concurrent SSE subscribers for a single user.
// One open tab or preview window holds one stream (GET /api/v1/events), so 8
// covers a many-tab power user with headroom; a 9th subscriber evicts the
// stalest existing one instead of being refused, so a leaked-but-dead tab can
// never wedge new tabs out.
const MaxStreamsPerUser = 8

// Hub dispatches string events to all SSE subscribers for a given user.
type Hub struct {
	mu      sync.RWMutex
	clients map[int64][]*subscriber // userID → subscribers, oldest last-write first in slice order
	done    chan struct{}           // closed by the reaper's stop func
	// now is the clock every eviction decision reads. Production leaves it at
	// time.Now; tests replace it so reap assertions do not depend on how
	// promptly the scheduler wakes a sleeping goroutine. A nil value is treated
	// as time.Now so a zero-value Hub still works.
	now func() time.Time
}

// subscriber is one SSE stream's entry in the hub. lastWrite records the last
// successful write reported by the handler via Touch; the reaper and the
// per-user cap use it to evict streams that are (probably) dead.
type subscriber struct {
	ch        chan string
	closeOnce sync.Once // unsub, the cap eviction and the reaper may each try to close
	lastWrite time.Time
}

func New() *Hub {
	return &Hub{
		clients: make(map[int64][]*subscriber),
		done:    make(chan struct{}),
		now:     time.Now,
	}
}

// NewWithClock builds a Hub whose eviction decisions read the supplied clock
// instead of time.Now. It exists so tests can move time forward and assert on
// reaping without sleeping: a wall-clock test of "idle longer than N" fails on
// a loaded machine because the sleep that should end just inside the window
// overshoots it (ticket 617, reproduced at 144 failures in 150 runs with the
// runner oversubscribed).
func NewWithClock(now func() time.Time) *Hub {
	h := New()
	h.now = now
	return h
}

// clock is the Hub's source of time, tolerating a zero-value Hub.
func (h *Hub) clock() time.Time {
	if h.now == nil {
		return time.Now()
	}
	return h.now()
}

// Subscribe registers a new subscriber for userID and returns the event channel
// and an unsubscribe function the caller must invoke when the connection closes.
// The unsubscribe function is idempotent: it is safe after the subscriber was
// evicted by the per-user cap or reaped as stale.
func (h *Hub) Subscribe(userID int64) (chan string, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	s := &subscriber{ch: make(chan string, 4), lastWrite: h.clock()}
	if len(h.clients[userID]) >= MaxStreamsPerUser {
		h.evictStalestLocked(userID)
	}
	h.clients[userID] = append(h.clients[userID], s)

	return s.ch, func() { h.unsub(userID, s) }
}

// evictStalestLocked removes the subscriber for userID whose last successful
// write is furthest in the past and closes its channel, which makes its
// handler's `event, ok := <-ch` read return !ok so the handler exits and runs
// its (now idempotent) unsub.
//
// Caller must hold h.mu.
func (h *Hub) evictStalestLocked(userID int64) {
	list := h.clients[userID]
	if len(list) == 0 {
		return
	}
	stalest := 0
	for i, s := range list {
		if s.lastWrite.Before(list[stalest].lastWrite) {
			stalest = i
		}
	}
	dropped := list[stalest]
	h.clients[userID] = append(list[:stalest], list[stalest+1:]...)
	if len(h.clients[userID]) == 0 {
		delete(h.clients, userID)
	}
	dropped.close()
}

// unsub removes the subscriber from the map (if still present) and closes its
// channel exactly once. Callers take the write lock around the removal so a
// concurrent Notify (read lock) can never see a closed channel.
func (h *Hub) unsub(userID int64, s *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	existing := h.clients[userID]
	for i, x := range existing {
		if x == s {
			h.clients[userID] = append(existing[:i], existing[i+1:]...)
			break
		}
	}
	if len(h.clients[userID]) == 0 {
		delete(h.clients, userID)
	}
	s.close()
}

func (s *subscriber) close() {
	s.closeOnce.Do(func() { close(s.ch) })
}

// Touch marks the subscriber identified by ch as freshly written. The SSE
// handler calls it after every successful write; StartReaper drops
// subscribers idle for longer than its maxIdle argument.
func (h *Hub) Touch(ch chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, list := range h.clients {
		for _, s := range list {
			if s.ch == ch {
				s.lastWrite = h.clock()
				return
			}
		}
	}
}

// ReapStale closes and removes every subscriber whose last successful write is
// more than idle in the past. Returns the number reaped. The SSE handler is
// expected to unsubscribe itself on write errors, so a stale subscriber here
// is a leaked or wedged stream that outlived its handler — a safety net, not
// the primary teardown path.
func (h *Hub) ReapStale(idle time.Duration) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.clock()
	reaped := 0
	for userID, list := range h.clients {
		kept := list[:0]
		for _, s := range list {
			if now.Sub(s.lastWrite) > idle {
				s.close()
				reaped++
			} else {
				kept = append(kept, s)
			}
		}
		if len(kept) == 0 {
			delete(h.clients, userID)
		} else {
			h.clients[userID] = kept
		}
	}
	return reaped
}

// StartReaper launches a goroutine that sweeps for stale subscribers every
// `every` and drops those with no successful write in the last `maxIdle`
// (2 keepalive intervals). Returns a stop func; call it on shutdown.
func (h *Hub) StartReaper(every, maxIdle time.Duration) (stop func()) {
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-h.done:
				return
			case <-ticker.C:
				h.ReapStale(maxIdle)
			}
		}
	}()
	var stopOnce sync.Once
	return func() { stopOnce.Do(func() { close(h.done) }) }
}

// Count returns the total number of live subscribers. Test/diagnostic use.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for _, list := range h.clients {
		n += len(list)
	}
	return n
}

// CountFor returns the number of live subscribers for one user.
func (h *Hub) CountFor(userID int64) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients[userID])
}

// Notify sends event to every subscriber for userID.
// Sends are non-blocking: if a subscriber's buffer is full, the event is dropped
// for that subscriber only.
func (h *Hub) Notify(userID int64, event string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, s := range h.clients[userID] {
		select {
		case s.ch <- event:
		default:
		}
	}
}
