package wikilinks

import "testing"

func i64p(v int64) *int64 { return &v }

func TestResolve_SameFolderWins(t *testing.T) {
	fA, fB := i64p(10), i64p(20)
	r := NewResolver(
		[]NoteRef{
			{ID: 1, FolderID: fA, Title: "Overview", Slug: "overview"},
			{ID: 2, FolderID: fB, Title: "Overview", Slug: "overview"},
		},
		[]FolderRef{{ID: 10}, {ID: 20}},
	)
	res := r.Resolve(fA, "Overview")
	if !res.Resolved || res.NoteID != 1 || res.Ambiguous {
		t.Fatalf("same-folder should win: %+v", res)
	}
}

func TestResolve_NearestCommonAncestor(t *testing.T) {
	fSub, fSub2, fB := i64p(11), i64p(12), i64p(20)
	r := NewResolver(
		[]NoteRef{
			{ID: 1, FolderID: fSub, Title: "X", Slug: "x"}, // in A/Sub
			{ID: 2, FolderID: fB, Title: "X", Slug: "x"},   // in B
		},
		[]FolderRef{
			{ID: 10},
			{ID: 20},
			{ID: 11, ParentID: i64p(10)},
			{ID: 12, ParentID: i64p(10)},
		},
	)
	// Source in A/Sub2: neither candidate is same-folder, but A/Sub shares
	// ancestor A with the source, while B only shares root.
	res := r.Resolve(fSub2, "X")
	if !res.Resolved || res.NoteID != 1 || res.Ambiguous {
		t.Fatalf("nearest common ancestor should win: %+v", res)
	}
}

func TestResolve_SingleGlobalCandidate(t *testing.T) {
	fA := i64p(10)
	r := NewResolver([]NoteRef{{ID: 7, FolderID: fA, Title: "Foo", Slug: "foo"}}, []FolderRef{{ID: 10}})
	res := r.Resolve(nil, "Foo")
	if !res.Resolved || res.NoteID != 7 {
		t.Fatalf("got %+v", res)
	}
}

func TestResolve_Unresolved(t *testing.T) {
	r := NewResolver(nil, nil)
	res := r.Resolve(nil, "Nonexistent")
	if res.Resolved || res.Ambiguous || res.NoteID != 0 || len(res.Candidates) != 0 {
		t.Fatalf("unresolved should not be ambiguous: %+v", res)
	}
}

func TestResolve_AmbiguousTie(t *testing.T) {
	fA, fB := i64p(10), i64p(20)
	r := NewResolver(
		[]NoteRef{
			{ID: 1, FolderID: fA, Title: "X", Slug: "x"},
			{ID: 2, FolderID: fB, Title: "X", Slug: "x"},
		},
		[]FolderRef{{ID: 10}, {ID: 20}},
	)
	// Source at root: both candidates are equally far -> ambiguous.
	res := r.Resolve(nil, "X")
	if res.Resolved || !res.Ambiguous || len(res.Candidates) != 2 {
		t.Fatalf("expected ambiguous with 2 candidates: %+v", res)
	}
}

func TestResolve_SlugDedupe(t *testing.T) {
	f := i64p(10)
	r := NewResolver(
		[]NoteRef{
			{ID: 1, FolderID: f, Title: "x", Slug: "x"},
			{ID: 2, FolderID: f, Title: "x", Slug: "x-2"},
		},
		[]FolderRef{{ID: 10}},
	)
	// [[x]] matches both via title_key -> ambiguous (same folder).
	amb := r.Resolve(f, "x")
	if amb.Resolved || !amb.Ambiguous || len(amb.Candidates) != 2 {
		t.Fatalf("[[x]] should be ambiguous, got %+v", amb)
	}
	// [[x-2]] matches only the deduped slug -> resolves exactly.
	exact := r.Resolve(f, "x-2")
	if !exact.Resolved || exact.NoteID != 2 {
		t.Fatalf("[[x-2]] should resolve to the deduped slug, got %+v", exact)
	}
}

func TestResolve_DashFolding(t *testing.T) {
	f := i64p(10)
	r := NewResolver(
		[]NoteRef{{ID: 1, FolderID: f, Title: "Foo — Bar", Slug: "foo-bar"}},
		[]FolderRef{{ID: 10}},
	)
	// Link written with ASCII hyphen resolves to the em-dash title.
	res := r.Resolve(f, "Foo - Bar")
	if !res.Resolved || res.NoteID != 1 {
		t.Fatalf("dash folding should resolve: %+v", res)
	}
}

func TestResolve_EdgeKeyPresent(t *testing.T) {
	f := i64p(10)
	r := NewResolver([]NoteRef{{ID: 1, FolderID: f, Title: "Foo", Slug: "foo"}}, []FolderRef{{ID: 10}})
	res := r.Resolve(f, "Foo")
	if res.EdgeKey != EdgeKey("Foo") || res.Key != "foo" || res.Slug != "foo" {
		t.Fatalf("metadata mismatch: %+v", res)
	}
}
