/* Default note view state machine - extracted from app.js
   Pure functions with no DOM dependencies. */
'use strict';

// The four views a note can be in. 'reading' is rendered markdown with the
// toolbar hidden and a 74ch measure — the same pane as preview, shown as
// something to read rather than something to glance at.
const NOTE_VIEW_MODES = ['editor', 'split', 'preview', 'reading'];

function isValidView(view) {
  return NOTE_VIEW_MODES.indexOf(view) !== -1;
}

// computeTargetView picks the view a note opens in. A new note always opens in
// the editor (a rendered empty note has nothing to read), and a stored value
// that is not one of the four — an old build's value, or a hand-edited
// localStorage — falls back to the editor rather than leaving the pane in a
// state nothing can explain.
function computeTargetView(isNew, storedView) {
  if (isNew) return 'editor';
  return isValidView(storedView) ? storedView : 'editor';
}

// viewClasses is the DOM the mode implies, as data: the wrapper's class, and
// whether the editor mount, the preview pane and the toolbar are shown. Kept
// pure so the mapping can be tested without rendering anything.
function viewClasses(view) {
  const mode = isValidView(view) ? view : 'editor';
  const rendered = mode === 'preview' || mode === 'reading';
  return {
    mode: mode,
    wrap: 'cm6-wrap' + (mode === 'split' ? ' split' : '') + (mode === 'reading' ? ' reading' : ''),
    mountShown: !rendered,
    previewShown: rendered || mode === 'split',
    toolbarShown: mode !== 'reading',
    reading: mode === 'reading',
  };
}

function shouldCloseSplit(targetView, editorSplitOpen) {
  return targetView !== 'split' && editorSplitOpen;
}

function shouldClosePreviewEdit(targetView, editorPreviewEditOpen) {
  return targetView !== 'previewedit' && editorPreviewEditOpen;
}

function shouldOpenPreview(targetView, editorPreviewOpen) {
  return targetView !== 'preview' && targetView !== 'split' && targetView !== 'previewedit' && editorPreviewOpen;
}

function shouldRenderPreview(targetView) {
  return targetView === 'preview' || targetView === 'split' || targetView === 'reading';
}

function shouldResetPreviewScroll(targetView) {
  return targetView === 'preview' || targetView === 'reading';
}

if (typeof module !== 'undefined' && module.exports) {
  module.exports = {
    NOTE_VIEW_MODES,
    isValidView,
    computeTargetView,
    viewClasses,
    shouldCloseSplit,
    shouldClosePreviewEdit,
    shouldOpenPreview,
    shouldRenderPreview,
    shouldResetPreviewScroll,
  };
}
