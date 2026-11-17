package handler

import (
	"context"
	"html/template"
	"net/http"
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
	c.Header("Cache-Control", "no-store")
	c.Header("X-Robots-Tag", "noindex")

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
