package db

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The migration sets are the one part of the schema floor that can rot silently:
// a version with an `up` but no `down` breaks the rollback path, a gap in the
// numbering breaks the source driver's walk, and an embedded order that disagrees
// with the filenames breaks the preflight's maxSupported calculation. These
// tests fail CI on any of the three, for both dialects.

var migrationFilenameRe = regexp.MustCompile(`^(\d+)_(.+)\.(up|down)\.sql$`)

// parseMigrationFilename splits 008_note_links.up.sql into (8, "up", true).
func parseMigrationFilename(name string) (version int, direction string, ok bool) {
	m := migrationFilenameRe.FindStringSubmatch(name)
	if m == nil {
		return 0, "", false
	}
	v, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, "", false
	}
	return v, m[3], true
}

// lintMigrationSet reports structural problems with one dialect's migration
// directory: versions missing an up or a down half, mismatched base names, gaps
// in the numbering, duplicate versions, and file order that does not match the
// version order. An empty directory is a problem too, so the check cannot pass
// vacuously.
func lintMigrationSet(fsys fs.FS, dir string) ([]string, error) {
	entries, err := fs.ReadDir(fsys, dir) // fs.ReadDir sorts by filename
	if err != nil {
		return nil, err
	}

	type halves struct {
		upName, downName string
		up, down         bool
	}
	versions := map[int]*halves{}
	var fileOrder []int

	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		version, direction, ok := parseMigrationFilename(entry.Name())
		if !ok {
			return nil, fmt.Errorf("%s/%s: not a migration filename (want NNN_name.up.sql or NNN_name.down.sql)", dir, entry.Name())
		}
		h := versions[version]
		if h == nil {
			h = &halves{}
			versions[version] = h
		}
		switch direction {
		case "up":
			if h.up {
				return nil, fmt.Errorf("%s: duplicate up migration for version %d", dir, version)
			}
			h.up, h.upName = true, entry.Name()
		case "down":
			if h.down {
				return nil, fmt.Errorf("%s: duplicate down migration for version %d", dir, version)
			}
			h.down, h.downName = true, entry.Name()
		}
		fileOrder = append(fileOrder, version)
	}

	var problems []string
	if len(versions) == 0 {
		return []string{dir + ": no migrations found"}, nil
	}

	numbers := make([]int, 0, len(versions))
	for v := range versions {
		numbers = append(numbers, v)
	}
	sort.Ints(numbers)

	for _, v := range numbers {
		h := versions[v]
		switch {
		case !h.up && h.down:
			problems = append(problems, fmt.Sprintf(
				"%s: version %03d has an up-less down migration: %s exists but %s is missing",
				dir, v, h.downName, strings.TrimSuffix(h.downName, ".down.sql")+".up.sql"))
		case h.up && !h.down:
			problems = append(problems, fmt.Sprintf(
				"%s: version %03d has no down migration: %s is missing",
				dir, v, strings.TrimSuffix(h.upName, ".up.sql")+".down.sql"))
		case h.up && h.down:
			if base := strings.TrimSuffix(h.upName, ".up.sql"); base+".down.sql" != h.downName {
				problems = append(problems, fmt.Sprintf(
					"%s: version %03d up/down base names disagree: %s vs %s",
					dir, v, h.upName, h.downName))
			}
		}
	}

	// Contiguous from 001, with no gaps: the source driver's Next() walk stops at
	// the first missing version, so a gap silently truncates maxSupported.
	for i, v := range numbers {
		if want := i + 1; v != want {
			problems = append(problems, fmt.Sprintf(
				"%s: versions are not contiguous from 001: expected %03d, found %03d", dir, want, v))
		}
	}

	// File order must agree with version order, and each version must appear
	// exactly twice (up + down, adjacent) — this is what the embedded FS hands to
	// the source driver.
	if !sort.IntsAreSorted(fileOrder) {
		problems = append(problems, fmt.Sprintf(
			"%s: file order does not match version order: %v", dir, fileOrder))
	}
	for i := 0; i < len(fileOrder); i += 2 {
		if i+1 >= len(fileOrder) || fileOrder[i] != fileOrder[i+1] {
			problems = append(problems, fmt.Sprintf(
				"%s: version %03d does not have exactly one up and one down file in order", dir, fileOrder[i]))
			break
		}
	}

	return problems, nil
}

