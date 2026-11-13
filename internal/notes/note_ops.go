package notes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"github.com/rs/zerolog/log"
	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
	"github.com/th0rn0/thornotes/internal/wikilinks"
)

// CreateNote creates a new note, writes the file, then saves to DB.
func (s *Service) CreateNote(ctx context.Context, userID int64, userUUID string, folderID *int64, title string, tags []string) (*model.Note, error) {
	return s.createNoteWithSlug(ctx, userID, userUUID, folderID, title, slugify(title), tags)
}

// createNoteWithSlug is the shared core of note creation. It validates input,
// resolves the parent folder path, writes the file, and inserts the DB row.
// Callers that need full control over the on-disk filename (e.g. the journal
// flow, which uses pretty names like "06 - Tuesday") pass an explicit slug
// instead of relying on slugify.
func (s *Service) createNoteWithSlug(ctx context.Context, userID int64, userUUID string, folderID *int64, title, slug string, tags []string) (*model.Note, error) {
	// Shared tree lock: creating a file is a path write, so it must not
	// interleave with a subtree rename that is rewriting the same directory.
	defer s.lockTreeShared(userID)()
	if len(title) == 0 || len(title) > maxTitleLen {
		return nil, apperror.BadRequest("title must be 1–500 characters")
	}
	if slug == "" {
		return nil, apperror.BadRequest("note slug must be non-empty")
	}
	if err := validateEntryName("note filename", slug); err != nil {
		return nil, err
	}
	if tags == nil {
		tags = []string{}
	}

	var folderPath string
	if folderID != nil {
		folder, err := s.folders.GetByID(ctx, userID, *folderID)
		if err != nil {
			return nil, err
		}
		folderPath = folder.DiskPath
	}
	diskPath := notesDiskPath(userUUID, folderPath, slug)

	// Reject duplicate slug in the target folder before touching the filesystem
	// so an existing note file is never truncated or deleted on conflict.
	if _, err := s.notes.GetByFolderAndSlug(ctx, userID, folderID, slug); err == nil {
		return nil, apperror.Conflict(fmt.Sprintf("note %q already exists in this folder", title))
	} else if !errors.Is(err, apperror.ErrNotFound) {
		return nil, err
	}

	content := ""
	hash := HashContent(content)

	// Write file exclusively (never overwrite a pre-existing file on disk).
	if err := s.fs.WriteExclusive(diskPath, content); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, apperror.Conflict(fmt.Sprintf("note %q already exists in this folder", title))
		}
		return nil, apperror.Internal("write note file", err)
	}

	n := &model.Note{
		UserID:      userID,
		FolderID:    folderID,
		Title:       title,
		Slug:        slug,
		TitleKey:    wikilinks.NormalizeKey(title),
		DiskPath:    diskPath,
		Content:     content,
		ContentHash: hash,
		Tags:        tags,
	}

	created, err := s.notes.Create(ctx, n)
	if err != nil {
		// Cleanup the file we exclusively created if the DB insert fails.
		if cleanupErr := s.fs.Delete(diskPath); cleanupErr != nil {
			log.Warn().Err(cleanupErr).Str("path", diskPath).Msg("cleanup file after failed db insert")
		}
		return nil, err
	}

	if err := s.indexLinks(ctx, created, ""); err != nil {
		log.Warn().Err(err).Int64("note_id", created.ID).Msg("index links after create")
	}

	return created, nil
}

// GetNote returns a note by ID, verifying it belongs to userID.
func (s *Service) GetNote(ctx context.Context, userID, noteID int64) (*model.Note, error) {
	return s.notes.GetByID(ctx, userID, noteID)
}

// GetNoteByShareToken returns a note by its public share token.
func (s *Service) GetNoteByShareToken(ctx context.Context, token string) (*model.Note, error) {
	return s.notes.GetByShareToken(ctx, token)
}

// ListNotes returns note metadata for a given folder (nil = root).
//
// It does NOT prove that folderID exists or belongs to userID: ListByFolder
// filters on user_id and answers an empty list for a folder that is somebody
// else's. Anything id-addressed must use FolderNotes instead, so that "not
// yours" and "not there" are the same answer.
func (s *Service) ListNotes(ctx context.Context, userID int64, folderID *int64) ([]*model.NoteListItem, error) {
	return s.notes.ListByFolder(ctx, userID, folderID)
}

