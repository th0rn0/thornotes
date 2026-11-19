package dbtest

// Tests for the shared-MySQL harness itself (ticket 731).
//
// The defect: `go test ./...` runs packages in parallel, and internal/repository
// and internal/notes both seed and truncate the same `thornotes_test` schema, so
// one package's truncation deleted the rows the other was midway through
// asserting on. It surfaced as three search failures — TestSearchAccents_MySQL,
// TestSearchContract_MySQL/single_term and TestN7_TitleRepair/mysql/search_finds
// — on a branch that had changed nothing on the search path.
//
// The guard is the advisory lock in Locked, and these tests pin both halves of
// it: what the lock has to be (a stable name, so two builds on one server still
// exclude each other), and what happens to a caller who truncates without it
// (a refusal that names the rule, rather than a race decided in microseconds).
//
// Everything that talks to a server skips itself when THORNOTES_TEST_MYSQL_DSN
// is unset, exactly as the suites it serves do.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appdb "github.com/th0rn0/thornotes/internal/db"
)

const testDSNEnv = "THORNOTES_TEST_MYSQL_DSN"

// TestMain takes the same lock every MySQL-touching package takes. This package
// is not exempt from its own rule: its tests truncate the schema too, and the
// static guard below would (correctly) flag it if it did not.
func TestMain(m *testing.M) {
	os.Exit(Locked(testDSNEnv, m.Run))
}

// scratchLock is the lock name the refusal tests use, so that "nobody holds it"
// and "somebody else holds it" can both be arranged without contending with the
// real lock this process holds for the length of TestMain.
const scratchLock = "thornotes_test_db_731_scratch"

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(testDSNEnv)
	if dsn == "" {
		t.Skip(testDSNEnv + " not set — skipping the MariaDB/MySQL harness tests")
	}
	return dsn
}

// withLockName points the helper at name for the duration of the test.
func withLockName(t *testing.T, name string) {
	t.Helper()
	previous := lockName
	lockName = name
	t.Cleanup(func() { lockName = previous })
}

// ensureSchemaOnce runs the application's migrations against the shared test
// database, at most once per process.
//
// Nothing else does it for this package. The suites that share this schema
// (internal/db, internal/repository, internal/repository/mysql, internal/notes)
// each migrate it in their own TestMain, so this package's tests opened a bare
// connection to a database nobody had created tables in and the first INSERT
// failed with `Table 'thornotes_test.users' doesn't exist`. That is how build
// 414 went red against the very branch that added this file: locally it passed
// because an earlier run of another package had already migrated the schema, and
// in CI this package was the first thing to touch an empty server.
//
// It is safe against the other packages because this package's TestMain takes
// the same advisory lock before any test runs, so no other suite can be midway
// through a migration while these tables are created.
var (
	ensureSchemaOnce sync.Once
	ensureSchemaErr  error
)

func ensureSchema(dsn string) error {
	ensureSchemaOnce.Do(func() {
		pool, err := appdb.OpenMySQL(dsn)
		if err != nil {
			ensureSchemaErr = err
			return
		}
		pool.Close()
	})
	return ensureSchemaErr
}

func openTestDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	require.NoError(t, ensureSchema(dsn), "migrating the shared test schema")
	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(2)
	require.NoError(t, db.Ping())
	return db
}

// fixture is what seedOneUserAndNote wrote, so an assertion can name the rows it
// is checking for instead of counting the whole table — this schema is shared,
// and rows from an earlier failed run are not this test's business.
type fixture struct {
	username string
	diskPath string
}

