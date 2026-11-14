package repository_test

// Search conformance: one table of fixtures and queries, run unchanged against
// SQLite (FTS5) and against a real MariaDB/MySQL (InnoDB FULLTEXT).
//
// This file is the acceptance test for tickets 452 (tags AND, terms AND) and 619
// (a term shorter than the full-text index's floor is still findable), and for
// the search contract documented in internal/searchtext. It deliberately does not
// import that package: the expected snippets below are literals, so the table
// states what a user must see rather than mirroring the implementation that
// produces it.
//
// The MySQL leg runs whenever THORNOTES_TEST_MYSQL_DSN is set (as in CI), and
// skips otherwise. Set THORNOTES_REQUIRE_MYSQL=1 to turn that skip into a
// failure, so a missing DSN cannot silently reduce this suite to SQLite only.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/th0rn0/thornotes/internal/db"
	"github.com/th0rn0/thornotes/internal/dbtest"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/repository/mysql"
	"github.com/th0rn0/thornotes/internal/repository/sqlite"
	"github.com/th0rn0/thornotes/internal/searchtext"
)

const (
	conformanceDSNEnv     = "THORNOTES_TEST_MYSQL_DSN"
	conformanceRequireEnv = "THORNOTES_REQUIRE_MYSQL"
)

// TestMain takes the same advisory lock as internal/repository/mysql and
// internal/db: every package that truncates the shared MySQL test schema has to
// run alone, and `go test ./...` runs packages in parallel.
func TestMain(m *testing.M) {
	os.Exit(dbtest.Locked(conformanceDSNEnv, m.Run))
}

// searcher is the single method this contract is about.
type searcher interface {
	Search(ctx context.Context, userID int64, query string, tags []string) ([]*model.SearchResult, error)
}

// noteCreator and userCreator are the write paths the fixtures need. Both
// dialects expose the same shape, so one seeding function serves both.
type noteCreator interface {
	Create(ctx context.Context, n *model.Note) (*model.Note, error)
}

type userCreator interface {
	Create(ctx context.Context, username, passwordHash, uuid string) (*model.User, error)
}

type backend struct {
	name   string
	notes  noteCreator
	users  userCreator
	search searcher
}

func newSQLiteBackend(t *testing.T) *backend {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "search-contract.db"))
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	return &backend{
		name:   "sqlite",
		notes:  sqlite.NewNoteRepo(pool.ReadDB, pool.WriteDB),
		users:  sqlite.NewUserRepo(pool.WriteDB),
		search: sqlite.NewSearchRepo(pool.ReadDB, pool.WriteDB),
	}
}

func newMySQLBackend(t *testing.T) *backend {
	t.Helper()
	dsn := os.Getenv(conformanceDSNEnv)
	if dsn == "" {
		msg := conformanceDSNEnv + " not set — skipping MariaDB/MySQL search conformance leg"
		if os.Getenv(conformanceRequireEnv) == "1" {
			t.Fatalf("%s (%s=1, MySQL must not be skipped)", msg, conformanceRequireEnv)
		}
		t.Skip(msg)
	}
	pool, err := db.OpenMySQL(dsn)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })
	truncateMySQL(t, pool)

	return &backend{
		name:   "mysql",
		notes:  mysql.NewNoteRepo(pool.WriteDB),
		users:  mysql.NewUserRepo(pool.WriteDB),
		search: mysql.NewSearchRepo(pool.WriteDB),
	}
}

// truncateMySQL empties every application table so each test starts from a
// known-empty schema. Foreign key checks are off for the duration so table
// order does not matter; schema_migrations is left alone.
func truncateMySQL(t *testing.T, pool *db.Pool) {
	t.Helper()
	ctx := context.Background()

	conn, err := pool.WriteDB.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	_, err = conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0")
	require.NoError(t, err)
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SET FOREIGN_KEY_CHECKS = 1")
	}()

	for _, table := range []string{
		"api_token_folder_permissions", "api_tokens", "journals", "note_links",
		"notes", "sessions", "folders", "users",
	} {
		_, err := conn.ExecContext(ctx, "DELETE FROM "+table)
		require.NoError(t, err, "reset table %s", table)
	}
}

