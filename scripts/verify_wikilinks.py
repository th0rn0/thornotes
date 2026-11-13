#!/usr/bin/env python3
"""Wikilink graph oracle — compare a Thornotes instance's link graph against an
independent re-implementation of the extraction + resolution rules.

WHY THIS EXISTS
---------------
The graph has two halves that must agree: what `internal/wikilinks` + the
`note_links` index *store*, and what the spec says those edges *should* be. The
first half is tested by Go unit tests; the second needs an oracle that re-derives
edges from note bodies independently, then diffs them against
`GET /api/v1/notes/:id/links` and `/backlinks` for every note in a real corpus.

The 1e run of that oracle reported 70 "mismatches" on a corpus with no real
defects. All 70 were oracle artefacts:

  * 68 x duplicate edges   — the oracle counted link *occurrences*, but
    `note_links` has UNIQUE (from_note_id, target_hash), so the server keeps one
    row per (note, NormalizeKey(target)). Linking a page twice in prose is
    normal, not a defect.
  * 2 x code fences        — the oracle counted `[[Note#Heading]]` and
    `[[Note|Display]]` written inside a ```sql block as links. The server strips
    fenced code (and inline code spans) before extracting.

A harness that cries wolf 70 times is ignored when it matters. This one models
the documented rule instead:

  extraction  fenced code blocks (``` or ~~~, 3+ markers, any leading indent)
              are not links; single-backtick inline code spans are not links;
              the remaining `[[...]]` are parsed into target / anchor /
              link_text (note 323 §4).
  edge key    sha256(NormalizeKey(target)) — note_links.target_hash. One edge
              per (note, key); the FIRST occurrence's anchor/link_text wins and
              later occurrences of the same target collapse into it.
  resolution  candidates = notes whose stored slug == Slugify(target) OR whose
              title_key == NormalizeKey(target); nearest common ancestor wins;
              an equal-nearest tie is `ambiguous` (the resolver refuses to
              guess) — note 323 §5.

Those three clauses are asserted on synthetic fixtures every time the check
runs (`selftest`), so "0 mismatches" can never be an accident of a corpus that
stopped containing fences or repeats.

USAGE (full walkthrough and the 1e numbers: docs/wikilinks-oracle.md)
-------------------------------------------------------------------
  # 0. build + start a throwaway instance on a spare port
  go build -o /tmp/o595/thornotes ./cmd/thornotes
  /tmp/o595/thornotes -addr 127.0.0.1:8111 -db /tmp/o595/tn.db \
      -notes-root /tmp/o595/notes -allow-registration &

  # 1. load the corpus into that instance (writes; loopback only)
  python3 scripts/verify_wikilinks.py import \
      --base http://127.0.0.1:8111 --username oracle --password oracle-local-2026 \
      --corpus .../prod_notes.json --folders .../prod_folders.json \
      --map-out /tmp/o595/note_map.json

  # 2. compare every note (read-only)
  python3 scripts/verify_wikilinks.py check \
      --base http://127.0.0.1:8111 --username oracle --password oracle-local-2026 \
      --corpus .../prod_notes.json --map /tmp/o595/note_map.json \
      --calibrate --report /tmp/o595/report.json

  # 3. ambiguous-title fixture (creates + removes its own notes)
  python3 scripts/verify_wikilinks.py fixture --base http://127.0.0.1:8111 \
      --username oracle --password oracle-local-2026

  # offline: assert the oracle's own rules, no instance needed
  python3 scripts/verify_wikilinks.py selftest

EXIT CODES
----------
  0  compared everything, zero mismatches (or the sub-command's checks passed)
  1  real disagreements between oracle and server — each one is a finding with
     the note, the target and both sides' edges
  2  the harness could NOT produce evidence: corpus missing/empty, instance
     unreachable, login failed, mapping incomplete, note bodies not identical,
     a non-200 on an edge endpoint, or "compared nothing". Absent evidence is
     never a pass.

READ-ONLY GUARANTEE
-------------------
`check` and `selftest` issue read requests only. `import` and `fixture` write,
so they refuse a non-loopback --base unless --allow-remote is given: they exist
to populate a throwaway instance, never a real vault.
"""

import argparse
import ast
import collections
import hashlib
import html
import http.cookiejar
import json
import os
import re
import sys
import time
import unicodedata
import urllib.error
import urllib.parse
import urllib.request
from concurrent.futures import ThreadPoolExecutor

EXIT_OK, EXIT_MISMATCH, EXIT_NO_EVIDENCE = 0, 1, 2


class HarnessError(Exception):
    """The harness could not produce evidence. Always exit code 2."""


# ═══════════════════════════════════════════════════════════════════════════
# Oracle rule 1: normalisation (note 323 §4)
# ═══════════════════════════════════════════════════════════════════════════

# Dash variants folded to ASCII '-'. Keep in sync with dashRunes in
# internal/wikilinks/normalize.go — a divergence here is a rule disagreement,
# which the oracle is supposed to surface, not to paper over.
DASH_RUNES = {
    "\u2012",  # figure dash
    "\u2013",  # en dash
    "\u2014",  # em dash
    "\u2015",  # horizontal bar
    "\u2212",  # minus sign
    "\ufe58",  # small em dash
    "\ufe63",  # small hyphen-minus
    "\uff0d",  # fullwidth hyphen-minus
}

NON_ALNUM_RE = re.compile(r"[^a-z0-9]+")


def normalize_key(s):
    """entity-decode → NFC → fold dash variants → collapse whitespace → casefold."""
    s = html.unescape(s or "")
    s = unicodedata.normalize("NFC", s)
    s = "".join("-" if ch in DASH_RUNES else ch for ch in s)
    s = " ".join(s.split())
    return s.casefold()


def slugify(s):
    """NormalizeKey then every run of non-[a-z0-9] becomes one '-', trimmed."""
    return NON_ALNUM_RE.sub("-", normalize_key(s)).strip("-")


def edge_key(target):
    """sha256(NormalizeKey(target)) hex — note_links.target_hash."""
    return hashlib.sha256(normalize_key(target).encode("utf-8")).hexdigest()


# ═══════════════════════════════════════════════════════════════════════════
# Oracle rule 2: extraction (note 323 §4, with the fence/inline-code exclusions
# the server documents in internal/wikilinks/extract.go)
# ═══════════════════════════════════════════════════════════════════════════

LINK_RE = re.compile(r"\[\[([^\[\]]+)\]\]")
CODE_SPAN_RE = re.compile("`[^`\n]*`")
FENCE_LINE_RE = re.compile(r"[ \t]*(`{3,}|~{3,})")


