# ── Build stage ───────────────────────────────────────────────────────────────
# Pinned to 1.26.3: go.mod requires >= 1.26.3 (security patches for
# html/template and x/net HTTP/2). A floating golang:1.26-alpine tag can
# resolve to a stale 1.26.2 on CI runners with GOTOOLCHAIN=local, which
# breaks the build with "go.mod requires go >= 1.26.3".
FROM golang:1.26.3-alpine AS builder

# Build-time version, stamped into the binary (main.Version) and onto the image
# (org.opencontainers.image.version). CI passes the git tag or commit.
ARG THORNOTES_VERSION=dev

WORKDIR /build

# Download dependencies separately so this layer is cached on code-only changes.
COPY go.mod go.sum ./
RUN go mod download

# Build a fully static binary.
# CGO_ENABLED=0 is explicit: modernc.org/sqlite is pure Go, but this ensures
# no CGO sneaks in from transitive deps and the binary stays scratch-compatible.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.Version=${THORNOTES_VERSION}" \
    -trimpath \
    -o /thornotes \
    ./cmd/thornotes

# ── Final stage ───────────────────────────────────────────────────────────────
FROM scratch

COPY --from=builder /thornotes /thornotes

# ── Artefact metadata ────────────────────────────────────────────────────────
# co.thornotes.schema.max is the highest migration this image embeds, so
# `docker inspect` answers "which schema does this image support?" without
# starting it — the question an operator needs answered at 2am, and the one the
# image previously could not answer at all.
#
# A database stamped above this number is refused at startup with a single log
# line naming both numbers (see internal/db/schema.go); it is never migrated
# down automatically.
#
# The defaults below are the SQLite floor for the default driver, and the MySQL
# floor for --db-driver=mysql (whose sequence is numbered differently). They are
# kept in step with internal/db/migrations/ and internal/db/mysql_migrations/ by
# TestDockerfile_SchemaLabelsMatchMigrations, so a new migration that forgets to
# bump them fails CI.
ARG THORNOTES_SCHEMA_MAX=8
ARG THORNOTES_SCHEMA_MAX_MYSQL=7
ARG THORNOTES_VERSION=dev

LABEL co.thornotes.schema.max="${THORNOTES_SCHEMA_MAX}" \
      co.thornotes.schema.max.mysql="${THORNOTES_SCHEMA_MAX_MYSQL}" \
      org.opencontainers.image.version="${THORNOTES_VERSION}"

# Configuration via environment variables:
#   THORNOTES_ADDR              bind address           (default :8080)
#   THORNOTES_DB                SQLite database path   (default /data/thornotes.db)
#   THORNOTES_NOTES_ROOT        notes directory        (default /data/notes)
#   THORNOTES_ALLOW_REGISTRATION                       (default true)
#   THORNOTES_SECURE_COOKIES    set true behind HTTPS  (default false)
#   THORNOTES_TRUSTED_PROXY     CIDR of trusted proxy  (optional)
#   THORNOTES_WATCH_INTERVAL    disk poll interval     (default 30s, set 0 to disable)
#
# Mount a writable volume at /data, e.g.:
#   docker run -v thornotes-data:/data -p 8080:8080 th0rn0/thornotes
#
# The data directory must be writable by UID 65532. On first run:
#   docker run --rm -v thornotes-data:/data busybox chown -R 65532:65532 /data

ENV THORNOTES_ADDR=:8080
ENV THORNOTES_DB=/data/thornotes.db
ENV THORNOTES_NOTES_ROOT=/data/notes

EXPOSE 8080

# Run as a non-root user. Numeric UID works without /etc/passwd in scratch.
USER 65532:65532

ENTRYPOINT ["/thornotes"]
