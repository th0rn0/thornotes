/* Real tests for default-note-view module - no mirrors */
const { test } = require('node:test');
const assert = require('node:assert');
const {
  computeTargetView,
  shouldCloseSplit,
  shouldClosePreviewEdit,
  shouldOpenPreview,
  shouldRenderPreview,
  shouldClosePreview,
  shouldResetPreviewScroll,
} = require('./default-note-view.js');

test('computeTargetView returns editor for new notes', () => {
  assert.strictEqual(computeTargetView(true, 'preview'), 'editor');
  assert.strictEqual(computeTargetView(true, 'split'), 'editor');
});

test('computeTargetView returns stored view for existing notes', () => {
  assert.strictEqual(computeTargetView(false, 'preview'), 'preview');
  assert.strictEqual(computeTargetView(false, 'split'), 'split');
  assert.strictEqual(computeTargetView(false, 'editor'), 'editor');
});

test('computeTargetView defaults to editor when no stored view', () => {
  assert.strictEqual(computeTargetView(false, null), 'editor');
  assert.strictEqual(computeTargetView(false, undefined), 'editor');
});

test('shouldCloseSplit returns true when switching to preview and split is open', () => {
  assert.strictEqual(shouldCloseSplit('preview', true), true);
  assert.strictEqual(shouldCloseSplit('preview', false), false);
  assert.strictEqual(shouldCloseSplit('editor', true), false);
});

test('shouldClosePreviewEdit returns true when switching to preview and preview-edit is open', () => {
  assert.strictEqual(shouldClosePreviewEdit('preview', true), true);
  assert.strictEqual(shouldClosePreviewEdit('preview', false), false);
  assert.strictEqual(shouldClosePreviewEdit('editor', true), false);
});

test('shouldOpenPreview returns true when switching to preview and preview is closed', () => {
  assert.strictEqual(shouldOpenPreview('preview', false), true);
  assert.strictEqual(shouldOpenPreview('preview', true), false);
  assert.strictEqual(shouldOpenPreview('editor', false), false);
});

test('shouldRenderPreview returns true when switching to preview and preview is open', () => {
  assert.strictEqual(shouldRenderPreview('preview', true), true);
  assert.strictEqual(shouldRenderPreview('preview', false), false);
  assert.strictEqual(shouldRenderPreview('editor', true), false);
});

test('shouldClosePreview returns true when switching to editor and preview is open', () => {
  assert.strictEqual(shouldClosePreview('editor', true), true);
  assert.strictEqual(shouldClosePreview('editor', false), false);
  assert.strictEqual(shouldClosePreview('preview', true), false);
});

test('shouldResetPreviewScroll returns true when switching to preview and preview is open', () => {
  assert.strictEqual(shouldResetPreviewScroll('preview', true), true);
  assert.strictEqual(shouldResetPreviewScroll('preview', false), false);
  assert.strictEqual(shouldResetPreviewScroll('editor', true), false);
});