def strip_fenced_code(body):
    """Remove ``` / ~~~ fenced blocks, returning (kept_text, dropped_text).

    Mirrors stripFencedCode in internal/wikilinks/extract.go: a fence opens on a
    line whose first non-space/tab run is 3+ backticks or 3+ tildes, and closes
    on a line with the same marker character; the fence lines and every line
    between them are dropped. An unterminated fence runs to end of body.
    """
    kept, dropped, fence = [], [], None
    for line in body.split("\n"):
        m = FENCE_LINE_RE.match(line)
        char = m.group(1)[0] if m else None
        if fence is None:
            if char:
                fence = char
                dropped.append(line)
                continue
            kept.append(line)
            continue
        dropped.append(line)
        if char and char == fence:
            fence = None
    return "\n".join(kept), "\n".join(dropped)


def parse_link_ref(raw):
    """Split an inner `[[...]]` body: [[T#A|L]] -> target T, anchor A, text L."""
    body, link_text = raw, ""
    i = body.find("|")
    if i >= 0:
        link_text = body[i + 1:].strip()
        body = body[:i]
    i = body.find("#")
    if i >= 0:
        return body[:i].strip(), body[i + 1:].strip(), link_text
    return body.strip(), "", link_text


def extract_edges(body):
    """Extract the note's edges the way the server indexes them.

    Returns (edges, stats). `edges` is a list of dicts in source order with one
    entry per unique (note, edge_key): the first occurrence wins, later
    occurrences of the same normalised target are counted in
    stats["duplicates_collapsed"] instead of becoming a second edge.
    """
    kept, dropped = strip_fenced_code(body or "")
    fenced_links = LINK_RE.findall(dropped)
    without_inline = CODE_SPAN_RE.sub("", kept)
    inline_links = []
    for span in CODE_SPAN_RE.findall(kept):
        inline_links.extend(LINK_RE.findall(span))

    stats = {
        "fenced_links_ignored": len(fenced_links),
        "inline_code_links_ignored": len(inline_links),
        "duplicates_collapsed": 0,
        "fenced_examples": [f"[[{r}]]" for r in fenced_links],
        "inline_examples": [f"[[{r}]]" for r in inline_links],
        "duplicate_examples": [],
    }

    edges, seen = [], {}
    for raw in LINK_RE.findall(without_inline):
        target, anchor, link_text = parse_link_ref(raw)
        key = edge_key(target)
        if key in seen:
            stats["duplicates_collapsed"] += 1
            seen[key]["occurrences"] += 1
            stats["duplicate_examples"].append(
                {"target": target, "raw_target": seen[key]["raw_target"],
                 "anchor": anchor, "link_text": link_text})
            continue
        edge = {"raw_target": target, "anchor": anchor, "link_text": link_text,
                "key": key, "occurrences": 1}
        seen[key] = edge
        edges.append(edge)
    return edges, stats


# ═══════════════════════════════════════════════════════════════════════════
# Oracle rule 3: resolution (note 323 §5)
# ═══════════════════════════════════════════════════════════════════════════

class Resolver:
    """Resolve a link target against a note set, exactly as internal/wikilinks
    resolve.go does. Note: it uses each note's STORED slug (that is the column
    the server matches on) and NormalizeKey(title) — never a fuzzy match."""

    def __init__(self, notes, folders):
        self.notes = {n["id"]: n for n in notes}
        self.title_key = {n["id"]: normalize_key(n["title"]) for n in notes}
        self.by_slug = collections.defaultdict(list)
        for n in notes:
            self.by_slug[n.get("slug") or ""].append(n["id"])
        # folder id -> parent id (None at root level)
        self.folder_parent = {f["id"]: f.get("parent_id") for f in folders}

    def folder_chain(self, folder_id):
        chain, cur, seen = [], folder_id or 0, set()
        while True:
            if cur in seen:
                break
            seen.add(cur)
            chain.append(cur)
            if cur == 0:
                break
            if cur not in self.folder_parent:
                break  # unknown folder: treat as a leaf
            cur = self.folder_parent[cur] or 0
        return chain

    @staticmethod
    def nca_distance(a, b):
        idx = {}
        for i, v in enumerate(a):
            idx.setdefault(v, i)
        for j, v in enumerate(b):
            if v in idx:
                return idx[v] + j
        return (len(a) - 1) + (len(b) - 1)

    def resolve(self, folder_id, target):
        key, slug = normalize_key(target), slugify(target)
        ids, seen = [], set()
        for nid in sorted(self.by_slug.get(slug, [])):
            if nid not in seen:
                seen.add(nid)
                ids.append(nid)
        for nid in sorted(self.title_key):
            if self.title_key[nid] == key and nid not in seen:
                seen.add(nid)
                ids.append(nid)

        if not ids:
            return {"state": "unresolved", "to_note_id": None, "candidates": []}
        if len(ids) == 1:
            return {"state": "resolved", "to_note_id": ids[0], "candidates": ids}

        src_chain = self.folder_chain(folder_id)
        dists = {nid: self.nca_distance(src_chain, self.folder_chain(self.notes[nid].get("folder_id")))
                 for nid in ids}
        best = min(dists.values())
        nearest = [nid for nid in ids if dists[nid] == best]
        if len(nearest) == 1:
            return {"state": "resolved", "to_note_id": nearest[0], "candidates": ids}
        return {"state": "ambiguous", "to_note_id": None, "candidates": nearest}


# ═══════════════════════════════════════════════════════════════════════════
# Client
# ═══════════════════════════════════════════════════════════════════════════

