package notes

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

// ExportReportName is the reserved top-level entry added to an archive when a
// note row has outlived its file. The leading underscore keeps it clear of every
// name the sanitiser can produce from a title, and the importer ignores it
// because it is not a .md file.
const ExportReportName = "_export-report.txt"

// exportNameMaxBytes bounds one archive path component, leaving room for a
// "-2".."-999" collision suffix and a ".md" extension inside the 255-byte limit
// every filesystem the archive may be restored on enforces.
const exportNameMaxBytes = 120

// zipZeroTime is the fallback entry time. It is a valid MS-DOS date rather than
// the zero time, so an archive always carries a readable timestamp, and it is
// constant so an export of the same tree does not change between runs.
var zipZeroTime = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)

// ExportEntry is one planned archive entry.
type ExportEntry struct {
	// Path is the entry name inside the archive, always with forward slashes.
	// Directory entries end in "/".
	Path string
	// DiskPath is the source file, relative to the notes root. Empty for
	// directories and for generated entries (the skip report).
	DiskPath string
	// ModTime is the entry's modified time: the note's updated_at for a note,
	// the folder's created_at for a directory.
	ModTime time.Time
	IsDir   bool
	// Data is the content of a generated entry. When nil, the entry is streamed
	// from DiskPath.
	Data []byte
}

// ExportPlan is a complete archive, planned before the first byte is written.
//
// Planning up front is what lets the export be streaming *and* honest. Every
// fallible database read and every file-existence check happens here; the entry
// order is fixed here; and the skip report — if any note's file has gone missing
// — is inserted here, so it can sit at its sorted position instead of being
// bolted on at the end. Streaming then only has to open one note file at a time.
type ExportPlan struct {
	// Filename is the download name, e.g. "work.zip".
	Filename string
	// Entries is the archive contents, already sorted by Path.
	Entries []ExportEntry
}

// PlanNoteExport plans the one-entry archive for a note.
//
// The note is looked up scoped to the caller, so a note that belongs to someone
// else and a note that does not exist both come back as ErrNotFound: the handler
// cannot tell them apart and answers both with the same 404 body.
func (s *Service) PlanNoteExport(ctx context.Context, userID, noteID int64) (*ExportPlan, error) {
	note, err := s.notes.GetByID(ctx, userID, noteID)
	if err != nil {
		return nil, err
	}

	name := sanitizeExportName(note.Title)
	entries, err := s.checkExportFiles([]ExportEntry{{
		Path:     name + ".md",
		DiskPath: note.DiskPath,
		ModTime:  note.UpdatedAt,
	}})
	if err != nil {
		return nil, err
	}

	plan := &ExportPlan{Filename: name + ".zip", Entries: entries}
	sortExportEntries(plan.Entries)
	return plan, nil
}

// PlanFolderExport plans the archive for a folder and everything under it.
//
// The folder's own name is the archive's top-level directory, so exporting
// work/ yields work/<note>.md and work/<sub>/<note>.md and re-importing the
// archive recreates work/ rather than a loose pile of files.
func (s *Service) PlanFolderExport(ctx context.Context, userID, folderID int64) (*ExportPlan, error) {
	root, err := s.folders.GetByID(ctx, userID, folderID)
	if err != nil {
		return nil, err
	}

	// Both halves of the subtree come from repository.DescendantPathPredicate
	// (ticket 598) via ListSubtree, so the folders and the notes are selected by
	// one definition of "under this folder" — bounded at a path separator and
	// with LIKE metacharacters escaped — instead of a second, drifting one.
	subtree, err := s.folders.ListSubtree(ctx, userID, root.DiskPath)
	if err != nil {
		return nil, apperror.Internal("list folder subtree for export", err)
	}
	noteRecords, err := s.notes.ListSubtree(ctx, userID, root.DiskPath)
	if err != nil {
		return nil, apperror.Internal("list notes for export", err)
	}

	dirs, dirPaths := planExportDirs(root, subtree)
	rootName := dirPaths[root.ID]

	// The root's own name occupies the top-level namespace; register it (and
	// every subdirectory) so a note cannot take a directory's name.
	taken := map[string]map[string]bool{}
	registerExportName(taken, "", rootName)
	for id, dirPath := range dirPaths {
		if id == root.ID {
			continue
		}
		parent, base := splitArchivePath(dirPath)
		registerExportName(taken, parent, base)
	}

	files := make([]ExportEntry, 0, len(noteRecords))
	// Name assignment must not depend on the database's ORDER BY: MySQL's default
	// collation is case-insensitive and SQLite's is not, and a collision suffix
	// ("-2") assigned in a different order between dialects would make the same
	// tree export differently on MariaDB and SQLite. Byte-wise order here.
	sort.Slice(noteRecords, func(i, j int) bool { return noteRecords[i].DiskPath < noteRecords[j].DiskPath })
	for _, rec := range noteRecords {
		var parent string
		if rec.FolderID != nil {
			parent = dirPaths[*rec.FolderID]
		}
		if parent == "" {
			// A row whose folder is not part of the subtree cannot be filed in
			// the archive tree. Keep it at the top level rather than dropping it:
			// losing a note silently is the failure this whole report exists to
			// prevent, and the dedupe below handles any name clash.
			parent = rootName
		}
		name := uniqueExportName(taken, parent, sanitizeExportName(rec.Title), ".md")
		files = append(files, ExportEntry{
			Path:     parent + "/" + name,
			DiskPath: rec.DiskPath,
			ModTime:  rec.UpdatedAt,
		})
	}

	entries, err := s.checkExportFiles(files)
	if err != nil {
		return nil, err
	}
	plan := &ExportPlan{Filename: rootName + ".zip", Entries: append(dirs, entries...)}
	sortExportEntries(plan.Entries)
	return plan, nil
}

