/* thornotes — table parsing utilities
   Pure functions; no DOM dependencies so they run under Node for testing. */
'use strict';

// Parse a single CSV line (RFC 4180 compatible).
// Handles quoted fields, embedded commas, and doubled-quote escapes.
function parseCSVLine(line) {
  const result = [];
  let cur = '';
  let inQ = false;
  for (let i = 0; i < line.length; i++) {
    const ch = line[i];
    if (inQ) {
      if (ch === '"') {
        if (line[i + 1] === '"') { cur += '"'; i++; } // "" → "
        else inQ = false;
      } else {
        cur += ch;
      }
    } else {
      if (ch === '"') { inQ = true; }
      else if (ch === ',') { result.push(cur); cur = ''; }
      else { cur += ch; }
    }
  }
  result.push(cur);
  return result;
}

// Detect whether `text` is tabular (TSV or CSV) and return the rows as a 2D
// array of trimmed strings, or null if the text does not look tabular.
// TSV (Excel copy/paste) is tried first; CSV is the fallback.
// A single-column result is never returned — callers expect at least 2 columns.
function parseTabularText(text) {
  const raw = (text || '').trim();
  if (!raw) return null;

  const lines = raw.split(/\r?\n/).filter(l => l.trim() !== '');
  if (lines.length < 1) return null;

  // TSV first — Excel pastes cells as tab-separated values.
  if (raw.includes('\t')) {
    const rows = lines.map(l => l.split('\t').map(c => c.trim()));
    const maxCols = rows.reduce((m, r) => Math.max(m, r.length), 0);
    if (maxCols >= 2) {
      // Pad ragged rows to uniform width.
      return rows.map(r => { while (r.length < maxCols) r.push(''); return r; });
    }
  }

  // CSV fallback.
  if (raw.includes(',')) {
    const rows = lines.map(l => parseCSVLine(l).map(c => c.trim()));
    const cols = rows[0].length;
    if (cols >= 2 && rows.every(r => r.length === cols)) {
      return rows;
    }
  }

  return null;
}

// Convert a 2D array of strings to a GitHub-Flavored Markdown table.
// The first row is treated as the header row.
function rowsToMarkdownTable(rows) {
  if (!rows || rows.length === 0) return '';
  const cols = rows.reduce((m, r) => Math.max(m, r.length), 0);

  function cell(c) {
    return String(c == null ? '' : c)
      .replace(/\|/g, '\\|')  // escape pipe characters
      .replace(/\n/g, ' ');   // collapse embedded newlines to space
  }

  function mdRow(r) {
    const cells = [];
    for (let i = 0; i < cols; i++) cells.push(cell(r[i] || ''));
    return '| ' + cells.join(' | ') + ' |';
  }

  const header = mdRow(rows[0]);
  const sep    = '| ' + Array(cols).fill('---').join(' | ') + ' |';
  const body   = rows.slice(1).map(mdRow).join('\n');
  return body ? header + '\n' + sep + '\n' + body : header + '\n' + sep;
}

// CommonJS export for the Node.js test runner — no-op in browsers.
if (typeof module !== 'undefined') {
  module.exports = { parseCSVLine, parseTabularText, rowsToMarkdownTable };
}
