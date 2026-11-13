# Verifying the wikilink graph against a real corpus

`scripts/verify_wikilinks.py` re-derives every `[[link]]` edge in a vault from
the note bodies, independently of the server, and diffs the result against
`GET /api/v1/notes/:id/links` and `/backlinks` for every note. It is the check
that answers "is the stored graph right?" — the Go unit tests prove the rules
against fixtures, not against 471 real notes.

It is **not** a per-PR gate: it needs a corpus and a running instance. Run it
after a change to link extraction, normalisation, resolution, the `note_links`
index, or the reconcile path, and before a release that touches them.

## What the oracle models

The oracle re-implements note 323 §4–5 from the spec; it does not call the Go
code. Three rules decide whether its edges may legitimately differ from the
server's:

| Rule | What the oracle does | Where the server says so |
|------|----------------------|--------------------------|
| **Extraction** | Drops ` ``` ` and `~~~` fenced code blocks (3+ markers, any leading indent — indented text is *not* a fence), drops single-backtick inline code spans, then parses the remaining `[[target#anchor\|link text]]` | `ExtractLinks` in [`internal/wikilinks/extract.go`](../internal/wikilinks/extract.go) |
| **Edge key** | One edge per `(note, sha256(NormalizeKey(target)))`; the **first** occurrence's anchor and link text win, later occurrences of the same target collapse into that edge | `computeEdges` in [`internal/notes/links.go`](../internal/notes/links.go), `UNIQUE (from_note_id, target_hash)` |
| **Resolution** | Candidates are notes whose stored slug equals `Slugify(target)` **or** whose `NormalizeKey(title)` equals `NormalizeKey(target)`; nearest common folder ancestor wins; an equal-nearest tie is `ambiguous` and resolves to nothing | `Resolve` in [`internal/wikilinks/resolve.go`](../internal/wikilinks/resolve.go) |

The oracle reads note titles, slugs and folders from the **instance it is
checking**, not from the corpus: resolution is a function of the index's own
data, and assuming the corpus's titles is how an oracle ends up measuring the
load instead of the rules (see "the title the file states" below).

Each rule is asserted on synthetic fixtures on **every** run (`selftest`), so a
"0 mismatches" result cannot be an accident of a corpus that happens to contain
no fence example or no repeated target.

## Why this exists: the 1e calibration

The first run of this comparison (ticket 1e) reported **70 mismatches** on a
corpus with no real defects. Every one was an artefact of the oracle:

* **68 duplicate edges** — the oracle counted link *occurrences*; the server
  stores one row per `(note, target_hash)`. 16 notes repeat a target; 68 repeat
  occurrences in total.
* **2 fenced-code examples** — `[[Note#Heading]]` and `[[Note|Display]]` written
  inside a ` ```sql ` block in note 323, i.e. documentation of the syntax rather
  than links.

Same corpus, same rules fixed:

| Run | Harness | Mismatches |
|-----|---------|-----------|
| 1e (recorded) | QA's oracle, pre-`N7` server | 70 (68 dupes + 2 fences) |
| today, replayed | **the identical 1e harness**, current server | 94 (68 + 2 + 24 title-drift, below) |
| today | `scripts/verify_wikilinks.py` | **0** |

The extra 24 in the replay are the same disease in a second place: the 1e oracle
resolved against the *corpus's* production titles, and a current server stores
the title each **file** states, so `[[Wikilinks 1c: … — t_8d442792]]` was
"resolved" to the oracle and "unresolved" to the server (12 edges, plus 12
backlink rows). Reading titles off the instance removes the whole class.

The two artefact categories are counted on every run (`ignored by fences`,
`collapsed dupes`), and `--calibrate` fails the run if either count is zero, so
the calibration cannot silently stop exercising them.

```
notes             : 471 corpus / 471 instance
corpus fidelity   : 97 title(s) converged onto the body's '# H1', 5 slug(s) recomputed
notes with links  : 152
source edges      : 601 (deduped per note+target)
server edges      : 601
backlink rows     : 581
ignored by fences : 2 link occurrence(s)
ignored in inline : 35 link occurrence(s)
collapsed dupes   : 68 repeat occurrence(s) folded into an existing edge
MISMATCHES: 0
```

## Running it

The oracle needs a **corpus** (a JSON snapshot of note bodies) and a
**throwaway instance** holding that corpus. Never point `import` at a real
vault: it writes notes. `check` only reads, and `import`/`fixture` refuse a
non-loopback `--base` unless you pass `--allow-remote`.

### 0. Build and start a scratch instance on a spare port

```sh
go build -o /tmp/o595/thornotes ./cmd/thornotes
mkdir -p /tmp/o595/notes
/tmp/o595/thornotes -addr 127.0.0.1:8111 -db /tmp/o595/tn.db \
  -notes-root /tmp/o595/notes -allow-registration &
curl -sf http://127.0.0.1:8111/healthz >/dev/null && echo up
```

### 1. Load the corpus

The corpus is one object of notes keyed by an arbitrary id:

```json
{ "12": { "title": "…", "slug": "…", "folder_id": "2", "content": "# …", "tags": [] } }
```

and the folder tree is `[{"id": 2, "name": "Projects", "parent_id": null}, …]`.
Both are read off a source instance read-only: `GET /api/v1/notes/all` (ids,
titles, slugs, folder ids), `GET /api/v1/notes/:id` (bodies) and
`GET /api/v1/folders`. The 1e snapshot lives at
`/opt/data/profiles/qa/cache/scratch/qa1e/prod_notes.json` +
`prod_folders.json`.

```sh
python3 scripts/verify_wikilinks.py import \
  --base http://127.0.0.1:8111 --username oracle --password oracle-local-2026 \
  --corpus /opt/data/profiles/qa/cache/scratch/qa1e/prod_notes.json \
  --folders /opt/data/profiles/qa/cache/scratch/qa1e/prod_folders.json \
  --map-out /tmp/o595/note_map.json
```

`import` refuses an account that already has notes or folders (except the
`Getting Started` note registration auto-creates, which it removes), so it can
never merge a corpus into a live vault. It then waits for the load to **settle**:
one full reconcile interval (`--reconcile-wait`, default 35s, must exceed the
instance's `--watch-interval`), then verifies that every title matches the
heading its file states and that the graph is byte-identical across two
consecutive samples. A load that never settles is a harness error.

### 2. Compare every note

This is the command that reproduces the numbers above:

```sh
python3 scripts/verify_wikilinks.py check \
  --base http://127.0.0.1:8111 --username oracle --password oracle-local-2026 \
  --corpus /opt/data/profiles/qa/cache/scratch/qa1e/prod_notes.json \
  --map /tmp/o595/note_map.json \
  --calibrate --report /tmp/o595/report.json
```

`--calibrate` additionally requires the corpus to contain at least one fenced
link and one repeated target — the calibration guarantee. Drop it for an
ordinary corpus. `--report` writes the full findings JSON (every disagreeing
note, target and both sides' edges) so a failure can be triaged without
re-running.

### 3. The ambiguous-title fixture

The real corpus has almost no duplicate titles, so `/links`'s `ambiguous` flag is
otherwise covered only by the seeded demo. This synthesises it: two notes with
the same title in sibling folders, one source in a third folder (equal distance
to both → must be `ambiguous`, no `to_note_id`), one source in the same folder as
a candidate (must resolve to it), and the oracle must agree on both — plus the
ambiguous edge must create no backlink while the resolved one does. It removes
its notes afterwards unless `--keep` is given, and is safe to re-run.

```sh
python3 scripts/verify_wikilinks.py fixture \
  --base http://127.0.0.1:8111 --username oracle --password oracle-local-2026
```

Run it on a **separate** instance (or after the corpus check): its duplicate
titles would otherwise make real corpus links ambiguous.

### 4. Offline: just the oracle's own rules

```sh
python3 scripts/verify_wikilinks.py selftest
```

No instance, no corpus. Four clauses: fence + inline-code exclusion (including
that four-space-indented text is *not* a fence, because the server's rule says
so), duplicate collapse with first-occurrence-wins, normalisation, and resolution
(slug, title key, same-folder win, ambiguity, unresolved).

## The title the file states

Since ticket N7 (`internal/notes/reconcile.go` step 9b) a note's stored title is
converged onto the `# H1` its file states, on the first reconcile pass after the
body appears. On the 1e corpus that moves **97 of 471** titles away from the
production title, and their slug is whatever the file stem was, so **5 of 471**
slugs differ from what a fresh load would compute (the `NN - Weekday` journal
notes, whose production slug contains spaces).

This is the server's rule, not drift, and it is why the oracle reads titles and
slugs from the instance. It is also why a replay of the 1e harness now reports
12 extra `resolved_vs_unresolved` findings: links written against the production
title stop resolving once the stored title is the heading. That is worth knowing
before a deploy converges titles on a real vault.

## Failure behaviour

Exit code `0` means everything was compared and agreed. `1` means real
disagreements — each is a finding with the note, the target and both sides'
edges. `2` means **the harness could not produce evidence**, which is never a
pass:

| Situation | Result |
|-----------|--------|
| Corpus file missing, empty or malformed | exit 2, names the path |
| Instance unreachable / `/healthz` not 200 | exit 2, says how to start one |
| Login and registration both fail, or auth is rate-limited | exit 2 |
| `--map` does not cover the corpus (or covers more than it) | exit 2, lists the ids |
| Instance note count ≠ corpus note count | exit 2 (refuses to compare a different vault) |
| A note's title is neither the corpus title nor its body's `# H1` | exit 2 (a load problem, not a rules problem) |
| A note's body differs from the corpus | exit 2 (the comparison would measure the import) |
| A mapped note id is missing from the instance | exit 2 |
| Any `/links` or `/backlinks` returned non-200 | exit 2 |
| The corpus produced zero edges | exit 2 ("compared nothing is not a pass") |
| `--calibrate` but the corpus has no fence or no repeat | exit 2 |
| `import` told to write to a non-loopback base | exit 2 |

Sample output (each is a real run):

```
$ python3 scripts/verify_wikilinks.py check --base http://127.0.0.1:8199 …
HARNESS ERROR: cannot reach instance at http://127.0.0.1:8199/api/v1/auth/login:
<urlopen error [Errno 111] Connection refused>. Start a scratch instance on a
spare port (see the module header).
Exit 2: no evidence was produced, so this run is NOT a pass.

$ python3 scripts/verify_wikilinks.py check --corpus /tmp/nope.json …
HARNESS ERROR: corpus not found: /tmp/nope.json
Exit 2: no evidence was produced, so this run is NOT a pass.

$ python3 scripts/verify_wikilinks.py check --corpus 1-note-no-links.json …
HARNESS ERROR: compared zero edges — the corpus produced no links, so a
zero-mismatch result would be an empty comparison, not a pass
Exit 2: no evidence was produced, so this run is NOT a pass.

$ python3 scripts/verify_wikilinks.py check --calibrate --corpus tiny.json …
HARNESS ERROR: --calibrate: this corpus contains no fenced-code link examples,
so it cannot show that fence handling was what removed the old artefact
Exit 2: no evidence was produced, so this run is NOT a pass.
```

When the check finds a real disagreement, **do not change the oracle to agree**.
The server is the reference: file the disagreement with the note, the target and
both sides' edges, and fix the server (or the spec) separately.

## Proving the harness still reports a mismatch

A harness that never reports anything is the other half of the 1e disease. The
mismatch path is exercised on purpose by loading a corpus and checking it before
the server has finished indexing — `import` normally prevents this with its
settle step, so switch the settle off:

```sh
python3 scripts/verify_wikilinks.py import … --reconcile-wait 0 --settle-seconds 1 --settle-poll 1
python3 scripts/verify_wikilinks.py check …      # -> exit 1
```

On the two-note corpus used for that run the output was:

```
MISMATCHES: 2
  backlink_missing: 1
  resolution_state: 1
    {"corpus_id": "1", "title": "Tiny A", "instance_id": 2, "target": "Tiny B",
     "oracle": "resolved", "server": "unresolved", "oracle_to_note_id": 3,
     "server_to_note_id": null, "oracle_candidates": [3]}

FAIL: the oracle and the server disagree — see the findings above.
```

The same corpus loaded with the default settle reports 0. The difference is the
server's own reindex pass, not the oracle: this is exactly the class of finding
a reader must be able to trust, and exactly why `import` waits for the graph to
settle before anyone draws a conclusion from it.

## Notes for whoever runs it next

* The auth endpoints are rate-limited per IP (burst 10, refill 1 per 90s). One
  `import` + `check` on a fresh account spends 5 of those. If a run reports
  "auth rate-limited", wait or start a fresh instance — do not work around it.
* `--workers` (default 8) sets the request parallelism for the per-note reads.
* The instance must be left running between `import` and `check`; the check is
  read-only and can be re-run as often as the rate limit allows.
