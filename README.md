# thornotes

> **Disclaimer:** This is a vibe-coded project built for personal research and experimentation. It is not production-hardened. Use at your own risk.

A self-hosted Markdown note-taking app with file-as-canonical storage. Every note is a real `.md` file on disk. The database is an index, not the source of truth.

![thornotes editor — light mode](docs/screenshot-editor.png)

![thornotes syntax highlighting — dark mode](docs/screenshot-syntax-dark.png)

## Features

- Write notes in Markdown with a live preview editor (EasyMDE)
- Syntax highlighting for fenced code blocks — ` ```go `, ` ```yaml `, ` ```json `, and [180+ languages](https://highlightjs.org/)
- Folder tree with lazy-loaded notes
- Full-text search with snippet highlighting
- Tags
- Shareable read-only note links
- MCP server — expose your notes as tools and resources to AI assistants (Claude Desktop, Cursor, etc.)
- API tokens for programmatic access
- Live sync — edits made directly to `.md` files on disk are detected and pushed to open browser tabs via SSE
- Dark mode

## Quick start with Docker

```sh
docker run -d \
  --name thornotes \
  -v thornotes-data:/data \
  -p 8080:8080 \
  th0rn0/thornotes
```

Open [http://localhost:8080](http://localhost:8080), register an account, and start writing.

### Docker Compose

```yaml
services:
  thornotes:
    image: th0rn0/thornotes
    container_name: thornotes
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - thornotes-data:/data
    environment:
      THORNOTES_ADDR: ":8080"
      THORNOTES_DB: "/data/thornotes.db"
      THORNOTES_NOTES_ROOT: "/data/notes"
      THORNOTES_ALLOW_REGISTRATION: "true"   # set to "false" after first user
      # THORNOTES_TRUSTED_PROXY: "172.16.0.0/12"  # uncomment if behind a proxy

volumes:
  thornotes-data:
```

Save as `docker-compose.yml` and run:

```sh
docker compose up -d
```

The `/data` volume holds the SQLite database (`thornotes.db`) and all note files (`notes/`). Back it up with any standard volume backup tool.

### Docker Compose with MySQL

For multi-user or hosted deployments, swap the SQLite default for MySQL (requires MySQL 8.0+):

```yaml
services:
  thornotes:
    image: th0rn0/thornotes
    container_name: thornotes
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - thornotes-notes:/data/notes
    environment:
      THORNOTES_DB_DRIVER: "mysql"
      THORNOTES_DB_DSN: "thornotes:secret@tcp(db:3306)/thornotes?parseTime=true"
      THORNOTES_NOTES_ROOT: "/data/notes"
      THORNOTES_ALLOW_REGISTRATION: "true"
    depends_on:
      db:
        condition: service_healthy

  db:
    image: mysql:8.0
    restart: unless-stopped
    environment:
      MYSQL_DATABASE: thornotes
      MYSQL_USER: thornotes
      MYSQL_PASSWORD: secret
      MYSQL_ROOT_PASSWORD: rootsecret
    volumes:
      - thornotes-db:/var/lib/mysql
    healthcheck:
      test: ["CMD", "mysqladmin", "ping", "-h", "localhost"]
      interval: 5s
      timeout: 5s
      retries: 10

volumes:
  thornotes-notes:
  thornotes-db:
