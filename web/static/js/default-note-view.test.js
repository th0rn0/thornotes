'use strict';
const { describe, test } = require('node:test');
const assert = require('node:assert/strict');

// Tests for the applyNoteOpenView logic.
//
// applyNoteOpenView(isNew, defaultNoteView) determines which editor mode to
// activate when a note is opened:
//   - isNew === true  → always editor mode (ignore setting)
//   - isNew === false → honour defaultNoteView ('editor' | 'preview')
//
// The function is tested via a self-contained state machine that mirrors the
// toggle functions and boolean flags in app.js.

function makeViewState({ initialPreview = false, initialSplit = false, initialPreviewEdit = false } = {}) {
  let editorPreviewOpen = initialPreview;
  let editorSplitOpen = initialSplit;
  let editorPreviewEditOpen = initialPreviewEdit;
  let previewRenderCount = 0;
  let editorFocusCount = 0;

  function renderPreviewContent() { previewRenderCount++; }

  function toggleEditorPreview() {
    editorPreviewOpen = !editorPreviewOpen;
    if (editorPreviewOpen) {
      renderPreviewContent();
    } else {
      editorFocusCount++;
    }
  }

  function toggleEditorSplit() {
    editorSplitOpen = !editorSplitOpen;
    if (!editorSplitOpen) editorFocusCount++;
  }

  function toggleEditorPreviewEdit() {
    editorPreviewEditOpen = !editorPreviewEditOpen;
    if (!editorPreviewEditOpen) editorFocusCount++;
  }

  // Mirror of applyNoteOpenView from app.js.
  function applyNoteOpenView(isNew, defaultNoteView) {
    const targetView = isNew ? 'editor' : (defaultNoteView || 'editor');
    if (targetView === 'preview') {
      if (editorSplitOpen) toggleEditorSplit();
      if (editorPreviewEditOpen) toggleEditorPreviewEdit();
      if (!editorPreviewOpen) {
        toggleEditorPreview();
      } else {
        // Preview already open — re-render with new note content.
        renderPreviewContent();
      }
    } else {
      if (editorPreviewOpen) toggleEditorPreview();
      if (editorSplitOpen) toggleEditorSplit();
      if (editorPreviewEditOpen) toggleEditorPreviewEdit();
    }
  }

  return {
    get editorPreviewOpen() { return editorPreviewOpen; },
    get editorSplitOpen() { return editorSplitOpen; },
    get editorPreviewEditOpen() { return editorPreviewEditOpen; },
    get previewRenderCount() { return previewRenderCount; },
    get editorFocusCount() { return editorFocusCount; },
    applyNoteOpenView,
  };
}

// ── New note always opens in editor mode ─────────────────────────────────────

describe('new note ignores defaultNoteView setting', () => {
  test('new note with default setting stays in editor mode', () => {
    const s = makeViewState();
    s.applyNoteOpenView(true, 'editor');
    assert.equal(s.editorPreviewOpen, false);
    assert.equal(s.editorSplitOpen, false);
    assert.equal(s.editorPreviewEditOpen, false);
  });

  test('new note with preview setting still opens in editor mode', () => {
    const s = makeViewState();
    s.applyNoteOpenView(true, 'preview');
    assert.equal(s.editorPreviewOpen, false, 'preview must not open for new note');
  });

  test('new note closes preview if it was already open', () => {
    const s = makeViewState({ initialPreview: true });
    assert.equal(s.editorPreviewOpen, true, 'precondition');
    s.applyNoteOpenView(true, 'preview');
    assert.equal(s.editorPreviewOpen, false, 'preview must be closed for new note');
  });

  test('new note closes split view if it was open', () => {
    const s = makeViewState({ initialSplit: true });
    s.applyNoteOpenView(true, 'preview');
    assert.equal(s.editorSplitOpen, false);
  });

  test('new note closes preview-edit if it was open', () => {
    const s = makeViewState({ initialPreviewEdit: true });
    s.applyNoteOpenView(true, 'preview');
    assert.equal(s.editorPreviewEditOpen, false);
  });
});

// ── Existing note respects defaultNoteView = 'editor' ────────────────────────