// FolderNotes lists the notes in one folder, after proving the folder belongs
// to userID. A folder owned by another user and a folder that does not exist
// are indistinguishable here — both return apperror.ErrNotFound — so an
// id-addressed route cannot be used to enumerate other users' folder ids.
func (s *Service) FolderNotes(ctx context.Context, userID, folderID int64) ([]*model.NoteListItem, error) {
	if _, err := s.folders.GetByID(ctx, userID, folderID); err != nil {
		return nil, err
	}
	return s.notes.ListByFolder(ctx, userID, &folderID)
}

// ListAllNotes returns note metadata for all notes owned by userID, across all folders.
func (s *Service) ListAllNotes(ctx context.Context, userID int64) ([]*model.NoteListItem, error) {
	return s.notes.ListAll(ctx, userID)
}

// UpdateNoteContent saves new content using optimistic concurrency.
// expectedHash must match the current content_hash in the DB.
//
// File write and DB update are serialised per note via lockNote so that two
// concurrent saves cannot leave the disk and DB pointing at different content
// (the "loser of optimistic conflict still wins on disk" race).
func (s *Service) UpdateNoteContent(ctx context.Context, userID, noteID int64, content, expectedHash string) (string, error) {
	defer s.lockTreeShared(userID)()
	if int64(len(content)) > 1<<20 {
		return "", &apperror.AppError{Code: 413, Message: "content exceeds 1 MB limit"}
	}

	mu := s.lockNote(noteID)
	mu.Lock()
	defer mu.Unlock()

	newHash := HashContent(content)

	note, err := s.notes.GetByID(ctx, userID, noteID)
	if err != nil {
		return "", err
	}

	// Re-check optimistic concurrency BEFORE touching the file. If the caller's
	// expectedHash doesn't match the current DB hash, reject the write outright
	// instead of clobbering the file with stale content.
	if note.ContentHash != expectedHash {
		return "", apperror.ErrConflict
	}

	if err := s.fs.Write(note.DiskPath, content); err != nil {
		return "", apperror.Internal("write note file", err)
	}

	if err := s.notes.UpdateContent(ctx, userID, noteID, content, newHash, expectedHash); err != nil {
		// File write succeeded but DB write failed. Roll the file back to the
		// content we know matches expectedHash so disk and DB stay in sync.
		if rollbackErr := s.fs.Write(note.DiskPath, note.Content); rollbackErr != nil {
			log.Warn().Err(rollbackErr).Int64("id", noteID).Msg("rollback file after failed db update")
		}
		return "", err
	}

	note.Content = content
	note.ContentHash = newHash
	if err := s.indexLinks(ctx, note, ""); err != nil {
		log.Warn().Err(err).Int64("note_id", noteID).Msg("index links after content update")
	}

	return newHash, nil
}

