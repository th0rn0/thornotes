// thornotes CM6 bundle — compiled to web/static/js/vendor/codemirror6.min.js
// Build: cd web/cm6-bundle && bun install && bun run build

import { EditorView, keymap, lineNumbers } from "@codemirror/view";
import { EditorState, Compartment } from "@codemirror/state";
import {
  history, historyKeymap, defaultKeymap, indentWithTab,
  undo as cmUndo, redo as cmRedo,
} from "@codemirror/commands";
import { markdown } from "@codemirror/lang-markdown";
import { syntaxHighlighting, defaultHighlightStyle } from "@codemirror/language";

// ── Themes ──────────────────────────────────────────────────────────────────

const catppuccinTheme = EditorView.theme({
  '&': { height: '100%', backgroundColor: '#1e1e2e', color: '#cdd6f4' },
  '.cm-scroller': {
    fontFamily: '"Cascadia Code", "Fira Code", "Consolas", monospace',
    fontSize: '13px',
    lineHeight: '1.6',
  },
  '.cm-content': { padding: '12px 16px', caretColor: '#cdd6f4', minHeight: '100%' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: '#cdd6f4' },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground': {
    backgroundColor: '#45475a',
  },
  '&.cm-focused': { outline: 'none' },
  '.cm-activeLine': { backgroundColor: 'rgba(137,180,250,0.06)' },
  '.cm-gutters': { display: 'none', backgroundColor: '#1e1e2e', borderRight: '1px solid #313244', color: '#585b70' },
  '.cm-lineNumbers .cm-gutterElement': { padding: '0 10px 0 8px', minWidth: '2.5em', textAlign: 'right' },
}, { dark: true });

const darkTheme = EditorView.theme({
  '&': { height: '100%', backgroundColor: '#1e1e1e', color: '#d4d4d4' },
  '.cm-scroller': {
    fontFamily: '"Cascadia Code", "Fira Code", "Consolas", monospace',
    fontSize: '13px',
    lineHeight: '1.6',
  },
  '.cm-content': { padding: '12px 16px', caretColor: '#d4d4d4', minHeight: '100%' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: '#d4d4d4' },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground': {
    backgroundColor: '#264f78',
  },
  '&.cm-focused': { outline: 'none' },
  '.cm-activeLine': { backgroundColor: 'rgba(255,255,255,0.04)' },
  '.cm-gutters': { display: 'none', backgroundColor: '#1e1e1e', borderRight: '1px solid #333', color: '#858585' },
  '.cm-lineNumbers .cm-gutterElement': { padding: '0 10px 0 8px', minWidth: '2.5em', textAlign: 'right' },
}, { dark: true });

const lightTheme = EditorView.theme({
  '&': { height: '100%', backgroundColor: '#ffffff', color: '#24292e' },
  '.cm-scroller': {
    fontFamily: '"Cascadia Code", "Fira Code", "Consolas", monospace',
    fontSize: '13px',
    lineHeight: '1.6',
  },
  '.cm-content': { padding: '12px 16px', caretColor: '#24292e', minHeight: '100%' },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground': {
    backgroundColor: '#b3d4ff',
  },
  '&.cm-focused': { outline: 'none' },
  '.cm-activeLine': { backgroundColor: 'rgba(0,0,0,0.03)' },
  '.cm-gutters': { display: 'none', backgroundColor: '#f5f5f5', borderRight: '1px solid #ddd', color: '#999' },
  '.cm-lineNumbers .cm-gutterElement': { padding: '0 10px 0 8px', minWidth: '2.5em', textAlign: 'right' },
}, { dark: false });

// ── Extensions ───────────────────────────────────────────────────────────────

function baseExtensions(onChange) {
  const exts = [
    history(),
    lineNumbers(),
    EditorView.lineWrapping,
    keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
    markdown(),
    syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
  ];
  if (onChange) {
    exts.push(EditorView.updateListener.of(update => {
      if (update.docChanged) onChange();
    }));
  }
  return exts;
}

// ── Formatting helpers ───────────────────────────────────────────────────────

function toggleInline(view, marker) {
  const { state } = view;
  const changes = [];
  for (const range of state.selection.ranges) {
    if (range.empty) {
      // No selection: insert pair and leave cursor between them
      changes.push({ from: range.from, insert: marker + marker });
    } else {
      const text = state.sliceDoc(range.from, range.to);
      const ml = marker.length;
      if (text.startsWith(marker) && text.endsWith(marker) && text.length > ml * 2) {
        changes.push({ from: range.from, to: range.to, insert: text.slice(ml, -ml) });
      } else {
        changes.push({ from: range.from, to: range.to, insert: marker + text + marker });
      }
    }
  }
  view.dispatch({ changes });
  view.focus();
}

function toggleLinePrefix(view, prefix) {
  const { state } = view;
  const changes = [];
  const sel = state.selection.main;
  const startLine = state.doc.lineAt(sel.from);
  const endLine = state.doc.lineAt(sel.to);
  let allHave = true;
  for (let i = startLine.number; i <= endLine.number; i++) {
    if (!state.doc.line(i).text.startsWith(prefix)) { allHave = false; break; }
  }
  for (let i = startLine.number; i <= endLine.number; i++) {
    const line = state.doc.line(i);
    if (allHave) {
      changes.push({ from: line.from, to: line.from + prefix.length, insert: '' });
    } else if (!line.text.startsWith(prefix)) {
      changes.push({ from: line.from, insert: prefix });
    }
  }
  view.dispatch({ changes });
  view.focus();
}

