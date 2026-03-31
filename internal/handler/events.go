package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/th0rn0/thornotes/internal/auth"
	"github.com/th0rn0/thornotes/internal/hub"
)

const sseKeepaliveInterval = 25 * time.Second

// EventsHandler streams Server-Sent Events to authenticated browser sessions.
// Clients receive a "notes_changed" event whenever the disk watcher detects
// that one or more of their notes has changed on disk.
type EventsHandler struct {
	hub *hub.Hub
}

func NewEventsHandler(h *hub.Hub) *EventsHandler {
	return &EventsHandler{hub: h}
}

// Stream handles GET /api/v1/events.
// The response is an infinite SSE stream; the connection is held open until the
// client disconnects or the server shuts down.
func (h *EventsHandler) Stream(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Verify the client accepts SSE (nice to have, not strictly required).
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Send an initial connection-established comment so the client knows the
	// stream is live. SSE comments start with ":".
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ch, unsub := h.hub.Subscribe(user.ID)
	defer unsub()

	keepalive := time.NewTicker(sseKeepaliveInterval)
	defer keepalive.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: {}\n\n", event)
			flusher.Flush()
		case <-keepalive.C:
			// SSE comment as keepalive to prevent proxies from closing the connection.
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
