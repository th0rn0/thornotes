package repository

import (
	"path/filepath"
	"strings"
)

// likeEscape is the ESCAPE character for the subtree predicate below.
//
// '!' is deliberately not a backslash: a backslash has to be doubled in a MySQL
// string literal to mean one backslash (and means two under
// NO_BACKSLASH_ESCAPES), so the same SQL text would decode differently per
// dialect. '!' needs no quoting in either, and it is escaped when it appears in a
// name, so a folder called "hi!" still matches only itself.
const likeEscape = "!"

// likeEscaper turns the LIKE metacharacters in a literal prefix into literals.
// Replacer does a single pass, so the escape character it inserts is never
// re-escaped.
var likeEscaper = strings.NewReplacer(
	likeEscape, likeEscape+likeEscape,
	"%", likeEscape+"%",
	"_", likeEscape+"_",
)

// DescendantPathPredicate returns the SQL predicate matching a row whose
// disk_path is prefix itself or lies strictly underneath it, together with the
// bind arguments it expects, in order. Both dialects share it because the text is
// identical in both — only the SET expression differs (SQLite's || versus
// MySQL's CONCAT).
//
// It exists because the unscoped `disk_path LIKE oldPrefix || '%'` it replaces
// was wrong in two independent ways (ticket 598):
//
//   - no path boundary, so renaming "Notes" also matched sibling "Notes2" and
//     rewrote rows outside the subtree. Those rows then pointed at paths that do
//     not exist; the next reconcile treated them as deleted, dropped them and
//     reimported them from disk — a reimported row is a new row, so its share
//     token was gone and any public link silently stopped working.
//   - no escaping, so '%' and '_' in a folder name acted as wildcards: a folder
//     called "100%_done" matched "1000Xdone" too.
//
// The separator comes from filepath.Separator, the same source the path builders
// (folderDiskPath, notesDiskPath) use, so the scope cannot drift from the paths
// those builders write — this is not a second, hard-coded '/'.
func DescendantPathPredicate(prefix string) (string, []any) {
	pattern := likeEscaper.Replace(prefix) + string(filepath.Separator) + "%"
	return "disk_path = ? OR disk_path LIKE ? ESCAPE '" + likeEscape + "'", []any{prefix, pattern}
}
