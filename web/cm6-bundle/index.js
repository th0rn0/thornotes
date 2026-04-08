// thornotes CM6 bundle — compiled to web/static/js/vendor/codemirror6.min.js
// Build: cd web/cm6-bundle && bun install && bun run build

import { EditorView, keymap, lineNumbers } from "@codemirror/view";
import { EditorState, Compartment } from "@codemirror/state";
import {
  history, historyKeymap, defaultKeymap, indentWithTab,
  undo as cmUndo, redo as cmRedo,
} from "@codemirror/commands";
import { markdown } from "@codemirror/lang-markdown";
import { syntaxHighlighting, HighlightStyle } from "@codemirror/language";
import { tags } from "@lezer/highlight";

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

// ── Syntax highlight styles ───────────────────────────────────────────────

function makeHighlight(o) {
  return HighlightStyle.define([
    // Markdown structure
    { tag: tags.heading1,   fontWeight: '700', color: o.heading,   fontSize: '1.25em' },
    { tag: tags.heading2,   fontWeight: '700', color: o.heading,   fontSize: '1.1em'  },
    { tag: [tags.heading3, tags.heading4, tags.heading5, tags.heading6], fontWeight: '600', color: o.heading },
    { tag: tags.strong,        fontWeight: '700' },
    { tag: tags.emphasis,      fontStyle: 'italic' },
    { tag: tags.strikethrough, textDecoration: 'line-through', color: o.muted },
    { tag: tags.link,          color: o.link,    textDecoration: 'underline' },
    { tag: tags.url,           color: o.link },
    { tag: tags.monospace,     color: o.code,    fontFamily: 'monospace' },
    { tag: tags.quote,         color: o.comment, fontStyle: 'italic' },
    // Code syntax
    { tag: [tags.keyword, tags.operatorKeyword, tags.controlKeyword, tags.moduleKeyword],
      color: o.keyword, fontWeight: '600' },
    { tag: [tags.string, tags.character, tags.attributeValue, tags.docString],
      color: o.string },
    { tag: [tags.number, tags.integer, tags.float], color: o.number },
    { tag: [tags.comment, tags.lineComment, tags.blockComment], color: o.comment, fontStyle: 'italic' },
    { tag: [tags.typeName, tags.className, tags.namespace], color: o.type },
    { tag: tags.operator,   color: o.operator },
    { tag: tags.punctuation, color: o.punctuation },
    { tag: [tags.definitionKeyword, tags.bool, tags.null, tags.atom], color: o.constant },
    { tag: tags.invalid, color: o.error },
  ]);
}

const highlightStyles = {
  light: makeHighlight({
    heading: '#1a1a1a', link: '#0366d6', code: '#d73a49', muted: '#999',
    keyword: '#d73a49', string: '#032f62', number: '#005cc5',
    comment: '#6a737d', type: '#6f42c1', operator: '#24292e',
    punctuation: '#999', constant: '#005cc5', error: '#f00',
  }),
  dark: makeHighlight({
    heading: '#e2e2e2', link: '#4ec9b0', code: '#ce9178', muted: '#666',
    keyword: '#569cd6', string: '#ce9178', number: '#b5cea8',
    comment: '#6a9955', type: '#4ec9b0', operator: '#d4d4d4',
    punctuation: '#555', constant: '#9cdcfe', error: '#f44747',
  }),
  catppuccin: makeHighlight({
    heading: '#cdd6f4', link: '#89dceb', code: '#f38ba8', muted: '#6c7086',
    keyword: '#cba6f7', string: '#a6e3a1', number: '#fab387',
    comment: '#6c7086', type: '#89b4fa', operator: '#cdd6f4',
    punctuation: '#6c7086', constant: '#fab387', error: '#f38ba8',
  }),
  nord: makeHighlight({
    heading: '#eceff4', link: '#88c0d0', code: '#bf616a', muted: '#616e88',
    keyword: '#81a1c1', string: '#a3be8c', number: '#b48ead',
    comment: '#616e88', type: '#88c0d0', operator: '#d8dee9',
    punctuation: '#4c566a', constant: '#b48ead', error: '#bf616a',
  }),
  tokyonight: makeHighlight({
    heading: '#c0caf5', link: '#7dcfff', code: '#f7768e', muted: '#3b4261',
    keyword: '#bb9af7', string: '#9ece6a', number: '#ff9e64',
    comment: '#565f89', type: '#7dcfff', operator: '#89ddff',
    punctuation: '#565f89', constant: '#ff9e64', error: '#f7768e',
  }),
  solarized: makeHighlight({
    heading: '#657b83', link: '#268bd2', code: '#dc322f', muted: '#93a1a1',
    keyword: '#859900', string: '#2aa198', number: '#d33682',
    comment: '#93a1a1', type: '#268bd2', operator: '#657b83',
    punctuation: '#93a1a1', constant: '#cb4b16', error: '#dc322f',
  }),
};

// ── Extensions ───────────────────────────────────────────────────────────────

function baseExtensions(onChange) {
  const exts = [
    history(),
    lineNumbers(),
    EditorView.lineWrapping,
    keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
    markdown(),
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
  const hlComp    = new Compartment();
  const resolvedTheme = theme ?? 'light';
  const state = EditorState.create({
    doc: '',
    extensions: [
      ...baseExtensions(onChange),
      themeComp.of(themes[resolvedTheme] ?? lightTheme),
      hlComp.of(syntaxHighlighting(highlightStyles[resolvedTheme] ?? highlightStyles.light)),
    ],
  });
  const view = new EditorView({ state, parent });

  return {
    getValue() { return view.state.doc.toString(); },
    setValue(text) {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } });
    },
    setTheme(name) {
      view.dispatch({ effects: [
        themeComp.reconfigure(themes[name] ?? lightTheme),
        hlComp.reconfigure(syntaxHighlighting(highlightStyles[name] ?? highlightStyles.light)),
      ]});
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
