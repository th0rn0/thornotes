package mysql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the harness invariants that keep a repeated run inside the
// server's connection ceiling (ticket 500). They are not about repository
// behaviour: if one fails, `go test -count=3` will exhaust a 151-connection
// MariaDB with Error 1040 long before any assertion below it runs, and the
// failure will look like dozens of unrelated repository tests breaking at
// repos_test.go's openTestMySQL call.

// Every test must be handed the same pool, or the suite is back to one pool per
// test — 297 of them at -count=3 — and the exhaustion returns.
func TestMySQLHarness_SharedPool_IsOnePerProcess(t *testing.T) {
	first := openTestMySQL(t)
	second := openTestMySQL(t)

	assert.Same(t, first, second,
		"both calls must return the TestMain-owned pool, not a per-test one")
	assert.Same(t, first.WriteDB, sharedPool.WriteDB)
}

// The pool must be bounded well below both the server ceiling and the
// application's own budget. database/sql exposes no getter for the lifetime and
// idle-time limits, so those are pinned by the constants and applied in
// openSharedTestPool; the observable half is the connection cap.
func TestMySQLHarness_SharedPool_IsBounded(t *testing.T) {
	pool := openTestMySQL(t)
	stats := pool.WriteDB.Stats()

	assert.Equal(t, testPoolMaxOpen, stats.MaxOpenConnections,
		"the pool must carry the harness cap, not database/sql's unlimited default")
	assert.Less(t, stats.MaxOpenConnections, 25,
		"the suite must stay below db.OpenMySQL's application budget of 25 per pool")
	assert.LessOrEqual(t, testPoolMaxIdle, testPoolMaxOpen,
		"idle connections cannot exceed open ones")
	assert.Positive(t, testConnMaxLifetime,
		"a connection the server dropped must not hold its slot indefinitely")
	assert.Positive(t, testConnMaxIdleTime)
}

// Sharing a pool must not mean sharing rows: the schema reset is what makes one
// pool safe across tests, so the second open — which is exactly what the next
// test receives — must see an empty database.
func TestMySQLHarness_TruncationStillIsolates(t *testing.T) {
	pool := openTestMySQL(t)
	createMySQLUser(t, pool)

	var seeded int
	require.NoError(t, pool.ReadDB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM users").Scan(&seeded))
	require.Equal(t, 1, seeded, "precondition: the fixture row must be visible")

	again := openTestMySQL(t)

	var after int
	require.NoError(t, again.ReadDB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM users").Scan(&after))
	assert.Equal(t, 0, after, "a shared pool must still hand the next test a clean schema")

	// A fresh user with the same fixed username must therefore be creatable:
	// this is the concrete failure ("username already taken") that per-test
	// truncation exists to prevent.
	createMySQLUser(t, again)
}
