// Package dbtest holds helpers for tests that share one real MariaDB/MySQL
// database across parallel test packages.
//
// `go test ./...` runs packages in parallel processes. Two packages that both
// run migrations against the same schema — and one that deliberately leaves it
// dirty to prove the refusal — will interleave, and the loser reads whatever
// state the winner left behind. That is not a flaky assertion; it is two
// writers on one resource, so the fix is a lock, not a retry.
//
// Two things make the lock hold in practice, and both live here:
//
//   - Locked takes MariaDB's GET_LOCK for the whole of a package's run, so the
//     packages that share the schema take turns. That lock is not new (#39); it
//     is what already keeps a parallel `go test ./...` green, and every package
//     that has since started touching MySQL adopted it.
//   - TruncateAll refuses to empty the schema unless this process holds that
//     lock, so a package that opens MySQL and truncates without taking it fails
//     with the rule in the message instead of racing another package and
//     reporting it as a search regression.
//
// The lock is the only isolation available here: the test user may not create a
// database (GRANT ALL ON thornotes_test.*, no CREATE), so a schema per package
// is not an option on either the CI service container or a shared dev server.
package dbtest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// LockName is the MariaDB advisory lock guarding the shared test schema.
//
// It is deliberately a constant and must stay one: a run of an older checkout
// and a run of a newer one against the same schema have to contend for the same
// name, or the two builds stop excluding each other and the collision returns
// for whoever upgrades second. TestLockName_IsStable pins it.
const LockName = "thornotes_test_db"

// lockTimeout bounds how long one package waits for the others to finish.
//
// It has to cover the *whole queue*, not one package: five packages now share
// this lock (internal/db, internal/dbtest, internal/repository,
// internal/repository/mysql, internal/notes), so the last to arrive waits for the
// sum of the rest. Measured under -race with coverage on a loaded 6-vCPU host,
// internal/notes alone ran 916s and the two cheap packages behind it gave up at
// 600s — a red suite caused by arithmetic, not by a test. At 1800s the queue
// drains even there.
//
// Keep it strictly below the per-binary `-timeout` (Makefile's test-with-db and
// CI's test step pass 2400s): a waiter that cannot get the lock must print the
// refusal below, not die of a test-binary timeout with a stack trace, which is
// the same event told in a way nobody can act on.
const lockTimeout = 1800 // seconds

// lockName is LockName, except inside this package's own tests, which point the
// helper at scratch names to exercise the refusal paths without waiting out
// lockTimeout.
var lockName = LockName

// holderID is the connection id of the session that holds lockName for this
// process, or 0 when this process holds nothing. TruncateAll compares it with
// the server's own answer so that a lock that was released underneath us — an
// idle session the server reaped, a package that never took it — is a refusal
// rather than a silently unguarded delete.
var (
	holderMu sync.RWMutex
	holderID int64
)

func setHolder(id int64) {
	holderMu.Lock()
	holderID = id
	holderMu.Unlock()
}

func heldBy() int64 {
	holderMu.RLock()
	defer holderMu.RUnlock()
	return holderID
}

// A suite that cannot be sure it is alone exits 1 rather than reporting results:
// a green run obtained by racing is worse than no run.
// reexecEnv marks a test binary that another test re-executed with
// os.Args[0] to run one test in a child process with a different environment
// (internal/db does this to check the migration guard). Such a child is not a
// parallel package: its parent already holds the advisory lock, and taking it
// again would block for the whole timeout — a deadlock, not a race.
const reexecEnv = "THORNOTES_TEST_REEXEC"

// Locked runs run() while holding an advisory lock on the shared test database,
// and returns its exit code.
//
// The lock is MariaDB's GET_LOCK, which needs no privilege beyond what the test
// DSN already has and works across processes and hosts. When envVar is unset the
// suites skip themselves, so run() is called with no lock and those skips happen
// for the ordinary reason.
func Locked(envVar string, run func() int) int {
	if os.Getenv(reexecEnv) != "" {
		return run()
	}

	dsn := os.Getenv(envVar)
	if dsn == "" {
		return run()
	}

	return lockedWith(dsn, lockTimeout, run)
}