class Client:
    def __init__(self, base, username, password, timeout=60):
        self.base = base.rstrip("/")
        self.username = username
        self.password = password
        self.timeout = timeout
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))
        self.csrf = None

    def request(self, method, path, body=None, extra_headers=None):
        data = json.dumps(body).encode("utf-8") if body is not None else None
        headers = {"Content-Type": "application/json", "Accept": "application/json"}
        if method not in ("GET", "HEAD") and self.csrf:
            headers["X-CSRF-Token"] = self.csrf
        headers.update(extra_headers or {})
        req = urllib.request.Request(self.base + path, data=data, method=method, headers=headers)
        try:
            with self.opener.open(req, timeout=self.timeout) as resp:
                raw = resp.read().decode("utf-8", "replace")
                return resp.status, _maybe_json(raw)
        except urllib.error.HTTPError as e:
            raw = e.read().decode("utf-8", "replace")
            return e.code, _maybe_json(raw)
        except (urllib.error.URLError, OSError) as e:
            raise HarnessError(
                f"cannot reach instance at {self.base}{path}: {e}. "
                "Start a scratch instance on a spare port (see the module header).")

    def health(self):
        status, body = self.request("GET", "/healthz")
        if status != 200:
            raise HarnessError(f"{self.base}/healthz returned {status}: {body}")
        return body

    def login_or_register(self):
        status, body = self.request("POST", "/api/v1/auth/login",
                                    {"username": self.username, "password": self.password})
        if status == 200:
            pass
        elif status == 429:
            raise HarnessError(
                f"auth rate-limited on {self.base} (burst is 10 auth requests per 15 minutes, "
                "refilling 1 per 90s). Wait, or run against a fresh instance.")
        elif status in (401, 404):
            status, body = self.request("POST", "/api/v1/auth/register",
                                        {"username": self.username, "password": self.password})
            if status not in (200, 201):
                raise HarnessError(f"login and register both failed for {self.username!r}: {status} {body}")
            status, body = self.request("POST", "/api/v1/auth/login",
                                        {"username": self.username, "password": self.password})
            if status != 200:
                raise HarnessError(
                    f"registered {self.username!r} but could not log in: {status} {body}"
                    + (" (auth rate limit: burst 10 per 15 minutes)" if status == 429 else ""))
        else:
            raise HarnessError(f"login failed for {self.username!r}: {status} {body}")

        status, body = self.request("GET", "/api/v1/csrf")
        if status != 200 or not isinstance(body, dict) or "csrf_token" not in body:
            raise HarnessError(f"could not obtain a CSRF token: {status} {body}")
        self.csrf = body["csrf_token"]
        return self

    # ── reads ───────────────────────────────────────────────────────────────
    def list_notes(self):
        status, body = self.request("GET", "/api/v1/notes/all")
        return _as_list(status, body, "GET /api/v1/notes/all")

    def list_folders(self):
        status, body = self.request("GET", "/api/v1/folders")
        return _as_list(status, body, "GET /api/v1/folders")

    def get_note(self, note_id):
        status, body = self.request("GET", f"/api/v1/notes/{note_id}")
        if status != 200 or not isinstance(body, dict):
            raise HarnessError(f"GET /api/v1/notes/{note_id} returned {status}: {body}")
        return body

    def links(self, note_id):
        status, body = self.request("GET", f"/api/v1/notes/{note_id}/links")
        return _as_list(status, body, f"GET /api/v1/notes/{note_id}/links")

    def backlinks(self, note_id):
        status, body = self.request("GET", f"/api/v1/notes/{note_id}/backlinks")
        return _as_list(status, body, f"GET /api/v1/notes/{note_id}/backlinks")

    # ── writes (import / fixture only) ──────────────────────────────────────
    def create_folder(self, name, parent_id):
        return self.request("POST", "/api/v1/folders", {"name": name, "parent_id": parent_id})

    def create_note(self, title, folder_id, tags=None):
        return self.request("POST", "/api/v1/notes",
                            {"title": title, "folder_id": folder_id, "tags": tags or []})

    def patch_note(self, note_id, content, content_hash):
        return self.request("PATCH", f"/api/v1/notes/{note_id}",
                            {"content": content, "content_hash": content_hash})

    def delete_note(self, note_id):
        return self.request("DELETE", f"/api/v1/notes/{note_id}")

    def delete_folder(self, folder_id):
        return self.request("DELETE", f"/api/v1/folders/{folder_id}")


def _maybe_json(raw):
    raw = (raw or "").strip()
    if not raw:
        return None
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return raw


def _as_list(status, body, what):
    """A list endpoint's answer, or a loud failure. Go marshals an empty slice as
    `null`, so `null` means "no rows", not "no evidence" — but a non-200 or an
    unexpected shape does, and that must stop the run."""
    if status != 200:
        raise HarnessError(f"{what} returned {status}: {body} — a note whose edges could not be "
                           "read is missing evidence, not a pass")
    if body is None:
        return []
    if not isinstance(body, list):
        raise HarnessError(f"{what} returned {status} with an unexpected body: {body!r}")
    return body


# ═══════════════════════════════════════════════════════════════════════════
# Corpus loading — loud on anything missing
# ═══════════════════════════════════════════════════════════════════════════

def load_json(path, what):
    if not os.path.exists(path):
        raise HarnessError(f"{what} not found: {path}")
    try:
        with open(path, "r", encoding="utf-8") as fh:
            return json.load(fh)
    except (OSError, json.JSONDecodeError) as e:
        raise HarnessError(f"{what} at {path} is unreadable: {e}")


def load_corpus(path):
    """Corpus JSON: {"<id>": {"title", "folder_id", "content", "tags"?}, ...}."""
    data = load_json(path, "corpus")
    if not isinstance(data, dict) or not data:
        raise HarnessError(f"corpus at {path} must be a non-empty object of notes")
    notes = {}
    for key, value in data.items():
        if not isinstance(value, dict) or "title" not in value or "content" not in value:
            raise HarnessError(f"corpus note {key!r} lacks title/content — refusing to guess")
        notes[str(key)] = {
            "title": value["title"],
            "slug": value.get("slug") or "",
            "folder_id": value.get("folder_id"),
            "content": value.get("content") or "",
            "tags": value.get("tags") or [],
        }
    return notes


def stated_heading(content):
    """The note title the file itself states: the first "# H1" in the opening
    16 lines, trimmed and capped at 500 characters — internal/notes/heading.go.

    A current server converges a note's title onto this heading on every
    reconcile pass (ticket N7), so a corpus load must expect it: 97 of the 1e
    notes state a heading that differs from their stored production title.
    Nothing else in the body influences resolution, which is why the oracle
    compares the stored titles it reads back from the instance rather than the
    corpus's titles.
    """
    lines = (content or "").split("\n")
    for line in lines[:16]:
        trimmed = line.strip()
        if not trimmed.startswith("# "):
            continue
        title = trimmed[1:].strip()
        if not title:
            continue
        return title[:500]
    return None


def expected_title(corpus_note):
    """The title a freshly loaded note will hold: its stated heading, else the
    corpus title (no heading = the row keeps the title it was created with)."""
    return stated_heading(corpus_note["content"]) or corpus_note["title"]


def as_tags(value):
    if isinstance(value, list):
        return value
    if isinstance(value, str) and value.strip():
        try:
            parsed = ast.literal_eval(value)
            if isinstance(parsed, list):
                return [str(t) for t in parsed]
        except (ValueError, SyntaxError):
            return [value]
    return []


def require_loopback(base, allow_remote):
    host = urllib.parse.urlparse(base).hostname or ""
    if allow_remote:
        return
    if host not in ("127.0.0.1", "localhost", "::1"):
        raise HarnessError(
            f"refusing to write to {base}: import/fixture are for a throwaway instance on "
            "loopback. Pass --allow-remote only if you are certain the target is scratch.")


# ═══════════════════════════════════════════════════════════════════════════
# Sub-command: selftest — assert the oracle's own rules, no instance
# ═══════════════════════════════════════════════════════════════════════════

FENCE_BODY = """A real link to [[Alpha]] and one more to [[Beta|the beta]].

```sql
-- [[Note#Heading]] -> "Heading"
-- [[Note|Display]] -> "Display"
```

~~~text
[[Tilde Fenced]]
~~~

Inline `[[Inline Code]]` is not a link, and `x` is not either.

    [[Indented Four Spaces]]
"""

DEDUPE_BODY = """[[Target Page]] first, then [[Target Page]] again,
plus [[target  page|an alias]] and [[Target Page#Heading]].
"""