// UpdateNoteMetadata updates the title and/or tags of a note.
//
// A title is one fact written down in two places: the row's title (the sidebar
// label, the [[wikilink]] target, the indexed column) and the file's "# H1"
// (what the file states, which reconcile treats as authoritative for a heading
// it did not write — ticket N7). A rename therefore writes both: the heading is
// rewritten with RetitleHeading and committed in the same statement as the
// title, under the note lock. Writing only the row is what made a rename
// survive until the next reconcile pass and then be undone by it.
//
// A title is also how other notes address this one, so a rename moves the links
// too: every `[[old title]]` that resolved here is rewritten to the new title,
// anchor and display text untouched (ticket 633 — the alternative, an alias
// table keeping old titles alive, was considered and rejected). The rewrite
// pass runs after the note lock is released, one source note at a time, and is
// logged and counted.
//
// When the title changes the slug is recomputed; if the slug differs the note
// file is renamed on disk and disk_path is updated in the DB. If the DB update
// fails after the disk rename (or the heading rewrite), both are rolled back so
// disk and DB stay in sync. A body that states no heading is left alone: no
// heading means no opinion, and the row's title is the only place that fact is
// written.
//
// The heading is rewritten from the file, not from the row, so an edit an
// external editor made since the last reconcile pass is carried through the
// rename instead of being overwritten by the row's older copy.
func (s *Service) UpdateNoteMetadata(ctx context.Context, userID int64, userUUID string, noteID int64, title string, tags []string) error {
	defer s.lockTreeShared(userID)()

	note, oldTitle, oldSlug, err := s.applyMetadataUpdate(ctx, userID, userUUID, noteID, title, tags)
	if err != nil {
		return err
	}

	if oldTitle != "" && oldTitle != note.Title {
		// The pass takes each *source* note's lock one at a time. Holding this
		// note's lock while doing that would let two renames that link to each
		// other deadlock, so applyMetadataUpdate has already released it.
		res, rerr := s.rewriteInboundLinks(ctx, userID, titleMove{
			noteID:   note.ID,
			oldTitle: oldTitle,
			newTitle: note.Title,
			oldSlug:  oldSlug,
		})
		if rerr != nil {
			// Fall back to re-resolution alone: nothing is rewritten, and the
			// links that now dangle are marked unresolved, which is the
			// truthful signal and exactly what happened before this pass
			// existed.
			log.Warn().Err(rerr).Int64("note_id", note.ID).Msg("rename: rewrite inbound links")
			if err := s.indexNoteLinksUnderLock(ctx, note, oldTitle); err != nil {
				log.Warn().Err(err).Int64("note_id", note.ID).Msg("index links after metadata update")
			}
			return nil
		}
		log.Info().
			Int64("note_id", note.ID).
			Str("from", oldTitle).
			Str("to", note.Title).
			Int("notes_rewritten", res.NotesRewritten).
			Int("links_rewritten", res.LinksRewritten).
			Int("links_skipped_shared", res.LinksSkippedShared).
			Int("links_skipped_unresolved", res.LinksSkippedUnresolved).
			Msg("rename: inbound links moved with the title")
	}

	if err := s.indexNoteLinksUnderLock(ctx, note, ""); err != nil {
		log.Warn().Err(err).Int64("note_id", noteID).Msg("index links after metadata update")
	}
	return nil
}

// indexNoteLinksUnderLock re-indexes one note's outgoing edges while holding its
// own note lock — the discipline every ordinary write uses, so a rename cannot
// race the index of a note whose body it did not touch.
func (s *Service) indexNoteLinksUnderLock(ctx context.Context, note *model.Note, oldTitle string) error {
	mu := s.lockNote(note.ID)
	mu.Lock()
	defer mu.Unlock()
	return s.indexLinks(ctx, note, oldTitle)
}

