package hub_test

import (
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