def selftest_checks():
    """Every clause the check relies on, asserted on synthetic bodies/notes.

    Returns a list of (name, detail) PASS records; raises HarnessError on the
    first broken clause. These run on every `check`, so a corpus that happens to
    contain no fence example or no repeated target cannot make the harness look
    clean by omission.
    """
    passed = []

    # ── fences: both marker styles, and inline code ────────────────────────
    edges, stats = extract_edges(FENCE_BODY)
    targets = [e["raw_target"] for e in edges]
    # "Indented Four Spaces" is indented but NOT fenced: the server only strips
    # ``` / ~~~ fences, so a four-space-indented line is still scanned. Asserting
    # this keeps the oracle on the server's rule instead of a plausible one.
    if targets != ["Alpha", "Beta", "Indented Four Spaces"]:
        raise HarnessError(
            "fence selftest: expected edges ['Alpha','Beta','Indented Four Spaces'], "
            f"got {targets}")
    if stats["fenced_links_ignored"] != 3:
        raise HarnessError(
            f"fence selftest: expected 3 fenced links ignored, got "
            f"{stats['fenced_links_ignored']} ({stats['fenced_examples']})")
    if stats["inline_code_links_ignored"] != 1:
        raise HarnessError(
            f"inline-code selftest: expected 1 inline-code link ignored, got "
            f"{stats['inline_code_links_ignored']}")
    if stats["duplicates_collapsed"] != 0:
        raise HarnessError("fence selftest: fenced/indented links leaked into the dedupe count")
    passed.append(("fence + inline-code exclusion",
                   f"3 fenced links ignored ({', '.join(stats['fenced_examples'])}), "
                   f"1 inline-code link ignored; kept {targets}"))

    # ── duplicates: one edge per (note, normalised target), first wins ─────
    edges, stats = extract_edges(DEDUPE_BODY)
    if len(edges) != 1:
        raise HarnessError(
            f"dedupe selftest: expected 1 edge for one repeated target, got {len(edges)}: "
            f"{[e['raw_target'] for e in edges]}")
    edge = edges[0]
    if edge["raw_target"] != "Target Page" or edge["anchor"] != "" or edge["link_text"] != "":
        raise HarnessError(
            "dedupe selftest: first occurrence must win, got "
            f"{edge['raw_target']!r} anchor={edge['anchor']!r} text={edge['link_text']!r}")
    if edge["occurrences"] != 4:
        raise HarnessError(f"dedupe selftest: expected 4 occurrences, got {edge['occurrences']}")
    if stats["duplicates_collapsed"] != 3:
        raise HarnessError(
            f"dedupe selftest: expected 3 collapsed duplicates, got {stats['duplicates_collapsed']}")
    passed.append(("duplicate collapse",
                   "[[Target Page]] x4 (incl. case/space/anchor variants) -> 1 edge, "
                   "first occurrence wins, 3 collapsed"))

    # ── normalisation: the same key from different spellings ───────────────
    same = ["Q3 — Review", "Q3 - Review", "  q3   -   review  ", "Q3&#32;-&#32;Review", "Q3 – Review"]
    if len({normalize_key(s) for s in same}) != 1:
        raise HarnessError(f"normalise selftest: {same} must share one key, got "
                           f"{[normalize_key(s) for s in same]}")
    if slugify("Q3 — Review") != "q3-review":
        raise HarnessError(f"slugify selftest: got {slugify('Q3 — Review')!r}")
    passed.append(("normalisation",
                   "dash variants, entity escapes, spacing and case fold to one key"))

    # ── resolution: title, slug, proximity, ambiguity ─────────────────────
    notes = [
        {"id": 1, "title": "Overview", "slug": "overview", "folder_id": 10},
        {"id": 2, "title": "Overview", "slug": "overview", "folder_id": 20},
        {"id": 3, "title": "Deep Page", "slug": "deep-page", "folder_id": 11},
        {"id": 4, "title": "Unique Page", "slug": "unique-page", "folder_id": 20},
    ]
    folders = [
        {"id": 10, "parent_id": None},
        {"id": 20, "parent_id": None},
        {"id": 11, "parent_id": 10},
        {"id": 12, "parent_id": None},   # sibling branch: 12 -> 0, 10 -> 0, 20 -> 0
    ]
    r = Resolver(notes, folders)
    same_folder = r.resolve(10, "Overview")          # folder 10 beats folder 20 (distance 0)
    if same_folder["state"] != "resolved" or same_folder["to_note_id"] != 1:
        raise HarnessError(f"proximity selftest: expected note 1, got {same_folder}")
    ambiguous = r.resolve(12, "Overview")            # equal distance to 10 and 20 -> tie
    if ambiguous["state"] != "ambiguous" or ambiguous["to_note_id"] is not None:
        raise HarnessError(f"ambiguity selftest: expected ambiguous, got {ambiguous}")
    by_slug = r.resolve(99, "unique page")           # slug match, no title-key match
    if by_slug["state"] != "resolved" or by_slug["to_note_id"] != 4:
        raise HarnessError(f"slug selftest: expected note 4, got {by_slug}")
    missing = r.resolve(10, "Nobody Home")
    if missing["state"] != "unresolved":
        raise HarnessError(f"unresolved selftest: expected unresolved, got {missing}")
    descendant = r.resolve(11, "Overview")           # 11 is inside 10: distance 1 -> note 1
    if descendant["state"] != "resolved" or descendant["to_note_id"] != 1:
        raise HarnessError(f"ancestor selftest: expected note 1, got {descendant}")
    passed.append(("resolution",
                   "slug + title-key candidates, same-folder wins, equal-nearest tie is "
                   "ambiguous, no candidates is unresolved"))

    return passed


def cmd_selftest(args):
    checks = selftest_checks()
    for name, detail in checks:
        print(f"PASS  {name}: {detail}")
    print(f"\nselftest: {len(checks)} clauses hold (no instance contacted)")
    return EXIT_OK


# ═══════════════════════════════════════════════════════════════════════════
# Sub-command: import — load a corpus into a throwaway instance
# ═══════════════════════════════════════════════════════════════════════════

