package repository_test

// Accent folding: the one class of query on which SQLite used to be the side
// that missed (ticket 626).
//
// Search agreed with MariaDB on ASCII, on punctuation and on short terms, and
// disagreed on accents. SQLite's FTS5 `ascii` tokenizer folds ASCII case only,
// so it indexed `café` as a token distinct from the ASCII spelling of the word:
// a query for CAFÉ returned nothing, while MariaDB's utf8mb4_unicode_ci found the
// note. Development runs SQLite, so development looked worse than production, and
// a self-hoster on SQLite had a search that silently found nothing.
//
// Migration 010 swaps the tokenizer for `porter unicode61`, which folds the
// accents and case-foldings and keeps porter's stemming chained on top. This file
// is the user-facing statement of that: the same queries, through Search, on both
// engines.
//
// Each note's title is plain ASCII and contains none of the queried words, so a
// match can only come from the accented content being indexed in a form the query
// reaches. That is deliberate — an earlier draft of this test put the ASCII
// spelling in the titles ("Naive Note") and every query matched its title, which
// made four of the five cases look already fixed.
//
// Two differences between the engines are asserted rather than glossed over,
// because they are documented behaviour, not defects to be discovered later:
//
//   - `ß` → `ss`. unicode61 folds by Unicode decomposition and `ß` has none, so
//     STRASSE finds "Straße" on MariaDB and not on SQLite. No FTS5 tokenizer
//     offers that rule; it is the residual the migration cannot close.
//   - stemming. `meeting` finds "meetings" on SQLite (porter is still chained)
//     and not on MariaDB (InnoDB does not stem) — unchanged by this ticket, and
//     the reason porter is chained rather than replaced by bare unicode61.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/model"
)

// accentExpect is what each engine must do, stated per engine so that a
// difference is a parameter rather than a surprise.
type accentExpect struct {
	// foldsSharpS is set for engines whose collation maps ß to ss.
	foldsSharpS bool
	// stems is set for engines whose tokenizer runs a stemmer.
	stems bool
}

func TestSearchAccents_SQLite(t *testing.T) {
	runAccents(t, newSQLiteBackend(t), accentExpect{foldsSharpS: false, stems: true})
}

func TestSearchAccents_MySQL(t *testing.T) {
	runAccents(t, newMySQLBackend(t), accentExpect{foldsSharpS: true, stems: false})
}

// accentFixtures are the same five cases the ticket measured, plus a stemming
// fixture. The accented form appears only in the content.
var accentFixtures = []*model.Note{
	{Title: "Corner Table", Slug: "accent-cafe", Tags: []string{},
		Content: "we met at the café on the corner"},
	{Title: "Token Argument", Slug: "accent-naive", Tags: []string{},
		Content: "a naïve assumption about tokens"},
	{Title: "Closed Road", Slug: "accent-strasse", Tags: []string{},
		Content: "the Straße was closed"},
	{Title: "Spring Trip", Slug: "accent-istanbul", Tags: []string{},
		Content: "İstanbul in the spring"},
	{Title: "Everything", Slug: "accent-uber", Tags: []string{},
		Content: "über alles"},
	{Title: "Standup", Slug: "accent-meeting", Tags: []string{},
		Content: "notes from the meetings"},
	// A note that contains none of the queried words, so an engine that matched
	// everything would not pass by accident.
	{Title: "Quiet Harbour", Slug: "accent-control", Tags: []string{},
		Content: "the harbour was quiet all afternoon"},
}

func runAccents(t *testing.T, b *backend, expect accentExpect) {
	t.Helper()
	ctx := context.Background()

	user, err := b.users.Create(ctx, "accent-user", "hash", "accent-user-uuid")
	require.NoError(t, err, "%s: create user", b.name)

	for _, n := range accentFixtures {
		n.UserID = user.ID
		n.DiskPath = "accent-user/" + n.Slug + ".md"
		n.ContentHash = "hash-" + n.Slug
		_, err := b.notes.Create(ctx, n)
		require.NoError(t, err, "%s: create note %s", b.name, n.Slug)
	}

	mustFind := func(query string, want []string) {
		t.Helper()
		results, err := b.search.Search(ctx, user.ID, query, nil)
		require.NoError(t, err, "%s: search %q", b.name, query)
		require.Equal(t, want, slugsOf(results), "%s: search %q", b.name, query)
	}

	// ── the accent cases ────────────────────────────────────────────────────
	// Each query is typed without the accent its note carries, except CAFÉ which
	// is typed with it in upper case: both directions have to fold.
	mustFind("CAFÉ", []string{"accent-cafe"})
	mustFind("café", []string{"accent-cafe"})
	mustFind("naive", []string{"accent-naive"})
	mustFind("istanbul", []string{"accent-istanbul"})
	mustFind("uber", []string{"accent-uber"})

	// ── ß → ss: the residual ────────────────────────────────────────────────
	if expect.foldsSharpS {
		mustFind("STRASSE", []string{"accent-strasse"})
	} else {
		// unicode61 folds by decomposition and ß has no decomposition, so the
		// note is not reachable by its ASCII spelling on SQLite. Asserted, not
		// tolerated silently: if a future tokenizer or an application-side
		// normalization closes this, the assertion is what says so.
		mustFind("STRASSE", nil)
		mustFind("strasse", nil)
		// The accented form is still findable, so the note is indexed — it is one
		// spelling of it that is not.
		mustFind("Straße", []string{"accent-strasse"})
	}

	// ── porter is still chained ─────────────────────────────────────────────
	// The tokenizer changed base; it did not stop stemming.
	if expect.stems {
		mustFind("meeting", []string{"accent-meeting"})
	}

	// ── the control ─────────────────────────────────────────────────────────
	// None of the accent queries above may have matched this note.
	mustFind("harbour", []string{"accent-control"})
}
