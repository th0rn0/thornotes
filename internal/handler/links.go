package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/th0rn0/thornotes/internal/auth"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/notes"
)

// outgoingLinkJSON is the API shape of one outgoing [[link]] edge. A resolved
// edge carries ToNoteID+ToTitle; an unresolved edge has both nil/empty and
// Unresolved=true; an ambiguous target has Ambiguous=true and no id (the
// resolver refuses to guess).
type outgoingLinkJSON struct {
	ToNoteID   *int64 `json:"to_note_id"`
	ToTitle    string `json:"to_title,omitempty"`
	RawTarget  string `json:"raw_target"`
	Anchor     string `json:"anchor"`
	LinkText   string `json:"link_text"`
	Ambiguous  bool   `json:"ambiguous"`
	Unresolved bool   `json:"unresolved"`
}

// mcpOutgoingLink / mcpBacklink mirror the note 323 §8 get_links contract:
// outgoing {id,title,target,anchor}, backlinks {id,title,folder_id,link_text}.
type mcpOutgoingLink struct {
	ID         *int64 `json:"id"`
	Title      string `json:"title"`
	Target     string `json:"target"`
	Anchor     string `json:"anchor"`
	Ambiguous  bool   `json:"ambiguous"`
	Unresolved bool   `json:"unresolved"`
}

type mcpBacklink struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	FolderID *int64 `json:"folder_id"`
	LinkText string `json:"link_text"`
	Anchor   string `json:"anchor"`
}

type mcpGetLinksResult struct {
	Outgoing  []mcpOutgoingLink `json:"outgoing"`
	Backlinks []mcpBacklink     `json:"backlinks"`
}

// buildOutgoingLinkView resolves each outgoing edge against the user's notes
// (for a title + folder) and drops any edge whose target sits outside the
// caller token's readable scope — a link leaks a title just as surely as
// get_note does. Session auth is unscoped, so the filter is a no-op there.
func buildOutgoingLinkView(
	ctx context.Context,
	authz *auth.TokenAuthz,
	svc *notes.Service,
	userID, noteID int64,
) ([]outgoingLinkJSON, error) {
	edges, err := svc.OutgoingLinks(ctx, userID, noteID)
	if err != nil {
		return nil, err
	}
	// One ListAll resolves titles + folders for every note the user owns.
	items, err := svc.ListAllNotes(ctx, userID)
	if err != nil {
		return nil, err
	}
	meta := make(map[int64]*model.NoteListItem, len(items))
	for _, it := range items {
		meta[it.ID] = it
	}

	folders := svc.Folders()
	canRead := func(folderID *int64) (bool, error) {
		if authz == nil || !authz.Scoped {
			return true, nil
		}
		return authz.CanRead(ctx, folders, userID, folderID)
	}

	out := make([]outgoingLinkJSON, 0, len(edges))
	for _, e := range edges {
		item := outgoingLinkJSON{
			RawTarget: e.RawTarget,
			Anchor:    e.Anchor,
			LinkText:  e.LinkText,
			Ambiguous: e.Ambiguous,
		}
		var target *model.NoteListItem
		if e.ToNoteID != nil {
			target = meta[*e.ToNoteID]
		}
		if target == nil {
			// No resolved target: either genuinely unresolved, or ambiguous
			// (the resolver refused to guess between equal candidates).
			if !e.Ambiguous {
				item.Unresolved = true
			}
			out = append(out, item)
			continue
		}
		allowed, err := canRead(target.FolderID)
		if err != nil {
			return nil, err
		}
		if !allowed {
			// Target is outside the token's scope — drop the edge entirely so
			// neither its title nor its existence leaks.
			continue
		}
		id := *e.ToNoteID
		item.ToNoteID = &id
		item.ToTitle = target.Title
		out = append(out, item)
	}
	return out, nil
}

// buildBacklinkView drops backlinks whose source note is outside the caller
// token's readable scope, for the same reason: a backlink carries a title.
func buildBacklinkView(
	ctx context.Context,
	authz *auth.TokenAuthz,
	svc *notes.Service,
	userID, noteID int64,
) ([]model.Backlink, error) {
	back, err := svc.Backlinks(ctx, userID, noteID)
	if err != nil {
		return nil, err
	}
	if authz == nil || !authz.Scoped {
		return back, nil
	}
	folders := svc.Folders()
	out := make([]model.Backlink, 0, len(back))
	for _, b := range back {
		allowed, err := authz.CanRead(ctx, folders, userID, b.FromFolderID)
		if err != nil {
			return nil, err
		}
		if allowed {
			out = append(out, b)
		}
	}
	return out, nil
}

func toMCPOutgoing(view []outgoingLinkJSON) []mcpOutgoingLink {
	out := make([]mcpOutgoingLink, 0, len(view))
	for _, v := range view {
		out = append(out, mcpOutgoingLink{
			ID:         v.ToNoteID,
			Title:      v.ToTitle,
			Target:     v.RawTarget,
			Anchor:     v.Anchor,
			Ambiguous:  v.Ambiguous,
			Unresolved: v.Unresolved,
		})
	}
	return out
}

func toMCPBacklinks(back []model.Backlink) []mcpBacklink {
	out := make([]mcpBacklink, 0, len(back))
	for _, b := range back {
		out = append(out, mcpBacklink{
			ID:       b.FromNoteID,
			Title:    b.FromTitle,
			FolderID: b.FromFolderID,
			LinkText: b.LinkText,
			Anchor:   b.Anchor,
		})
	}
	return out
}

// Links handles GET /api/v1/notes/:id/links — the resolved outgoing edges.
func (h *NotesHandler) Links(c *gin.Context) {
	user := ginUser(c)
	noteID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "note id must be a positive integer"})
		return
	}
	if _, err := h.svc.GetNote(c.Request.Context(), user.ID, noteID); err != nil {
		writeError(c, err)
		return
	}
	view, err := buildOutgoingLinkView(c.Request.Context(), auth.AuthzFromContext(c.Request.Context()), h.svc, user.ID, noteID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

// Backlinks handles GET /api/v1/notes/:id/backlinks — the inbound edges.
func (h *NotesHandler) Backlinks(c *gin.Context) {
	user := ginUser(c)
	noteID, err := ginParamInt64(c, "id")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "note id must be a positive integer"})
		return
	}
	if _, err := h.svc.GetNote(c.Request.Context(), user.ID, noteID); err != nil {
		writeError(c, err)
		return
	}
	view, err := buildBacklinkView(c.Request.Context(), auth.AuthzFromContext(c.Request.Context()), h.svc, user.ID, noteID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}
