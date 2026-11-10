package db

import (
	"os"
	"strings"
	"testing"
)

// The image is `scratch`: no /tmp, and when TMPDIR is unset SQLite's temp-file
// fallback chain ends at the current directory, which is / and unwritable by the
// container's uid. SQLite then reports SQLITE_IOERR_DELETE_NOENT (6410) — "disk
// I/O error" — rather than anything that mentions temp files, which is why this
// took a runner-side reproduction to find.
//
// The guard is a text check on the Dockerfile because the alternative is a test
// that builds and runs an image, which the unit suite cannot do. It is the same
// shape as TestDockerfile_SchemaLabelsMatchMigrations: a build input that no Go
// test would otherwise notice.
func TestDockerfile_SetsTmpDirForSQLiteTemp(t *testing.T) {
	raw, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	dockerfile := string(raw)

	if !strings.Contains(dockerfile, "FROM scratch") {
		t.Skip("not a scratch image any more; revisit this guard")
	}
	if !strings.Contains(dockerfile, "ENV TMPDIR=") {
		t.Fatal("Dockerfile does not set TMPDIR: a scratch image has no /tmp, and SQLite " +
			"temp-file fallback then ends at / (unwritable), which surfaces as " +
			"SQLITE_IOERR_DELETE_NOENT during migration")
	}
}
