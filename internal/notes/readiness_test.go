package notes_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/th0rn0/thornotes/internal/notes"
)

// TestStartupWindow_ZeroValueIsReady pins the default that keeps every existing
// constructor working: a Service that never runs a startup pass is ready, and
// only the process that actually reconciles at startup opens the window. The
// other default would turn a forgotten EndStartup into a permanently dead
// instance, which is the failure mode this bit is most likely to be broken by.
func TestStartupWindow_ZeroValueIsReady(t *testing.T) {
	svc := notes.NewService(nil, nil, nil, nil, nil, nil)
	assert.False(t, svc.StartupReconciling(), "a Service with no startup pass in flight must be ready")
}

// TestStartupWindow_RoundTrip covers the two transitions the router and /healthz
// read: closed until BeginStartup, and closed again from EndStartup.
func TestStartupWindow_RoundTrip(t *testing.T) {
	svc := notes.NewService(nil, nil, nil, nil, nil, nil)

	svc.BeginStartup()
	assert.True(t, svc.StartupReconciling())

	svc.EndStartup()
	assert.False(t, svc.StartupReconciling())
}

// TestStartupWindow_EndIsIdempotent: closing a window that is already closed is
// not an error. The startup pass has several exit paths and more than one of them
// may reasonably be defensive about ending it.
func TestStartupWindow_EndIsIdempotent(t *testing.T) {
	svc := notes.NewService(nil, nil, nil, nil, nil, nil)

	svc.EndStartup()
	assert.False(t, svc.StartupReconciling())

	svc.BeginStartup()
	svc.EndStartup()
	svc.EndStartup()
	assert.False(t, svc.StartupReconciling())
}

// TestStartupWindow_ReadAndWriteConcurrently is the reason the bit is atomic:
// every request on the instance reads it while the startup pass writes it, and
// -race is the assertion.
func TestStartupWindow_ReadAndWriteConcurrently(t *testing.T) {
	svc := notes.NewService(nil, nil, nil, nil, nil, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			svc.BeginStartup()
			svc.EndStartup()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = svc.StartupReconciling()
		}
	}()
	wg.Wait()
}
