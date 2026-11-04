package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	migratemysql "github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed mysql_migrations/*.sql
var mysqlMigrationsFS embed.FS

// OpenMySQL opens a MySQL/MariaDB connection, runs migrations, and returns a Pool.
// For MySQL there is no read/write split — both ReadDB and WriteDB point to
// the same *sql.DB connection pool. MySQL handles concurrency internally.
func OpenMySQL(dsn string) (*Pool, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}

	// Migrations run on a separate connection with multiStatements=true.
	// The go-sql-driver/mysql driver rejects SQL files that contain more than
	// one semicolon-separated statement unless this flag is set. Keeping it off
	// on the main pool avoids any risk of multi-statement injection via the app.
	applied, maxSupported, err := runMySQLMigrations(dsn)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("mysql migrate: %w", err)
	}

	return &Pool{ReadDB: db, WriteDB: db, schemaVersion: applied, schemaMax: maxSupported}, nil
}

// runMySQLMigrations applies pending migrations and returns the version now
// stamped in the database plus the newest migration this build embeds.
func runMySQLMigrations(dsn string) (applied, maxSupported int, err error) {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	migDB, err := sql.Open("mysql", dsn+sep+"multiStatements=true")
	if err != nil {
		return 0, 0, fmt.Errorf("open migration db: %w", err)
	}
	defer migDB.Close()

	src, err := iofs.New(mysqlMigrationsFS, "mysql_migrations")
	if err != nil {
		return 0, 0, fmt.Errorf("iofs: %w", err)
	}

	// The MySQL sequence is numbered differently from SQLite's (no
	// 004_api_tokens_hash), so the floor must come from this source driver and
	// never from the SQLite directory.
	maxSupported, err = maxSupportedVersion(src)
	if err != nil {
		return 0, 0, fmt.Errorf("schema floor: %w", err)
	}

	driver, err := migratemysql.WithInstance(migDB, &migratemysql.Config{})
	if err != nil {
		return 0, 0, fmt.Errorf("migrate driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "mysql", driver)
	if err != nil {
		return 0, 0, fmt.Errorf("migrate instance: %w", err)
	}

	// Same preflight as SQLite: refuse a database newer than this build, or a
	// dirty one, without applying anything.
	if _, err := preflightSchema(m, maxSupported); err != nil {
		return 0, 0, err
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return 0, 0, fmt.Errorf("migrate up: %w", err)
	}

	version, _, err := m.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return 0, 0, fmt.Errorf("read applied schema version: %w", err)
	}

	return int(version), maxSupported, nil
}
