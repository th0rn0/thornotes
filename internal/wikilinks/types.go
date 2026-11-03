// Package wikilinks implements phase-1 wikilink extraction, normalisation and
// resolution. It is a pure, deterministic core (no DB, no API, no UI) that is
// ported from the reference implementation at
// /opt/data/skills/research/llm-wiki/scripts/thornotes_wiki.py and the spec in
// Thornotes note 323 §4–5. Later slices wire it to the repositories.
package wikilinks

// LinkRef is one parsed [[...]] link.
type LinkRef struct {
	Raw      string // inner text exactly as written, e.g. "Overview#Trust boundaries"
	Target   string // "Overview" (the note title/path to resolve)
	Anchor   string // "Trust boundaries" ("" when no '#')
	LinkText string // "the map" ("" when no '|' pipe)
}

// NoteRef is the minimal note data the resolver needs.
type NoteRef struct {
	ID       int64
	FolderID *int64 // nil = root
	Title    string
	Slug     string
}

// FolderRef is the minimal folder data needed to compute nearest-common-ancestor.
type FolderRef struct {
	ID       int64
	ParentID *int64 // nil = root folder
}

// Resolution is the outcome of resolving one link target.
type Resolution struct {
	Target     string  // raw target text as written
	Key        string  // NormalizeKey(Target)
	Slug       string  // Slugify(Target)
	EdgeKey    string  // sha256(Key), hex
	NoteID     int64   // 0 when unresolved
	Resolved   bool    // true when exactly one candidate wins
	Ambiguous  bool    // true when >1 equally-near candidate (no guess)
	Candidates []int64 // candidate ids, populated only when ambiguous
}