// Snippet fixtures, shared with internal/searchtext's unit tests by intent: the
// same content must produce the same excerpt on both engines.
const (
	snippetStartContent   = "Snippetstart marks the very first word of this note, so the excerpt needs no leading ellipsis at all."
	snippetMiddleContent  = "The first sentence of this note is plain filler text before the match. middleword appears here and the note continues afterwards with more plain filler text."
	snippetTailContent    = "The opening of this note is a long run of plain filler words used only to push the interesting part of the text towards the end of the note so that a bounded excerpt has to be trimmed somewhere. tailword"
	snippetLongContent    = "plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. plain filler words to make this note long enough. longword and more filler words after the match and more filler words after the match and more filler words after the match and more filler words after the match and more filler words after the match and more filler words after the match and more filler words after the match and more filler words after the match and more filler words after the match and more filler words after the match and more filler words after the match and more filler words after the match and more filler words after the match and more filler words after the match and more filler words after the match and more filler words after the match and more filler words after the match and more filler words after the match and more filler words after the match and more filler words after the match"
	snippetUnicodeContent = "äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü Wasser äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü äöü"
)

// seedFixtures creates one user and the note set the table below queries, and
// returns slug → id. Both backends get byte-identical fixtures.
//
// The five "body" notes share a title length and a token count on purpose: the
// two engines score relevance differently, so a cross-engine ordering assertion
// is only meaningful where the contract's tie-breakers (title, then id) decide.
func seedFixtures(t *testing.T, b *backend) (int64, map[string]int64) {
	t.Helper()
	ctx := context.Background()

	user, err := b.users.Create(ctx, "contract-user", "hash", "contract-user-uuid")
	require.NoError(t, err, "%s: create user", b.name)

	fixtures := []*model.Note{
		// ── the "body" group: equal title length, equal token count ──────────
		{Title: "Boiler Fixes", Slug: "boiler-fixes", Tags: []string{"home", "maintenance"},
			Content: "The boiler body needs a service before this coming winter."},
		{Title: "Budget Notes", Slug: "budget-notes", Tags: []string{"home"},
			Content: "The budget body covers every household running cost each month."},
		{Title: "Sprint Recap", Slug: "sprint-recap", Tags: []string{"work"},
			Content: "The sprint body records what the review meeting said today."},
		{Title: "Kitchen Taps", Slug: "kitchen-taps", Tags: []string{"home", "kitchen"},
			Content: "The kitchen body notes where the cold water drips again."},
		// Two occurrences of the term: both engines rank it above the others.
		{Title: "Garage Stuff", Slug: "garage-stuff", Tags: []string{},
			Content: "The body of the body copy here is a sample."},

		// ── a tag stored with different case than the query ──────────────────
		{Title: "Case Tagged", Slug: "case-tagged", Tags: []string{"Home"},
			Content: "Nothing in this prose mentions the searched term at all; it carries a tag and says tagged."},

		// ── tag values that look like LIKE patterns ──────────────────────────
		{Title: "Percent Release", Slug: "percent-release", Tags: []string{"100%_done"},
			Content: "Percent release checklist for the next build."},
		{Title: "Percent Old", Slug: "percent-old", Tags: []string{"100Xdone"},
			Content: "Percent release notes from the old build."},

		// ── short terms: below the full-text index's floor ───────────────────
		// "go" cannot be answered by the MySQL FULLTEXT index at all
		// (innodb_ft_min_token_size is 3), "kvm" is exactly at that floor and
		// "wifi" is the control above it. "go" appears in a title here and in
		// content there, because the short-term scan has to cover both columns.
		{Title: "Go Bag", Slug: "go-bag", Tags: []string{},
			Content: "Packed for emergencies: a torch, rations and a first-aid kit."},
		{Title: "Short Lexicon", Slug: "short-lexicon", Tags: []string{"home"},
			Content: "Tiny words: go, kvm and wifi, with one extra token at the end."},

		// ── LIKE metacharacters in a search term ────────────────────────────
		// The bait notes hold what an unescaped pattern would reach: "500" for
		// '%50%%', "axb" for '%a_b%'.
		{Title: "Discount Ledger", Slug: "discount-ledger", Tags: []string{},
			Content: "Discount 50% on the annual plan."},
		{Title: "Count Bait", Slug: "count-bait", Tags: []string{},
			Content: "Reference 500 units shipped."},
		{Title: "Underscore Token", Slug: "underscore-token", Tags: []string{},
			Content: "The identifier a_b is in this sentence."},
		{Title: "Underscore Bait", Slug: "underscore-bait", Tags: []string{},
			Content: "The identifier axb is in this sentence."},

		// ── stopwords: a word the index cannot hold must not empty a query ────
		// One note carries every word of InnoDB's default stopword list (and
		// "ago", for the short-term scan below), so a query containing a
		// stopword has something to find once the word stops being required.
		// The title holds none of them, and none of the terms the rest of this
		// table searches for: this note is reachable only through "marker" and
		// through the short-term scan for "go" (inside "ago").
		{Title: "Stopword Prose", Slug: "stopword-prose", Tags: []string{},
			Content: "About the notes on this page: that is what we were told when we asked where it came from, who will come with us, for how long, und was sonst noch. About are com, for from how, the this that, what when where who will with www - every word of InnoDB's list is in this sentence, written two days ago. Marker: stopmark."},

		// ── snippet fixtures ─────────────────────────────────────────────────
		{Title: "Snippet Start", Slug: "snippet-start", Tags: []string{}, Content: snippetStartContent},
		{Title: "Snippet Middle", Slug: "snippet-middle", Tags: []string{}, Content: snippetMiddleContent},
		{Title: "Snippet Tail", Slug: "snippet-tail", Tags: []string{}, Content: snippetTailContent},
		{Title: "Titlesearch", Slug: "snippet-title", Tags: []string{},
			Content: "Nothing relevant in the prose here."},
		{Title: "Snippet Long", Slug: "snippet-long", Tags: []string{}, Content: snippetLongContent},
		{Title: "Snippet Unicode", Slug: "snippet-unicode", Tags: []string{}, Content: snippetUnicodeContent},
	}

	ids := make(map[string]int64, len(fixtures))
	for _, n := range fixtures {
		n.UserID = user.ID
		n.DiskPath = "contract-user/" + n.Slug + ".md"
		n.ContentHash = "hash-" + n.Slug
		created, err := b.notes.Create(ctx, n)
		require.NoError(t, err, "%s: create note %s", b.name, n.Slug)
		ids[n.Slug] = created.ID
	}
	return user.ID, ids
}