// StreamExport writes the plan to w as a zip archive, one entry at a time.
//
// Only one note file is open at a time and nothing is buffered beyond the
// deflate window, so memory does not scale with the size of the vault, and no
// temporary file is involved. The plan is already sorted, so the entries come
// out in the same order on every run.
func (s *Service) StreamExport(ctx context.Context, plan *ExportPlan, w io.Writer) error {
	zw := zip.NewWriter(w)
	for _, entry := range plan.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.writeExportEntry(zw, entry); err != nil {
			return err
		}
	}
	return zw.Close()
}

// writeExportEntry writes one entry. A note is copied straight from disk, so the
// bytes in the archive are the bytes on disk — unicode, [[wikilinks]] and code
// fences included — rather than a database column that may have drifted.
func (s *Service) writeExportEntry(zw *zip.Writer, entry ExportEntry) error {
	modTime := entry.ModTime
	if modTime.IsZero() {
		modTime = zipZeroTime
	}
	header := &zip.FileHeader{Name: entry.Path, Method: zip.Deflate, Modified: modTime.UTC()}

	if entry.IsDir {
		header.Method = zip.Store
		header.SetMode(0o755 | os.ModeDir)
		_, err := zw.CreateHeader(header)
		return err
	}

	header.SetMode(0o600)
	if entry.Data != nil {
		fw, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		_, err = fw.Write(entry.Data)
		return err
	}

	f, err := s.fs.Open(entry.DiskPath)
	if err != nil {
		return fmt.Errorf("open export entry %q: %w", entry.Path, err)
	}
	defer f.Close()

	fw, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return fmt.Errorf("write export entry %q: %w", entry.Path, err)
	}
	return nil
}

// checkExportFiles separates the notes that have a file from the ones that do
// not, and turns the latter into the skip report.
//
// Notes are disk-canonical, so a row really can outlive its file after a
// half-finished delete. That must not crash the export and must not quietly
// ship a short archive, so the entry is left out and the report names it. A
// symlink is treated the same way: the store refuses to follow one (it is not
// part of the notes model, and reconcile skips it as non-regular), and a report
// line is a better answer than either an explosion or silence.
func (s *Service) checkExportFiles(files []ExportEntry) ([]ExportEntry, error) {
	entries := make([]ExportEntry, 0, len(files)+1)
	var skipped []ExportEntry
	for _, entry := range files {
		if _, err := s.fs.Stat(entry.DiskPath); err != nil {
			if errors.Is(err, apperror.ErrNotFound) || errors.Is(err, apperror.ErrPathTraversal) {
				skipped = append(skipped, entry)
				continue
			}
			return nil, apperror.Internal("stat export entry "+entry.Path, err)
		}
		entries = append(entries, entry)
	}
	if len(skipped) > 0 {
		entries = append(entries, ExportEntry{
			Path:    ExportReportName,
			ModTime: newestExportModTime(append(entries, skipped...)),
			Data:    renderExportReport(skipped),
		})
	}
	return entries, nil
}

// renderExportReport is the archive's account of what it could not carry.
func renderExportReport(skipped []ExportEntry) []byte {
	var b strings.Builder
	b.WriteString("# Export report\n\n")
	fmt.Fprintf(&b, "%d note file(s) could not be exported and are not in this archive:\n\n", len(skipped))
	for _, entry := range skipped {
		fmt.Fprintf(&b, "- %s (notes-root path %q: file not found)\n", entry.Path, entry.DiskPath)
	}
	b.WriteString("\nThe note rows still exist in the database — only their files are missing from disk.\n")
	return []byte(b.String())
}

func newestExportModTime(entries []ExportEntry) time.Time {
	var newest time.Time
	for _, entry := range entries {
		if entry.ModTime.After(newest) {
			newest = entry.ModTime
		}
	}
	return newest
}

