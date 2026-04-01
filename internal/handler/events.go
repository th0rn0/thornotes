package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
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
func (h *EventsHandler) Stream(c *gin.Context) {
	user := ginUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	// Verify the client accepts SSE (nice to have, not strictly required).
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming not supported"})
		return
	}

	// Send an initial connection-established comment so the client knows the
	// stream is live. SSE comments start with ":".
	fmt.Fprintf(c.Writer, ": connected\n\n")
	flusher.Flush()

	ch, unsub := h.hub.Subscribe(user.ID)
	defer unsub()

	keepalive := time.NewTicker(sseKeepaliveInterval)
	defer keepalive.Stop()

	ctx := c.Request.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(c.Writer, "event: %s\ndata: {}\n\n", event)
			flusher.Flush()
		case <-keepalive.C:
			// SSE comment as keepalive to prevent proxies from closing the connection.
			fmt.Fprintf(c.Writer, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
