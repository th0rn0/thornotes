/* Real tests for editor-scroll-reset module - no mirrors */
const { test } = require('node:test');
const assert = require('node:assert');
const {
  shouldResetScroll,
  computeScrollToTopState,
} = require('./editor-scroll-reset.js');

test('shouldResetScroll returns true for new notes', () => {
  assert.strictEqual(shouldResetScroll(true), true);
});

test('shouldResetScroll returns false for existing notes', () => {
  assert.strictEqual(shouldResetScroll(false), false);
});

test('computeScrollToTopState resets scrollTop to 0', () => {
  const editorState = {
    scrollDOM: { scrollTop: 100 },
    inputState: { lastScrollTop: 50, lastScrollLeft: 0 },
  };
  const result = computeScrollToTopState(editorState);
  assert.strictEqual(result.scrollDOM.scrollTop, 0);
});

test('computeScrollToTopState clears lastScrollTop', () => {
  const editorState = {
    scrollDOM: { scrollTop: 100 },
    inputState: { lastScrollTop: 50, lastScrollLeft: 0 },
  };
  const result = computeScrollToTopState(editorState);
  assert.strictEqual(result.inputState.lastScrollTop, 0);
});

test('computeScrollToTopState clears lastScrollLeft', () => {
  const editorState = {
    scrollDOM: { scrollTop: 100 },
    inputState: { lastScrollTop: 50, lastScrollLeft: 10 },
  };
  const result = computeScrollToTopState(editorState);
  assert.strictEqual(result.inputState.lastScrollLeft, 0);
});
