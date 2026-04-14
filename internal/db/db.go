package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"

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
// of check name to "ok" or an error message. Used by the /healthz endpoint.
func (p *Pool) HealthCheck() map[string]string {
	checks := make(map[string]string, 2)
	if err := p.ReadDB.Ping(); err != nil {
		checks["db_read"] = err.Error()
	} else {
		checks["db_read"] = "ok"
	}
	if err := p.WriteDB.Ping(); err != nil {
		checks["db_write"] = err.Error()
	} else {
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

	driver, err := migratesqlite.WithInstance(p.WriteDB, &migratesqlite.Config{})
	if err != nil {
		return fmt.Errorf("migrate driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "sqlite", driver)
	if err != nil {
		return fmt.Errorf("migrate instance: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		var dirtyErr migrate.ErrDirty
		if errors.As(err, &dirtyErr) {
			// A previous run was interrupted and left the schema in a dirty state.
			// Roll back to the last clean version and retry. All up migrations use
			// CREATE TABLE IF NOT EXISTS so re-running a partially applied migration
			// is safe.
			if ferr := m.Force(dirtyErr.Version - 1); ferr != nil {
				return fmt.Errorf("migrate up: force version after dirty state: %w", ferr)
			}
			if err := m.Up(); err != nil && err != migrate.ErrNoChange {
				return fmt.Errorf("migrate up: %w", err)
			}
			return nil
		}
		return fmt.Errorf("migrate up: %w", err)
	}

	return nil
}
