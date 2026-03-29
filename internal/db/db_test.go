package db

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDB_Open_And_Close(t *testing.T) {
	dir := t.TempDir()
	pool, err := Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	require.NotNil(t, pool)

	err = pool.WriteDB.Ping()
	require.NoError(t, err)

	pool.Close()
}

func TestDB_Open_InvalidPath(t *testing.T) {
	// Pass a path in a non-existent directory.
	_, err := Open("/nonexistent/path/to/db.sqlite")
	require.Error(t, err)
}

func TestDB_Close_NilSafe(t *testing.T) {
	// Create a Pool with nil DBs — Close should not panic.
	p := &Pool{ReadDB: nil, WriteDB: nil}
	assert.NotPanics(t, func() {
		p.Close()
	})
}
