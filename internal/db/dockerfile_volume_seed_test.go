package db

import (
	"os"
	"strings"
	"testing"
)

// The image seeds a fresh Docker volume with uid 65532 ownership so the
// documented quick start (`docker run -v thornotes-data:/data ...`) works on
// a brand-new volume: without the seeded, pre-owned /data the volume is
// root-owned and SQLite fails with "unable to open database file" on the very
// first run. The seeding only happens when BOTH halves are present — a /data
// directory owned by 65532 in the image (COPY --chown) and VOLUME /data
// (which makes Docker copy that content into an empty named or anonymous
// volume at first mount). Dropping either reintroduces the first-run failure,
// and nothing else in the unit suite would notice, so this guard checks both
// lines the same way TestDockerfile_SetsTmpDirForSQLiteTemp and
// TestDockerfile_SchemaLabelsMatchMigrations check their build inputs.
func TestDockerfile_SeedsFreshVolumesForUid65532(t *testing.T) {
	raw, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	dockerfile := string(raw)

	if !strings.Contains(dockerfile, "COPY --from=builder --chown=65532:65532 /data/") {
		t.Fatal("Dockerfile does not copy a pre-owned /data into the final stage: a fresh " +
			"Docker volume is root-owned and the container (uid 65532) then fails to open " +
			"its SQLite database on first run")
	}
	if !strings.Contains(dockerfile, "VOLUME /data") {
		t.Fatal("Dockerfile does not declare VOLUME /data: without it Docker does not seed " +
			"an empty named volume from the image's pre-owned /data, so the first-run " +
			"volume stays root-owned and unwritable by uid 65532")
	}
}