// planExportDirs names every directory in the archive and returns one directory
// entry per folder, plus the archive path for each folder ID.
//
// The walk follows parent links from the root with a visited set. The subtree
// query already cannot loop, but a parent chain that does (bad data written
// before the cycle guard existed) must not turn an export into an infinite
// loop, and a repeated folder must not produce a duplicate entry.
func planExportDirs(root *model.Folder, subtree []*model.Folder) ([]ExportEntry, map[int64]string) {
	byID := make(map[int64]*model.Folder, len(subtree)+1)
	for _, folder := range subtree {
		if folder == nil {
			continue
		}
		byID[folder.ID] = folder
	}
	byID[root.ID] = root

	children := make(map[int64][]*model.Folder, len(byID))
	for _, folder := range byID {
		if folder.ParentID == nil {
			continue
		}
		children[*folder.ParentID] = append(children[*folder.ParentID], folder)
	}
	for parent := range children {
		siblings := children[parent]
		// Sort by name so the collision suffixes do not depend on the database's
		// collation (MySQL's default is case-insensitive, SQLite's is not).
		sort.Slice(siblings, func(i, j int) bool {
			if siblings[i].Name != siblings[j].Name {
				return siblings[i].Name < siblings[j].Name
			}
			return siblings[i].ID < siblings[j].ID
		})
	}

	taken := map[string]map[string]bool{}
	dirPaths := make(map[int64]string, len(byID))
	dirPaths[root.ID] = uniqueExportName(taken, "", sanitizeExportName(root.Name), "")

	visited := map[int64]bool{root.ID: true}
	queue := []int64{root.ID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		parentPath := dirPaths[id]
		for _, child := range children[id] {
			if visited[child.ID] {
				continue
			}
			visited[child.ID] = true
			dirPaths[child.ID] = parentPath + "/" + uniqueExportName(taken, parentPath, sanitizeExportName(child.Name), "")
			queue = append(queue, child.ID)
		}
	}

	entries := make([]ExportEntry, 0, len(dirPaths))
	for id, dirPath := range dirPaths {
		entries = append(entries, ExportEntry{
			Path:    dirPath + "/",
			ModTime: byID[id].CreatedAt,
			IsDir:   true,
		})
	}
	return entries, dirPaths
}

// splitArchivePath splits "work/sub/Note.md" into ("work/sub", "Note.md").
func splitArchivePath(archivePath string) (parent, base string) {
	trimmed := strings.TrimSuffix(archivePath, "/")
	i := strings.LastIndexByte(trimmed, '/')
	if i < 0 {
		return "", trimmed
	}
	return trimmed[:i], trimmed[i+1:]
}

// registerExportName marks a name as taken in its parent directory.
func registerExportName(taken map[string]map[string]bool, parentDir, base string) {
	names := taken[parentDir]
	if names == nil {
		names = map[string]bool{}
		taken[parentDir] = names
	}
	names[strings.ToLower(base)] = true
}

// uniqueExportName returns name+ext, or name-2, name-3 … when that name is
// already taken in the same archive directory. The comparison is case-insensitive
// because the archive may be restored on a case-insensitive filesystem and the
// importer lowercases slugs: "Notes" and "notes" must not both be emitted.
func uniqueExportName(taken map[string]map[string]bool, parentDir, name, ext string) string {
	names := taken[parentDir]
	if names == nil {
		names = map[string]bool{}
		taken[parentDir] = names
	}
	for attempt := 1; ; attempt++ {
		base := name
		if attempt > 1 {
			base = fmt.Sprintf("%s-%d", name, attempt)
		}
		candidate := base + ext
		if !names[strings.ToLower(candidate)] {
			names[strings.ToLower(candidate)] = true
			return candidate
		}
	}
}

// sanitizeExportName turns a note title or folder name into one path component.
//
// The archive is also the import format and the importer derives the title from
// the file name, so the goal is to keep every character a file name can carry:
//
//   - only [A-Za-z0-9 ._-] survive. Everything else — including '/', '\', ':' and
//     NUL — becomes a single space, so a note called "Q3/Q4 plan" is one entry
//     and never a directory;
//   - runs of spaces collapse, and leading/trailing spaces and dots are trimmed:
//     a leading dot is the store's reserved internal namespace, and a name made
//     only of dots or spaces is not a name;
//   - the result is capped at exportNameMaxBytes so a collision suffix and
//     ".md" still fit in a 255-byte component;
//   - an empty result becomes "untitled", never "".
func sanitizeExportName(name string) string {
	var b strings.Builder
	afterSpace := false
	for _, r := range name {
		if isExportNameRune(r) {
			if r == ' ' {
				if afterSpace {
					continue
				}
				afterSpace = true
			} else {
				afterSpace = false
			}
			b.WriteRune(r)
			continue
		}
		if !afterSpace && b.Len() > 0 {
			b.WriteByte(' ')
			afterSpace = true
		}
	}

	out := strings.Trim(b.String(), " .")
	if len(out) > exportNameMaxBytes {
		// Every rune above is ASCII by construction, so slicing cannot split one.
		out = strings.TrimRight(out[:exportNameMaxBytes], " .")
	}
	if out == "" {
		return "untitled"
	}
	return out
}

// isExportNameRune reports whether r may appear in an archive path component.
func isExportNameRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == ' ', r == '.', r == '_', r == '-':
		return true
	default:
		return false
	}
}

// sortExportEntries orders the archive by path, which is what makes two exports
// of the same tree identical in entries and order.
func sortExportEntries(entries []ExportEntry) {
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
}
