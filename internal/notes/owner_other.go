//go:build !linux

package notes

import (
	"errors"
	"os"
)

// errLockBusy is returned when another process already holds the notes-root
// lock.
var errLockBusy = errors.New("notes root lock is held by another owner")

// errLockUnsupported reports that advisory locking is unavailable.
var errLockUnsupported = errors.New("advisory locking is not supported on this platform")

// lockFileExclusive is a no-op on platforms without flock(2). The multi-process
// hazard is documented in owner.go; on these platforms the enforcement is the
// honour system plus the in-process locks.
func lockFileExclusive(f *os.File) error { return nil }

// unlockFile is a no-op on platforms without flock(2).
func unlockFile(f *os.File) error { return nil }
