package notes

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/repository"
)

// Folder shares: a bearer token pointed at a folder, rendered anonymously at
// /f/<token>.
//
// This file is the whole server-side rule for that surface, and it is in this
// package on purpose: the two things a folder share must not re-invent — "when
// is a link dead" (ShareLinkExpired) and "how often may a view be recorded"
// (ShareViewWindow) — are unexported-or-package-level functions here, so
// re-inventing them would be visible in the diff rather than a matter of
// discipline. Nothing had to be exported to share them.
//
// The read paths answer apperror.ErrNotFound for every failure. That is not
// laziness: the anonymous route turns any error into one 404 with an empty body,
// so a distinct error type here would be a type that tempts a caller to
// distinguish "expired" from "never existed" — which is the oracle the surface
// is built not to be.

// FolderShareCrumb is one element of a breadcrumb or one subfolder entry: a
// display name and its path relative to the shared root ("" for the root
// itself).
//
// The path, not an id, is what a link is built from: trimming the root's prefix
// from disk_path means an out-of-scope ancestor's name cannot appear in a URL by
// construction rather than by policy.
type FolderShareCrumb struct {
	Name string
	Path string
}

// FolderShareNote is one note entry in a listing. Path is the note's path
// relative to the shared root WITHOUT the ".md" suffix; Slug is the last
// segment.
type FolderShareNote struct {
	Title string
	Slug  string
	Path  string
}

// FolderShareListing is one level of a shared folder, as data — the handler
// turns it into URLs (it is the only place that holds the token) and the
// template renders them.
//
// One level, not a subtree: the recursive query the export path uses is a scan
// of every note the owner has, which is the wrong shape behind a URL anybody can
// hold. A subfolder is a link down, so the cost of a listing is one index seek
// per section and the 200-entry cap bounds the rest.
type FolderShareListing struct {
	// RootName is the shared folder's own name: the first breadcrumb element and
	// the page's title. Nothing above it is ever rendered.
	RootName string
	// FolderName is the folder being listed (RootName at the root).
	FolderName string
	// Path is the listing's own path relative to the root ("" at the root).
	Path string
	// Breadcrumb runs from the shared root down to the current folder
	// inclusive, so the last element is the page you are on.
	Breadcrumb []FolderShareCrumb
	// Folders and Notes are the two sections of this level, already capped.
	Folders []FolderShareCrumb
	Notes   []FolderShareNote
	// HasMore* say whether another page exists in that section; the cursor is
	// the last element of the section that came back, which is what makes the
	// next page start strictly after it (no repeated entry, no skipped one).
	HasMoreFolders bool
	HasMoreNotes   bool
	FolderCursor   string
	NoteCursor     string

	// FolderID and LastViewedAt are not rendered. The handler needs them to
	// record a view of the link after the response is written, without a second
	// read on an unauthenticated route.
	FolderID     int64
	LastViewedAt *time.Time
}