type contractCase struct {
	name string
	// query and tags are the request, verbatim as the API would receive them.
	query string
	tags  []string
	// want is the exact, ordered list of slugs the search must return.
	want []string
	// wantSnippets pins the snippet for the slugs it names; other results are
	// not checked for content.
	wantSnippets map[string]string
}

func contractCases() []contractCase {
	homeBody := []string{"boiler-fixes", "budget-notes", "kitchen-taps"}
	// garage-stuff mentions "body" twice, so it leads on relevance; the rest tie
	// and fall back to title order.
	bodyRanked := []string{"garage-stuff", "boiler-fixes", "budget-notes", "kitchen-taps", "sprint-recap"}

	return []contractCase{
		// ── text terms ───────────────────────────────────────────────────────
		{name: "single term", query: "body", want: bodyRanked},
		{
			name:  "two terms are AND, both in one note",
			query: "boiler body",
			want:  []string{"boiler-fixes"},
		},
		{
			name:  "two terms are AND, one in each note, returns nothing",
			query: "boiler kitchen",
			want:  nil,
		},
		{
			name:  "two terms are AND, second term only in one content",
			query: "body service",
			want:  []string{"boiler-fixes"},
		},

		// ── tags are AND ─────────────────────────────────────────────────────
		{
			name:  "one tag",
			query: "body",
			tags:  []string{"home"},
			want:  homeBody,
		},
		{
			// The regression this ticket exists for: a note carrying both tags
			// must be found by the conjunction of both.
			name:  "two tags on one note, verbatim regression",
			query: "body",
			tags:  []string{"home", "maintenance"},
			want:  []string{"boiler-fixes"},
		},
		{
			name:  "tag order does not matter",
			query: "body",
			tags:  []string{"maintenance", "home"},
			want:  []string{"boiler-fixes"},
		},
		{
			name:  "duplicate tag is the same predicate",
			query: "body",
			tags:  []string{"home", "home"},
			want:  homeBody,
		},
		{
			name:  "one tag",
			query: "body",
			tags:  []string{"work"},
			want:  []string{"sprint-recap"},
		},
		{
			name:  "three tags, note carries two",
			query: "body",
			tags:  []string{"home", "maintenance", "work"},
			want:  nil,
		},
		{
			name:  "tag on no note",
			query: "body",
			tags:  []string{"missing"},
			want:  nil,
		},
		{
			// Thorno's call, 2026-11-09: #Home is #home. Case never decides a match.
			name:  "tag case variation is the same tag (query upper, stored lower)",
			query: "body",
			tags:  []string{"Home"},
			want:  homeBody,
		},
		{
			name:  "tag case variation is the same tag (query lower, stored upper)",
			query: "tagged",
			tags:  []string{"home"},
			want:  []string{"case-tagged"},
		},
		{
			name:  "tag case variation is the same tag (both upper)",
			query: "tagged",
			tags:  []string{"HOME"},
			want:  []string{"case-tagged"},
		},
		{
			name:  "percent and underscore are values, not patterns",
			query: "percent",
			tags:  []string{"100%_done"},
			want:  []string{"percent-release"},
		},
		{
			name:  "a tag without the pattern characters is not matched by them",
			query: "percent",
			tags:  []string{"100Xdone"},
			want:  []string{"percent-old"},
		},

		// ── short terms: the full-text index cannot answer for them ──────────
		// On MySQL a token of one or two characters is not in the index, so
		// these queries used to return nothing at all while SQLite returned the
		// note — the silent cross-engine disagreement this ticket closes. They
		// are matched by a scan of the caller's notes on both engines.
		{
			name:  "short term: two characters, found in a title and in a content",
			query: "go",
			want:  []string{"go-bag", "short-lexicon", "stopword-prose"},
		},
		{
			name:  "short term: three characters is the index's floor, still found",
			query: "kvm",
			want:  []string{"short-lexicon"},
		},
		{
			name:  "four characters is the control above the floor",
			query: "wifi",
			want:  []string{"short-lexicon"},
		},
		{
			name:  "short term: it does not exist",
			query: "zz",
			want:  nil,
		},
		{
			name:  "short term is AND with a tag the note carries",
			query: "go",
			tags:  []string{"home"},
			want:  []string{"short-lexicon"},
		},
		{
			name:  "short term is AND with a tag the note does not carry",
			query: "go",
			tags:  []string{"work"},
			want:  nil,
		},
		{
			name:  "short term and a long term are AND",
			query: "tiny go",
			want:  []string{"short-lexicon"},
		},
		{
			// The scan compares case-insensitively on both engines, like the
			// index does, so the case a term is typed in never decides a match.
			name:  "short term matches whatever case it is typed in",
			query: "GO",
			want:  []string{"go-bag", "short-lexicon", "stopword-prose"},
		},
		{
			// '%' in a search term is a character. Unescaped, the pattern
			// '%50%%' would also reach "Reference 500 units shipped".
			name:  "percent in a search term is not a wildcard",
			query: "50%",
			want:  []string{"discount-ledger"},
		},
		{
			// ... and '_' is a character too, not "any one character".
			name:  "underscore in a search term is not a wildcard",
			query: "a_b",
			want:  []string{"underscore-token"},
		},

		// ── stopwords: the word cannot narrow the query, and cannot empty it ──
		// InnoDB refuses to index a stopword, so requiring one in boolean mode
		// returned zero rows however many notes contained it. The word is
		// dropped instead, on both engines, so these five queries are all the
		// same query as "body" — the row set must not change.
		{
			name:  "stopword: a leading stopword narrows nothing",
			query: "the body",
			want:  bodyRanked,
		},
		{
			name:  "stopword: a trailing stopword narrows nothing",
			query: "body the",
			want:  bodyRanked,
		},
		{
			name:  "stopword: the same word typed in capitals",
			query: "The Body",
			want:  bodyRanked,
		},
		{
			name:  "stopword: punctuation around the stopword",
			query: "the, body",
			want:  bodyRanked,
		},
		{
			name:  "stopword: two stopwords around a term",
			query: "who will body",
			want:  bodyRanked,
		},
		{
			// The stopword is dropped, the term it was ANDed with decides the
			// rows: "tagged" appears in one note only.
			name:  "stopword: dropped, the remaining term still decides",
			query: "when the tagged",
			want:  []string{"case-tagged"},
		},
		{
			// A stopword is not searchable on either engine, so a query made
			// only of stopwords finds nothing — not everything, and not one
			// engine's set. SQLite used to answer this with every note
			// containing "the" while MySQL answered with none.
			name:  "stopword: a stopword is not a query on its own",
			query: "the",
			want:  nil,
		},
		{
			name:  "stopword: only stopwords find nothing",
			query: "the what when",
			want:  nil,
		},
		{
			// 619 is untouched: dropping the stopword leaves "go", which is
			// still below the index floor and still matched by the LIKE scan
			// (including inside "ago").
			name:  "stopword: beside a short term, which still scans",
			query: "the go",
			want:  []string{"go-bag", "short-lexicon", "stopword-prose"},
		},
		{
			name:  "stopword: beside a tag filter",
			query: "the body",
			tags:  []string{"home"},
			want:  homeBody,
		},

		// ── snippets ─────────────────────────────────────────────────────────
		{
			name:  "snippet: match at the start",
			query: "snippetstart",
			want:  []string{"snippet-start"},
			wantSnippets: map[string]string{
				"snippet-start": "<mark>Snippetstart</mark> marks the very first word of this note, so the excerpt needs no leading ellipsis at all.",
			},
		},
		{
			name:  "snippet: match in the middle",
			query: "middleword",
			want:  []string{"snippet-middle"},
			wantSnippets: map[string]string{
				"snippet-middle": "…is plain filler text before the match. <mark>middleword</mark> appears here and the note continues afterwards with more plain filler text.",
			},
		},
		{
			name:  "snippet: match in the last characters",
			query: "tailword",
			want:  []string{"snippet-tail"},
			wantSnippets: map[string]string{
				"snippet-tail": "…excerpt has to be trimmed somewhere. <mark>tailword</mark>",
			},
		},
		{
			// The note matches on its title, so the content holds no term: the
			// excerpt is the opening, unmarked — not the empty string.
			name:  "snippet: no match in content",
			query: "titlesearch",
			want:  []string{"snippet-title"},
			wantSnippets: map[string]string{
				"snippet-title": "Nothing relevant in the prose here.",
			},
		},
		{
			name:  "snippet: very long note is bounded",
			query: "longword",
			want:  []string{"snippet-long"},
			wantSnippets: map[string]string{
				"snippet-long": "…words to make this note long enough. <mark>longword</mark>" +
					strings.Repeat(" and more filler words after the match", 3) +
					" and more filler words after the…",
			},
		},
		{
			name:  "snippet: unicode is not cut mid-rune",
			query: "wasser",
			want:  []string{"snippet-unicode"},
			wantSnippets: map[string]string{
				"snippet-unicode": "…" + strings.Repeat("äöü ", 9) + "<mark>Wasser</mark>" +
					strings.Repeat(" äöü", 38) + "…",
			},
		},
	}
}

