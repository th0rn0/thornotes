package hub_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/hub"
)

func TestHub_SubscribeAndNotify(t *testing.T) {
	h := hub.New()

	ch, unsub := h.Subscribe(1)
	defer unsub()

	h.Notify(1, "notes_changed")

	select {
	case event := <-ch:
		assert.Equal(t, "notes_changed", event)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected event within 100ms")
	}
}

func TestHub_NotifyNoSubscribers(t *testing.T) {
	h := hub.New()
	// Should not panic when there are no subscribers.
	h.Notify(99, "notes_changed")
}

func TestHub_NotifyDifferentUser(t *testing.T) {
	h := hub.New()

	ch, unsub := h.Subscribe(1)
	defer unsub()

	// Notify a different user — channel should stay empty.
	h.Notify(2, "notes_changed")

	select {
	case <-ch:
		t.Fatal("should not receive event for different user")
	case <-time.After(50 * time.Millisecond):
		// Correct: nothing received.
	}
}

func TestHub_MultipleSubscribers(t *testing.T) {
	h := hub.New()

	ch1, unsub1 := h.Subscribe(1)
	defer unsub1()
	ch2, unsub2 := h.Subscribe(1)
	defer unsub2()

	h.Notify(1, "notes_changed")

	for _, ch := range []chan string{ch1, ch2} {
		select {
		case event := <-ch:
			assert.Equal(t, "notes_changed", event)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("expected event on both subscribers")
		}
	}
}

func TestHub_Unsubscribe(t *testing.T) {
	h := hub.New()

	ch, unsub := h.Subscribe(1)
	unsub() // unsubscribe immediately

	// Channel should be closed after unsub.
	select {
	case _, ok := <-ch:
		require.False(t, ok, "channel should be closed after unsub")
	case <-time.After(50 * time.Millisecond):
		t.Fatal("expected closed channel")
	}

	// Notify after unsub should not panic.
	h.Notify(1, "notes_changed")
}

func TestHub_SlowConsumerDropped(t *testing.T) {
	h := hub.New()

	ch, unsub := h.Subscribe(1)
	defer unsub()

	// Fill the buffer (size 4) and then one more — should not block.
	for i := 0; i < 10; i++ {
		h.Notify(1, "notes_changed")
	}

	// Should have received up to buffer size worth of events.
	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	assert.LessOrEqual(t, count, 4, "buffer size is 4, can't have more")
	assert.Greater(t, count, 0, "should have received at least one event")
}

func TestHub_MaxStreamsPerUser_EvictsStalest(t *testing.T) {
	h := hub.New()

	// Subscribe 8 times with a slight pause so the first has the oldest
	// lastWrite timestamp.
	first, unsub1 := h.Subscribe(1)
	defer unsub1()
	time.Sleep(time.Millisecond)
	chans := []chan string{first}
	unsubs := []func(){unsub1}
	for i := 0; i < 7; i++ {
		ch, unsub := h.Subscribe(1)
		chans = append(chans, ch)
		unsubs = append(unsubs, unsub)
		defer unsub()
		time.Sleep(time.Millisecond)
	}
	require.Equal(t, hub.MaxStreamsPerUser, h.CountFor(1))

	// The 9th subscribe evicts the stalest (first) subscriber.
	ch9, unsub9 := h.Subscribe(1)
	defer unsub9()

	assert.Equal(t, hub.MaxStreamsPerUser, h.CountFor(1), "cap enforced")

	// The evicted channel was closed: its handler sees !ok and exits.
	select {
	case _, ok := <-first:
		require.False(t, ok, "evicted channel should be closed")
	case <-time.After(time.Second):
		t.Fatal("evicted channel not closed within 1s")
	}

	// Live subscribers still receive events.
	h.Notify(1, "notes_changed")
	select {
	case <-chans[1]:
	case <-ch9:
	case <-time.After(time.Second):
		t.Fatal("no live subscriber received event after eviction")
	}
}

// fakeClock is a manual clock for tests that assert on elapsed time. Reaping is
// a comparison between two instants, so the test should move the instant rather
// than wait for a real one: an assertion that a 20ms sleep ends inside a 50ms
// window is a claim about the scheduler, and on a loaded runner that claim is
// false (ticket 617).
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_700_000_000, 0)}
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

func TestHub_ReapStale(t *testing.T) {
	clock := newFakeClock()
	h := hub.NewWithClock(clock.Now)

	ch, unsub := h.Subscribe(1)

	// A subscriber that was just touched survives the sweep.
	h.Touch(ch)
	assert.Equal(t, 0, h.ReapStale(50*time.Millisecond))
	assert.Equal(t, 1, h.CountFor(1))

	// One idle beyond the threshold is dropped and its channel closed.
	clock.Advance(60 * time.Millisecond)
	assert.Equal(t, 1, h.ReapStale(50*time.Millisecond))
	assert.Equal(t, 0, h.Count())
	select {
	case _, ok := <-ch:
		require.False(t, ok, "reaped channel should be closed")
	default:
		t.Fatal("reaped channel should be closed")
	}

	// The handler's deferred unsub after a reap is a no-op, not a panic.
	assert.NotPanics(t, func() { unsub() })
	assert.Equal(t, 0, h.Count())

	// Notify must not panic against subscribers removed by the sweep.
	assert.NotPanics(t, func() { h.Notify(1, "notes_changed") })
}

func TestHub_TouchKeepsSubscriberAlive(t *testing.T) {
	clock := newFakeClock()
	h := hub.NewWithClock(clock.Now)
	ch, unsub := h.Subscribe(1)
	defer unsub()

	// Total age well past the threshold, but writes kept it fresh: a write
	// inside the window resets the idle measure, so the sweep must not reap.
	clock.Advance(40 * time.Millisecond)
	h.Touch(ch)
	clock.Advance(40 * time.Millisecond) // idle 40ms, inside the 50ms window
	assert.Equal(t, 0, h.ReapStale(50*time.Millisecond))
	assert.Equal(t, 1, h.Count())

	// Without another write the same stream crosses the window and is reaped.
	clock.Advance(60 * time.Millisecond) // idle 100ms
	assert.Equal(t, 1, h.ReapStale(50*time.Millisecond))
	assert.Equal(t, 0, h.Count())
}

func TestHub_CountAcrossUsers(t *testing.T) {
	h := hub.New()
	_, unsub1 := h.Subscribe(1)
	_, unsub1b := h.Subscribe(1)
	_, unsub2 := h.Subscribe(2)
	defer unsub1()
	defer unsub1b()
	defer unsub2()

	assert.Equal(t, 2, h.CountFor(1))
	assert.Equal(t, 1, h.CountFor(2))
	assert.Equal(t, 3, h.Count())
}