// SetFolderShareToken issues, rotates or revokes a folder's public link.
//
// clear=true revokes: the row is deleted, so a revoked link and a folder that was
// never shared are the same state and answer the same 404. Revocation is not
// subject to the expiry guard — it destroys the link rather than publishing it
// again, and a leaked link that has already expired must still be killable.
// clear=false always mints a NEW token, even if the folder already had one —
// re-sharing rotates, exactly as POST /notes/:id/share does. Two share routes ten
// lines apart that disagreed about whether they rotate would be a trap, and the
// note UI already carries the confirm ("generating a new one replaces it").
//
// expiresAt is opt-in: nil keeps the link immortal, which is the documented
// default. It is written after the rotation, over the NULL that the rotating
// statement leaves behind — which is what keeps a re-share working on a folder
// whose previous link has expired, since the deadline write itself refuses a dead
// link (SetFolderShareExpiry).
//
// The ownership check comes first and is the #40 fix: a folder the caller does
// not own and a folder that does not exist both answer apperror.ErrNotFound, and
// nothing is written for either. The repository repeats the guard in SQL, so a
// folder deleted between the check and the write cannot end up shared either.
func (s *Service) SetFolderShareToken(ctx context.Context, userID, folderID int64, clear bool, expiresAt *time.Time) (*string, error) {
	if _, err := s.folders.GetByID(ctx, userID, folderID); err != nil {
		return nil, err
	}

	if clear {
		if err := s.folderShares.Clear(ctx, userID, folderID); err != nil {
			return nil, err
		}
		return nil, nil
	}

	// Normalised before either write, with the note-share normaliser: one UTC
	// rule for both tables, because the SQLite representation defect it fixes is
	// a property of the column, not of the surface. A deadline the column cannot
	// hold is refused while the row is untouched, rather than after a token has
	// been issued for a link that can never be read.
	expiry, err := normalizeShareExpiry(expiresAt)
	if err != nil {
		return nil, err
	}

	token, err := newShareToken()
	if err != nil {
		return nil, err
	}
	if err := s.folderShares.UpsertToken(ctx, userID, folderID, token); err != nil {
		return nil, err
	}
	// Always written, including the nil case: the deadline belongs to the link
	// that was just created. The statement above cleared the previous link's
	// deadline, so this lands on NULL and cannot be refused as an expired link.
	if err := s.folderShares.SetExpiry(ctx, userID, folderID, expiry); err != nil {
		// The token landed but the deadline did not. Reporting success would claim
		// an expiry the row does not have, so this is an error — the caller can
		// revoke the share and retry.
		return nil, mapFolderShareExpiryError(fmt.Errorf("set folder share expiry: %w", err))
	}
	return &token, nil
}

// FolderShareExpiredMessage is what an owner is told when a deadline write is
// refused because the folder link has already expired. It names the only way
// forward on purpose: a caller told merely "conflict" would try the same PATCH
// again, and re-sharing is the one action that works (it issues a new token, and
// the old URL stays dead).
//
// The wording mirrors the note-share message; the two surfaces must not disagree
// about what an owner is being told.
const FolderShareExpiredMessage = "this share link has expired; share the folder again to create a new link"

// SetFolderShareExpiry changes the deadline on an existing folder link (nil = no
// expiry) without rotating the token, so URLs already handed out keep working.
//
// It refuses a link that has already expired, whether the caller is moving the
// deadline forward or clearing it: expiry is final, and the way back is a fresh
// share (SetFolderShareToken), which rotates the token and leaves the old URL
// dead. The refusal is the repository's — the guard is a predicate on the UPDATE,
// so a request cannot slip between a check here and the write there — and it is
// turned into a 409 with an explanation at this layer, because the repository
// speaks in sentinels and the caller needs to be told what to do instead.
func (s *Service) SetFolderShareExpiry(ctx context.Context, userID, folderID int64, expiresAt *time.Time) error {
	expiry, err := normalizeShareExpiry(expiresAt)
	if err != nil {
		return err
	}
	return mapFolderShareExpiryError(s.folderShares.SetExpiry(ctx, userID, folderID, expiry))
}

// mapFolderShareExpiryError turns the repository's expired sentinel into the 409
// the caller sees, and leaves every other error as it found it.
//
// One place, rather than one per caller: both doors into folder_shares.expires_at
// (POST with a deadline, PATCH) must answer an expired link identically, and a
// divergence would be a route that reports 500 for a state the other explains.
func mapFolderShareExpiryError(err error) error {
	if err == nil || !errors.Is(err, apperror.ErrShareExpired) {
		return err
	}
	return apperror.ShareExpired(FolderShareExpiredMessage)
}