// applyMetadataUpdate is the locked half of UpdateNoteMetadata: it commits the
// rename and returns the note as it now stands, plus the title and slug it had
// before, which the link rewrite needs. The caller already holds the tree-shared
// lock; a nested RLock here could deadlock behind a waiting writer.
func (s *Service) applyMetadataUpdate(ctx context.Context, userID int64, userUUID string, noteID int64, title string, tags []string) (*model.Note, string, string, error) {
	mu := s.lockNote(noteID)
	mu.Lock()
	defer mu.Unlock()

	note, err := s.notes.GetByID(ctx, userID, noteID)
	if err != nil {
		return nil, "", "", err
	}
	oldTitle := note.Title
	oldSlug := note.Slug

	oldContent := note.Content
	oldDiskPath := note.DiskPath
	renamed := false
	rewriteBody := false
	if title != "" && title != note.Title {
		// The create path rejects this length, and the heading may not state
		// more than headingTitle will read back, so a title that could not
		// survive the round trip is refused rather than truncated.
		if len(title) > maxTitleLen {
			return nil, "", "", apperror.BadRequest("title must be 1–500 characters")
		}
		newSlug := slugify(title)
		if newSlug != note.Slug {
			if existing, err := s.notes.GetByFolderAndSlug(ctx, userID, note.FolderID, newSlug); err == nil && existing.ID != note.ID {
				return nil, "", "", apperror.Conflict(fmt.Sprintf("note %q already exists in this folder", title))
			} else if err != nil && !errors.Is(err, apperror.ErrNotFound) {
				return nil, "", "", err
			}
			var folderPath string
			if note.FolderID != nil {
				folder, err := s.folders.GetByID(ctx, userID, *note.FolderID)
				if err != nil {
					return nil, "", "", err
				}
				folderPath = folder.DiskPath
			}
			newDiskPath := notesDiskPath(userUUID, folderPath, newSlug)
			if err := s.fs.RenameFileExclusive(note.DiskPath, newDiskPath); err != nil {
				if errors.Is(err, os.ErrExist) {
					return nil, "", "", apperror.Conflict(fmt.Sprintf("note %q already exists in this folder", title))
				}
				return nil, "", "", apperror.Internal("rename note file", err)
			}
			note.Slug = newSlug
			note.DiskPath = newDiskPath
			renamed = true
		}
		note.Title = title
		note.TitleKey = wikilinks.NormalizeKey(title)
		// The heading is the same fact as the row's title, so it moves with it.
		// The base for the rewrite is the file, not the row: an external editor
		// may have changed the body since the last reconcile pass, and writing
		// the row's copy back would throw that edit away — the disk is the side
		// that wins in this app, and a rename is no place to break the rule.
		if onDisk, readErr := s.fs.Read(note.DiskPath); readErr != nil {
			// A file we cannot read is a file we cannot rewrite without
			// clobbering bytes we never saw. The rename goes ahead as it did
			// before, and reconcile owns the note whose file it cannot read.
			log.Warn().Err(readErr).Int64("note_id", noteID).Msg("rename: note file unreadable — heading left untouched")
		} else if rewritten := RetitleHeading(onDisk, title); rewritten != onDisk {
			oldContent = onDisk
			note.Content = rewritten
			note.ContentHash = HashContent(rewritten)
			rewriteBody = true
		} else if onDisk != note.Content {
			// The heading did not move, but the row's body is behind the file.
			// Commit the file's copy instead of writing the row's back over it.
			note.Content = onDisk
			note.ContentHash = HashContent(onDisk)
		}
	}
	if tags != nil {
		note.Tags = tags
	}

	// rollback undoes the disk half of a metadata update that could not be
	// committed. It runs in the reverse order of the writes above: the file goes
	// back to its old name first, then to the bytes it had there.
	rollback := func() {
		if renamed {
			if rbErr := s.fs.RenameFileExclusive(note.DiskPath, oldDiskPath); rbErr != nil {
				log.Warn().Err(rbErr).Str("from", note.DiskPath).Str("to", oldDiskPath).Msg("rollback note rename after failed metadata update")
			}
		}
		if rewriteBody {
			if wErr := s.fs.Write(oldDiskPath, oldContent); wErr != nil {
				log.Warn().Err(wErr).Str("path", oldDiskPath).Msg("rollback note heading after failed metadata update")
			}
		}
	}

	if rewriteBody {
		if err := s.fs.Write(note.DiskPath, note.Content); err != nil {
			rollback()
			return nil, "", "", apperror.Internal("write note heading", err)
		}
	}

	// One statement: the row's title, slug, path and body move together, so no
	// reader can observe a row whose title and heading disagree.
	if err := s.notes.UpdateMetadataAndContent(ctx, note); err != nil {
		rollback()
		return nil, "", "", err
	}

	return note, oldTitle, oldSlug, nil
}

// MoveNote moves a note to newFolderID (nil = root). It:
// 1. Moves the file on disk.
// 2. Updates the note's folder_id and disk_path in DB.
func (s *Service) MoveNote(ctx context.Context, userID int64, userUUID string, noteID int64, newFolderID *int64) error {
	defer s.lockTreeShared(userID)()
	mu := s.lockNote(noteID)
	mu.Lock()
	defer mu.Unlock()

	note, err := s.notes.GetByID(ctx, userID, noteID)
	if err != nil {
		return err
	}

	// No-op if already in the target folder.
	if ptrEq(note.FolderID, newFolderID) {
		return nil
	}

	var newFolderPath string
	if newFolderID != nil {
		folder, err := s.folders.GetByID(ctx, userID, *newFolderID)
		if err != nil {
			return err
		}
		newFolderPath = folder.DiskPath
	}

	if existing, err := s.notes.GetByFolderAndSlug(ctx, userID, newFolderID, note.Slug); err == nil && existing.ID != note.ID {
		return apperror.Conflict("a note with that name already exists in the destination folder")
	} else if err != nil && !errors.Is(err, apperror.ErrNotFound) {
		return err
	}

	oldDiskPath := note.DiskPath
	newDiskPath := notesDiskPath(userUUID, newFolderPath, note.Slug)

	if err := s.fs.RenameFileExclusive(oldDiskPath, newDiskPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return apperror.Conflict("a note with that name already exists in the destination folder")
		}
		return apperror.Internal("move note file", err)
	}

	if err := s.notes.Move(ctx, userID, noteID, newFolderID, newDiskPath); err != nil {
		_ = s.fs.RenameFileExclusive(newDiskPath, oldDiskPath)
		return err
	}

	// Reindex outgoing links — relative targets resolve differently in a new folder.
	note.FolderID = newFolderID
	if err := s.indexLinks(ctx, note, ""); err != nil {
		log.Warn().Err(err).Int64("note_id", noteID).Msg("index links after move")
	}

	return nil
}

