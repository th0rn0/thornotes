/* thornotes — zip download helpers
   Pure helpers used by app.js to drive the "Download as zip" context-menu
   action. Split out so the Node test runner can exercise them without a DOM. */
'use strict';

// kind: 'note' | 'folder'. Returns the same-origin endpoint URL that, per
// ticket 607, returns application/zip with a Content-Disposition attachment.
function buildZipUrl(kind, id) {
  if (kind !== 'note' && kind !== 'folder') {
    throw new Error('buildZipUrl: kind must be "note" or "folder"');
  }
  if (!Number.isFinite(id) || id <= 0) {
    throw new Error('buildZipUrl: id must be a positive number');
  }
  return kind === 'note'
    ? `/api/v1/notes/${id}/zip`
    : `/api/v1/folders/${id}/zip`;
}

// Pull a filename out of a Content-Disposition header. Falls back to the
// supplied fallback (including when the header is missing or unparseable).
// Handles both `filename="..."` and RFC 5987 `filename*=UTF-8''...`.
function parseContentDispositionFilename(header, fallback) {
  if (typeof header !== 'string' || header.length === 0) return fallback;
  // RFC 5987 form first — it takes precedence when both are present.
  const starMatch = header.match(/filename\*\s*=\s*([A-Za-z0-9_-]+)''([^;]+)/i);
  if (starMatch) {
    try {
      const decoded = decodeURIComponent(starMatch[2].trim());
      if (decoded) return decoded;
    } catch (_) { /* fall through */ }
  }
  const plainMatch = header.match(/filename\s*=\s*"?([^";]+)"?/i);
  if (plainMatch && plainMatch[1] && plainMatch[1].trim()) {
    return plainMatch[1].trim();
  }
  return fallback;
}

// The tree can be stale when another session deleted the row between the last
// loadFolderTree() and a right-click. Return true only when the id is still
// present in the rendered tree state. `folders` is the flat folder list,
// `rootNotes` are notes with no folder, `notesByFolder` maps folderId→notes.
function rowExistsInTree(kind, id, folders, rootNotes, notesByFolder) {
  if (!Number.isFinite(id) || id <= 0) return false;
  if (kind === 'folder') {
    return Array.isArray(folders) && folders.some(f => f && f.id === id);
  }
  if (kind === 'note') {
    if (Array.isArray(rootNotes) && rootNotes.some(n => n && n.id === id)) return true;
    if (notesByFolder && typeof notesByFolder === 'object') {
      for (const list of Object.values(notesByFolder)) {
        if (Array.isArray(list) && list.some(n => n && n.id === id)) return true;
      }
    }
    return false;
  }
  return false;
}

// Build a sensible download filename from the row's title/name. Sanitises
// characters that are awkward in filenames but preserves unicode.
function buildZipFilename(kind, title) {
  const safe = typeof title === 'string' && title.trim().length > 0
    ? title.trim().replace(/[\\/:*?"<>|]/g, '_').slice(0, 120)
    : (kind === 'note' ? 'note' : 'folder');
  return `${safe}.zip`;
}

// CommonJS export for the Node.js test runner — no-op in browsers.
if (typeof module !== 'undefined') {
  module.exports = {
    buildZipUrl,
    parseContentDispositionFilename,
    rowExistsInTree,
    buildZipFilename,
  };
}
