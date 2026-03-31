// Package hub provides a lightweight per-user pub/sub channel for SSE notifications.
// Each connected browser tab subscribes with its user ID and receives string events.
// Notification is best-effort: slow consumers are dropped rather than blocked.
package hub

import "sync"

// Hub dispatches string events to all SSE subscribers for a given user.
type Hub struct {
	mu      sync.RWMutex
	clients map[int64][]chan string // userID → subscriber channels
}

func New() *Hub {
	return &Hub{clients: make(map[int64][]chan string)}
}

// Subscribe registers a new subscriber for userID and returns the event channel
// and an unsubscribe function the caller must invoke when the connection closes.
// The channel is buffered (size 4) so a single slow flush does not stall Notify.
func (h *Hub) Subscribe(userID int64) (chan string, func()) {
	ch := make(chan string, 4)
	h.mu.Lock()
	h.clients[userID] = append(h.clients[userID], ch)
	h.mu.Unlock()

	unsub := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		existing := h.clients[userID]
		for i, c := range existing {
			if c == ch {
				h.clients[userID] = append(existing[:i], existing[i+1:]...)
				break
			}
		}
		if len(h.clients[userID]) == 0 {
			delete(h.clients, userID)
		}
		close(ch)
	}
	return ch, unsub
}

// Notify sends event to every subscriber for userID.
// Sends are non-blocking: if a subscriber's buffer is full, the event is dropped
// for that subscriber only.
func (h *Hub) Notify(userID int64, event string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ch := range h.clients[userID] {
		select {
		case ch <- event:
		default:
		}
	}
}
