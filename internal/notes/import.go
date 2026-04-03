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

// ImportMarkdown imports a single .md file as a root-level note.
func (s *Service) ImportMarkdown(ctx context.Context, userID int64, userUUID string, filename, content string) (*ImportResult, error) {
	title := strings.TrimSuffix(filename, ".md")
	if title == "" {
		title = "Imported Note"
	}

	note, err := s.CreateNote(ctx, userID, userUUID, nil, title, nil)
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
		title := strings.TrimSuffix(base, ".md")
		if title == "" {
			title = "Imported Note"
		}

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

		note, err := s.CreateNote(ctx, userID, userUUID, folderID, title, nil)
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
