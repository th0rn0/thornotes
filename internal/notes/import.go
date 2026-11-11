package notes

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

// ImportResult summarises what was created during an import.
type ImportResult struct {
	NotesCreated   int `json:"notes_created"`
	FoldersCreated int `json:"folders_created"`
}

// importTitleFallback is the title an imported note falls back to: the entry or
// file name without its .md suffix, and "Imported Note" when the name carries
// nothing to use. It is also what the slug is derived from, so it must stay the
// filename's own reading — a round trip depends on the file keeping the name it
// arrived with (ticket 615).
func importTitleFallback(filename string) string {
	name := strings.TrimSuffix(filename, ".md")
	if name == "" {
		name = "Imported Note"
	}
	return name
}

// importTitle is the title an imported note is created with: the "# H1" its
// body states, or the filename-derived fallback when the body states no
// heading.
//
// This is the same rule disk discovery applies to a note found on disk
// (deriveTitle, reconcile step 8) — the body states the title, the filename is
// only the fallback — and it is applied at import because import writes the
// body in the same breath as the row. Titling from the filename and letting
// the first reconcile pass converge the row a moment later titled one note
// twice in front of the user: one title on insert, another a pass later, which
// is what ticket 631 is about. Deriving it here means the pass finds nothing
// to converge, because there was never a disagreement to repair.
//
// One deliberate difference from deriveTitle: a heading longer than the title
// limit falls back to the filename instead of being truncated. A truncated
// heading is a title nobody wrote, and it would be stored on a row whose file
// still states the longer line.
func importTitle(content, fallback string) string {
	heading, ok := statedHeading(content)
	if !ok || len(heading) > maxTitleLen {
		return fallback
	}
	return heading
}

// ImportMarkdown imports a single .md file as a root-level note.
func (s *Service) ImportMarkdown(ctx context.Context, userID int64, userUUID string, filename, content string) (*ImportResult, error) {
	name := importTitleFallback(filename)
	title := importTitle(content, name)

	// The slug — and therefore the file's path — still comes from the filename.
	// The zip round trip depends on it: entry names are sanitised titles, so a
	// re-import restores the same layout (ticket 615). Only the title is derived
	// from the body, and deriving it must not move a file.
	note, err := s.createNoteWithSlug(ctx, userID, userUUID, nil, title, slugify(name), nil)
	if err != nil {
		return nil, err
	}

	if content != "" {
		if _, err := s.UpdateNoteContent(ctx, userID, note.ID, content, note.ContentHash); err != nil {
			return nil, err
		}
	}

	return &ImportResult{NotesCreated: 1}, nil
}

// ImportZip imports a ZIP archive. Each .md file becomes a note; directories
// become folders. Non-.md files are silently skipped.
func (s *Service) ImportZip(ctx context.Context, userID int64, userUUID string, data []byte) (*ImportResult, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, apperror.BadRequest("invalid zip archive: " + err.Error())
	}

	result := &ImportResult{}
	// folderCache maps dir path (as it appears in the zip) → *model.Folder
	folderCache := map[string]*model.Folder{}

	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}

		// Only import .md files.
		if !strings.HasSuffix(strings.ToLower(f.Name), ".md") {
			continue
		}

		// Read content.
		rc, err := f.Open()
		if err != nil {
			return nil, apperror.Internal("open zip entry", err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			rc.Close()
			return nil, apperror.Internal("read zip entry", err)
		}
		rc.Close()

		content := buf.String()
		dir := filepath.ToSlash(filepath.Dir(f.Name))
		base := filepath.Base(f.Name)
		name := importTitleFallback(base)
		title := importTitle(content, name)

		// Resolve or create the parent folder.
		var parentFolder *model.Folder
		if dir != "." && dir != "" {
			parentFolder, err = s.ensureZipFolder(ctx, userID, userUUID, dir, folderCache, &result.FoldersCreated)
			if err != nil {
				return nil, fmt.Errorf("ensure folder %q: %w", dir, err)
			}
		}

		var folderID *int64
		if parentFolder != nil {
			folderID = &parentFolder.ID
		}

		// Slug and path come from the entry's filename, never from the title.
		note, err := s.createNoteWithSlug(ctx, userID, userUUID, folderID, title, slugify(name), nil)
		if err != nil {
			// Skip conflicts (duplicate title in same folder).
			if apperror.IsConflict(err) {
				continue
			}
			return nil, err
		}

		if content != "" {
			if _, err := s.UpdateNoteContent(ctx, userID, note.ID, content, note.ContentHash); err != nil {
				return nil, err
			}
		}
		result.NotesCreated++
	}

	return result, nil
}

// ensureZipFolder resolves or creates nested folders for a zip dir path like "Work/Projects".
func (s *Service) ensureZipFolder(ctx context.Context, userID int64, userUUID, dirPath string, cache map[string]*model.Folder, created *int) (*model.Folder, error) {
	if f, ok := cache[dirPath]; ok {
		return f, nil
	}

	parts := strings.Split(dirPath, "/")
	var parentID *int64
	built := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		if built == "" {
			built = part
		} else {
			built = built + "/" + part
		}
		if f, ok := cache[built]; ok {
			parentID = &f.ID
			continue
		}
		folder, err := s.CreateFolder(ctx, userID, userUUID, parentID, part)
		if err != nil {
			if !apperror.IsConflict(err) {
				return nil, err
			}
			// Folder already exists — find it by disk path.
			diskPath := folderDiskPath(userUUID, func() string {
				if parentID != nil {
					if p, ok := cache[built[:len(built)-len(part)-1]]; ok {
						return p.DiskPath
					}
				}
				return ""
			}(), part)
			f, err := s.folders.GetByDiskPath(ctx, diskPath)
			if err != nil {
				return nil, err
			}
			folder = f
		} else {
			*created++
		}
		cache[built] = folder
		parentID = &folder.ID
	}
	return cache[dirPath], nil
}