def cmd_import(args):
    require_loopback(args.base, args.allow_remote)
    corpus = load_corpus(args.corpus)
    folder_rows = load_json(args.folders, "folder tree")
    if not isinstance(folder_rows, list):
        raise HarnessError(f"folder tree at {args.folders} must be a list (empty for a root-only corpus)")
    parent_of = {int(f["id"]): f.get("parent_id") for f in folder_rows}

    client = Client(args.base, args.username, args.password).login_or_register()
    client.health()

    existing_notes, existing_folders = client.list_notes(), client.list_folders()
    if existing_folders or existing_notes:
        # Registration auto-creates one root "Getting Started" note; anything
        # else means this account is not the throwaway target it claims to be.
        stray = [n for n in existing_notes
                 if not (n.get("folder_id") is None and n.get("title") == "Getting Started")]
        if stray or existing_folders:
            raise HarnessError(
                f"instance account {args.username!r} is not empty "
                f"({len(existing_notes)} notes, {len(existing_folders)} folders). "
                "Point --base at a fresh data dir; import will not merge into a live vault.")
        for n in existing_notes:
            status, _ = client.delete_note(n["id"])
            if status not in (200, 204):
                raise HarnessError(f"could not remove auto-created note {n['id']}: {status}")
            print(f"removed auto-created note {n['title']!r} (id {n['id']})")

    # ── folders, parents first ─────────────────────────────────────────────
    order, seen = [], set()

    def visit(parent):
        for fid in sorted(k for k, p in parent_of.items() if p == parent):
            if fid in seen:
                continue
            seen.add(fid)
            order.append(fid)
            visit(fid)

    visit(None)
    order.extend(sorted(set(parent_of) - seen))  # orphans: attach at root

    folder_map = {}
    for fid in order:
        parent = parent_of.get(fid)
        parent_local = folder_map.get(parent) if parent is not None else None
        name = next(f["name"] for f in folder_rows if int(f["id"]) == fid)
        status, body = client.create_folder(name, parent_local)
        if status == 201:
            folder_map[fid] = body["id"]
        elif status == 409:
            hit = next((f for f in client.list_folders()
                        if f.get("name") == name and f.get("parent_id") == parent_local), None)
            if hit is None:
                raise HarnessError(f"folder {name!r} exists but cannot be found for reuse")
            folder_map[fid] = hit["id"]
        else:
            raise HarnessError(f"create folder {name!r}: {status} {body}")
    print(f"folders: {len(folder_map)}/{len(folder_rows)} mapped")

    # ── notes, in corpus-id order ──────────────────────────────────────────
    note_map, failures = {}, []
    started = time.time()
    for i, cid in enumerate(sorted(corpus, key=_sort_key), 1):
        note = corpus[cid]
        folder_local = folder_map.get(int(note["folder_id"])) if note["folder_id"] is not None else None
        # Create with the corpus title. Two corpus notes in one folder can state
        # the same "# H1" while having distinct titles (production allowed that),
        # so creating with the heading would collide on the folder+slug index.
        status, body = client.create_note(note["title"], folder_local, as_tags(note["tags"]))
        if status != 201:
            failures.append({"corpus_id": cid, "stage": "create", "status": status, "body": body})
            continue
        note_map[cid] = body["id"]
        if note["content"]:
            status, body2 = client.patch_note(body["id"], note["content"], body["content_hash"])
            if status != 200:
                failures.append({"corpus_id": cid, "stage": "content", "status": status, "body": body2})
                del note_map[cid]
                continue
        if i % 100 == 0:
            print(f"  {i}/{len(corpus)} notes ({time.time() - started:.0f}s)")

    print(f"notes: {len(note_map)}/{len(corpus)} imported, {len(failures)} failure(s), "
          f"{time.time() - started:.0f}s")
    if failures:
        raise HarnessError(f"import incomplete: {len(failures)} note(s) failed, first: {failures[0]}")

    # ── settle: let the server converge titles onto each file's "# H1" ─────
    #
    # internal/notes/reconcile.go step 9b (ticket N7) rewrites a note's title to
    # the heading its file states, on the first reconcile/watch pass after the
    # body appears. 97 of the 1e notes state a heading that differs from their
    # stored production title. Waiting here means `check` compares a settled
    # graph instead of one that is about to be reindexed underneath it.
    converged = _settle(client, corpus, note_map, args.reconcile_wait, args.settle_seconds,
                       args.settle_poll, args.workers)
    if converged:
        print(f"  {converged} note(s) had their title converged onto their stated '# H1' "
              "(ticket N7 — the file states the title; this is the server's rule, not drift)")
    if args.map_out:
        os.makedirs(os.path.dirname(os.path.abspath(args.map_out)), exist_ok=True)
        with open(args.map_out, "w", encoding="utf-8") as fh:
            json.dump(note_map, fh, indent=1, sort_keys=True)
        print(f"wrote id map: {args.map_out}")
    return EXIT_OK


def _graph_signature(client, ids, workers):
    """A hash of every note's edges and backlinks — used only to tell whether the
    index is still moving."""
    with ThreadPoolExecutor(max_workers=workers) as pool:
        links = list(pool.map(client.links, ids))
        backs = list(pool.map(client.backlinks, ids))
    payload = json.dumps([links, backs], sort_keys=True, ensure_ascii=False)
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()


def _settle(client, corpus, note_map, reconcile_wait, settle_seconds, poll, workers):
    """Wait until the loaded corpus has stopped moving, then report what converged.

    A corpus load is not instantly comparable: notes are created and filled one
    at a time, so an early note's edges are indexed before later notes exist, and
    the server converges each title onto its body's "# H1" on a reconcile pass
    (internal/notes/reconcile.go steps 9b and 10). Both are repaired by the
    watcher's periodic full reconcile, so wait past one interval and then verify
    rather than assume: titles converged AND the graph identical across two
    consecutive samples. A load that never settles is a harness error, not a
    comparison.

    Returns the number of titles that had to converge onto their heading.
    """
    started = time.time()
    while time.time() - started < reconcile_wait:
        time.sleep(min(poll, max(0.0, reconcile_wait - (time.time() - started))))

    ids = [note_map[cid] for cid in corpus]
    deadline = time.time() + settle_seconds
    previous = None
    while True:
        titles = {n["id"]: n["title"] for n in client.list_notes()}
        drifted = [cid for cid in corpus
                   if titles.get(note_map[cid]) != expected_title(corpus[cid])]
        signature = _graph_signature(client, ids, workers)
        if not drifted and signature == previous:
            return sum(1 for cid in corpus if titles[note_map[cid]] != corpus[cid]["title"])
        previous = signature
        if time.time() >= deadline:
            if drifted:
                raise HarnessError(
                    f"{len(drifted)} note title(s) still differ from their stated '# H1' after "
                    f"{settle_seconds}s (first: {[(c, corpus[c]['title']) for c in drifted[:3]]}) — "
                    "the instance is not converging, so there is nothing to compare against")
            raise HarnessError(
                "the link graph was still changing after the settle window — the instance has "
                "not finished indexing this corpus, so a comparison now would measure a "
                "half-built index")
        time.sleep(poll)


def _sort_key(key):
    try:
        return (0, int(key))
    except (TypeError, ValueError):
        return (1, str(key))


# ═══════════════════════════════════════════════════════════════════════════
# Sub-command: check — the comparison itself
# ═══════════════════════════════════════════════════════════════════════════

