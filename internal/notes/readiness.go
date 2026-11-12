package notes

// The startup window: one bit saying whether the index can be trusted yet.
//
// cmd/thornotes opens the HTTP listener before it runs the synchronous startup
// reconcile — the port has to answer quickly, and a bind conflict must fail at
// t=0 rather than after a pass that may take half a minute. The index, though,
// is only trustworthy once that pass has finished, and in between a request is
// answered from a half-repaired one.
//
// That answer is wrong, not slow. With a restored notes tree or a rebuilt
// database the index holds no rows for files that are plainly on disk: the list
// comes back short, a search misses notes that are there, and a note the caller
// already knows the id of can answer 404. Every one of those is
// indistinguishable from a real deletion, and nobody reports three wrong
// seconds. Measured on this build: a 401-note vault served a 64-note list for
// 2.7s, and a 5,001-note vault takes 33s to reconcile, so the window is as long
// as the corpus is big.
//
// So the instance carries this bit. The router refuses index-derived requests
// while a startup pass is in flight, and /healthz reports the phase. The bit is
// per instance and not per user: the startup pass repairs every user's index,
// and no user's index is complete until it has been through.
//
// The zero value is *ready*, deliberately. A Service built by a test, or by any
// future embedder that never runs a startup pass, must serve immediately; only
// the process that actually runs a startup pass opens the window, with
// BeginStartup. The opposite default would turn one forgotten EndStartup into a
// permanently dead instance — the failure mode this is most likely to be
// broken by — and would make every existing constructor return 503.
//
// Scope: this is the *startup* pass only. The disk watcher also reconciles, but
// it applies small deltas to an index that is already complete, and gating a
// live instance for the duration of every watch tick would be a worse bug than
// the one being fixed.

// BeginStartup opens the startup window: from here until EndStartup, requests
// that would be answered from the note index are refused. It must be called
// before the HTTP listener accepts its first connection.
func (s *Service) BeginStartup() { s.reconciling.Store(true) }

// EndStartup closes the startup window, whether the pass succeeded or not.
//
// It must be called on *every* exit path out of the startup pass, including the
// `--skip-reconciliation` path and the path where a pass failed: the point of
// the bit is to keep a booting instance out of rotation, and a failed pass that
// forgot to clear it would leave the instance unready for its whole life while
// the process was perfectly alive and logging.
func (s *Service) EndStartup() { s.reconciling.Store(false) }

// StartupReconciling reports whether a startup reconcile pass is still running,
// i.e. whether the index is still being repaired and must not be read.
func (s *Service) StartupReconciling() bool { return s.reconciling.Load() }
