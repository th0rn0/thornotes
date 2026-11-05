// Package dbtest holds helpers for tests that share one real MariaDB/MySQL
// database across parallel test packages.
//
// `go test ./...` runs packages in parallel processes. Two packages that both
// run migrations against the same schema — and one that deliberately leaves it
// dirty to prove the refusal — will interleave, and the loser reads whatever
// state the winner left behind. That is not a flaky assertion; it is two
// writers on one resource, so the fix is a lock, not a retry.
package dbtest

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// LockName is the MariaDB advisory lock guarding the shared test schema.
const LockName = "thornotes_test_db"

// lockTimeout bounds how long one package waits for the other to finish.
const lockTimeout = 600 // seconds

// Locked runs run() while holding an advisory lock on the shared test database,
// and returns its exit code.
//
// The lock is MariaDB's GET_LOCK, which needs no privilege beyond what the test
// DSN already has and works across processes and hosts. When envVar is unset the
// suites skip themselves, so run() is called with no lock and those skips happen
// for the ordinary reason.
//
// A suite that cannot be sure it is alone exits 1 rather than reporting results:
// a green run obtained by racing is worse than no run.
// reexecEnv marks a test binary that another test re-executed with
// os.Args[0] to run one test in a child process with a different environment
// (internal/db does this to check the migration guard). Such a child is not a
// parallel package: its parent already holds the advisory lock, and taking it
// again would block for the whole timeout — a deadlock, not a race.
const reexecEnv = "THORNOTES_TEST_REEXEC"

func Locked(envVar string, run func() int) int {
	if os.Getenv(reexecEnv) != "" {
		return run()
	}

	dsn := os.Getenv(envVar)
	if dsn == "" {
		return run()
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: open lock connection: %v\n", err)
		return 1
	}
	// Closing the pool releases the advisory lock: it is held by the connection
	// GET_LOCK ran on, so one connection is the whole point.
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	if err := ping(db); err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: lock connection unusable: %v\n", err)
		return 1
	}

	var got sql.NullInt64
	if err := db.QueryRow("SELECT GET_LOCK(?, ?)", LockName, lockTimeout).Scan(&got); err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: GET_LOCK(%q): %v\n", LockName, err)
		return 1
	}
	if !got.Valid || got.Int64 != 1 {
		fmt.Fprintf(os.Stderr,
			"dbtest: could not acquire %q within %ds — another package still holds the shared test database, refusing to race it\n",
			LockName, lockTimeout)
		return 1
	}

	return run()
}

// ping waits for the database to answer, because a CI service container can be
// listening before it is ready to authenticate.
func ping(db *sql.DB) error {
	var err error
	for i := 0; i < 30; i++ {
		if err = db.Ping(); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return err
}
