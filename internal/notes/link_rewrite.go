package notes

import (
	"context"
	"sort"

	"github.com/rs/zerolog/log"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/wikilinks"
)

// A title is how a note is addressed. Link resolution matches on it
// (internal/wikilinks/resolve.go), so the moment a title moves every
// `[[old title]]` that resolved to that note names nothing at all — the link
// the author wrote is orphaned by an edit that happened somewhere else
// (ticket 633).
//
// The repair is to move the links, not to keep the old name alive. An alias or
// redirect table was considered and rejected: it would leave the vault with two
// names for one note forever, and every future read would have to agree with
// both. So when a title moves, the notes that link to it are rewritten — only
// the target of each link, never the anchor or the display text, and never
// inside code, where a `[[...]]` is documentation of the syntax rather than a
// link.

// titleMove is one title change whose inbound links must follow it.
type titleMove struct {
	// noteID is the note whose title moved.
	noteID int64
	// oldTitle and newTitle are the row values either side of the move.
	oldTitle string
	newTitle string
	// oldSlug is the note's slug before the move. A rename that changes the
	// title usually changes the slug with it, which orphans links written in
	// the slug form (`[[old-title]]`) just as surely as the title form: both
	// resolved to this note through the same resolver.
	oldSlug string
}

// linkRewriteResult counts what one rewrite pass did. Every number here is
// reported: a pass that edits other people's notes in silence is the same
// trust problem as losing the links.
type linkRewriteResult struct {
	// NotesRewritten is the number of source notes whose body changed.
	NotesRewritten int
	// LinksRewritten is the number of [[links]] whose target was moved.
	LinksRewritten int
	// LinksSkippedShared counts links left alone because the old title (or the
	// old slug) named more than one note before the move. Such a link was
	// already ambiguous, and rewriting it would silently pick a winner.
	LinksSkippedShared int
	// LinksSkippedUnresolved counts links left alone because the new title does
	// not resolve back to the moved note from the linking note's folder — the
	// rewrite would point somewhere else, which is worse than a visible
	// dangling link.
	LinksSkippedUnresolved int
}

// linkRewritePass is the world one rewrite pass works in: the resolver and the
// note list it was built from. A pass builds it once, *after* every title it is
// about has moved, and hands it to each rewrite — that ordering is what makes it
// the right view (the world the rewritten links have to resolve in), and sharing
// it is what makes the pass affordable: loading the whole note list once per
// moved title made the loop O(movers × notes), so a 200-note vault whose every
// title drifted spent ~5 s of startup reconcile in it, in front of the 466
// readiness gate, on every restart until the vault converged.
type linkRewritePass struct {
	resolver *wikilinks.Resolver
	// items is the note list the resolver was built from. The rewrite asks it
	// who else answers to a title that just moved, to refuse a shared old
	// title rather than silently pick a winner.
	items []*model.NoteListItem
}

