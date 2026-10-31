package notes

import (
	"errors"
	"os"
	"time"

	"github.com/rs/zerolog/log"
)

// ErrReconcileAborted is returned (wrapped) when the reconciler refuses to
// apply a pass that it believes would destroy data: an unreadable or empty
// notes root with a populated index, or a deletion burst past the circuit
// breaker. Callers treat it as "this instance must not be trusted"; nothing
// was deleted.
var ErrReconcileAborted = errors.New("reconcile aborted")

// ReconcileHealth is the outcome of the most recent reconcile pass. It is
// recorded for every pass, successful or not, because an operator needs to see
// a refusal before trusting the instance rather than discovering it after the
// index has been rebuilt from a broken mount.
type ReconcileHealth struct {
	At    time.Time
	OK    bool
	Error string
}

// NotesRootHealth is the operator-visible state of the notes root and of the
// last reconcile pass.
type NotesRootHealth struct {
	Path          string
	Exists        bool
	Readable      bool
	LastReconcile *ReconcileHealth
	Error         string
}

// Healthy reports whether the notes root can currently be trusted: it must be
// present and readable, and the last reconcile pass must not have been aborted.
// A never-reconciled instance is considered healthy on readability alone.
func (h NotesRootHealth) Healthy() bool {
	if !h.Exists || !h.Readable {
		return false
	}
	if h.LastReconcile != nil && !h.LastReconcile.OK {
		return false
	}
	return true
}

// NotesRootHealth inspects the notes root and returns its current state plus
// the outcome of the last reconcile pass.
func (s *Service) NotesRootHealth() NotesRootHealth {
	h := NotesRootHealth{Path: s.fs.notesRoot}

	info, err := os.Stat(s.fs.notesRoot)
	switch {
	case err == nil:
		h.Exists = true
		if !info.IsDir() {
			h.Error = "notes root is not a directory"
			break
		}
		// Directory readability needs open(2), which is what os.ReadDir
		// ultimately does; stat() alone succeeds on a mode-000 directory.
		f, openErr := os.Open(s.fs.notesRoot)
		if openErr != nil {
			h.Error = openErr.Error()
			break
		}
		_ = f.Close()
		h.Readable = true
	case errors.Is(err, os.ErrNotExist):
		h.Error = "notes root does not exist"
	default:
		h.Error = err.Error()
	}

	s.healthMu.RLock()
	h.LastReconcile = s.lastReconcile
	s.healthMu.RUnlock()
	return h
}

// NotesRootHealthy implements the handler-facing readiness interface without
// sharing a type across packages.
func (s *Service) NotesRootHealthy() bool { return s.NotesRootHealth().Healthy() }

// NotesRootHealthError returns a human-readable reason for an unhealthy notes
// root, or "" when it is healthy.
func (s *Service) NotesRootHealthError() string {
	h := s.NotesRootHealth()
	if h.Healthy() {
		return ""
	}
	if h.Error != "" {
		return h.Error
	}
	if h.LastReconcile != nil && !h.LastReconcile.OK {
		return "last reconcile aborted: " + h.LastReconcile.Error
	}
	return "notes root unhealthy"
}

// recordReconcile stores the outcome of one pass for readiness reporting.
func (s *Service) recordReconcile(err error) {
	rec := &ReconcileHealth{At: time.Now().UTC(), OK: err == nil}
	if err != nil {
		rec.Error = err.Error()
	}
	s.healthMu.Lock()
	s.lastReconcile = rec
	s.healthMu.Unlock()
}

// logAbort emits the loud, operator-facing line that accompanies a refusal.
// A silent abort would be nearly as bad as the wipe it prevents: the index is
// intact but stale, and someone has to go and look at the mount.
func logAbort(userID int64, root string, err error) {
	log.Error().
		Err(err).
		Int64("user_id", userID).
		Str("notes_root", root).
		Msg("reconcile: REFUSING to apply pass — index left untouched, instance is not trustworthy until this is fixed")
}
