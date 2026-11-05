package handler

// Real-connection SSE tests. httptest.NewRecorder-based tests (events_test.go)
// exercise the guard paths, but the interesting deadline and teardown
// behaviour needs a real net.Conn under the gin ResponseWriter, so these run
// the handler through httptest.NewServer / http.Server.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/hub"
	"github.com/th0rn0/thornotes/internal/model"
)

// readSome reads from the response body in a goroutine until the stream
// contains the SSE frame markers or the bounded wait expires, so a stuck
// server fails the test with a message instead of hanging it.
func readSome(t *testing.T, resp *http.Response, wait time.Duration) string {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		data := ""
		for !strings.Contains(data, "event:") ||
			!strings.Contains(data, ": connected") {
			n, err := resp.Body.Read(buf)
			data += string(buf[:n])
			if err != nil {
				break
			}
		}
		got <- data
	}()
	select {
	case d := <-got:
		return d
	case <-time.After(wait):
		return "<no matching data within " + wait.String() + ">"
	}
}

// drainUntilEOF reads the body until the server closes it, bounded in time.
func drainUntilEOF(t *testing.T, resp *http.Response, wait time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(wait):
		t.Errorf("body did not reach EOF within %s after teardown", wait)
	}
}

func TestStream_RealServer_EventDeliveryAndUnsubscribeOnClientClose(t *testing.T) {
	gin.SetMode(gin.TestMode)
	notifyHub := hub.New()
	eh := NewEventsHandler(notifyHub, nil)
	user := &model.User{ID: 7, Username: "eve"}

	r := gin.New()
	r.GET("/events", func(c *gin.Context) {
		c.Set("user", user)
		eh.Stream(c)
	})
	ts := httptest.NewServer(r)
	defer ts.Close()

	reqCtx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, ts.URL+"/events", nil)
	require.NoError(t, err)
	client := ts.Client()
	client.Timeout = 0 // stream lives as long as reqCtx does
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// The stream opens and holds exactly one hub subscriber.
	assert.Eventually(t, func() bool { return notifyHub.Count() == 1 },
		2*time.Second, 10*time.Millisecond)

	// Deliverability: an event notified while the stream is open arrives.
	notifyHub.Notify(user.ID, "notes_changed")
	body := readSome(t, resp, 3*time.Second)
	assert.Contains(t, body, ": connected")
	assert.Contains(t, body, "event: notes_changed")

	// Killing the client ends the handler within one keepalive, and the
	// subscriber count drops — unsub is asserted observable, not implied by
	// the request returning.
	start := time.Now()
	cancel()
	drainUntilEOF(t, resp, 5*time.Second)
	assert.Less(t, time.Since(start), SSEKeepaliveInterval,
		"stream outlived one keepalive after the client cancelled")
	assert.Eventually(t, func() bool { return notifyHub.Count() == 0 },
		2*time.Second, 10*time.Millisecond,
		"subscriber must be unsubscribed after client disconnect")
}

func TestServer_ShutdownWithOpenStream_HookCancelsSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	notifyHub := hub.New()
	user := &model.User{ID: 8, Username: "frank"}

	// Mirror cmd/thornotes/main.go exactly: a cancellable context that the
	// SSE handlers select on, cancelled by srv.RegisterOnShutdown so open
	// streams release Shutdown's idle wait.
	sseShutdown, sseShutdownCancel := context.WithCancel(context.Background())
	defer sseShutdownCancel()
	eh := NewEventsHandler(notifyHub, sseShutdown)

	r := gin.New()
	r.GET("/events", func(c *gin.Context) {
		c.Set("user", user)
		eh.Stream(c)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{Handler: r}
	srv.RegisterOnShutdown(sseShutdownCancel)
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet,
		"http://"+ln.Addr().String()+"/events", nil)
	require.NoError(t, err)
	resp, err := (&http.Client{Timeout: 0}).Do(req) // hold the stream open
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Eventually(t, func() bool { return notifyHub.Count() == 1 },
		2*time.Second, 10*time.Millisecond)

	// Shutdown with a stream open must return well under a second instead of
	// burning its context waiting for a never-idle connection.
	start := time.Now()
	err = srv.Shutdown(context.Background())
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.Less(t, elapsed, time.Second,
		"Shutdown was held open by an open SSE stream (took %s)", elapsed)
	assert.Eventually(t, func() bool { return notifyHub.Count() == 0 },
		2*time.Second, 10*time.Millisecond,
		"subscriber must be unsubscribed by the shutdown hook")
}

// TestStream_WriteDeadlineReachesRealConnectionThroughGin proves
// http.NewResponseController unwraps gin's ResponseWriter down to the actual
// *http.response: a write against a client that never reads fails at the
// per-write deadline instead of blocking forever. If gin did not unwrap, the
// write would hang, nothing would be sent on done, and the bounded receive
// below would fail the test.
func TestStream_WriteDeadlineReachesRealConnectionThroughGin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	type outcome struct {
		writeErr error
		elapsed  time.Duration
	}
	done := make(chan outcome, 1)

	r := gin.New()
	r.GET("/blow", func(c *gin.Context) {
		rc := http.NewResponseController(c.Writer)
		_, _ = fmt.Fprint(c.Writer, "streaming\n") // let the client see headers
		_ = rc.Flush()

		start := time.Now()
		if err := rc.SetWriteDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			done <- outcome{writeErr: fmt.Errorf("SetWriteDeadline: %w", err), elapsed: time.Since(start)}
			return
		}
		chunk := make([]byte, 1<<20)
		for i := 0; i < 512; i++ {
			if _, err := c.Writer.Write(chunk); err != nil {
				// The deadline fired while blocked on a non-reading
				// client: this is the pass condition.
				done <- outcome{writeErr: nil, elapsed: time.Since(start)}
				return
			}
		}
		done <- outcome{writeErr: errors.New("sent 512MiB without blocking; write deadline ineffective")}
	})

	ts := httptest.NewServer(r)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/blow", nil)
	require.NoError(t, err)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close() // deliberately never read the body

	select {
	case o := <-done:
		require.NoError(t, o.writeErr,
			"per-write deadline should have errored a blocked write, not been rejected")
		assert.Less(t, o.elapsed, 4*time.Second,
			"blocked write took %s to surface at the write deadline", o.elapsed)
	case <-time.After(5 * time.Second):
		t.Fatal("handler blocked > 5 s against a non-reading client; per-write deadline did not reach the real connection")
	}
}
