/* Default note view state machine - extracted from app.js
   Pure functions with no DOM dependencies. */
'use strict';

function computeTargetView(isNew, storedView) {
  return isNew ? 'editor' : (storedView || 'editor');
}

function shouldCloseSplit(targetView, editorSplitOpen) {
  return targetView === 'preview' && editorSplitOpen;
}

function shouldClosePreviewEdit(targetView, editorPreviewEditOpen) {
  return targetView === 'preview' && editorPreviewEditOpen;
}

function shouldOpenPreview(targetView, editorPreviewOpen) {
  return targetView === 'preview' && !editorPreviewOpen;
}

function shouldRenderPreview(targetView, editorPreviewOpen) {
  return targetView === 'preview' && editorPreviewOpen;
}

function shouldClosePreview(targetView, editorPreviewOpen) {
  return targetView === 'editor' && editorPreviewOpen;
}

function shouldResetPreviewScroll(targetView, editorPreviewOpen) {
  return targetView === 'preview' && editorPreviewOpen;
}

if (typeof module !== 'undefined' && module.exports) {
  module.exports = {
    computeTargetView,
    shouldCloseSplit,
    shouldClosePreviewEdit,
    shouldOpenPreview,
    shouldRenderPreview,
    shouldClosePreview,
    shouldResetPreviewScroll,
  };
}
