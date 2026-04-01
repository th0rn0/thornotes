package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	migratemysql "github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/go-sql-driver/mysql"
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
	if err := runMySQLMigrations(dsn); err != nil {
		db.Close()
		return nil, fmt.Errorf("mysql migrate: %w", err)
	}

	return &Pool{ReadDB: db, WriteDB: db}, nil
}

func runMySQLMigrations(dsn string) error {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	migDB, err := sql.Open("mysql", dsn+sep+"multiStatements=true")
	if err != nil {
		return fmt.Errorf("open migration db: %w", err)
	}
	defer migDB.Close()

	src, err := iofs.New(mysqlMigrationsFS, "mysql_migrations")
	if err != nil {
		return fmt.Errorf("iofs: %w", err)
	}

	driver, err := migratemysql.WithInstance(migDB, &migratemysql.Config{})
	if err != nil {
		return fmt.Errorf("migrate driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "mysql", driver)
	if err != nil {
		return fmt.Errorf("migrate instance: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		var dirtyErr migrate.ErrDirty
		if errors.As(err, &dirtyErr) {
			// A previous run was interrupted and left the schema in a dirty state.
			// Force(-1) clears the version tracking entirely (golang-migrate convention:
			// -1 = no migrations applied). Up() then re-runs all migrations from
			// scratch; all up migrations use CREATE TABLE IF NOT EXISTS so this is safe.
			// We cannot use Force(version-1) because Force(0) is invalid — golang-migrate
			// tries to read a down file for version 0 which does not exist.
			if ferr := m.Force(-1); ferr != nil {
				return fmt.Errorf("force version after dirty state: %w", ferr)
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
