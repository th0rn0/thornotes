package notes

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/wikilinks"
)

// Links that were orphaned before the rewrite existed cannot be reconstructed
// from the database. `note_links` stores sha256(NormalizeKey(target)) — a hash,
// not a name — and the old titles themselves are gone: a converged title
// overwrote the row, and an imported corpus never had them. What is left is the
// honest state of the vault: some links name nothing.
//
// This report is deliberately read-only and deliberately does not guess. An
// automatic fuzzy repair would replace a visible dangling link (which a person
// can see and fix) with an invisible wrong one (which nobody will ever notice),
// and the wrongness would be silent. So the report lists each broken link with
// the note it sits in, the target it names, and — as a *hint for a human* — the
// note whose title is nearest to that target.
//
// Repairing them properly needs a title history: for each note whose title
// moved, what it used to be called. The notes tree keeps one only where the
// instance was started with -enable-git-history (the git commits record every
// save), and that history is per-instance: it cannot be assumed, and it does
// not exist at all on a vault imported without it. Without that history the
// only honest repair is a human reading the dangling links.

// BrokenLink is one [[link]] that does not resolve today.
type BrokenLink struct {
	NoteID    int64  `json:"note_id"`
	NoteTitle string `json:"note_title"`
	DiskPath  string `json:"disk_path"`
	// Target, Anchor and LinkText are the link exactly as the author wrote it.
	Target   string `json:"target"`
	Anchor   string `json:"anchor,omitempty"`
	LinkText string `json:"link_text,omitempty"`
	// Ambiguous is true when two notes claim the target and the resolver
	// refuses to pick one. Such a link is not broken, it is contested — it is
	// reported because it also fails to resolve, and the distinction matters
	// to whoever repairs it.
	Ambiguous bool `json:"ambiguous"`
	// NearMatch names the note whose title looks closest to the target, or ""
	// when nothing is close. It is a hint for a human reader and is never
	// applied: the server does not rewrite a link on a guess.
	NearMatch string `json:"near_match,omitempty"`
}

// LinkInventory is the read-only report of links that do not resolve.
type LinkInventory struct {
	// Notes is the number of notes scanned, Links the number of links that do
	// resolve (one per distinct target per note, as the index counts them).
	Notes int `json:"notes"`
	Links int `json:"links"`
	// Broken is every link that fails to resolve, ordered by note then target.
	Broken []BrokenLink `json:"broken"`
}

// BrokenLinks reports every link in the user's vault that does not resolve
// today. It reads notes and folders only — it writes nothing, and it does not
// touch note_links, so it answers the same on a freshly imported vault whose
// index has never been built.
func (s *Service) BrokenLinks(ctx context.Context, userID int64) (LinkInventory, error) {
	notes, err := s.notes.ListForContext(ctx, userID, nil)
	if err != nil {
		return LinkInventory{}, err
	}
	resolver, items, err := s.loadResolverAndNotes(ctx, userID)
	if err != nil {
		return LinkInventory{}, err
	}

	inv := LinkInventory{Notes: len(notes), Broken: []BrokenLink{}}
	for _, n := range notes {
		// One entry per distinct target, the same dedupe the index applies, so
		// a note that names a missing page three times is one broken link.
		seen := make(map[string]struct{}, 4)
		for _, ref := range wikilinks.ExtractLinks(n.Content) {
			hash := wikilinks.EdgeKey(ref.Target)
			if _, dup := seen[hash]; dup {
				continue
			}
			seen[hash] = struct{}{}

			res := resolver.Resolve(n.FolderID, ref.Target)
			if res.Resolved {
				inv.Links++
				continue
			}
			inv.Broken = append(inv.Broken, BrokenLink{
				NoteID:    n.ID,
				NoteTitle: n.Title,
				DiskPath:  n.DiskPath,
				Target:    ref.Target,
				Anchor:    ref.Anchor,
				LinkText:  ref.LinkText,
				Ambiguous: res.Ambiguous,
				NearMatch: nearMatch(ref.Target, items),
			})
		}
	}

	sort.Slice(inv.Broken, func(i, j int) bool {
		a, b := inv.Broken[i], inv.Broken[j]
		if a.NoteTitle != b.NoteTitle {
			return a.NoteTitle < b.NoteTitle
		}
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		return a.NoteID < b.NoteID
	})
	return inv, nil
}

// nearMatchDistance is the edit distance at or below which a title is reported
// as a plausible intended target. Three is enough for a dropped or added word
// fragment ("Notes" vs "Note") and small enough that unrelated titles stay out.
const nearMatchDistance = 3

// nearMatch returns a description of the note whose normalized title is closest
// to target, or "" when no title is close enough to be worth mentioning.
//
// Three relationships count as "close", in that order: a small edit distance
// (a typo, a dropped plural); one title being a prefix of the other ("Retitle"
// → "Retitle Notes"); and a shared leading run of words, which is what a title
// that had its last word replaced looks like ("Topic 01 Draft" → "Topic 01
// Final") — a large edit distance for an obviously related name.
func nearMatch(target string, notes []*model.NoteListItem) string {
	key := wikilinks.NormalizeKey(target)
	if key == "" {
		return ""
	}

	// Lower score is a better hint. The bands are ordered so a small edit
	// distance always outranks a structural hint, and a structural hint
	// outranks nothing.
	const (
		scorePrefix     = nearMatchDistance + 1
		scoreWordPrefix = nearMatchDistance + 2
	)
	type candidate struct {
		note  *model.NoteListItem
		score int
		// how describes the relationship, for the human reading the report.
		how string
	}
	best := candidate{score: scoreWordPrefix + 1}

	for _, n := range notes {
		nk := wikilinks.NormalizeKey(n.Title)
		if nk == "" || nk == key {
			continue
		}
		if d := editDistance(key, nk); d <= nearMatchDistance && d < best.score {
			best = candidate{note: n, score: d, how: fmt.Sprintf("%d edit(s)", d)}
		}
		// A prefix relationship is worth reporting even at a larger distance,
		// and only when the shared part is long enough to mean something.
		shorter, longer := key, nk
		if len(shorter) > len(longer) {
			shorter, longer = longer, shorter
		}
		if len(shorter) >= 6 && strings.HasPrefix(longer, shorter) && scorePrefix < best.score {
			how := "title starts with the target"
			if len(nk) < len(key) {
				how = "target starts with the title"
			}
			best = candidate{note: n, score: scorePrefix, how: how}
		}
		// The same idea one level up: most of the title's words in the same
		// order, with the tail replaced. Requiring at least half the shorter
		// title's words keeps "Project Plan" from hinting at "Project Budget".
		if shared := leadingWords(key, nk); shared >= 2 {
			shortest := len(strings.Fields(nk))
			if w := len(strings.Fields(key)); w < shortest {
				shortest = w
			}
			if shared*2 >= shortest && scoreWordPrefix < best.score {
				best = candidate{note: n, score: scoreWordPrefix,
					how: fmt.Sprintf("%d leading words shared", shared)}
			}
		}
	}

	if best.note == nil {
		return ""
	}
	return fmt.Sprintf("%q (note %d, %s)", best.note.Title, best.note.ID, best.how)
}

// leadingWords counts how many words a and b share from the start, in order.
func leadingWords(a, b string) int {
	aw, bw := strings.Fields(a), strings.Fields(b)
	n := 0
	for n < len(aw) && n < len(bw) && aw[n] == bw[n] {
		n++
	}
	return n
}

// editDistance is the Levenshtein distance between a and b over runes. Both
// inputs are normalized keys, which are short, so the quadratic table is fine.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := 0; j <= len(br); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