// DeleteNote removes a note from DB and disk.
func (s *Service) DeleteNote(ctx context.Context, userID, noteID int64) error {
	defer s.lockTreeShared(userID)()
	note, err := s.notes.GetByID(ctx, userID, noteID)
	if err != nil {
		return err
	}
	diskPath := note.DiskPath

	// Disk first. Moving the file out of the live tree is the commit point: if
	// it fails (read-only volume, busy mount, permissions) we return the error
	// with the row untouched. The old order removed the row first and discarded
	// the filesystem error, so a failed unlink left the file behind and the next
	// reconcile re-imported it as a brand-new note — the note "came back" with
	// its tags and share token gone.
	token, err := s.fs.MoveToTrash(diskPath)
	if err != nil {
		return err
	}

	if err := s.notes.Delete(ctx, userID, noteID); err != nil {
		if token != "" {
			if restoreErr := s.fs.RestoreFromTrash(token, diskPath); restoreErr != nil {
				log.Error().Err(restoreErr).
					Str("disk_path", diskPath).Str("trash", token).
					Msg("notes: could not restore the file after a failed delete — it is still in the trash")
			}
		}
		return err
	}

	if token != "" {
		if purgeErr := s.fs.PurgeTrash(token); purgeErr != nil {
			log.Warn().Err(purgeErr).Str("trash", token).
				Msg("notes: could not purge the trash entry (harmless, cleaned up later)")
		}
		s.fs.RecordDelete(diskPath)
	}
	return nil
}

// SetShareToken generates a new share token or clears it (pass clear=true).
func (s *Service) SetShareToken(ctx context.Context, userID, noteID int64, clear bool) (*string, error) {
	if clear {
		return nil, s.notes.SetShareToken(ctx, userID, noteID, nil)
	}

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, apperror.Internal("generate share token", err)
	}
	token := hex.EncodeToString(b)

	if err := s.notes.SetShareToken(ctx, userID, noteID, &token); err != nil {
		return nil, fmt.Errorf("set share token: %w", err)
	}
	return &token, nil
}

// FindNotesByTag returns all notes that have every tag in the given list (AND semantics).
// An empty tags slice returns all notes.
func (s *Service) FindNotesByTag(ctx context.Context, userID int64, tags []string) ([]*model.NoteListItem, error) {
	all, err := s.notes.ListAll(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(tags) == 0 {
		return all, nil
	}
	var out []*model.NoteListItem
	for _, note := range all {
		if hasAllTags(note.Tags, tags) {
			out = append(out, note)
		}
	}
	return out, nil
}

// ListAllTags returns the sorted unique set of tags in use across all notes.
func (s *Service) ListAllTags(ctx context.Context, userID int64) ([]string, error) {
	all, err := s.notes.ListAll(ctx, userID)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, note := range all {
		for _, t := range note.Tags {
			seen[t] = struct{}{}
		}
	}
	tags := make([]string, 0, len(seen))
	for t := range seen {
		tags = append(tags, t)
	}
	sortStrings(tags)
	return tags, nil
}

// hasAllTags reports whether item contains every tag in want (case-sensitive).
func hasAllTags(have, want []string) bool {
	m := make(map[string]struct{}, len(have))
	for _, t := range have {
		m[t] = struct{}{}
	}
	for _, t := range want {
		if _, ok := m[t]; !ok {
			return false
		}
	}
	return true
}

// sortStrings sorts a string slice in place.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