def cmd_check(args):
    checks = selftest_checks()          # the rules are asserted before any data
    corpus = load_corpus(args.corpus)
    raw_map = load_json(args.map, "id map")
    if not isinstance(raw_map, dict) or not raw_map:
        raise HarnessError(f"id map at {args.map} must be a non-empty object")

    missing = sorted(set(corpus) - set(raw_map), key=_sort_key)
    if missing:
        raise HarnessError(
            f"id map covers {len(raw_map)} of {len(corpus)} corpus notes; "
            f"unmapped (first 5): {missing[:5]}")
    extra = sorted(set(raw_map) - set(corpus), key=_sort_key)
    if extra:
        raise HarnessError(
            f"id map has {len(extra)} entr(y/ies) the corpus does not (first 5: {extra[:5]}) — "
            "the map and the corpus are from different snapshots")

    client = Client(args.base, args.username, args.password).login_or_register()
    client.health()

    instance_notes = client.list_notes()
    folders = client.list_folders()
    by_id = {n["id"]: n for n in instance_notes}

    # ── preconditions: the corpus really is the instance's content ─────────
    if len(instance_notes) != len(corpus):
        raise HarnessError(
            f"corpus has {len(corpus)} notes but the instance account has "
            f"{len(instance_notes)} — the comparison would not be against this corpus "
            "(import the corpus into a fresh instance, or point --base at the right one)")
    mapped_ids = [int(v) for v in raw_map.values()]
    if len(set(mapped_ids)) != len(mapped_ids):
        raise HarnessError("id map is not injective — two corpus notes share one instance note")
    unmapped_instance = sorted(set(by_id) - set(mapped_ids))
    if unmapped_instance:
        raise HarnessError(f"instance has notes outside the id map: {unmapped_instance[:5]}")
    dangling = sorted(i for i in set(mapped_ids) if i not in by_id)
    if dangling:
        raise HarnessError(
            f"the id map points at {len(dangling)} note id(s) the instance does not have "
            f"(first: {dangling[:5]}) — the map and the instance are from different loads")
    # A note's stored title is the file's stated heading when it has one (the
    # server converges it there — internal/notes/reconcile.go step 9b), else the
    # title it was created with. Anything else means the instance did not load
    # this corpus and the comparison would be measuring the import.
    title_mismatch, titles_from_heading = [], 0
    for cid in corpus:
        want = expected_title(corpus[cid])
        got = by_id[int(raw_map[cid])]["title"]
        if got != want:
            title_mismatch.append({"corpus_id": cid, "expected": want, "instance": got})
        elif got != corpus[cid]["title"]:
            titles_from_heading += 1
    if title_mismatch:
        raise HarnessError(
            f"{len(title_mismatch)} note title(s) are neither the corpus title nor the body's "
            f"stated '# H1' (resolution uses titles, so this is a data problem, not an oracle one): "
            f"{title_mismatch[:3]}")
    slugs_recomputed = sum(
        1 for cid in corpus
        if corpus[cid].get("slug") and corpus[cid]["slug"] != by_id[int(raw_map[cid])].get("slug"))

    resolver = Resolver(instance_notes, folders)

    # ── content fidelity (a truncated body would fake a mismatch) ──────────
    ids = [int(raw_map[cid]) for cid in corpus]
    with ThreadPoolExecutor(max_workers=args.workers) as pool:
        bodies = dict(zip(ids, pool.map(client.get_note, ids)))
    content_bad = []
    for cid in corpus:
        iid = int(raw_map[cid])
        want, got = corpus[cid]["content"], bodies[iid].get("content") or ""
        if want.rstrip("\n") != got.rstrip("\n"):
            content_bad.append({"corpus_id": cid, "instance_id": iid,
                                "corpus_len": len(want), "instance_len": len(got)})
    if content_bad:
        raise HarnessError(
            f"{len(content_bad)} note body/bodies differ between corpus and instance "
            f"(first: {content_bad[0]}) — the comparison would measure the import, not the rules")

    # ── fetch both edge endpoints for every note ───────────────────────────
    with ThreadPoolExecutor(max_workers=args.workers) as pool:
        links_by = dict(zip(ids, pool.map(client.links, ids)))
        back_by = dict(zip(ids, pool.map(client.backlinks, ids)))

    findings = collections.defaultdict(list)
    totals = collections.Counter()
    corpus_fences = corpus_inline = corpus_dupes = 0

    # Extract every body once: the per-note comparison and the inbound set both
    # need it, and a 471-note corpus makes a second pass noticeable.
    prepared = {}
    for cid in corpus:
        prepared[cid] = extract_edges(corpus[cid]["content"])

    # The oracle's inbound set: which note each resolved edge lands on, keyed by
    # the note it lands on. An ambiguous or unresolved edge contributes nothing —
    # exactly like the server, which only writes to_note_id when it resolved.
    inbound_by_note = collections.defaultdict(set)
    for cid, (edges_, _stats) in prepared.items():
        iid = int(raw_map[cid])
        src_folder = by_id[iid].get("folder_id")
        for edge in edges_:
            res = resolver.resolve(src_folder, edge["raw_target"])
            if res["state"] == "resolved":
                inbound_by_note[res["to_note_id"]].add(
                    (iid, edge["anchor"], edge["link_text"]))

    for cid in sorted(corpus, key=_sort_key):
        iid = int(raw_map[cid])
        note = corpus[cid]
        src_folder = by_id[iid].get("folder_id")
        edges, stats = prepared[cid]

        corpus_fences += stats["fenced_links_ignored"]
        corpus_inline += stats["inline_code_links_ignored"]
        corpus_dupes += stats["duplicates_collapsed"]

        if edges:
            totals["notes_with_links"] += 1
        totals["source_edges"] += len(edges)

        server_edges = links_by[iid]
        totals["server_edges"] += len(server_edges)

        # The server response must itself be one row per edge key.
        by_key, dupes = {}, []
        for e in server_edges:
            key = edge_key(e.get("raw_target") or "")
            if key in by_key:
                dupes.append(e.get("raw_target"))
            by_key.setdefault(key, e)
        if dupes:
            findings["server_duplicate_edge"].append(
                {"corpus_id": cid, "title": note["title"], "instance_id": iid,
                 "raw_targets": dupes})

        for edge in edges:
            server_edge = by_key.get(edge["key"])
            if server_edge is None:
                findings["edge_missing_from_server"].append({
                    "corpus_id": cid, "title": note["title"], "instance_id": iid,
                    "target": edge["raw_target"], "raw": f"[[{edge['raw_target']}]]",
                    "anchor": edge["anchor"], "link_text": edge["link_text"],
                    "oracle_state": resolver.resolve(src_folder, edge["raw_target"])["state"],
                })
                continue
            totals["edges_matched"] += 1

            oracle_res = resolver.resolve(src_folder, edge["raw_target"])
            server_state = ("resolved" if server_edge.get("to_note_id") is not None
                            else ("ambiguous" if server_edge.get("ambiguous") else "unresolved"))
            if server_state != oracle_res["state"]:
                findings["resolution_state"].append({
                    "corpus_id": cid, "title": note["title"], "instance_id": iid,
                    "target": edge["raw_target"], "oracle": oracle_res["state"],
                    "server": server_state,
                    "oracle_to_note_id": oracle_res["to_note_id"],
                    "server_to_note_id": server_edge.get("to_note_id"),
                    "oracle_candidates": oracle_res["candidates"],
                })
            elif server_state == "resolved" and oracle_res["to_note_id"] is not None:
                server_prod = _corpus_of(raw_map, server_edge.get("to_note_id"))
                if server_prod != _corpus_of(raw_map, oracle_res["to_note_id"]):
                    findings["resolved_to_different_note"].append({
                        "corpus_id": cid, "title": note["title"], "instance_id": iid,
                        "target": edge["raw_target"],
                        "oracle_to_corpus_id": _corpus_of(raw_map, oracle_res["to_note_id"]),
                        "server_to_corpus_id": server_prod,
                        "server_to_title": server_edge.get("to_title"),
                    })

            for field, want, got in (("raw_target", edge["raw_target"], server_edge.get("raw_target")),
                                     ("anchor", edge["anchor"], server_edge.get("anchor") or ""),
                                     ("link_text", edge["link_text"], server_edge.get("link_text") or "")):
                if want != got:
                    findings[f"{field}_mismatch"].append({
                        "corpus_id": cid, "title": note["title"], "instance_id": iid,
                        "target": edge["raw_target"], "oracle": want, "server": got})

        for key, server_edge in by_key.items():
            if all(e["key"] != key for e in edges):
                findings["edge_extra_in_server"].append({
                    "corpus_id": cid, "title": note["title"], "instance_id": iid,
                    "server_raw_target": server_edge.get("raw_target")})

        # ── backlinks: compare against the oracle's resolved inbound set ───
        wanted = {(iid,) + row for row in inbound_by_note.get(iid, set())}
        got = {(iid, b["from_note_id"], b.get("anchor") or "", b.get("link_text") or "")
               for b in back_by[iid]}
        totals["backlink_rows"] += len(got)
        for row in got - wanted:
            findings["backlink_extra"].append(
                {"corpus_id": cid, "title": note["title"], "from_corpus_id": _corpus_of(raw_map, row[1]),
                 "from_title": by_id.get(row[1], {}).get("title")})
        for row in wanted - got:
            findings["backlink_missing"].append(
                {"corpus_id": cid, "title": note["title"], "from_corpus_id": _corpus_of(raw_map, row[1]),
                 "anchor": row[2], "link_text": row[3]})

    if totals["source_edges"] == 0 or totals["edges_matched"] == 0:
        raise HarnessError(
            "compared zero edges — the corpus produced no links, so a zero-mismatch "
            "result would be an empty comparison, not a pass")

    if args.calibrate:
        if corpus_fences == 0:
            raise HarnessError(
                "--calibrate: this corpus contains no fenced-code link examples, so it cannot "
                "show that fence handling was what removed the old artefact")
        if corpus_dupes == 0:
            raise HarnessError(
                "--calibrate: this corpus contains no repeated targets, so it cannot show that "
                "dedupe was what removed the old artefact")

    total = sum(len(v) for v in findings.values())
    summary = {
        "base": args.base,
        "corpus_file": args.corpus,
        "corpus_notes": len(corpus),
        "instance_notes": len(instance_notes),
        "titles_from_heading": titles_from_heading,
        "slugs_recomputed": slugs_recomputed,
        "notes_with_links": totals["notes_with_links"],
        "source_edges": totals["source_edges"],
        "server_edges": totals["server_edges"],
        "edges_matched": totals["edges_matched"],
        "backlink_rows": totals["backlink_rows"],
        "corpus_fenced_links_ignored": corpus_fences,
        "corpus_inline_code_links_ignored": corpus_inline,
        "corpus_duplicate_occurrences_collapsed": corpus_dupes,
        "total_mismatches": total,
        "mismatch_counts": {k: len(v) for k, v in sorted(findings.items())},
    }

    print("\nWikilink oracle vs server")
    print(f"  corpus            : {args.corpus}")
    print(f"  instance          : {args.base}")
    print(f"  notes             : {summary['corpus_notes']} corpus / {summary['instance_notes']} instance")
    if titles_from_heading or slugs_recomputed:
        print(f"  corpus fidelity   : {titles_from_heading} title(s) converged onto the body's "
              f"'# H1', {slugs_recomputed} slug(s) recomputed from the title — the server's "
              "own load rules, not oracle disagreements")
    print(f"  notes with links  : {summary['notes_with_links']}")
    print(f"  source edges      : {summary['source_edges']} (deduped per note+target)")
    print(f"  server edges      : {summary['server_edges']}")
    print(f"  backlink rows     : {summary['backlink_rows']}")
    print(f"  ignored by fences : {corpus_fences} link occurrence(s)")
    print(f"  ignored in inline : {corpus_inline} link occurrence(s)")
    print(f"  collapsed dupes   : {corpus_dupes} repeat occurrence(s) folded into an existing edge")
    print("\nSelf-checks (run on every check, independent of the corpus):")
    for name, detail in checks:
        print(f"  PASS  {name}: {detail}")
    print(f"\nMISMATCHES: {total}")
    for category, rows in sorted(findings.items()):
        print(f"  {category}: {len(rows)}")
        for row in rows[:args.max_examples]:
            print(f"    {json.dumps(row, ensure_ascii=False)[:400]}")

    if args.report:
        report = {"generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                  "summary": summary,
                  "calibration": {
                      "fence": {"corpus_fenced_links_ignored": corpus_fences,
                                "clause": dict(checks[0:1])},
                      "dedupe": {"corpus_duplicate_occurrences_collapsed": corpus_dupes,
                                 "clause": dict(checks[1:2])},
                  },
                  "findings": {k: v for k, v in findings.items()}}
        os.makedirs(os.path.dirname(os.path.abspath(args.report)), exist_ok=True)
        with open(args.report, "w", encoding="utf-8") as fh:
            json.dump(report, fh, indent=1, ensure_ascii=False)
        print(f"\nreport: {args.report}")

    if total:
        print("\nFAIL: the oracle and the server disagree — see the findings above.")
        return EXIT_MISMATCH
    print("\nOK: zero mismatches across every note's edges and backlinks.")
    return EXIT_OK


