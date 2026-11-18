package notes_test

// This package's MySQL leg runs against the same thornotes_test schema the
// repository packages use, so it carries the same two obligations they do: take
// the shared dbtest advisory lock for the whole run, and open one pool for the
// process rather than one per test.
//
// The second obligation is ticket 500's fix in internal/repository/mysql, and it
// had never been applied here. newMySQLTitleStack used to call db.OpenMySQL per
// subtest, and every OpenMySQL pings, runs migrations on a connection of its own
// and leaves a pool open for the length of the test whose ceiling is 25. Repeat
// the run and those pools outlive the server's patience: with
// max_connections = 151, `go test ./internal/notes -count=10` died inside a
// migration with
//
//	mysql migrate: migrate driver: Error 1040 (08004): Too many connections
//
// and the server reported Max_used_connections = 152. -count=1 is green, so this
// never showed up in CI; it fires exactly when somebody repeats the suite to
// check for order dependence, which is when a suite has to be trustworthy.
//
// Test isolation never came from a fresh pool. It comes from dbtest.TruncateAll,
// which every stack still calls before seeding — TestMySQLHarness_OnePoolPerProcess
// pins both halves: one handle per process, and an empty schema per test.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/dbtest"
)

// The shared test pool's bounds, and why they are this small.
//
// Nothing in this package runs in parallel (there is no t.Parallel in it), and a
// subtest holds a handful of connections at most: one for the truncation that
// starts it and one or two for the queries it asserts on. 8 is well clear of
// that, and far below the 25 db.OpenMySQL hands the application — this suite runs
// against a server the application and other packages also use, so it must not
// spend the application's budget.
//
// The fan-out, counted pessimistically: one pool per package process, and every
// package that touches this schema serialises on the dbtest advisory lock, so at
// most one pool is ever doing work while each of the others holds a single lock
// connection.
const (
	testPoolMaxOpen = 8
	testPoolMaxIdle = 4

	// A connection the server has already dropped must not keep occupying a slot
	// in this process's pool. One run finishes in seconds, so neither fires during
	// a healthy one; they exist for a stalled or very long run.
	testConnMaxLifetime = 5 * time.Minute
	testConnMaxIdleTime = 2 * time.Minute
)

// sharedMySQLPool is the one pool this package's MySQL tests use, opened by
// TestMain and closed when the process exits. It is nil when
// THORNOTES_TEST_MYSQL_DSN is unset, in which case the MySQL legs skip themselves
// for the ordinary reason — and newMySQLTitleStack turns that skip into a failure
// when THORNOTES_REQUIRE_MYSQL=1.
var sharedMySQLPool *db.Pool

// TestMain holds the shared-schema advisory lock whenever the MySQL DSN is set:
// this package truncates the same database the repository packages use, and
// `go test ./...` runs packages in parallel processes. It also owns the pool, so
// that migrations run once per process instead of once per subtest.
func TestMain(m *testing.M) {
	os.Exit(dbtest.Locked(titleRepairDSNEnv, func() int {
		if dsn := os.Getenv(titleRepairDSNEnv); dsn != "" {
			pool, err := openSharedMySQLPool(dsn)
			if err != nil {
				fmt.Fprintf(os.Stderr, "notes: open shared test pool: %v\n", err)
				return 1
			}
			defer pool.Close()
			sharedMySQLPool = pool
		}
		return m.Run()
	}))
}

// openSharedMySQLPool opens the package's pool through the application's own
// constructor — so the tests connect and migrate exactly as the app does — and
// then bounds it to what this suite needs. db.OpenMySQL is left alone: 25/10 is
// the application's budget on its own server.
func openSharedMySQLPool(dsn string) (*db.Pool, error) {
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

// TestMySQLHarness_OnePoolPerProcess pins the two facts that make one pool per
// process correct, because either one alone is a bug: a second stack must hand
// out the same handle (otherwise -count>1 fans pools out again and the ceiling
// comes back), and it must still start from an empty schema (because that, not a
// fresh pool, is what keeps subtests isolated).
func TestMySQLHarness_OnePoolPerProcess(t *testing.T) {
	ctx := context.Background()

	first := newMySQLTitleStack(t)
	_, err := first.svc.CreateNote(ctx, first.userID, first.user.UUID, nil, "Seeded By The First Stack", nil)
	if err != nil {
		t.Fatalf("seed the first stack: %v", err)
	}

	second := newMySQLTitleStack(t)
	if first.writeDB != second.writeDB {
		t.Fatal("two stacks opened two pools — a repeated run would exhaust max_connections again")
	}
	if got, want := second.writeDB.Stats().MaxOpenConnections, testPoolMaxOpen; got != want {
		t.Fatalf("shared pool MaxOpenConnections = %d, want %d", got, want)
	}
	all, err := second.svc.ListAllNotes(ctx, second.userID)
	if err != nil {
		t.Fatalf("list notes on the second stack: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("the second stack saw %d note(s) from the first — truncation no longer isolates subtests", len(all))
	}
}
