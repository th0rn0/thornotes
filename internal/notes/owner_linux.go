//go:build linux

package notes

import (
	"errors"
	"os"
	"syscall"
)

// errLockBusy is returned when another process (or another open file
// description) already holds the notes-root lock.
var errLockBusy = errors.New("notes root lock is held by another owner")

// errLockUnsupported is never returned on Linux; it exists so the portable
// fallback can share the same sentinel names.
var errLockUnsupported = errors.New("advisory locking is not supported on this platform")

// lockFileExclusive takes a non-blocking exclusive flock on f.
func lockFileExclusive(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errLockBusy
	}
	if errors.Is(err, syscall.ENOLCK) || errors.Is(err, syscall.EOPNOTSUPP) {
		// No lock support on this filesystem (some network mounts). The
		// in-process locks still hold; report it rather than pretending.
		return errLockUnsupported
	}
	return err
}

// unlockFile releases the flock held on f.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
