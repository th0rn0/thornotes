package handler

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
	"github.com/th0rn0/thornotes/internal/hub"
)

// SSEKeepaliveInterval is the cadence of SSE keepalive comments on the
// /api/v1/events stream. The hub's stale-subscriber reaper and the stream's
// per-write deadline are both expressed in terms of this value (exported
// because cmd/thornotes wires the reaper with it).
const SSEKeepaliveInterval = 25 * time.Second

// sseWriteDeadline is the per-write deadline the SSE handler grants itself:
// 2× the keepalive interval. The server-level WriteTimeout (30 s) is a deadline
// on the whole response and cannot coexist with a stream that never finishes,
// so refreshing the deadline before every write is what keeps a healthy stream
// alive indefinitely while still bounding how long a dead client can tie up the
// connection: a write that cannot complete within the deadline returns an
// error and ends the handler.
const sseWriteDeadline = 2 * SSEKeepaliveInterval

// EventsHandler streams Server-Sent Events to authenticated browser sessions.
// Clients receive a "notes_changed" event whenever the disk watcher detects
// that one or more of their notes has changed on disk.
type EventsHandler struct {
	hub         *hub.Hub
	shutdownCtx context.Context // cancelled via srv.RegisterOnShutdown; never done when nil
}

func NewEventsHandler(h *hub.Hub, shutdown context.Context) *EventsHandler {
	if shutdown == nil {
		shutdown = context.Background()
	}
	return &EventsHandler{hub: h, shutdownCtx: shutdown}
}

// Stream handles GET /api/v1/events.
// The response is an infinite SSE stream; the connection is held open until the
// client disconnects, a write fails, or the server shuts down. Any write or
// flush error exits the handler, so a dead client stops being a hub subscriber
// within one keepalive instead of lingering on a dead connection.
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

	if _, ok := c.Writer.(http.Flusher); !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming not supported"})
		return
	}

	// NewResponseController unwraps gin's ResponseWriter (gin provides Unwrap()
	// since 1.10; this build is gin 1.12) down to the *http.response, so
	// SetWriteDeadline reaches the real connection. It can only fail with
	// http.ErrNotSupported, i.e. where there is no real connection
	// (e.g. httptest.ResponseRecorder); the write then proceeds undelimited
	// and the request-scoped context remains the only teardown signal.
	rc := http.NewResponseController(c.Writer)

	// write emits one SSE frame. Returns false when the connection is dead —
	// the caller's signal to give up on the stream.
	write := func(format string, args ...any) bool {
		if err := rc.SetWriteDeadline(time.Now().Add(sseWriteDeadline)); err != nil {
			log.Debug().Err(err).
				Int64("user_id", user.ID).
				Msg("sse: per-write deadline unavailable")
		}
		if _, err := fmt.Fprintf(c.Writer, format, args...); err != nil {
			log.Debug().Err(err).
				Int64("user_id", user.ID).
				Msg("sse: write failed, closing stream")
			return false
		}
		if err := rc.Flush(); err != nil {
			log.Debug().Err(err).
				Int64("user_id", user.ID).
				Msg("sse: flush failed, closing stream")
			return false
		}
		return true
	}

	// Send an initial connection-established comment so the client knows the
	// stream is live. SSE comments start with ":".
	if !write(": connected\n\n") {
		return
	}

	ch, unsub := h.hub.Subscribe(user.ID)
	defer unsub()

	keepalive := time.NewTicker(SSEKeepaliveInterval)
	defer keepalive.Stop()

	ctx := c.Request.Context()
	for {
		select {
		case <-ctx.Done():
			log.Debug().Int64("user_id", user.ID).Msg("sse: client disconnected")
			return
		case <-h.shutdownCtx.Done():
			// srv.RegisterOnShutdown cancels h.shutdownCtx before
			// srv.Shutdown's idle wait, so open streams release the
			// graceful-shutdown window immediately instead of holding it.
			log.Debug().Int64("user_id", user.ID).Msg("sse: server shutting down")
			return
		case event, ok := <-ch:
			if !ok {
				// Evicted by the per-user cap or reaped as stale.
				log.Debug().Int64("user_id", user.ID).Msg("sse: subscriber dropped by hub")
				return
			}
			if !write("event: %s\ndata: {}\n\n", event) {
				return
			}
		case <-keepalive.C:
			// SSE comment as keepalive to prevent proxies from closing the
			// connection. A successful write also refreshes the subscriber's
			// hub freshness so the reaper never kills a healthy stream.
			if !write(": keepalive\n\n") {
				return
			}
			h.hub.Touch(ch)
		}
	}
}