describe("existing note with defaultNoteView = 'editor'", () => {
  test('stays in editor mode when already in editor mode', () => {
    const s = makeViewState();
    s.applyNoteOpenView(false, 'editor');
    assert.equal(s.editorPreviewOpen, false);
    assert.equal(s.editorSplitOpen, false);
    assert.equal(s.editorPreviewEditOpen, false);
  });

  test('closes preview mode when switching to editor-default note', () => {
    const s = makeViewState({ initialPreview: true });
    s.applyNoteOpenView(false, 'editor');
    assert.equal(s.editorPreviewOpen, false, 'preview must close');
  });

  test('closes split mode when switching to editor-default note', () => {
    const s = makeViewState({ initialSplit: true });
    s.applyNoteOpenView(false, 'editor');
    assert.equal(s.editorSplitOpen, false);
  });

  test('closes preview-edit mode when switching to editor-default note', () => {
    const s = makeViewState({ initialPreviewEdit: true });
    s.applyNoteOpenView(false, 'editor');
    assert.equal(s.editorPreviewEditOpen, false);
  });
});

// ── Existing note respects defaultNoteView = 'preview' ───────────────────────

describe("existing note with defaultNoteView = 'preview'", () => {
  test('opens preview when editor was active', () => {
    const s = makeViewState();
    s.applyNoteOpenView(false, 'preview');
    assert.equal(s.editorPreviewOpen, true, 'preview must open');
    assert.equal(s.editorSplitOpen, false);
    assert.equal(s.editorPreviewEditOpen, false);
  });

  test('preview is rendered when opening in preview mode', () => {
    const s = makeViewState();
    s.applyNoteOpenView(false, 'preview');
    assert.ok(s.previewRenderCount > 0, 'preview must be rendered');
  });

  test('stays in preview and re-renders when already in preview mode', () => {
    const s = makeViewState({ initialPreview: true });
    const before = s.previewRenderCount;
    s.applyNoteOpenView(false, 'preview');
    assert.equal(s.editorPreviewOpen, true, 'preview must remain open');
    assert.ok(s.previewRenderCount > before, 'preview must re-render for new note content');
  });

  test('closes split before opening preview', () => {
    const s = makeViewState({ initialSplit: true });
    s.applyNoteOpenView(false, 'preview');
    assert.equal(s.editorSplitOpen, false, 'split must close');
    assert.equal(s.editorPreviewOpen, true, 'preview must open');
  });

  test('closes preview-edit before opening preview', () => {
    const s = makeViewState({ initialPreviewEdit: true });
    s.applyNoteOpenView(false, 'preview');
    assert.equal(s.editorPreviewEditOpen, false, 'preview-edit must close');
    assert.equal(s.editorPreviewOpen, true, 'preview must open');
  });
});

// ── Default fallback ─────────────────────────────────────────────────────────

describe('defaultNoteView fallback', () => {
  test('no saved setting defaults to editor mode', () => {
    const s = makeViewState({ initialPreview: true });
    s.applyNoteOpenView(false, null);
    assert.equal(s.editorPreviewOpen, false, 'must default to editor when no setting is saved');
  });

  test('empty string setting defaults to editor mode', () => {
    const s = makeViewState({ initialPreview: true });
    s.applyNoteOpenView(false, '');
    assert.equal(s.editorPreviewOpen, false);
  });
});

// ── Multiple consecutive note switches ───────────────────────────────────────

describe('multiple consecutive note opens', () => {
  test('each open applies the current setting consistently', () => {
    const s = makeViewState();

    // Open first note in preview default.
    s.applyNoteOpenView(false, 'preview');
    assert.equal(s.editorPreviewOpen, true, 'first note: preview');

    // Open second note (existing) — should stay in preview and re-render.
    const countAfterFirst = s.previewRenderCount;
    s.applyNoteOpenView(false, 'preview');
    assert.equal(s.editorPreviewOpen, true, 'second note: preview still open');
    assert.ok(s.previewRenderCount > countAfterFirst, 'second note: preview re-rendered');
  });

  test('new note resets to editor then existing note applies default', () => {
    const s = makeViewState({ initialPreview: true });

    // New note → must close preview.
    s.applyNoteOpenView(true, 'preview');
    assert.equal(s.editorPreviewOpen, false, 'new note: editor mode');

    // Next existing note with preview default → must open preview.
    s.applyNoteOpenView(false, 'preview');
    assert.equal(s.editorPreviewOpen, true, 'next existing note: preview');
  });
});
