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

## Which build is this?

```bash
curl -s https://notes.example.com/healthz | jq -r .build.version
```

The value is the commit the binary was built from, injected at build time (`-X main.Version`); CI passes `${DRONE_COMMIT_SHA}`. A binary built without the flag reports `dev`, and one built with an empty value reports `unknown` — neither is ever an empty string, because an empty field reads like a missing key rather than a missing stamp.

The endpoint is unauthenticated deliberately. Knowing the commit of a notes server is not a secret, and requiring a login to answer it means only someone with host access can tell whether a deploy landed. Use it instead of the front-end asset digest (`app.js?v=…`): that hashes the assets, not the commit, so several releases that touch no JavaScript serve byte-identical digests.

## Starting up: "reconciling" is not an error

The listener opens before the startup reconcile finishes, so a booting instance answers `503` for anything that reads or writes the note index until that pass is done — `status` is `"reconciling"` and `startup.phase` says the same, while `schema`, `build` and `checks` remain readable:

```bash
curl -s https://notes.example.com/healthz | jq -r '.status, .startup.phase'
# reconciling
# reconciling
```

Wait for `status: "ok"` before treating the instance as ready; a healthcheck that reads any non-200 as "down" already does the right thing here, and a consumer with `depends_on: {thornotes: {condition: service_healthy}}` will now wait for the pass too — that is the intent, not a side effect. `error` still means what it meant: the database or the notes root needs a human, not a moment.

How long that window lasts depends on which of two passes you are in, and the two differ by more than an order of magnitude. The figures usually quoted for this pass — ~2.7s for 400 notes, ~13s for 2,000, ~33s for 5,000 on SQLite — are the **rebuild**: the notes table is empty or does not match the disk, as on a first boot, a fresh or restored volume, or an index that was lost. That pass reads and hashes every file and costs roughly 4.4 ms per note. A **restart over a vault whose database already matches the disk** — what an ordinary deploy actually pays — finds every hash unchanged, at roughly 0.08 ms per note: 0.16s rather than 8.8s for 2,000 notes, about 55× cheaper. Both were measured with 2,000 notes, and in both cases the port is bound and answering within 0.02s; it is only the note routes that answer `503` until the pass ends, while the app shell, `/healthz`, `/api/v1/auth/*` and static assets serve normally.

So size a container start period for the rebuild, not for the restart. A start period chosen from the rebuild number is harmless on a restart that is ready in a fraction of a second; one chosen from the restart number will flap on a genuine rebuild. `--skip-reconciliation` remains the escape hatch for a trusted restart, but it disables the repair itself, so it is safe only when you know the disk and the index agree — it is not a way to avoid a delay that a matching restart does not incur anyway.

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

## CI runner cache volumes (Drone)

The Drone runner keeps expensive tool caches on its host and bind-mounts them into pipeline steps.

| Host path | Mounted at | Used by | Content |
| --- | --- | --- | --- |
| `/var/cache/drone/gradle-home` | `/root/.gradle` | `build-android` | Gradle caches |

- Go module downloads are **not** cached on the runner: the Go steps (`lint`, `test`, `build-linux-binary`) each fetch their own module tree. Measured on this runner, a fully cold download adds ~1 s to the `test` step (52 `go: downloading` lines), so a shared module cache does not pay for itself — and while two builds run at once it made them *slower* (~+50 s per `test` step), so it was removed in ticket 597.
- The `test` step instead retries the *download* once (`go mod download all || go mod download all`): a single stream reset from `proxy.golang.org` (HTTP/2 `stream error`, as on build #226) no longer turns the build red. The download is idempotent and hash-verified against `go.sum`, so the retry cannot mask a corrupted or wrong module; real test failures are never retried.

## Safety-net backups (ticket 602) — what runs where

Thornotes production auto-deploys `th0rn0/thornotes:latest` via watchtower; nobody runs `scripts/deploy-gate.sh` on that path. `scripts/host-backup.sh` is the protection for it: a rolling dump-plus-bundle taken **before** an unseen deploy can matter, with a restore proof every run — and, per the reachability finding below, it runs **on the host (MAX) itself**, not from CI.

### Where the Drone runner actually is (measured 2026-11-06, corrected 2026-11-14)

There are two Drone runners: `max` (441 G) and `drone-runner-tehhulk` (74 G). A runner probe reports on whichever runner picked up that build, so the earlier probes in builds #271, #275, and #277 (branch `ops/safety-net`) establish facts about `drone-runner-tehhulk`, not every runner. A container-name list alone does not distinguish a CI box from production.

Build #379 (branch `ops/ci-prune-when-full`) measured the other case from inside a runner:

```text
+ docker info --format 'runner={{.Name}}'
runner=max
+ cat /etc/hostname
max
```

That is MAX, the production Docker host: its daemon has the production services (`thornotes`, `thornotes-db-1`, `watchtower`, `homeassistant`, `jellyfin`, `radarr`, `sonarr`, `portainer`) as well as `drone-runner` and `drone-server`; `/servdata` is the production data mount. Thus one of the two runners is production, but nothing pins the `safety-net` pipeline to it. An unpinned Drone cron could land on `drone-runner-tehhulk`, where it would back up only a CI box or abort. **Do not wire the safety-net backup to a Drone cron; run it host-side on MAX using the block below.**

### Paste-ready host-side block (MAX) — no CI dependency

Merge this PR first (the merge policy is human-only), then, as root on MAX:

```sh
# install
mkdir -p /servdata/thornotes/backups
curl -fsSL https://raw.githubusercontent.com/th0rn0/thornotes/main/scripts/host-backup.sh \
  -o /usr/local/bin/thornotes-backup
chmod 755 /usr/local/bin/thornotes-backup

# prove before scheduling: both subcommands, read the BACKUP OK / VERIFY OK
# and VERIFY DONE lines yourself (they include the row counts)
/usr/local/bin/thornotes-backup backup
/usr/local/bin/thornotes-backup verify
/usr/local/bin/thornotes-backup status

# then wire the cron (root): hourly dump + bundle + rotation, and the restore
# proof every hour; failures leave a FATAL line in backup.log
( crontab -l 2>/dev/null; echo '5 * * * * /usr/local/bin/thornotes-backup backup >> /servdata/thornotes/backups/backup.log 2>&1 && /usr/local/bin/thornotes-backup verify >> /servdata/thornotes/backups/verify.log 2>&1 || echo "[safety-net] FATAL backup or verify failed" >> /servdata/thornotes/backups/backup.log' ) | crontab -

# to unwire:
# crontab -l | grep -v thornotes-backup | crontab -
```

Two mount rules in the script keep it honest (docker `v:` sources resolve against the **host** daemon, not the caller's filesystem):

1. The host backup directory is used at the same **named path** (`/servdata/thornotes/backups`) everywhere, so what the caller can `ls` is exactly what the helper containers write.
2. Never bind or pass it as `/out` — that was the 2026-11-06 bug: artefacts landed where the step could not see them.

**Rotation bound: 168 of each artefact** (`KEEP_HOURLY`, one pair per hour). This bound is primarily a **disk** decision — on top of it, the 24-hour coverage requirement ("reach the evening before the bad pull, any day of the week") is met with margin: 168 hours is a full week, so a bad pull on a Monday morning still reaches far past the whole weekend, not just the last day. Each hourly pair is small (a compressed dump plus a bundle; real per-run sizes are printed by the `BACKUP OK` line and accumulated by `scripts/host-backup.sh status`).
