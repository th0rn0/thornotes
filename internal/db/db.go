package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Pool holds the two-pool SQLite setup.
// readDB: multiple connections for concurrent reads.
// writeDB: single connection serializes all writes, avoids "database is locked".
type Pool struct {
	ReadDB  *sql.DB
	WriteDB *sql.DB

	// schemaVersion is the migration version applied to the database when it was
	// opened and schemaMax is the newest migration this build embeds. Both are
	// set during Open/OpenMySQL, before the Pool is shared with anything else,
	// and are read-only afterwards.
	schemaVersion int
	schemaMax     int
}

// SchemaStatus reports the applied migration version and the newest migration
// this build supports. It is deliberately expressed with primitives so the
// health handler can consume it without importing this package.
func (p *Pool) SchemaStatus() (version, maxSupported int) {
	return p.schemaVersion, p.schemaMax
}

func Open(dsn string) (*Pool, error) {
	// WAL mode + busy timeout via DSN parameters.
	fullDSN := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", dsn)

	writeDB, err := sql.Open("sqlite", fullDSN)
	if err != nil {
		return nil, fmt.Errorf("open writeDB: %w", err)
	}
	writeDB.SetMaxOpenConns(1)

	readDB, err := sql.Open("sqlite", fullDSN+"&_pragma=query_only(ON)")
	if err != nil {
		writeDB.Close()
		return nil, fmt.Errorf("open readDB: %w", err)
	}
	readDB.SetMaxOpenConns(10)

	if err := writeDB.Ping(); err != nil {
		writeDB.Close()
		readDB.Close()
		return nil, fmt.Errorf("ping writeDB: %w", err)
	}

	p := &Pool{ReadDB: readDB, WriteDB: writeDB}
	if err := p.migrate(); err != nil {
		p.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return p, nil
}

// HealthCheck pings the read and write database connections and returns a map
// of check name to "ok" or "unavailable". Raw driver errors are logged
// server-side; the public response never exposes internal paths or messages.
func (p *Pool) HealthCheck() map[string]string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	checks := map[string]string{
		"db_read":  "unavailable",
		"db_write": "unavailable",
	}
	if err := p.ReadDB.PingContext(ctx); err == nil {
		checks["db_read"] = "ok"
	}
	if err := p.WriteDB.PingContext(ctx); err == nil {
		checks["db_write"] = "ok"
	}
	return checks
}

func (p *Pool) Close() {
	if p.ReadDB != nil {
		p.ReadDB.Close()
	}
	if p.WriteDB != nil {
		p.WriteDB.Close()
	}
}

func (p *Pool) migrate() error {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("iofs: %w", err)
	}

	// The newest migration this build carries. Read from the source driver, not
	// from directory numbering — see maxSupportedVersion.
	maxSupported, err := maxSupportedVersion(src)
	if err != nil {
		return fmt.Errorf("schema floor: %w", err)
	}

	driver, err := migratesqlite.WithInstance(p.WriteDB, &migratesqlite.Config{})
	if err != nil {
		return fmt.Errorf("migrate driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "sqlite", driver)
	if err != nil {
		return fmt.Errorf("migrate instance: %w", err)
	}

	// Refuse a database that is ahead of this binary, or dirty, before applying
	// anything. The typed errors are returned unwrapped so callers and tests can
	// assert on the value rather than on a string.
	if _, err := preflightSchema(m, maxSupported); err != nil {
		return err
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate up: %w", err)
	}

	// Record what /healthz reports: the version now applied, and the floor this
	// build supports. Up() leaves the database at maxSupported, but reading it
	// back keeps the reported number honest if that ever stops being true.
	applied, _, err := m.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return fmt.Errorf("read applied schema version: %w", err)
	}
	p.schemaVersion = int(applied)
	p.schemaMax = maxSupported

	return nil
}
