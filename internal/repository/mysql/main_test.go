package mysql

import (
	"os"
	"testing"

	"github.com/th0rn0/thornotes/internal/dbtest"
)

// TestMain serialises this package against internal/db, which shares the same
// MySQL test database and leaves schema_migrations dirty on purpose while it
// tests the refusal path. Without the lock, openTestMySQL can migrate in that
// window and fail with "schema version 7 is dirty" — a failure about the other
// package's fixture, not about anything in here.
func TestMain(m *testing.M) {
	os.Exit(dbtest.Locked(mysqlDSNEnv, m.Run))
}
