package db

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source"
)

// The applied migration version lives in the database (schema_migrations), not
// in the image. A binary built from a commit older than its database therefore
// asks golang-migrate to "migrate up" a database that is already ahead of every
// migration the binary embeds, and golang-migrate fails by trying to read the
// *down* file for the current version:
//
//	migrate: migrate up: no migration found for version 8:
//	    read down for version 8 migrations: file does not exist
//
// With `restart: unless-stopped` that turns one wrong image into a permanent
// crash loop, and the message never says "your database is newer than this
// binary". The preflight below refuses the same situation explicitly, before
// any migration is applied.
//
// The refusal is deliberate: down migrations in this repo are structurally
// tested but not behaviourally exercised, so mutating or dropping user data
// automatically because a container restarted against the wrong image is not an
// option. A loud refusal plus a documented recovery path is the fix.

// ErrSchemaTooNew reports that the database has already applied a migration
// version that this build does not contain. It is returned instead of running
// migrations, so the database is left exactly as it was.
type ErrSchemaTooNew struct {
	DBVersion    int
	MaxSupported int
}

func (e *ErrSchemaTooNew) Error() string {
	return fmt.Sprintf(
		"schema version %d is newer than this build supports (%d) — "+
			"deploy a build with migration ≥ %d, or restore the database backup taken before migration %d",
		e.DBVersion, e.MaxSupported, e.DBVersion, e.DBVersion)
}

// ErrSchemaDirty reports that a previous migration was interrupted, leaving the
// database flagged dirty at Version. The schema is in an unknown state, so the
// process refuses to start rather than guessing.
type ErrSchemaDirty struct {
	Version int
}

func (e *ErrSchemaDirty) Error() string {
	return fmt.Sprintf(
		"schema version %d is dirty: a migration was interrupted and the schema is in an unknown state — "+
			"inspect the database and verify the schema by hand, then force version %d only if it is correct, "+
			"or restore the database backup taken before migration %d",
		e.Version, e.Version, e.Version)
}

// maxSupportedVersion returns the highest migration version present in src.
//
// It walks the source driver (First, then Next until the driver reports the
// next version does not exist) rather than counting files or taking the highest
// number in a directory: the two dialect sequences are not numbered identically.
// SQLite is at 008 for note_links while MySQL is at 007, because the MySQL
// sequence has no 004_api_tokens_hash, so any "count the files" or "highest
// number in the dir" shortcut is wrong for one backend or the other.
func maxSupportedVersion(src source.Driver) (int, error) {
	first, err := src.First()
	if err != nil {
		return 0, fmt.Errorf("read first migration version: %w", err)
	}
	max := int(first)
	for {
		next, err := src.Next(uint(max))
		if errors.Is(err, fs.ErrNotExist) {
			return max, nil
		}
		if err != nil {
			return 0, fmt.Errorf("read migration version after %d: %w", max, err)
		}
		max = int(next)
	}
}

// preflightSchema compares the version stamped in the database with the newest
// migration this build embeds.
//
// It reads only the version table — no migration runs and no row is modified —
// so a refusal leaves the database untouched. It returns the database's applied
// version (0 for an empty database) and a typed error when the database is
// ahead of this build, or was left dirty by an interrupted migration.
func preflightSchema(m *migrate.Migrate, maxSupported int) (int, error) {
	version, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		// Nothing has ever been applied: this is a fresh database.
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if dirty {
		// Checked before the version comparison: a dirty row means the stamped
		// version cannot be trusted, so naming it as "newer" would be a guess.
		return int(version), &ErrSchemaDirty{Version: int(version)}
	}
	if int(version) > maxSupported {
		return int(version), &ErrSchemaTooNew{DBVersion: int(version), MaxSupported: maxSupported}
	}
	return int(version), nil
}