// sqliteMigrationsForTest returns the SQLite migration set to lint. The negative
// control points this at a mutated copy so that "a missing down file fails" is
// demonstrated by an actual failing run rather than asserted in prose.
func sqliteMigrationsForTest(t *testing.T) fs.FS {
	t.Helper()
	if dir := os.Getenv("THORNOTES_TEST_MIGRATIONS_DIR"); dir != "" {
		return os.DirFS(dir)
	}
	return migrationsFS
}

func TestMigrations_SQLite_Structure(t *testing.T) {
	problems, err := lintMigrationSet(sqliteMigrationsForTest(t), "migrations")
	require.NoError(t, err)
	assert.Empty(t, problems, "SQLite migrations are structurally invalid:\n%s", strings.Join(problems, "\n"))
}

func TestMigrations_MySQL_Structure(t *testing.T) {
	problems, err := lintMigrationSet(mysqlMigrationsFS, "mysql_migrations")
	require.NoError(t, err)
	assert.Empty(t, problems, "MySQL migrations are structurally invalid:\n%s", strings.Join(problems, "\n"))
}

// filenameVersionOrder is the version order implied by the file names alone.
func filenameVersionOrder(t *testing.T, fsys fs.FS, dir string) []int {
	t.Helper()
	entries, err := fs.ReadDir(fsys, dir)
	require.NoError(t, err)

	var order []int
	for _, entry := range entries {
		version, _, ok := parseMigrationFilename(entry.Name())
		if !ok || entry.IsDir() {
			continue
		}
		if len(order) > 0 && order[len(order)-1] == version {
			continue // up + down of the same version
		}
		order = append(order, version)
	}
	return order
}

// sourceVersionOrder is the order the source driver actually walks — what the
// preflight's maxSupportedVersion sees.
func sourceVersionOrder(t *testing.T, fsys fs.FS, dir string) []int {
	t.Helper()
	src, err := iofs.New(fsys, dir)
	require.NoError(t, err)

	var order []int
	version, err := src.First()
	require.NoError(t, err)
	for {
		order = append(order, int(version))
		next, err := src.Next(version)
		if errors.Is(err, fs.ErrNotExist) {
			return order
		}
		require.NoError(t, err)
		version = next
	}
}

// TestMigrations_EmbeddedOrderMatchesFilenames checks that what the source
// driver walks is exactly the sequence the file names describe.
func TestMigrations_EmbeddedOrderMatchesFilenames(t *testing.T) {
	for _, tc := range []struct {
		name string
		fsys fs.FS
		dir  string
	}{
		{"sqlite", sqliteMigrationsForTest(t), "migrations"},
		{"mysql", mysqlMigrationsFS, "mysql_migrations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := filenameVersionOrder(t, tc.fsys, tc.dir)
			require.NotEmpty(t, want)
			assert.Equal(t, want, sourceVersionOrder(t, tc.fsys, tc.dir),
				"the embedded migration order must match the file names")
		})
	}
}

// TestMigrations_Lint_NegativeControl proves the guard above actually fails when
// a down migration is missing: it copies the real SQLite set to a temp directory,
// deletes 008_note_links.down.sql, and re-runs the structure test in a child
// process. The child must exit non-zero and name the missing file.
func TestMigrations_Lint_NegativeControl(t *testing.T) {
	if os.Getenv("THORNOTES_TEST_MIGRATIONS_DIR") != "" {
		t.Skip("running inside the negative control's child process")
	}

	root := t.TempDir()
	dir := filepath.Join(root, "migrations")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	const missing = "008_note_links.down.sql"
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	require.NoError(t, err)
	copied := 0
	for _, entry := range entries {
		if entry.Name() == missing {
			continue
		}
		data, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, entry.Name()), data, 0o644))
		copied++
	}
	require.Equal(t, len(entries)-1, copied, "the copy must be complete apart from the deleted down file")

	cmd := exec.Command(os.Args[0], "-test.run=^TestMigrations_SQLite_Structure$", "-test.v")
	cmd.Env = append(os.Environ(), "THORNOTES_TEST_MIGRATIONS_DIR="+root)
	out, err := cmd.CombinedOutput()

	require.Error(t, err, "the guard must fail when a down migration is missing; child output:\n%s", out)
	assert.Contains(t, string(out), missing, "the failure must name the missing file; child output:\n%s", out)
	assert.Contains(t, string(out), "FAIL", "the child test run must report FAIL; child output:\n%s", out)
}
