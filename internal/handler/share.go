package handler

import (
	"context"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/notes"
)

// shareViewTouchTimeout bounds the background write that records a share view.
// It is a ceiling on a single indexed UPDATE, not a budget: the request that
// triggered it has already been answered by then.
const shareViewTouchTimeout = 5 * time.Second

// ShareHandler serves the public shared note view and lists the caller's own
// share links.
type ShareHandler struct {
	svc  *notes.Service
	tmpl *template.Template
}

func NewShareHandler(svc *notes.Service, tmpl *template.Template) *ShareHandler {
	return &ShareHandler{svc: svc, tmpl: tmpl}
}

func (h *ShareHandler) View(c *gin.Context) {
	// A share URL is a bearer capability: whoever holds it reads the note, with no
	// session and no account. Two consequences for the response, and they apply to
	// every outcome of this handler — including the 404, which is the same
	// capability with a wrong token, so a cacheable 404 is the same disclosure in
	// the other direction:
	//
	//   no-store   nothing between here and the reader may keep a copy. A proxy that
	//              caches this serves the note to whoever asks for it next.
	//   noindex    if a share URL is ever pasted somewhere that gets crawled, the
	//              note must not enter a search index.
	setAnonymousShareHeaders(c)

	token := c.Param("token")
	if token == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	note, err := h.svc.GetNoteByShareToken(c.Request.Context(), token)
	if err != nil {
		// One branch for both outcomes on purpose. An expired link and a token
		// that never existed are the same answer — same status, same empty body,
		// same headers set above — because telling them apart would turn this
		// route into an oracle: it would confirm that a token was real once, and
		// when it died, which is information the holder of a burned URL should
		// not be able to get. Nothing below may distinguish them either: adding
		// a body, a header or a redirect to the expired case is the bug.
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(c.Writer, "share.html", note); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "render error"})
		return
	}

	// After the body: recording the view must never delay the reader or change
	// what they get. See recordView for the bound that keeps this off the write
	// path almost always.
	h.recordView(c.Request.Context(), note)
}

// recordView records that a share link was opened, without making the reader
// wait for it.
//
// Three things are deliberate here:
//
//   - The bound is checked before any work: inside the window this returns
//     without spawning anything, so a flood of views on one link costs a
//     comparison per request and no goroutine at all.
//   - It runs off the request goroutine, with the request's cancellation
//     detached (a reader who closes the tab must not cancel the stamp) and its
//     own deadline, so a slow database cannot hold the response open.
//   - A failure is logged and dropped. Counting a view is telemetry; it is never
//     worth failing a read for.
func (h *ShareHandler) recordView(ctx context.Context, note *model.Note) {
	now := time.Now().UTC()
	if !notes.ShareViewDue(note.LastViewedAt, now) {
		return
	}

	base := context.WithoutCancel(ctx)
	noteID := note.ID
	lastViewed := note.LastViewedAt
	go func() {
		ctx, cancel := context.WithTimeout(base, shareViewTouchTimeout)
		defer cancel()
		if _, err := h.svc.RecordShareView(ctx, noteID, lastViewed, now); err != nil {
			log.Debug().Err(err).Int64("note_id", noteID).Msg("record share view")
		}
	}()
}

// --- Folder shares (ticket 616, slice 1) -----------------------------------
//
// A folder share is the same kind of capability as a note share — a token in a
// URL, read with no session — so everything the note view does for the reader's
// safety it does too, and for the same reasons:
//
//   no-store   nothing between here and the reader may keep a copy of a page
//              whose URL is the credential.
//   noindex    a pasted link must not enter a search index.
//   one 404    unknown token, revoked link, expired link, malformed path, a
//              folder outside the tree, a note outside the tree: one status,
//              one empty body, one set of headers. Anything else tells the
//              holder of a burned URL that it was real once.
//
// The two headers are set before the first branch in every handler below, so the
// 404 carries them too.

// setAnonymousShareHeaders applies the two headers a share response must always
// carry. It is a function so the rule has one home: a second copy of it is how
// one route ends up cacheable.
func setAnonymousShareHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Robots-Tag", "noindex")
}

// ViewFolder serves GET /f/:token and GET /f/:token/d/*path: one level of a
// shared folder, HTML only.
//
// One level at a time, with a subfolder rendered as a link down rather than as
// an inlined subtree. That is a cost decision as much as a UI one: the recursive
// query behind the export path is a scan of every note the owner has, which is
// the wrong shape behind a URL anyone can hold, while one level is an index seek
// per section.
func (h *ShareHandler) ViewFolder(c *gin.Context) {
	setAnonymousShareHeaders(c)

	token := c.Param("token")
	// The wildcard carries its leading separator; the service wants a clean
	// root-relative path and validates whatever it is given.
	rel := strings.TrimPrefix(c.Param("path"), "/")

	listing, err := h.svc.FolderShareListing(c.Request.Context(), token, rel,
		c.Query("fa"), c.Query("na"))
	if err != nil {
		// Every failure, including an internal one: a 500 above a valid token
		// would tell the holder the token is real. See the note view's branch
		// for the same reasoning.
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(c.Writer, "folder-share.html", buildFolderSharePage(token, listing)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "render error"})
		return
	}

	h.recordFolderView(c.Request.Context(), listing)
}