// seedOneUserAndNote puts a row in each of the two tables a MySQL leg asserts
// on, so "the schema was emptied" is a claim about real data rather than about
// a database that happened to be empty already.
//
// The write is idempotent: the tests that prove a truncation is *refused* leave
// their rows behind on purpose, and username and disk_path are unique.
func seedOneUserAndNote(t *testing.T, db *sql.DB) fixture {
	t.Helper()
	suffix := strings.NewReplacer("/", "-", " ", "-", "_", "-").Replace(t.Name())
	f := fixture{username: "harness-731-" + suffix, diskPath: "harness-731/" + suffix + ".md"}

	ctx := context.Background()
	res, err := db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash) VALUES (?, ?)
		 ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`,
		f.username, "$2a$12$harnesshashharnesshashharnesshashharnesshashharnes")
	require.NoError(t, err)
	userID, err := res.LastInsertId()
	require.NoError(t, err)

	_, err = db.ExecContext(ctx,
		`INSERT INTO notes (user_id, title, slug, disk_path, tags) VALUES (?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE id = id`,
		userID, "Harness note", "harness-note-"+suffix, f.diskPath, "[]")
	require.NoError(t, err)
	return f
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM "+table).Scan(&n))
	return n
}

func countFixture(t *testing.T, db *sql.DB, f fixture) (users, notes int) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM users WHERE username = ?", f.username).Scan(&users))
	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM notes WHERE disk_path = ?", f.diskPath).Scan(&notes))
	return users, notes
}

// TestTruncateAll_EmptiesEveryTable is the positive path: a package that took
// the lock may empty the schema, and every table in Tables really is emptied —
// the list is what keeps a table added to the schema from being emptied by
// whichever of the three copies used to be remembered.
func TestTruncateAll_EmptiesEveryTable(t *testing.T) {
	db := openTestDB(t, testDSN(t))
	f := seedOneUserAndNote(t, db)
	users, notes := countFixture(t, db, f)
	require.Equal(t, 1, users, "precondition: the fixture user is visible")
	require.Equal(t, 1, notes, "precondition: the fixture note is visible")

	migrations := countRows(t, db, "schema_migrations")

	require.NoError(t, TruncateAll(context.Background(), db))

	for _, table := range Tables {
		assert.Zero(t, countRows(t, db, table), "%s must be empty after TruncateAll", table)
	}
	assert.Equal(t, migrations, countRows(t, db, "schema_migrations"),
		"the migration ledger is not application data and must survive")
}

// TestTables_CoversEverySchemaTable is the guard the list above needs: it asks
// the server which tables exist and fails when one of them is not in Tables.
//
// Without it, a migration that adds a table leaves Tables short and every
// truncation silently leaves that table's rows behind, which presents as a
// product failure in whichever suite runs next (ticket 616 slice 1 added
// `folder_shares` and hit exactly this). schema_migrations is the one exception:
// it is the migration ledger, not application data.
func TestTables_CoversEverySchemaTable(t *testing.T) {
	db := openTestDB(t, testDSN(t))

	rows, err := db.Query(`SHOW TABLES`)
	require.NoError(t, err)
	defer rows.Close()

	var onServer []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		if name == "schema_migrations" {
			continue
		}
		onServer = append(onServer, name)
	}
	require.NoError(t, rows.Err())

	known := map[string]bool{}
	for _, table := range Tables {
		known[table] = true
	}
	for _, table := range onServer {
		assert.True(t, known[table],
			"the schema has a table Tables does not: %q must be added to dbtest.Tables, "+
				"or every truncation leaves its rows behind for the next suite", table)
	}
}

// TestTruncateAll_RefusesWhenTheLockIsFree is the package that forgot TestMain:
// the lock is held by nobody, so the truncation would be unguarded. It must be
// refused, and refused before the first DELETE — a refusal that had already
// emptied half the schema would be the defect with better logging.
func TestTruncateAll_RefusesWhenTheLockIsFree(t *testing.T) {
	db := openTestDB(t, testDSN(t))
	withLockName(t, scratchLock)
	f := seedOneUserAndNote(t, db)

	err := TruncateAll(context.Background(), db)

	require.Error(t, err)
	assert.Contains(t, err.Error(), scratchLock)
	assert.Contains(t, err.Error(), "is not held")
	assert.Contains(t, err.Error(), "dbtest.Locked", "the refusal has to name the fix")
	users, notes := countFixture(t, db, f)
	assert.Equal(t, 1, users, "a refused truncation must delete nothing")
	assert.Equal(t, 1, notes)
}

// TestTruncateAll_RefusesWhenAnotherSessionHoldsTheLock is the live collision:
// another package (or another build, on a shared dev server) is using the same
// schema right now. This is the case that used to be silent.
func TestTruncateAll_RefusesWhenAnotherSessionHoldsTheLock(t *testing.T) {
	dsn := testDSN(t)
	db := openTestDB(t, dsn)
	withLockName(t, scratchLock)
	f := seedOneUserAndNote(t, db)

	ctx := context.Background()
	holder, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = holder.Close() })
	holder.SetMaxOpenConns(1)
	conn, err := holder.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	var got sql.NullInt64
	require.NoError(t, conn.QueryRowContext(ctx,
		"SELECT GET_LOCK(?, ?)", scratchLock, 1).Scan(&got))
	require.True(t, got.Valid && got.Int64 == 1, "the stand-in package must hold the lock")

	err = TruncateAll(ctx, db)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "held by connection")
	assert.Contains(t, err.Error(), "not by this process")
	users, notes := countFixture(t, db, f)
	assert.Equal(t, 1, users, "a refused truncation must delete nothing")
	assert.Equal(t, 1, notes)
}

// TestLocked_RunsWithoutADSN is the ordinary local case: no DSN, so the suites
// skip themselves and the lock is not attempted at all.
func TestLocked_RunsWithoutADSN(t *testing.T) {
	ran := false
	code := Locked("THORNOTES_TEST_NO_SUCH_DSN_VARIABLE", func() int {
		ran = true
		return 7
	})
	assert.True(t, ran)
	assert.Equal(t, 7, code)
}

// TestLocked_ReexecChildDoesNotRelock pins the escape hatch internal/db relies
// on: a child process re-executed by a test already runs inside its parent's
// lock, so taking it again would block for the whole timeout.
func TestLocked_ReexecChildDoesNotRelock(t *testing.T) {
	t.Setenv(reexecEnv, "1")

	ran := false
	code := Locked(testDSNEnv, func() int {
		ran = true
		return 3
	})
	assert.True(t, ran, "a re-executed child must not try to take the lock")
	assert.Equal(t, 3, code)
}

// TestLocked_RefusesWhenTheLockIsTaken is "two packages, one schema" at the lock
// itself, with a one-second timeout instead of ten minutes.
func TestLocked_RefusesWhenTheLockIsTaken(t *testing.T) {
	dsn := testDSN(t)
	withLockName(t, scratchLock)

	ctx := context.Background()
	holder, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = holder.Close() })
	holder.SetMaxOpenConns(1)
	conn, err := holder.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	var got sql.NullInt64
	require.NoError(t, conn.QueryRowContext(ctx,
		"SELECT GET_LOCK(?, ?)", scratchLock, 1).Scan(&got))
	require.True(t, got.Valid && got.Int64 == 1)

	before := heldBy()
	ran := false
	code := lockedWith(dsn, 1, func() int {
		ran = true
		return 0
	})

	assert.Equal(t, 1, code, "a package that cannot get the lock exits 1 rather than racing")
	assert.False(t, ran, "the suite must not run without the lock")
	assert.Equal(t, before, heldBy(), "a failed acquisition must not change the recorded holder")
}

// TestLocked_RecordsTheHolder is what makes TruncateAll's check mean something:
// while Locked holds the lock, TruncateAll is allowed; the id it recorded is the
// id the server reports as the holder. When the call returns, the holder must go
// back to what it was — this package's TestMain lock — and not be cleared, or the
// rest of the package's truncations would be refused.
func TestLocked_RecordsTheHolder(t *testing.T) {
	dsn := testDSN(t)
	withLockName(t, scratchLock)

	before := heldBy()
	require.NotZero(t, before, "TestMain must be holding the lock for this test to mean anything")

	var observed int64
	code := lockedWith(dsn, 5, func() int {
		observed = heldBy()
		require.NotZero(t, observed, "Locked must record the session that holds the lock")
		require.NoError(t, TruncateAll(context.Background(), openTestDB(t, dsn)))
		return 0
	})

	assert.Zero(t, code)
	assert.NotZero(t, observed)
	assert.NotEqual(t, before, observed, "the scratch lock is a different session from TestMain's")
	assert.Equal(t, before, heldBy(), "the holder is restored, so later truncations stay guarded")
}

// TestLockName_IsStable pins the one thing about the lock name that is not free
// to change: two builds of this repository must contend for the same name, or an
// older checkout running against the same schema stops excluding a newer one and
// the collision returns for whoever upgraded second.
func TestLockName_IsStable(t *testing.T) {
	assert.Equal(t, "thornotes_test_db", LockName,
		"changing the lock name splits the mutex between builds sharing one schema")
}

// TestEveryMySQLPackageTakesTheLock is the static half of the guard: the runtime
// check only fires for a package that truncates, while a package that merely
// opens MySQL is broken the same way by somebody else's truncation and would
// never call it. Such a package is caught here, at review time, instead of
// producing a 1-in-15 failure that reads as a product bug.
func TestEveryMySQLPackageTakesTheLock(t *testing.T) {
	root := repoRoot(t)

	usesMySQL := regexp.MustCompile(`"THORNOTES_TEST_MYSQL_DSN"|\bOpenMySQL\(`)
	takesLock := regexp.MustCompile(`os\.Exit\((?:dbtest\.)?Locked\(`)

	type dirState struct {
		rel      string
		usesMySQ bool
		locks    bool
	}
	dirs := map[string]*dirState{}

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			if path == root {
				return nil
			}
			// Hidden trees (".git", ".worktrees") and dependency trees are not
			// this module's tests.
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, "_test.go") {
			return nil
		}

		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relDir, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		d, ok := dirs[relDir]
		if !ok {
			d = &dirState{rel: relDir}
			dirs[relDir] = d
		}
		if usesMySQL.Match(body) {
			d.usesMySQ = true
		}
		if takesLock.Match(body) {
			d.locks = true
		}
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, dirs, "the walk found no test files — the root is wrong")

	var unguarded []string
	var checked int
	for _, d := range dirs {
		if !d.usesMySQ {
			continue
		}
		checked++
		if !d.locks {
			unguarded = append(unguarded, d.rel)
		}
	}

	assert.Positive(t, checked, "expected at least one package to use the MySQL test DSN")
	assert.Empty(t, unguarded,
		"these packages touch the shared MySQL test schema but never take dbtest.Locked, so `go test ./...` runs them against another package's rows: %s",
		strings.Join(unguarded, ", "))
}

// repoRoot is the module root, two levels above internal/dbtest.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, "go.mod"))
	require.NoError(t, err, "expected the module root at %s", root)
	return root
}