```

## Configuration

All options are available as environment variables and CLI flags.

| Environment variable | Flag | Default | Description |
|---|---|---|---|
| `THORNOTES_ADDR` | `--addr` | `:8080` | Listen address |
| `THORNOTES_DB_DRIVER` | `--db-driver` | `sqlite` | Database driver: `sqlite` or `mysql` |
| `THORNOTES_DB` | `--db` | `thornotes.db` | SQLite database path (sqlite driver only) |
| `THORNOTES_DB_DSN` | `--db-dsn` | _(none)_ | MySQL DSN e.g. `user:pass@tcp(host:3306)/dbname?parseTime=true` (mysql driver only) |
| `THORNOTES_NOTES_ROOT` | `--notes-root` | `notes` | Root directory for `.md` files |
| `THORNOTES_ALLOW_REGISTRATION` | `--allow-registration` | `true` | Allow new user sign-up |
| `THORNOTES_SECURE_COOKIES` | `--secure-cookies` | `false` | Set `Secure` flag on session cookie — enable when serving over HTTPS |
| `THORNOTES_TRUSTED_PROXY` | `--trusted-proxy` | _(none)_ | CIDR of trusted reverse proxy (e.g. `10.0.0.0/8`) — enables `X-Forwarded-For` for rate limiting |
| `THORNOTES_WATCH_INTERVAL` | `--watch-interval` | `30s` | How often to poll the notes directory for external file changes. Set to `0` to disable |

## Running behind a reverse proxy

thornotes expects to be proxied behind nginx, Caddy, or similar. Set `THORNOTES_TRUSTED_PROXY` to your proxy's IP/CIDR so the rate limiter sees real client IPs from `X-Forwarded-For`.

Minimal nginx config:

```nginx
server {
    listen 443 ssl;
    server_name notes.example.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $remote_addr;
        # SSE requires buffering off
        proxy_buffering off;
        proxy_cache off;
    }
}
```

## MCP integration

thornotes exposes a Model Context Protocol server at `POST /mcp` so AI assistants can read and write your notes.

1. Open the **Account** modal in the app
2. Create an API token
3. Copy the connection snippet and paste it into your AI assistant's MCP config

Available MCP tools: `list_notes`, `get_note`, `search_notes`, `create_note`, `update_note`, `list_folders`

## File format

Notes are stored as plain `.md` files under `THORNOTES_NOTES_ROOT`:

```
notes/
  1/                  # user ID
    my-note.md
    Work/
      project.md
```

The file is always the authoritative copy. If you edit a file directly (external editor, `git checkout`, `rsync`), thornotes detects the change within `THORNOTES_WATCH_INTERVAL` and syncs the database and any open browser tabs automatically.

## Building from source

Requires Go 1.26+.

```sh
git clone https://github.com/th0rn0/thornotes
cd thornotes
make build
./thornotes --addr :8080 --db thornotes.db --notes-root notes
```

Or with `make dev` for development defaults (separate dev database, registration always on).

## Testing

```sh
make test
```

## Release process

Releases are version-tagged commits on `main`. The CI pipeline automatically builds and pushes the Docker image and creates a GitHub release when a `v*` tag is pushed.

### Automated (via CI)

1. Merge all changes to `main`.
2. Update `VERSION` (e.g. `0.9.0.0`) and add a `## [0.9.0.0] - YYYY-MM-DD` section to `CHANGELOG.md`.
3. Commit: `git commit -m "chore: release v0.9.0.0"`
4. Tag: `git tag v0.9.0.0`
5. Push branch and tag:
   ```sh
   git push origin main
   git push origin v0.9.0.0
   ```

CI will then:
- Run lint and tests.
- Build and push `th0rn0/thornotes:latest` and `th0rn0/thornotes:v0.9.0.0` to Docker Hub.
- Create a GitHub release with the changelog section for that version as release notes.

### Manual (no CI)

If you need to release without CI, run the Docker build and push yourself:

```sh
# Build
docker build -t th0rn0/thornotes:latest -t th0rn0/thornotes:v0.9.0.0 .

# Push
docker push th0rn0/thornotes:latest
docker push th0rn0/thornotes:v0.9.0.0

# Create GitHub release (requires gh CLI)
VERSION=$(cat VERSION | tr -d '[:space:]')
awk '/^## \['"$VERSION"'\]/{found=1; next} found && /^## \[/{exit} found{print}' CHANGELOG.md > /tmp/release_notes.md
gh release create "v$VERSION" --title "v$VERSION" --notes-file /tmp/release_notes.md
```

## License

MIT