// lockedWith is Locked's body with the timeout exposed, so a test can prove what
// happens when the lock cannot be had without spending ten minutes proving it.
func lockedWith(dsn string, timeout int, run func() int) int {
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

	ctx := context.Background()
	// Pin the session: GET_LOCK, CONNECTION_ID and the lock's lifetime are all
	// properties of this one connection, so it must not be handed back to the
	// pool between the two queries.
	conn, err := db.Conn(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: pin lock connection: %v\n", err)
		return 1
	}
	defer func() { _ = conn.Close() }()

	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", lockName, timeout).Scan(&got); err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: GET_LOCK(%q): %v\n", lockName, err)
		return 1
	}
	if !got.Valid || got.Int64 != 1 {
		fmt.Fprintf(os.Stderr,
			"dbtest: could not acquire %q within %ds — another package still holds the shared test database, refusing to race it\n",
			lockName, timeout)
		return 1
	}

	var id int64
	if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: CONNECTION_ID(): %v\n", err)
		return 1
	}
	// Restore rather than clear: this package's own tests call lockedWith with a
	// scratch name while TestMain's lock is still held, and clearing the holder
	// would leave the rest of the package's truncations refused.
	previous := heldBy()
	setHolder(id)
	defer setHolder(previous)

	return run()
}

// Tables is every application table, children before parents. One list on
// purpose: it was copied into three packages, and a table added to the schema
// would be emptied by whichever copy was remembered.
//
// TestTables_CoversEverySchemaTable is what makes "a table added to the schema"
// a test failure rather than a habit: migration 012 (`folder_shares`, ticket 616)
// was added without this list being told, so every package that truncated the
// schema left stale folder links behind — invisible until the first folder-share
// tests ran after another package's truncation and found rows pointing at
// folders that no longer existed.
var Tables = []string{
	"api_token_folder_permissions",
	"api_tokens",
	"journals",
	"note_links",
	"notes",
	"folder_shares",
	"sessions",
	"folders",
	"users",
}

// TruncateAll empties every application table so a test starts from a
// known-empty schema. Foreign key checks are off for the duration so table order
// does not matter; schema_migrations is left alone.
//
// It refuses — before deleting anything — unless this process holds the advisory
// lock TruncateAll's caller took in TestMain, because an unguarded truncation is
// exactly the defect of ticket 731: one package deletes the rows another package
// is midway through asserting on, and the loser reports a product bug.
func TruncateAll(ctx context.Context, handle *sql.DB) error {
	conn, err := handle.Conn(ctx)
	if err != nil {
		return fmt.Errorf("dbtest: acquire connection to truncate: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := assertHoldsLock(ctx, conn); err != nil {
		return err
	}

	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		return fmt.Errorf("dbtest: disable foreign key checks: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SET FOREIGN_KEY_CHECKS = 1")
	}()

	for _, table := range Tables {
		if _, err := conn.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return fmt.Errorf("dbtest: reset table %s: %w", table, err)
		}
	}
	return nil
}

// assertHoldsLock asks the server who holds the shared lock and refuses unless
// the answer is this process's own lock session.
//
// The two refusals are different problems and say so: a free lock means the
// package never called Locked, an id that is not ours means another package (or
// another build on the same server) is using the schema right now.
func assertHoldsLock(ctx context.Context, conn *sql.Conn) error {
	var used sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT IS_USED_LOCK(?)", lockName).Scan(&used); err != nil {
		return fmt.Errorf("dbtest: IS_USED_LOCK(%q): %w", lockName, err)
	}

	held := heldBy()
	switch {
	case !used.Valid:
		return fmt.Errorf(
			"dbtest: %q is not held — this package is about to empty the shared MySQL test schema without taking the lock first; add `func TestMain(m *testing.M) { os.Exit(dbtest.Locked(\"THORNOTES_TEST_MYSQL_DSN\", m.Run)) }` to the package, or `go test ./...` runs it against another package's rows",
			lockName)
	case held == 0 || used.Int64 != held:
		return fmt.Errorf(
			"dbtest: %q is held by connection %d, not by this process (%d) — refusing to empty a schema another package is working on",
			lockName, used.Int64, held)
	}
	return nil
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
