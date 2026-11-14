package mysql

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/dbtest"
)

// The shared test pool's ceiling.
//
// The connection ceiling is a property of the server the DSN points at, not of
// this repository: both the CI MariaDB service and the shared dev server run
// with max_connections = 151. A cap on this pool therefore only means something
// if the whole fan-out fits under that number.
//
// What this package can actually want at once is three connections, and no
// more: tests are serial (there is no t.Parallel anywhere in the package),
// truncateAll holds one connection while it deletes, and the pinned-clock tests
// add a separate single-connection pool. 8 is 2.6x that peak, so the cap never
// makes a query wait behind another one.
//
// The fan-out, counted pessimistically: one pool per suite process, and suite
// processes serialise on the dbtest advisory lock, so at most one pool is ever
// doing work while every other process holds exactly its lock connection.
// Even ten suite processes at once — more than CI or this box has ever run —
// reach at most 10 * (8 pool + 1 lock + 1 pinned clock) = 100 < 151, which
// leaves 51 connections for the application, the mysql CLI and whoever is
// debugging against the same server.
const (
	testPoolMaxOpen = 8
	testPoolMaxIdle = 4

	// A connection the server has already dropped must not keep occupying a
	// slot in this process's pool. Normal runs finish in seconds, so neither
	// limit fires during one; they exist for a stalled or very long run.
	testConnMaxLifetime = 5 * time.Minute
	testConnMaxIdleTime = 2 * time.Minute
)

// sharedPool is the one pool this package's tests use, opened once by TestMain
// and closed when the process exits.
//
// It used to be one pool per test — sql.Open, Ping, runMySQLMigrations and
// Close, ninety-nine times per run and so 297 times under -count=3 — and each
// pool is allowed 25 connections while each migration run opened another one.
// The result was measured, not guessed: during `go test -count=3` against a
// server with max_connections = 151, Threads_connected climbed to exactly 151
// with Threads_running = 1, and every further test failed inside
// db.OpenMySQL with "Error 1040 (08004): Too many connections". Idle pooled
// connections, not concurrency between tests, were the whole failure.
//
// Test isolation therefore does not come from a fresh pool: every test still
// starts from truncateAll (see openTestMySQL). That invariant is what makes a
// shared pool correct, and TestMySQLHarness_TruncationStillIsolates pins it.
var sharedPool *db.Pool

// TestMain serialises this package against internal/db, which shares the same
// MySQL test database and leaves schema_migrations dirty on purpose while it
// tests the refusal path. Without the lock, the migration below can run in that
// window and fail with "schema version 7 is dirty" — a failure about the other
// package's fixture, not about anything in here.
//
// It also owns the pool. Opening it once per process rather than once per test
// moves migrations from once per test to once per process: under -count=3 that
// is 297 migration runs becoming one, which is most of the suite's wall clock
// as well as most of its connections.
func TestMain(m *testing.M) {
	os.Exit(dbtest.Locked(mysqlDSNEnv, func() int {
		// With no DSN the suites skip themselves for the ordinary reason, and
		// openTestMySQL still turns a missing DSN into a failure when
		// THORNOTES_REQUIRE_MYSQL=1.
		if dsn := os.Getenv(mysqlDSNEnv); dsn != "" {
			pool, err := openSharedTestPool(dsn)
			if err != nil {
				fmt.Fprintf(os.Stderr, "mysql: open shared test pool: %v\n", err)
				return 1
			}
			defer pool.Close()
			sharedPool = pool
		}
		return m.Run()
	}))
}

// openSharedTestPool opens the package's pool through the application's own
// constructor — so the tests connect and migrate exactly as the app does — and
// then bounds it to what the suite needs.
//
// db.OpenMySQL is left alone: 25/10 is the application's budget on its own
// server, and this suite must not spend it.
func openSharedTestPool(dsn string) (*db.Pool, error) {
	pool, err := db.OpenMySQL(dsn)
	if err != nil {
		return nil, err
	}

	tune := func(handle *sql.DB) {
		handle.SetMaxOpenConns(testPoolMaxOpen)
		handle.SetMaxIdleConns(testPoolMaxIdle)
		handle.SetConnMaxLifetime(testConnMaxLifetime)
		handle.SetConnMaxIdleTime(testConnMaxIdleTime)
	}
	tune(pool.WriteDB)
	// MySQL has no read/write split: OpenMySQL hands the same *sql.DB to both
	// fields, so this branch does not fire today. It is here so a future split
	// cannot leave half the pool unbounded.
	if pool.ReadDB != pool.WriteDB && pool.ReadDB != nil {
		tune(pool.ReadDB)
	}

	return pool, nil
}
