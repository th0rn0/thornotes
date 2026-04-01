package db

import (
	"database/sql"
	"embed"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	migratemysql "github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/go-sql-driver/mysql"
)

//go:embed mysql_migrations/*.sql
var mysqlMigrationsFS embed.FS

// OpenMySQL opens a MySQL connection, runs migrations, and returns a Pool.
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

	p := &Pool{ReadDB: db, WriteDB: db}
	if err := p.migrateMySQL(); err != nil {
		db.Close()
		return nil, fmt.Errorf("mysql migrate: %w", err)
	}

	return p, nil
}

func (p *Pool) migrateMySQL() error {
	src, err := iofs.New(mysqlMigrationsFS, "mysql_migrations")
	if err != nil {
		return fmt.Errorf("iofs: %w", err)
	}

	driver, err := migratemysql.WithInstance(p.WriteDB, &migratemysql.Config{})
	if err != nil {
		return fmt.Errorf("migrate driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "mysql", driver)
	if err != nil {
		return fmt.Errorf("migrate instance: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate up: %w", err)
	}

	return nil
}
