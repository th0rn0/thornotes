package db

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The image's co.thornotes.schema.max label is what an operator reads to decide
// whether an image is older than a database, so it must never drift from the
// migrations the binary actually embeds. The numbers live in the Dockerfile
// (defaults) and in .drone.yml (build args); this test is the thing that keeps
// them honest, because a stale label is worse than no label.

func readRepoFile(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", name))
	require.NoError(t, err)
	return strings.Split(string(data), "\n")
}

// valueAfter returns the value of the first line containing prefix, trimmed.
func valueAfter(t *testing.T, lines []string, prefix string) string {
	t.Helper()
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		}
	}
	t.Fatalf("no line starting with %q found", prefix)
	return ""
}

func TestDockerfile_SchemaLabelsMatchMigrations(t *testing.T) {
	sqliteSrc, err := iofs.New(migrationsFS, "migrations")
	require.NoError(t, err)
	sqliteMax, err := maxSupportedVersion(sqliteSrc)
	require.NoError(t, err)

	mysqlSrc, err := iofs.New(mysqlMigrationsFS, "mysql_migrations")
	require.NoError(t, err)
	mysqlMax, err := maxSupportedVersion(mysqlSrc)
	require.NoError(t, err)

	dockerfile := readRepoFile(t, "Dockerfile")

	// The label must exist and be wired from the build arg, or `docker inspect`
	// still cannot answer the question.
	joined := strings.Join(dockerfile, "\n")
	assert.Contains(t, joined, `co.thornotes.schema.max="${THORNOTES_SCHEMA_MAX}"`)
	assert.Contains(t, joined, `co.thornotes.schema.max.mysql="${THORNOTES_SCHEMA_MAX_MYSQL}"`)
	assert.Contains(t, joined, `org.opencontainers.image.version="${THORNOTES_VERSION}"`)

	assert.Equal(t, strconv.Itoa(sqliteMax),
		valueAfter(t, dockerfile, "ARG THORNOTES_SCHEMA_MAX="),
		"Dockerfile ARG THORNOTES_SCHEMA_MAX must equal the newest SQLite migration (%d); "+
			"bump it when you add a migration", sqliteMax)
	assert.Equal(t, strconv.Itoa(mysqlMax),
		valueAfter(t, dockerfile, "ARG THORNOTES_SCHEMA_MAX_MYSQL="),
		"Dockerfile ARG THORNOTES_SCHEMA_MAX_MYSQL must equal the newest MySQL migration (%d)", mysqlMax)

	// Every pipeline build arg must agree with the Dockerfile defaults.
	drone := readRepoFile(t, ".drone.yml")
	var checked int
	for _, line := range drone {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- THORNOTES_SCHEMA_MAX") {
			continue
		}
		checked++
		key, value, ok := strings.Cut(strings.TrimPrefix(trimmed, "- "), "=")
		require.True(t, ok, "malformed build arg: %q", trimmed)
		want := sqliteMax
		if key == "THORNOTES_SCHEMA_MAX_MYSQL" {
			want = mysqlMax
		}
		assert.Equal(t, strconv.Itoa(want), value,
			".drone.yml %s build arg disagrees with the migration set", key)
	}
	assert.NotZero(t, checked, ".drone.yml must pass the schema labels as docker build args")
}