// FolderShareListing resolves one level of a shared folder.
//
// relPath is the folder to list, relative to the shared root ("" lists the
// root). folderCursor and noteCursor are the two independent keyset cursors from
// the previous page; each section pages on its own sort key (folder name, note
// slug), which is why there are two rather than one.
func (s *Service) FolderShareListing(ctx context.Context, token, relPath, folderCursor, noteCursor string) (*FolderShareListing, error) {
	rel, err := repository.NormalizeShareRelPath(relPath)
	if err != nil {
		return nil, err
	}
	share, root, err := s.openFolderShare(ctx, token)
	if err != nil {
		return nil, err
	}
	current, err := s.resolveShareFolder(ctx, share, root, rel)
	if err != nil {
		return nil, err
	}

	// One row past the cap, so "is there a next page" is answered by the query
	// rather than by counting or by a second query.
	limit := repository.FolderSharePageSize + 1

	folders, err := s.folderShares.ListChildFolders(ctx, share.UserID, current.ID, folderCursor, limit)
	if err != nil {
		return nil, apperror.Internal("list shared subfolders", err)
	}
	notes, err := s.folderShares.ListChildNotes(ctx, share.UserID, current.ID, noteCursor, limit)
	if err != nil {
		return nil, apperror.Internal("list shared notes", err)
	}

	listing := &FolderShareListing{
		RootName:     root.Name,
		FolderName:   current.Name,
		Path:         rel,
		Breadcrumb:   buildShareBreadcrumb(root, rel, current),
		FolderID:     share.FolderID,
		LastViewedAt: share.LastViewedAt,
	}

	if len(folders) > repository.FolderSharePageSize {
		listing.HasMoreFolders = true
		folders = folders[:repository.FolderSharePageSize]
	}
	for _, f := range folders {
		listing.Folders = append(listing.Folders, FolderShareCrumb{
			Name: f.Name,
			Path: joinSharePath(rel, f.Name),
		})
	}
	if listing.HasMoreFolders {
		listing.FolderCursor = folders[len(folders)-1].Name
	}

	if len(notes) > repository.FolderSharePageSize {
		listing.HasMoreNotes = true
		notes = notes[:repository.FolderSharePageSize]
	}
	for _, n := range notes {
		listing.Notes = append(listing.Notes, FolderShareNote{
			Title: n.Title,
			Slug:  n.Slug,
			Path:  joinSharePath(rel, n.Slug),
		})
	}
	if listing.HasMoreNotes {
		listing.NoteCursor = notes[len(notes)-1].Slug
	}

	return listing, nil
}

// FolderShareNote resolves one note inside a shared folder.
//
// relPath is the note's path relative to the shared root, WITHOUT the ".md"
// suffix. It resolves the folder by disk path and the note by (folder, slug) —
// two indexed lookups, neither of which can step outside the subtree, because
// the folder lookup is pinned to the shared root's prefix first.
func (s *Service) FolderShareNote(ctx context.Context, token, relPath string) (*model.Note, error) {
	rel, err := repository.NormalizeShareRelPath(relPath)
	if err != nil {
		return nil, err
	}
	if rel == "" {
		// The root path names a folder, not a note.
		return nil, apperror.ErrNotFound
	}
	share, root, err := s.openFolderShare(ctx, token)
	if err != nil {
		return nil, err
	}

	dir, slug := splitShareNotePath(rel)
	if slug == "" {
		return nil, apperror.ErrNotFound
	}
	folder, err := s.resolveShareFolder(ctx, share, root, dir)
	if err != nil {
		return nil, err
	}
	note, err := s.notes.GetByFolderAndSlug(ctx, share.UserID, &folder.ID, slug)
	if err != nil {
		// Renamed, moved out, deleted, or never there: one answer.
		return nil, apperror.ErrNotFound
	}
	return note, nil
}

// RecordFolderShareView records a view of a folder link at now, subject to
// ShareViewWindow — the same window, and the same off-request-path discipline,
// as a note share.
//
// lastViewedAt is the value the caller has already read for the link; passing it
// back is what decides the bound without a second read on an unauthenticated
// route. Errors are the caller's to log and discard: counting a view is never
// worth failing a read for.
func (s *Service) RecordFolderShareView(ctx context.Context, folderID int64, lastViewedAt *time.Time, now time.Time) (bool, error) {
	return s.folderShares.TouchView(ctx, folderID, lastViewedAt, now, ShareViewWindow)
}