function insertLink(view) {
  const { state } = view;
  const range = state.selection.main;
  const selected = state.sliceDoc(range.from, range.to);
  const linkText = selected || 'link text';
  const insert = `[${linkText}](url)`;
  view.dispatch({
    changes: { from: range.from, to: range.to, insert },
    selection: { anchor: range.from + 1, head: range.from + 1 + linkText.length },
  });
  view.focus();
}

// ── Public factory ───────────────────────────────────────────────────────────

const nordTheme = EditorView.theme({
  '&': { height: '100%', backgroundColor: '#2e3440', color: '#eceff4' },
  '.cm-scroller': {
    fontFamily: '"Cascadia Code", "Fira Code", "Consolas", monospace',
    fontSize: '13px',
    lineHeight: '1.6',
  },
  '.cm-content': { padding: '12px 16px', caretColor: '#eceff4', minHeight: '100%' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: '#eceff4' },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground': {
    backgroundColor: '#4c566a',
  },
  '&.cm-focused': { outline: 'none' },
  '.cm-activeLine': { backgroundColor: 'rgba(136,192,208,0.06)' },
  '.cm-gutters': { display: 'none', backgroundColor: '#2e3440', borderRight: '1px solid #4c566a', color: '#616e88' },
  '.cm-lineNumbers .cm-gutterElement': { padding: '0 10px 0 8px', minWidth: '2.5em', textAlign: 'right' },
}, { dark: true });

const tokyonightTheme = EditorView.theme({
  '&': { height: '100%', backgroundColor: '#1a1b26', color: '#a9b1d6' },
  '.cm-scroller': {
    fontFamily: '"Cascadia Code", "Fira Code", "Consolas", monospace',
    fontSize: '13px',
    lineHeight: '1.6',
  },
  '.cm-content': { padding: '12px 16px', caretColor: '#a9b1d6', minHeight: '100%' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: '#a9b1d6' },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground': {
    backgroundColor: '#283457',
  },
  '&.cm-focused': { outline: 'none' },
  '.cm-activeLine': { backgroundColor: 'rgba(122,162,247,0.06)' },
  '.cm-gutters': { display: 'none', backgroundColor: '#1a1b26', borderRight: '1px solid #292e42', color: '#565f89' },
  '.cm-lineNumbers .cm-gutterElement': { padding: '0 10px 0 8px', minWidth: '2.5em', textAlign: 'right' },
}, { dark: true });

const solarizedTheme = EditorView.theme({
  '&': { height: '100%', backgroundColor: '#fdf6e3', color: '#657b83' },
  '.cm-scroller': {
    fontFamily: '"Cascadia Code", "Fira Code", "Consolas", monospace',
    fontSize: '13px',
    lineHeight: '1.6',
  },
  '.cm-content': { padding: '12px 16px', caretColor: '#657b83', minHeight: '100%' },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground': {
    backgroundColor: '#cce0f5',
  },
  '&.cm-focused': { outline: 'none' },
  '.cm-activeLine': { backgroundColor: 'rgba(38,139,210,0.04)' },
  '.cm-gutters': { display: 'none', backgroundColor: '#fdf6e3', borderRight: '1px solid #c7bca0', color: '#93a1a1' },
  '.cm-lineNumbers .cm-gutterElement': { padding: '0 10px 0 8px', minWidth: '2.5em', textAlign: 'right' },
}, { dark: false });

const themes = { light: lightTheme, dark: darkTheme, catppuccin: catppuccinTheme, nord: nordTheme, tokyonight: tokyonightTheme, solarized: solarizedTheme };

function createEditor(parent, { onChange, theme } = {}) {
  const themeComp = new Compartment();
  const state = EditorState.create({
    doc: '',
    extensions: [
      ...baseExtensions(onChange),
      themeComp.of(themes[theme] ?? lightTheme),
    ],
  });
  const view = new EditorView({ state, parent });

  return {
    getValue() { return view.state.doc.toString(); },
    setValue(text) {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } });
    },
    setTheme(name) {
      view.dispatch({ effects: themeComp.reconfigure(themes[name] ?? lightTheme) });
    },
    focus() { view.focus(); },
    destroy() { view.destroy(); },
    _view: view,
  };
}

// ── Commands (called by toolbar) ─────────────────────────────────────────────

const commands = {
  bold(ed)          { toggleInline(ed._view, '**'); },
  italic(ed)        { toggleInline(ed._view, '_'); },
  heading(ed)       { toggleLinePrefix(ed._view, '## '); },
  quote(ed)         { toggleLinePrefix(ed._view, '> '); },
  unorderedList(ed) { toggleLinePrefix(ed._view, '- '); },
  orderedList(ed)   { toggleLinePrefix(ed._view, '1. '); },
  link(ed)          { insertLink(ed._view); },
  undo(ed)          { cmUndo(ed._view); },
  redo(ed)          { cmRedo(ed._view); },
};

window.CM6 = { createEditor, commands };
