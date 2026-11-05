# Operations and recovery

## Before deploying migrations

Take a database backup before every deploy whose image includes new migrations. This backup is the rollback, not a convenience. Verify that the backup exists and is readable before continuing.

For SQLite, stop Thornotes or otherwise ensure the database is quiescent, then copy the database file (replace the path with the actual mounted database path):

```sh
cp /data/thornotes.db /data/backups/thornotes-before-migration.db
```

SQLite's online backup command is also available while the database is open:

```sh
sqlite3 /data/thornotes.db '.backup /data/backups/thornotes-before-migration.db'
```

For MySQL or MariaDB, dump the database before deploying (replace the example database name and supply credentials through your configured MySQL client option file or environment). The client binary is named differently depending on the image: the official `mariadb` images ship `mariadb-dump` and `mariadb` and do **not** include `mysqldump`, while MySQL installs ship `mysqldump` and `mysql`. Use the pair your client provides — the flags are the same:

```sh
mariadb-dump --single-transaction --routines --triggers thornotes > thornotes-before-migration.sql
# MySQL client installs:
# mysqldump --single-transaction --routines --triggers thornotes > thornotes-before-migration.sql
```

Check the dump is non-empty, and restore it once into a scratch database before you need it. A backup you have never restored is a guess.

Keep the backup outside the database volume or on another durable system. A backup is useful only if you can restore it.

## The deploy-time gate: compare the database against the candidate image

The startup refusal in "Schema too new" happens at container start — after the deploy has already been cut over. Catch it before the swap with the gate script: it compares the **running** instance's `/healthz` `schema.version` with the **candidate** image's `co.thornotes.schema.max` label (SQLite) or `co.thornotes.schema.max.mysql` label (MySQL — production runs MySQL). Run it on the Docker host, after `docker pull` of the candidate image and before swapping containers:

```sh
docker pull th0rn0/thornotes:latest
scripts/deploy-gate.sh \
  --image th0rn0/thornotes:latest \
  --driver mysql \
  --healthz https://notes.example.com/healthz \
  --backup /data/backups/thornotes-before-migration.sql
```

Its decisions:

- **database version > image ceiling** — refuse. Starting that image is a guaranteed exit 1 at startup; the gate prints both numbers and the remedy (deploy a build with a higher ceiling, or restore the backup taken before that migration, then pin the older image). Nothing is started.
- **database version < image ceiling** — the image will migrate on start. It refuses to proceed unless the `--backup` path exists, is non-empty, and is no older than the age budget (`--max-backup-age-min`, default 240). Take the backup per "Before deploying migrations" above first.
- **equal** — proceed; no migration will run on start.
- **unknown** — refuse. An unreachable `/healthz`, an image that cannot be inspected, a missing or non-numeric label, or a `/healthz` body without a readable `schema.version` all stop the deploy. Absent evidence is not permission.

Exit code 0 means safe to cut over; 1 means refused (one line per failure). The scripted check `scripts/deploy-gate.test.sh` (run on the Docker host) exercises all three decisions plus both refusals — a too-new database refused before any container starts, and a missing backup — against a pulled `th0rn0/thornotes` image so the label path is always real.

## Reverse proxy cache headers: keep /sw.js and the document out of the expires rule

Production sits behind Nginx Proxy Manager (openresty). With its "Cache Assets" behaviour, NPM matches static extensions (`.js` among them) and **overrides** whatever the app sent with `expires` + `cache-control: max-age≈1d`. Since PR #33 asset URLs are versioned (`/static/...?v=<8 hex>`), a pinned stale body can no longer happen for versioned assets — but a long-cached `/sw.js` delays service-worker rotation and leaves dead caches on disk, and `/` is the document that pins those asset URLs.

Paste this into the proxy host's **Advanced** tab (openresty resolves a later exact-match `location` over the generated prefix-match blocks; `conf.d/include/proxy.conf` supplies the upstream proxy directives with the scheme/server/port NPM already `set` at server scope — no upstream address to duplicate):

```nginx
# The service worker must always revalidate: a stale cached copy delays its
# rotation and keeps dead caches alive on the client.
location = /sw.js {
    expires off;
    add_header Cache-Control "no-cache" always;
    include conf.d/include/proxy.conf;
}

# The document that pins the versioned asset URLs; the app sends no-cache.
location = / {
    expires off;
    add_header Cache-Control "no-cache" always;
    include conf.d/include/proxy.conf;
}
```

After saving and allowing NPM to reload, verify with (curl -sI, three probes — note the header each must show):

```sh
curl -sI https://notes.th0rn0.co.uk/sw.js            # must show cache-control: no-cache
curl -sI https://notes.th0rn0.co.uk/                 # must show cache-control: no-cache
curl -sI "https://notes.th0rn0.co.uk/static/js/app.js?v=<current hex>"   # may keep a long max-age
```