// openFolderShare resolves a live token to its share row and the folder it
// points at.
//
// Every dead end returns apperror.ErrNotFound and nothing else: an expired link,
// a token that never existed, and a share whose folder was deleted (revoked by
// the foreign key, so no row should remain — but a row outliving its folder must
// still not resolve) are the same answer.
func (s *Service) openFolderShare(ctx context.Context, token string) (*model.FolderShare, *model.Folder, error) {
	if token == "" {
		return nil, nil, apperror.ErrNotFound
	}
	share, err := s.folderShares.GetByToken(ctx, token)
	if err != nil {
		return nil, nil, apperror.ErrNotFound
	}
	if ShareLinkExpired(share.ExpiresAt, time.Now().UTC()) {
		return nil, nil, apperror.ErrNotFound
	}
	root, err := s.folders.GetByID(ctx, share.UserID, share.FolderID)
	if err != nil {
		return nil, nil, apperror.ErrNotFound
	}
	return share, root, nil
}

// resolveShareFolder maps a path relative to the shared root onto the folder row
// that holds it, or ErrNotFound.
//
// Three checks, and the second two are deliberately redundant with the first:
//
//   - the disk path is resolved with an equality lookup (folders.disk_path is
//     UNIQUE), so no query can match a folder outside the named path;
//   - the row must belong to the share's owner — disk_path is unique across
//     users, but a share must never walk into another account;
//   - the row's path must still sit under the root, by a separator. That is the
//     assertion that survives a future refactor of the other two: whatever a
//     caller sends, the answer is inside the shared tree or it is 404.
func (s *Service) resolveShareFolder(ctx context.Context, share *model.FolderShare, root *model.Folder, rel string) (*model.Folder, error) {
	if rel == "" {
		return root, nil
	}
	folder, err := s.folders.GetByDiskPath(ctx, shareDiskPath(root.DiskPath, rel))
	if err != nil {
		return nil, apperror.ErrNotFound
	}
	if folder.UserID != share.UserID {
		return nil, apperror.ErrNotFound
	}
	if !strings.HasPrefix(folder.DiskPath, root.DiskPath+string(filepath.Separator)) {
		return nil, apperror.ErrNotFound
	}
	return folder, nil
}

// buildShareBreadcrumb runs from the shared root down to the current folder.
//
// Names come from the path segments (a folder's disk path is built from its
// ancestors' names, and a name may not contain a separator), except the last,
// which is taken from the resolved row so a trailing rename cannot render a stale
// label. Nothing above the root is representable here at all — that is the
// property, not an omission.
func buildShareBreadcrumb(root *model.Folder, rel string, current *model.Folder) []FolderShareCrumb {
	crumbs := []FolderShareCrumb{{Name: root.Name, Path: ""}}
	if rel == "" {
		return crumbs
	}
	segments := strings.Split(rel, "/")
	for i, seg := range segments {
		name := seg
		if i == len(segments)-1 {
			name = current.Name
		}
		crumbs = append(crumbs, FolderShareCrumb{
			Name: name,
			Path: strings.Join(segments[:i+1], "/"),
		})
	}
	return crumbs
}

// splitShareNotePath splits a note's root-relative path into its folder part and
// its slug: "projects/plan" becomes ("projects", "plan"), "plan" becomes
// ("", "plan").
func splitShareNotePath(rel string) (dir, slug string) {
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[:i], rel[i+1:]
	}
	return "", rel
}

// joinSharePath appends one segment to a root-relative path.
func joinSharePath(rel, segment string) string {
	if rel == "" {
		return segment
	}
	return rel + "/" + segment
}

// shareDiskPath maps a root-relative path onto disk, using the platform
// separator the stored disk_paths use.
func shareDiskPath(rootDiskPath, rel string) string {
	return filepath.Join(rootDiskPath, filepath.FromSlash(rel))
}