// loadLinkRewritePass builds the resolver for a rewrite pass. During a title
// rewrite nothing but link targets changes, so one resolver stays true for the
// whole pass: titles, slugs and folders are fixed once the retitles are applied.
func (s *Service) loadLinkRewritePass(ctx context.Context, userID int64) (*linkRewritePass, error) {
	resolver, items, err := s.loadResolverAndNotes(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &linkRewritePass{resolver: resolver, items: items}, nil
}

// rewriteInboundLinks points the notes that link to a moved title at the new
// one. Callers hold the tree-shared lock (or the exclusive one) but must NOT
// hold the moved note's lock: this pass takes each source note's own lock, one
// at a time, so two renames that cross-link cannot deadlock.
//
// pass is the resolver for the whole pass, built after the titles moved; a nil
// pass is loaded here, which is only appropriate for a single move.
//
// Writes go through the normal save path — per-note lock, content-hash compare,
// disk first then DB, file rolled back if the DB refuses — so a rewrite cannot
// clobber an edit in flight and lands the same way any other save does.
func (s *Service) rewriteInboundLinks(ctx context.Context, userID int64, mv titleMove, pass *linkRewritePass) (linkRewriteResult, error) {
	var res linkRewriteResult
	if mv.oldTitle == "" || mv.oldTitle == mv.newTitle {
		return res, nil
	}

	if pass == nil {
		p, err := s.loadLinkRewritePass(ctx, userID)
		if err != nil {
			return res, err
		}
		pass = p
	}
	resolver, items := pass.resolver, pass.items
	// The resolver describes the world *after* the move, which is the world the
	// rewritten links have to resolve in.

	oldKey := wikilinks.NormalizeKey(mv.oldTitle)
	// Did the old name belong to more than one note? Then every link that used
	// it was ambiguous before the move, and there is no honest way to pick this
	// note out of that set now.
	shared := false
	for _, n := range items {
		if n.ID == mv.noteID {
			continue
		}
		if wikilinks.NormalizeKey(n.Title) == oldKey || (mv.oldSlug != "" && n.Slug == mv.oldSlug) {
			shared = true
			break
		}
	}

	inbound, err := s.links.ListInboundForRename(ctx, userID, mv.noteID)
	if err != nil {
		return res, err
	}

	// Group by source note: the rewrite is one save per note, not one per link.
	sources := make([]int64, 0, len(inbound))
	bySource := make(map[int64][]model.InboundLink, len(inbound))
	for _, il := range inbound {
		if _, seen := bySource[il.FromNoteID]; !seen {
			sources = append(sources, il.FromNoteID)
		}
		bySource[il.FromNoteID] = append(bySource[il.FromNoteID], il)
	}
	// Deterministic write order, so two runs over the same vault log the same
	// sequence of notes.
	sort.Slice(sources, func(i, j int) bool { return sources[i] < sources[j] })

	// names reports whether a link target addresses the title that moved. The
	// comparison is the resolver's, not a string compare: same NormalizeKey for
	// the title form, same Slugify for the filename form. A target that
	// resolves to a different note never reaches here — the edge list is the
	// set of links that already resolved to this one.
	names := func(target string) bool {
		if wikilinks.NormalizeKey(target) == oldKey {
			return true
		}
		return mv.oldSlug != "" && wikilinks.Slugify(target) == mv.oldSlug
	}

	for _, fromID := range sources {
		links := bySource[fromID]
		matching := 0
		for _, il := range links {
			if names(il.RawTarget) {
				matching++
			}
		}
		if matching == 0 {
			// The edge resolved here by some other spelling; re-resolution
			// (below) owns it.
			continue
		}
		if shared {
			res.LinksSkippedShared += matching
			log.Info().
				Int64("user_id", userID).
				Int64("note_id", fromID).
				Int64("target_note_id", mv.noteID).
				Str("from", mv.oldTitle).
				Str("to", mv.newTitle).
				Int("links", matching).
				Msg("rename: inbound links left alone — the old title named more than one note")
			continue
		}
		// The new title has to land back on this note from the linking note's
		// folder. If it does not (another note claims it more closely, or it is
		// itself ambiguous), pointing the link at it would be a silent guess.
		if r := resolver.Resolve(links[0].FromFolderID, mv.newTitle); !r.Resolved || r.NoteID != mv.noteID {
			res.LinksSkippedUnresolved += matching
			log.Info().
				Int64("user_id", userID).
				Int64("note_id", fromID).
				Int64("target_note_id", mv.noteID).
				Str("from", mv.oldTitle).
				Str("to", mv.newTitle).
				Int("links", matching).
				Msg("rename: inbound links left alone — the new title does not resolve back to this note")
			continue
		}

		n, changed := s.rewriteNoteLinks(ctx, userID, fromID, mv.newTitle, names, resolver)
		if !changed {
			continue
		}
		res.NotesRewritten++
		res.LinksRewritten += n
	}

	// Whatever is left still claims a title that no longer exists. Mark it
	// unresolved so the graph tells the truth: a dangling link the reader can
	// see, not a resolved edge pointing at a name that is gone.
	if err := s.repairInbound(ctx, userID, mv.noteID, resolver); err != nil {
		return res, err
	}
	return res, nil
}

// rewriteNoteLinks rewrites one source note's body under its own note lock and
// returns the number of links rewritten (0 when nothing matched, or when the
// write could not be committed). It is the ordinary save path: content-hash
// compare against the row, disk write, DB update, file restored if the row
// refuses.
//
// resolver is the pass's, not a fresh one: re-indexing the note it just wrote
// resolves targets against the same world the rewrite validated against, and
// loading the note list again here would put the per-note cost back.
func (s *Service) rewriteNoteLinks(
	ctx context.Context,
	userID, noteID int64,
	newTitle string,
	names func(target string) bool,
	resolver *wikilinks.Resolver,
) (rewritten int, changed bool) {
	mu := s.lockNote(noteID)
	mu.Lock()
	defer func() { mu.Unlock() }()

	note, err := s.notes.GetByID(ctx, userID, noteID)
	if err != nil {
		log.Warn().Err(err).Int64("note_id", noteID).Msg("rename: load linking note for rewrite")
		return 0, false
	}
	newContent, n := wikilinks.ReplaceTargets(note.Content, names, func(string) string { return newTitle })
	if n == 0 {
		return 0, false
	}

	newHash := HashContent(newContent)
	if err := s.fs.Write(note.DiskPath, newContent); err != nil {
		log.Warn().Err(err).Int64("note_id", noteID).Str("disk_path", note.DiskPath).
			Msg("rename: write rewritten links")
		return 0, false
	}
	// The hash compare is the same one any save uses: if the note moved under
	// us, the DB refuses and the file goes back to the bytes we replaced, so an
	// edit in flight is never clobbered by a link rename.
	if err := s.notes.UpdateContent(ctx, userID, noteID, newContent, newHash, note.ContentHash); err != nil {
		if rbErr := s.fs.Write(note.DiskPath, note.Content); rbErr != nil {
			log.Warn().Err(rbErr).Int64("note_id", noteID).Str("disk_path", note.DiskPath).
				Msg("rename: restore linking note after failed rewrite")
		}
		log.Warn().Err(err).Int64("note_id", noteID).Msg("rename: commit rewritten links")
		return 0, false
	}

	note.Content = newContent
	note.ContentHash = newHash
	log.Info().
		Int64("user_id", userID).
		Int64("note_id", note.ID).
		Str("note_title", note.Title).
		Str("disk_path", note.DiskPath).
		Str("to", newTitle).
		Int("links", n).
		Msg("rename: rewrote inbound link targets")

	// Re-resolve this note's outgoing edges now, so the graph reflects the new
	// targets in the same pass that wrote them.
	if err := s.indexLinksWithResolver(ctx, note, "", resolver); err != nil {
		log.Warn().Err(err).Int64("note_id", note.ID).Msg("rename: reindex rewritten links")
	}
	return n, true
}