def _corpus_of(raw_map, instance_id):
    for cid, iid in raw_map.items():
        if int(iid) == instance_id:
            return cid
    return None


# ═══════════════════════════════════════════════════════════════════════════
# Sub-command: fixture — synthesise the ambiguous-title case
# ═══════════════════════════════════════════════════════════════════════════

FIXTURE_TITLE = "Oracle Fixture Ambiguous Target"


def cmd_fixture(args):
    require_loopback(args.base, args.allow_remote)
    client = Client(args.base, args.username, args.password).login_or_register()
    client.health()

    created_notes, created_folders = [], []

    def mkfolder(name):
        status, body = client.create_folder(name, None)
        if status != 201:
            raise HarnessError(f"fixture folder {name!r}: {status} {body}")
        created_folders.append(body["id"])
        return body["id"]

    def mknote(title, folder_id, content):
        status, body = client.create_note(title, folder_id, ["oracle-fixture"])
        if status != 201:
            raise HarnessError(f"fixture note {title!r}: {status} {body}")
        created_notes.append(body["id"])
        if content:
            status, body2 = client.patch_note(body["id"], content, body["content_hash"])
            if status != 200:
                raise HarnessError(f"fixture note body {title!r}: {status} {body2}")
        return body["id"]

    try:
        folder_a = mkfolder("Oracle Fixture A")
        folder_b = mkfolder("Oracle Fixture B")
        folder_src = mkfolder("Oracle Fixture Source")

        # Same title (and therefore same slug) in two sibling folders.
        target_a = mknote(FIXTURE_TITLE, folder_a, "# A copy\n")
        target_b = mknote(FIXTURE_TITLE, folder_b, "# B copy\n")
        # A source at equal distance from both -> ambiguous.
        source_far = mknote("Oracle Fixture Far Linker", folder_src,
                            f"Links [[{FIXTURE_TITLE}]] from a third branch.\n")
        # A source inside folder A -> the same-folder candidate wins.
        source_near = mknote("Oracle Fixture Near Linker", folder_a,
                             f"Links [[{FIXTURE_TITLE}]] from the same folder.\n")

        server_notes = {n["id"]: n for n in client.list_notes()}
        folders = client.list_folders()
        resolver = Resolver(list(server_notes.values()), folders)

        failures = []

        far_edges = client.links(source_far)
        if len(far_edges) != 1:
            failures.append(f"far linker: expected 1 edge, got {len(far_edges)}")
        else:
            edge = far_edges[0]
            if not edge.get("ambiguous") or edge.get("to_note_id") is not None:
                failures.append(f"far linker: server did not report ambiguous: {edge}")
            oracle_far = resolver.resolve(folder_src, FIXTURE_TITLE)
            if oracle_far["state"] != "ambiguous":
                failures.append(f"far linker: oracle says {oracle_far['state']}, not ambiguous")

        near_edges = client.links(source_near)
        if len(near_edges) != 1:
            failures.append(f"near linker: expected 1 edge, got {len(near_edges)}")
        else:
            edge = near_edges[0]
            if edge.get("to_note_id") != target_a or edge.get("ambiguous"):
                failures.append(f"near linker: expected resolved to {target_a}, got {edge}")
            oracle_near = resolver.resolve(folder_a, FIXTURE_TITLE)
            if oracle_near["state"] != "resolved" or oracle_near["to_note_id"] != target_a:
                failures.append(f"near linker: oracle says {oracle_near}, expected {target_a}")

        # The ambiguous edge must not fabricate a backlink on either candidate.
        for target, label in ((target_a, "A"), (target_b, "B")):
            sources = {b["from_note_id"] for b in client.backlinks(target)}
            if source_far in sources:
                failures.append(f"ambiguous edge produced a backlink on candidate {label}")
            if target == target_a and source_near not in sources:
                failures.append("near linker's resolved edge is missing from A's backlinks")

        if failures:
            for f in failures:
                print(f"FAIL  {f}")
            return EXIT_MISMATCH
        print("PASS  ambiguous-title fixture")
        print(f"  two notes titled {FIXTURE_TITLE!r} in sibling folders (ids {target_a}, {target_b})")
        print(f"  source in a third folder -> /links reports ambiguous=true, no to_note_id")
        print(f"  source in the same folder -> /links resolves to the near candidate ({target_a})")
        print("  ambiguous edge creates no backlink; resolved edge does")
        return EXIT_OK
    finally:
        if not args.keep:
            for nid in reversed(created_notes):
                client.delete_note(nid)
            for fid in reversed(created_folders):
                client.delete_folder(fid)
            print("  fixture removed from the instance")