// runContract seeds the backend and checks every case against the table.
func runContract(t *testing.T, b *backend) {
	t.Helper()
	ctx := context.Background()
	userID, _ := seedFixtures(t, b)

	for _, tc := range contractCases() {
		t.Run(tc.name, func(t *testing.T) {
			results, err := b.search.Search(ctx, userID, tc.query, tc.tags)
			require.NoError(t, err)

			assert.Equal(t, tc.want, slugsOf(results), "%s: %q tags=%v", b.name, tc.query, tc.tags)
			for slug, wantSnippet := range tc.wantSnippets {
				found := false
				for _, r := range results {
					if r.Slug != slug {
						continue
					}
					found = true
					assert.Equal(t, wantSnippet, r.Snippet, "%s: snippet for %s", b.name, slug)
				}
				assert.True(t, found, "%s: no result for %s to check the snippet of", b.name, slug)
			}
		})
	}
}

func TestSearchContract_SQLite(t *testing.T) {
	runContract(t, newSQLiteBackend(t))
}

func TestSearchContract_MySQL(t *testing.T) {
	runContract(t, newMySQLBackend(t))
}

// TestSearchContract_Stopwords is the acceptance test for the stopword defect,
// stated as the ticket states it: for every word InnoDB refuses to index, a query
// containing it returns the same rows as the same query with the word removed —
// on both engines — and a query made only of those words returns nothing on both.
//
// It walks searchtext.Stopwords rather than repeating the list, because the point
// is the list that ships: a word added to it is covered here without this file
// being edited, and the list's contents are pinned separately by
// internal/searchtext's TestStopwords_FrozenList.
func TestSearchContract_Stopwords(t *testing.T) {
	for _, makeBackend := range []struct {
		name string
		new  func(*testing.T) *backend
	}{
		{name: "sqlite", new: newSQLiteBackend},
		{name: "mysql", new: newMySQLBackend},
	} {
		t.Run(makeBackend.name, func(t *testing.T) {
			runStopwordContract(t, makeBackend.new(t))
		})
	}
}