Operational notes: `expires off` removes the injected `Expires`/`max-age` on those two exact paths and lets the app's own `Cache-Control` stand; the app already serves `no-cache` for `/` and for unversioned or stale-versioned `/static/` URLs (see `internal/router/assets.go`, `cacheControlFor`). If the `?v=` parameter is dropped from a patch-formed URL, the app answers `no-cache` itself — the proxy must not re-add a `max-age` to those paths under any rule that pattern-matches extensions. Versioned assets (`?v=<current hex>`) may keep caching: the URL changes whenever the bytes do.

## Rollback is restore, then pin

`:latest` is not a rollback target. Once a migration has run, an older image refuses to start against the newer database by design. To roll back, restore the pre-migration backup first, then pin and deploy the older image. Do not expect pulling an older image alone to reverse a schema change; this project does not offer an automatic downgrade recovery path.

## Read the schema state without starting Thornotes

These commands query the migration bookkeeping table directly. Use the actual database path for SQLite and your normal connection options for MySQL/MariaDB:

```sh
sqlite3 /data/thornotes.db 'select version, dirty from schema_migrations'
```

```sh
mariadb --database=thornotes --execute='select version, dirty from schema_migrations'
# MySQL client installs:
# mysql --database=thornotes --execute='select version, dirty from schema_migrations'
```

The `version` is the migration version recorded in the database. `dirty=1` means the migration process marked the schema dirty when interrupted; it does not certify which statements completed.

## Schema too new

The database has been migrated by a newer build than the image you just started supports. Startup refuses before applying migrations. For a running instance, inspect `GET /healthz`: compare `schema.version` (database version) with `schema.max_supported` (the running binary's migration ceiling). This endpoint is unavailable when startup itself is refused, so inspect the image before deployment as well: its OCI label `co.thornotes.schema.max` reports the SQLite migration ceiling and `co.thornotes.schema.max.mysql` reports the MySQL ceiling. Compare the appropriate label with the database's recorded version.

The emitted error is:

```text
schema version 9 is newer than this build supports (8) — deploy a build with migration ≥ 9, or restore the database backup taken before migration 9
```

There are two legitimate exits:

1. Deploy a build whose migration set reaches the database's recorded version (at least version 9 in the example).
2. Restore the backup taken before migration 9, then deploy the older build.

Do not lower the database version marker to make an old image start. That does not reverse the migration.

## Schema dirty

A migration was interrupted and `schema_migrations.dirty` is set. The version shown in the message is the migration version stamped when the interruption occurred; the repeated version in “force version N” is the proposed version marker to clear the dirty state to; the final version is the migration number whose pre-migration backup would be restored. A dirty version is not proof that every operation in that migration completed.

The emitted error is:

```text
schema version 8 is dirty: a migration was interrupted and the schema is in an unknown state — inspect the database and verify the schema by hand, then force version 8 only if it is correct, or restore the database backup taken before migration 8
```

This is a manual-intervention event. Inspect the live schema and compare it with the expected schema at that migration, including whether each migration statement took effect. Only after verifying that the schema is genuinely in the expected state may an operator clear the dirty flag. Note that **Thornotes itself has no force command** — there is no flag, endpoint or subcommand for it, despite the wording of the error. Clearing it means a direct write to the bookkeeping table:

```sh
sqlite3 /data/thornotes.db 'update schema_migrations set version = 8, dirty = 0'
# MySQL/MariaDB:
# mariadb --database=thornotes --execute='update schema_migrations set version = 8, dirty = 0'
```

That statement only rewrites the bookkeeping. It cannot complete a half-applied migration and it cannot undo one, so it is only correct when you have independently established that the schema matches version 8. Forcing a version you have not verified is how you corrupt a database. If you cannot establish the exact schema state, restore the backup taken before migration 8. Do not retry startup hoping the migration will repair itself.

## Symptom → check → action

| Symptom | Check | Action |
| --- | --- | --- |
| Startup says schema version is newer than supported | Read `version, dirty` directly; compare the recorded version with `/healthz` `schema.version` / `schema.max_supported` when available and the appropriate image schema label | Deploy a build supporting that version, or restore the pre-migration backup and deploy the older build |
| Startup says schema version is dirty | Read `version, dirty` directly and inspect actual schema objects/data against the interrupted migration's expected result | Force the version only after manual verification proves the schema is complete; otherwise restore the pre-migration backup |
| Considering an older image or `:latest` | Check whether the deployed migration changed the database; inspect the database version and image label | `:latest` is not rollback: restore the backup, then pin the older image |

## Error text source

The two startup errors above are emitted by `internal/db/schema.go`. The example versions shown here match the source format; actual numbers vary with the database and build.