# ═══════════════════════════════════════════════════════════════════════════
# CLI
# ═══════════════════════════════════════════════════════════════════════════

def build_parser():
    p = argparse.ArgumentParser(
        prog="verify_wikilinks.py",
        description="Compare a Thornotes instance's wikilink graph against an independent oracle.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__.split("USAGE")[0].strip())
    sub = p.add_subparsers(dest="command", required=True)

    def common(sp):
        sp.add_argument("--base", default="http://127.0.0.1:8111",
                        help="instance base URL (default: %(default)s)")
        sp.add_argument("--username", default="oracle")
        sp.add_argument("--password", default="oracle-local-2026")
        sp.add_argument("--allow-remote", action="store_true",
                        help="allow import/fixture to write to a non-loopback instance")

    imp = sub.add_parser("import", help="load a corpus into a throwaway instance (writes)")
    common(imp)
    imp.add_argument("--corpus", required=True, help='notes JSON: {"<id>": {"title","folder_id","content"}}')
    imp.add_argument("--folders", required=True, help='folder JSON: [{"id","name","parent_id"}]')
    imp.add_argument("--map-out", help="write the corpus-id -> instance-id map here")
    imp.add_argument("--settle-seconds", type=int, default=120,
                     help="how long to keep checking that the load has settled (default: %(default)s)")
    imp.add_argument("--settle-poll", type=int, default=5,
                     help="seconds between settle checks (default: %(default)s)")
    imp.add_argument("--reconcile-wait", type=int, default=35,
                     help="seconds to wait for one full reconcile pass before checking the load "
                          "settled; must exceed the instance's --watch-interval (default 30s)")
    imp.add_argument("--workers", type=int, default=8)
    imp.set_defaults(func=cmd_import)

    chk = sub.add_parser("check", help="compare every note against the oracle (read-only)")
    common(chk)
    chk.add_argument("--corpus", required=True)
    chk.add_argument("--map", required=True, help="id map written by `import --map-out`")
    chk.add_argument("--report", help="write the full report JSON here")
    chk.add_argument("--workers", type=int, default=8)
    chk.add_argument("--max-examples", type=int, default=5,
                     help="findings printed per category (default: %(default)s)")
    chk.add_argument("--calibrate", action="store_true",
                     help="fail unless the corpus exercises both fence and duplicate cases")
    chk.set_defaults(func=cmd_check)

    fix = sub.add_parser("fixture", help="synthesise + verify the ambiguous-title case (writes)")
    common(fix)
    fix.add_argument("--keep", action="store_true", help="leave the fixture notes in place")
    fix.set_defaults(func=cmd_fixture)

    st = sub.add_parser("selftest", help="assert the oracle's own rules; no instance needed")
    st.set_defaults(func=cmd_selftest)

    return p


def main(argv=None):
    args = build_parser().parse_args(argv)
    try:
        return args.func(args)
    except HarnessError as e:
        print(f"\nHARNESS ERROR: {e}", file=sys.stderr)
        print("Exit 2: no evidence was produced, so this run is NOT a pass.", file=sys.stderr)
        return EXIT_NO_EVIDENCE


if __name__ == "__main__":
    sys.exit(main())