// runStopwordContract is the per-engine half: "marker" finds the one note that
// carries every stopword, so it is the control every stopword query must reduce
// to. Before the fix the MySQL leg failed on every one of the 18 with zero rows
// while the SQLite leg returned the note for each word that appears in it, and a
// query of a stopword alone returned every SQLite note containing the word and
// no MySQL row at all.
func runStopwordContract(t *testing.T, b *backend) {
	t.Helper()
	ctx := context.Background()
	userID, _ := seedFixtures(t, b)

	control, err := b.search.Search(ctx, userID, "marker", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"stopword-prose"}, slugsOf(control),
		"%s: the control query must find the note that holds every stopword", b.name)

	for _, word := range searchtext.Stopwords {
		t.Run(word, func(t *testing.T) {
			// The same query, three shapes a person types: the word first, the
			// word in capitals, and the word with a comma after it.
			for _, query := range []string{
				word + " marker",
				strings.ToUpper(word) + " marker",
				word + ", marker",
			} {
				got, err := b.search.Search(ctx, userID, query, nil)
				require.NoError(t, err)
				assert.Equal(t, slugsOf(control), slugsOf(got),
					"%s: %q must return the rows of the same query without %q", b.name, query, word)
			}

			// ... and alone it is not a query at all: nothing, not everything.
			alone, err := b.search.Search(ctx, userID, word, nil)
			require.NoError(t, err)
			assert.Empty(t, slugsOf(alone), "%s: %q on its own must find nothing", b.name, word)
		})
	}
}

