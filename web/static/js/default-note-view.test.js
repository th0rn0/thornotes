/* Real tests for default-note-view module - no mirrors */
const { test } = require('node:test');
const assert = require('node:assert');
const {
  NOTE_VIEW_MODES,
  isValidView,
  computeTargetView,
  viewClasses,
  shouldRenderPreview,
  shouldResetPreviewScroll,
} = require('./default-note-view.js');

test('the four views are the four the switcher offers', () => {
  assert.deepStrictEqual(NOTE_VIEW_MODES, ['editor', 'split', 'preview', 'reading']);
});

test('isValidView accepts the four and nothing else', () => {
  for (const v of ['editor', 'split', 'preview', 'reading']) {
    assert.strictEqual(isValidView(v), true, v + ' must be a view');
  }
  for (const v of ['', null, undefined, 'Preview', 'previewedit', 'html', 0]) {
    assert.strictEqual(isValidView(v), false, JSON.stringify(v) + ' must not be a view');
  }
});

test('a new note always opens in the editor', () => {
  for (const stored of ['reading', 'preview', 'split', 'editor', null]) {
    assert.strictEqual(computeTargetView(true, stored), 'editor');
  }
});

test('an existing note opens in the stored view, including reading', () => {
  assert.strictEqual(computeTargetView(false, 'reading'), 'reading');
  assert.strictEqual(computeTargetView(false, 'preview'), 'preview');
  assert.strictEqual(computeTargetView(false, 'split'), 'split');
  assert.strictEqual(computeTargetView(false, 'editor'), 'editor');
});

test('a stored value that is not a view falls back to the editor', () => {
  for (const stored of [null, undefined, '', 'previewedit', 'PREVIEW', 'garbage']) {
    assert.strictEqual(computeTargetView(false, stored), 'editor', JSON.stringify(stored));
  }
});

test('viewClasses maps each view to the pane it shows', () => {
  assert.deepStrictEqual(viewClasses('editor'), {
    mode: 'editor',
    wrap: 'cm6-wrap',
    mountShown: true,
    previewShown: false,
    toolbarShown: true,
    reading: false,
  });
  assert.deepStrictEqual(viewClasses('split'), {
    mode: 'split',
    wrap: 'cm6-wrap split',
    mountShown: true,
    previewShown: true,
    toolbarShown: true,
    reading: false,
  });
  assert.deepStrictEqual(viewClasses('preview'), {
    mode: 'preview',
    wrap: 'cm6-wrap',
    mountShown: false,
    previewShown: true,
    toolbarShown: true,
    reading: false,
  });
  assert.deepStrictEqual(viewClasses('reading'), {
    mode: 'reading',
    wrap: 'cm6-wrap reading',
    mountShown: false,
    previewShown: true,
    toolbarShown: false,
    reading: true,
  });
});

test('reading hides the toolbar and the editor; preview keeps the toolbar', () => {
  const reading = viewClasses('reading');
  const preview = viewClasses('preview');
  assert.strictEqual(reading.mountShown, false);
  assert.strictEqual(reading.toolbarShown, false);
  assert.strictEqual(preview.mountShown, false);
  assert.strictEqual(preview.toolbarShown, true);
  assert.strictEqual(reading.reading, true);
  assert.strictEqual(preview.reading, false);
});

test('viewClasses refuses an unknown mode instead of inventing a pane', () => {
  const c = viewClasses('previewedit');
  assert.strictEqual(c.mode, 'editor');
  assert.strictEqual(c.mountShown, true);
  assert.strictEqual(c.previewShown, false);
  const u = viewClasses(undefined);
  assert.strictEqual(u.mode, 'editor');
});

test('only the views that render markdown need a render pass', () => {
  assert.strictEqual(shouldRenderPreview('preview'), true);
  assert.strictEqual(shouldRenderPreview('reading'), true);
  assert.strictEqual(shouldRenderPreview('split'), true);
  assert.strictEqual(shouldRenderPreview('editor'), false);
  assert.strictEqual(shouldRenderPreview('previewedit'), false);
});

test('the preview scroll resets when the reader arrives, not when editing', () => {
  assert.strictEqual(shouldResetPreviewScroll('preview'), true);
  assert.strictEqual(shouldResetPreviewScroll('reading'), true);
  assert.strictEqual(shouldResetPreviewScroll('split'), false);
  assert.strictEqual(shouldResetPreviewScroll('editor'), false);
});
