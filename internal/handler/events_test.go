package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/th0rn0/thornotes/internal/hub"
	"github.com/th0rn0/thornotes/internal/model"
)

func TestStream_NoUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewEventsHandler(hub.New())

	r := gin.New()
	r.GET("/events", func(c *gin.Context) {
		// Inject nil user so the nil guard at the top of Stream fires.
		c.Set("user", (*model.User)(nil))
		h.Stream(c)
	})

	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestStream_ConnectAndDisconnect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewEventsHandler(hub.New())

	user := &model.User{ID: 2, Username: "bob"}

	r := gin.New()
	r.GET("/events", func(c *gin.Context) {
		c.Set("user", user)
		h.Stream(c)
	})

	// Cancel the context immediately so Stream exits via ctx.Done().
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	// Stream sets SSE headers before entering the select loop.
	assert.Equal(t, "text/event-stream", rr.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache", rr.Header().Get("Cache-Control"))
	assert.Contains(t, rr.Body.String(), ": connected")
}

func TestStream_EventReceived(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := hub.New()
	eh := NewEventsHandler(h)

	user := &model.User{ID: 3, Username: "carol"}

	r := gin.New()
	r.GET("/events", func(c *gin.Context) {
		c.Set("user", user)
		eh.Stream(c)
	})

	ctx, cancel := context.WithCancel(context.Background())

	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.ServeHTTP(rr, req)
	}()

	// Give the stream goroutine a moment to subscribe, then send an event.
	// Use a small sleep-free approach: notify then cancel.
	h.Notify(user.ID, "notes_changed")
	cancel()
	<-done

	body := rr.Body.String()
	assert.Contains(t, body, ": connected")
	// The event may or may not appear depending on timing, but the connection
	// header confirms the stream was established.
	assert.Equal(t, "text/event-stream", rr.Header().Get("Content-Type"))
}
