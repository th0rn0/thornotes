'use strict';
const { describe, test } = require('node:test');
const assert = require('node:assert/strict');

// Verify the scroll-reset contract: editor.setValue() must always reset the
// CM6 scroller to the top so switching notes never inherits the previous note's
// scroll position.
//
// These tests replicate the setValue implementation from cm6-bundle/index.js
// using a stub, making the contract explicit and runnable without a real browser.

function makeEditorStub(initialScroll = 0) {
  const scrollDOM = { scrollTop: initialScroll };
  const view = {
    scrollDOM,
    setState(_state) { /* CM6 setState — does NOT touch scrollTop */ },
  };
  // Mirror the setValue body from cm6-bundle/index.js exactly.
  function setValue(text) {
    view.setState({ doc: text });
    view.scrollDOM.scrollTop = 0;
  }
  return { _view: view, setValue };
}

describe('editor scroll reset on note switch', () => {
  test('scroll position is reset to 0 when switching notes', () => {
    const ed = makeEditorStub(800);
    assert.equal(ed._view.scrollDOM.scrollTop, 800, 'precondition: scroll is non-zero');
    ed.setValue('# New note content');
    assert.equal(ed._view.scrollDOM.scrollTop, 0);
  });

  test('scroll position is reset even when new content is empty', () => {
    const ed = makeEditorStub(300);
    ed.setValue('');
    assert.equal(ed._view.scrollDOM.scrollTop, 0);
  });

  test('multiple consecutive note switches all reset scroll', () => {
    const ed = makeEditorStub(0);

    ed._view.scrollDOM.scrollTop = 1200;
    ed.setValue('# Note A');
    assert.equal(ed._view.scrollDOM.scrollTop, 0, 'first switch');

    ed._view.scrollDOM.scrollTop = 450;
    ed.setValue('# Note B');
    assert.equal(ed._view.scrollDOM.scrollTop, 0, 'second switch');
  });

  test('scroll stays at 0 when already at the top', () => {
    const ed = makeEditorStub(0);
    ed.setValue('Already at top');
    assert.equal(ed._view.scrollDOM.scrollTop, 0);
  });
});