// TestSearchContract_CrossEngine is the ticket's actual claim: the same
// fixtures and the same queries return the same rows, in the same order, with
// the same snippets, on both engines.
func TestSearchContract_CrossEngine(t *testing.T) {
	lite := newSQLiteBackend(t)
	maria := newMySQLBackend(t) // skips the whole test when no DSN is set

	ctx := context.Background()
	liteUser, _ := seedFixtures(t, lite)
	mariaUser, _ := seedFixtures(t, maria)

	for _, tc := range contractCases() {
		t.Run(tc.name, func(t *testing.T) {
			liteResults, err := lite.search.Search(ctx, liteUser, tc.query, tc.tags)
			require.NoError(t, err)
			mariaResults, err := maria.search.Search(ctx, mariaUser, tc.query, tc.tags)
			require.NoError(t, err)

			assert.Equal(t, slugsOf(liteResults), slugsOf(mariaResults), "ids and order")
			require.Len(t, mariaResults, len(liteResults))
			for i := range liteResults {
				assert.Equal(t, liteResults[i].Snippet, mariaResults[i].Snippet, "snippet for %s", liteResults[i].Slug)
				assert.Equal(t, liteResults[i].Title, mariaResults[i].Title)
				assert.Equal(t, liteResults[i].Tags, mariaResults[i].Tags)
			}
		})
	}
}

func slugsOf(results []*model.SearchResult) []string {
	if len(results) == 0 {
		return nil
	}
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.Slug
	}
	return out
}