// ViewFolderNote serves GET /f/:token/n/*path: one note read inside a shared
// folder, rendered with the note-share template unchanged.
//
// The path is root-relative and carries no ".md": a slug is the note's filename,
// and a URL that names a file invites storing a retrieval. Reusing share.html
// also reuses share.js, which renders [[wikilinks]] as plain text — resolving one
// would confirm that an out-of-scope note exists, by title.
func (h *ShareHandler) ViewFolderNote(c *gin.Context) {
	setAnonymousShareHeaders(c)

	token := c.Param("token")
	rel := strings.TrimPrefix(c.Param("path"), "/")

	note, err := h.svc.FolderShareNote(c.Request.Context(), token, rel)
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(c.Writer, "share.html", note); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "render error"})
		return
	}
}

// folderSharePage is the template's view of one level: names and links, and
// nothing else. In particular it carries no id of any kind and no count that was
// not derived from the rows on this page — the anonymous response must not tell a
// reader how big the vault is.
type folderSharePage struct {
	Title       string
	RootName    string
	FolderName  string
	Breadcrumb  []folderShareLink
	Folders     []folderShareLink
	Notes       []folderShareLink
	MoreFolders string
	MoreNotes   string
}

type folderShareLink struct {
	Name string
	Href string
}

// buildFolderSharePage turns the service's data into links.
//
// This is the only layer that holds the token, which is why the service returns
// paths relative to the root and not URLs: the token appears in the response
// because the reader already has it in the address bar, and nowhere else.
func buildFolderSharePage(token string, listing *notes.FolderShareListing) folderSharePage {
	page := folderSharePage{
		Title:      listing.FolderName,
		RootName:   listing.RootName,
		FolderName: listing.FolderName,
	}
	for i, crumb := range listing.Breadcrumb {
		href := ""
		if i < len(listing.Breadcrumb)-1 { // the current folder is not a link
			href = folderHref(token, crumb.Path)
		}
		page.Breadcrumb = append(page.Breadcrumb, folderShareLink{Name: crumb.Name, Href: href})
	}
	for _, f := range listing.Folders {
		page.Folders = append(page.Folders, folderShareLink{
			Name: f.Name,
			Href: folderHref(token, f.Path),
		})
	}
	for _, n := range listing.Notes {
		page.Notes = append(page.Notes, folderShareLink{
			Name: n.Title,
			Href: noteHref(token, n.Path),
		})
	}
	if listing.HasMoreFolders {
		page.MoreFolders = folderHref(token, listing.Path) + "?fa=" + url.QueryEscape(listing.FolderCursor)
	}
	if listing.HasMoreNotes {
		page.MoreNotes = folderHref(token, listing.Path) + "?na=" + url.QueryEscape(listing.NoteCursor)
	}
	return page
}

// folderHref builds the address of a folder listing under a token. rel is the
// folder's path relative to the shared root, and "" is the root itself.
//
// Segments are escaped one at a time: a folder name may contain a space or a
// non-ASCII character (and may not contain a separator), and escaping the whole
// path would turn those separators into %2F.
func folderHref(token, rel string) string {
	if rel == "" {
		return "/f/" + url.PathEscape(token)
	}
	return "/f/" + url.PathEscape(token) + "/d/" + escapeSharePath(rel)
}

// noteHref builds the address of a note read under a token. rel is the note's
// path relative to the shared root, with the ".md" suffix omitted.
func noteHref(token, rel string) string {
	return "/f/" + url.PathEscape(token) + "/n/" + escapeSharePath(rel)
}

func escapeSharePath(rel string) string {
	segments := strings.Split(rel, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return strings.Join(segments, "/")
}

// recordFolderView stamps the link's last_viewed_at, off the request path and
// bounded by ShareViewWindow — the same discipline as recordView, because this
// route has the same exposure: an unauthenticated reader who can hit it as fast
// as they like.
func (h *ShareHandler) recordFolderView(ctx context.Context, listing *notes.FolderShareListing) {
	now := time.Now().UTC()
	if !notes.ShareViewDue(listing.LastViewedAt, now) {
		return
	}

	base := context.WithoutCancel(ctx)
	folderID := listing.FolderID
	lastViewed := listing.LastViewedAt
	go func() {
		ctx, cancel := context.WithTimeout(base, shareViewTouchTimeout)
		defer cancel()
		if _, err := h.svc.RecordFolderShareView(ctx, folderID, lastViewed, now); err != nil {
			log.Debug().Err(err).Int64("folder_id", folderID).Msg("record folder share view")
		}
	}()
}

// List answers GET /api/v1/shares with the caller's own share links.
//
// The token is redacted to a prefix by the repository, so this handler cannot
// leak one even by accident. The endpoint exists so a user can find and revoke
// the links they handed out: before it, a link could only be cleared from the
// note that produced it, which meant you had to already hold the note — and the
// whole point of a link is that it gets passed around.
func (h *ShareHandler) List(c *gin.Context) {
	user := ginUser(c)
	shares, err := h.svc.ListShares(c.Request.Context(), user.ID)
	if err != nil {
		writeError(c, err)
		return
	}
	if shares == nil {
		shares = []model.ShareLink{}
	}
	c.JSON(http.StatusOK, shares)
}
