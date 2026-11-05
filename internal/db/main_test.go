package db

import (
	"os"
	"testing"

	"github.com/th0rn0/thornotes/internal/dbtest"
)

// TestMain serialises this package against the other suite that shares the
// MySQL test database.
//
// TestMySQL_DirtyState_RefusesLoudly leaves schema_migrations dirty on purpose
// for the length of the test — that is the state under test. A package running
// in parallel that opens the same database in that window reads the dirty flag
// and fails with a refusal that has nothing to do with its own code. Both
// packages take the same advisory lock, so one finishes before the other
// migrates.
func TestMain(m *testing.M) {
	os.Exit(dbtest.Locked("THORNOTES_TEST_MYSQL_DSN", m.Run))
}
