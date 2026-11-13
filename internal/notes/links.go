package notes

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/wikilinks"
)

// loadResolver builds an in-memory resolver over the user's full note + folder
// set. Resolution matches the wikilinks reference (slug OR normalized title,
// nearest common ancestor, explicit ambiguity) without any fuzzy matching.
func (s *Service) loadResolver(ctx context.Context, userID int64) (*wikilinks.Resolver, error) {
	resolver, _, err := s.loadResolverAndNotes(ctx, userID)
	return resolver, err
}

// loadResolverAndNotes is loadResolver for callers that also need the note list
// the resolver was built from (the link rewrite asks it who else answers to a
// title that just moved).
func (s *Service) loadResolverAndNotes(ctx context.Context, userID int64) (*wikilinks.Resolver, []*model.NoteListItem, error) {
	items, err := s.notes.ListAll(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	folders, err := s.folders.Tree(ctx, userID)
	if err != nil {
		return nil, nil, err
	}

	nrefs := make([]wikilinks.NoteRef, 0, len(items))
	for _, n := range items {
		nrefs = append(nrefs, wikilinks.NoteRef{ID: n.ID, FolderID: n.FolderID, Title: n.Title, Slug: n.Slug})
	}
	frefs := make([]wikilinks.FolderRef, 0, len(folders))
	for _, f := range folders {
		frefs = append(frefs, wikilinks.FolderRef{ID: f.ID, ParentID: f.ParentID})
	}
	return wikilinks.NewResolver(nrefs, frefs), items, nil
}

// computeEdges extracts links from a note's content and resolves each target,
// deduplicating by target_hash (the unique per-note edge key).
func computeEdges(note *model.Note, resolver *wikilinks.Resolver) []model.NoteLink {
	refs := wikilinks.ExtractLinks(note.Content)
	edges := make([]model.NoteLink, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		hash := wikilinks.EdgeKey(ref.Target)
		if _, dup := seen[hash]; dup {
			continue
		}
		seen[hash] = struct{}{}

		res := resolver.Resolve(note.FolderID, ref.Target)
		edge := model.NoteLink{
			UserID:     note.UserID,
			FromNoteID: note.ID,
			RawTarget:  ref.Target,
			TargetHash: hash,
			Anchor:     ref.Anchor,
			LinkText:   ref.LinkText,
		}
		switch {
		case res.Resolved:
			id := res.NoteID
			edge.ToNoteID = &id
			now := time.Now().UTC()
			edge.ResolvedAt = &now
		case res.Ambiguous:
			edge.Ambiguous = true
		}
		edges = append(edges, edge)
	}
	return edges
}

// indexLinks replaces the outgoing edges for note, then (on rename) re-resolves
// inbound edges that pointed at its old title. Callers hold the per-note lock.
func (s *Service) indexLinks(ctx context.Context, note *model.Note, oldTitle string) error {
	resolver, err := s.loadResolver(ctx, note.UserID)
	if err != nil {
		return err
	}
	return s.indexLinksWithResolver(ctx, note, oldTitle, resolver)
}

// indexLinksWithResolver is indexLinks for a caller that already holds a
// resolver over the user's notes — the title rewrite pass, which would otherwise
// reload the whole note list once per note it rewrote, making the pass
// O(rewritten notes × notes). The resolver must describe the current titles; a
// caller mid-rewrite is safe because a rewrite moves link targets only.
func (s *Service) indexLinksWithResolver(ctx context.Context, note *model.Note, oldTitle string, resolver *wikilinks.Resolver) error {
	if err := s.links.ReplaceLinks(ctx, note.UserID, note.ID, computeEdges(note, resolver)); err != nil {
		return err
	}
	if oldTitle != "" && oldTitle != note.Title {
		if err := s.repairInbound(ctx, note.UserID, note.ID, resolver); err != nil {
			log.Warn().Err(err).Int64("note_id", note.ID).Msg("indexLinks: repair inbound after rename")
		}
	}
	return nil
}

// repairInbound re-resolves inbound edges (to_note_id = noteID) after a title
// rename. Links whose raw target no longer resolves become unresolved, which is
// the truthful signal that a link now points at a missing page.
func (s *Service) repairInbound(ctx context.Context, userID, noteID int64, resolver *wikilinks.Resolver) error {
	inbound, err := s.links.ListInboundForRename(ctx, userID, noteID)
	if err != nil {
		return err
	}
	for _, il := range inbound {
		res := resolver.Resolve(il.FromFolderID, il.RawTarget)
		var toID *int64
		if res.Resolved {
			id := res.NoteID
			toID = &id
		}
		if err := s.links.UpdateResolution(ctx, userID, il.ID, toID, res.Ambiguous); err != nil {
			return err
		}
	}
	return nil
}

// reindexUserLinks rebuilds the whole link graph for a user: backfills title
// keys (migration 008 leaves them empty), clears note_links, then reindexes
// every note. Idempotent; safe to run on every reconcile.
func (s *Service) reindexUserLinks(ctx context.Context, user *model.User) error {
	notes, err := s.notes.ListForContext(ctx, user.ID, nil)
	if err != nil {
		return err
	}

	for _, n := range notes {
		tk := wikilinks.NormalizeKey(n.Title)
		if n.TitleKey != tk {
			if err := s.notes.SetTitleKey(ctx, user.ID, n.ID, tk); err != nil {
				log.Warn().Err(err).Int64("note_id", n.ID).Msg("reindex: set title key")
			}
			n.TitleKey = tk
		}
	}

	resolver, err := s.loadResolver(ctx, user.ID)
	if err != nil {
		return err
	}
	if err := s.links.DeleteForUser(ctx, user.ID); err != nil {
		return err
	}
	for _, n := range notes {
		if err := s.links.ReplaceLinks(ctx, user.ID, n.ID, computeEdges(n, resolver)); err != nil {
			return err
		}
	}
	return nil
}

// OutgoingLinks returns the resolved outgoing [[link]] edges for a note.
func (s *Service) OutgoingLinks(ctx context.Context, userID, noteID int64) ([]model.NoteLink, error) {
	return s.links.ListOutgoing(ctx, userID, noteID)
}

// Backlinks returns the inbound edges (notes that link to noteID).
func (s *Service) Backlinks(ctx context.Context, userID, noteID int64) ([]model.Backlink, error) {
	return s.links.ListBacklinks(ctx, userID, noteID)
}
