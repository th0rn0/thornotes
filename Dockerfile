# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

WORKDIR /build

# Download dependencies separately so this layer is cached on code-only changes.
COPY go.mod go.sum ./
RUN go mod download

# Build a fully static binary.
# CGO_ENABLED=0 is explicit: modernc.org/sqlite is pure Go, but this ensures
# no CGO sneaks in from transitive deps and the binary stays scratch-compatible.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -trimpath \
    -o /thornotes \
    ./cmd/thornotes

# ── Final stage ───────────────────────────────────────────────────────────────
FROM scratch

COPY --from=builder /thornotes /thornotes

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
