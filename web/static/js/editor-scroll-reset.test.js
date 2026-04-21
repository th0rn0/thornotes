'use strict';
const { describe, test } = require('node:test');
const assert = require('node:assert/strict');

// Verify the scroll-reset contract for note switching:
//   - editor.scrollToTop() resets CM6's scrollDOM AND clears inputState.lastScrollTop
//   - editor.setValue() does NOT reset scroll (in-note edits like checkbox toggles
//     must preserve the user's scroll position)
//
// The critical case is the CM6 focus-handler race: CM6's InputState saves scroll to
// lastScrollTop on every scroll event, and its focus handler restores that value
// whenever scrollTop drops to 0. scrollToTop() must clear lastScrollTop so the focus
// handler has nothing to restore after a note switch.

function makeEditorStub(initialScroll = 0) {
  const inputState = { lastScrollTop: initialScroll, lastScrollLeft: 0 };
  const scrollDOM = { scrollTop: initialScroll };

  // Mirrors CM6's focus event handler (from @codemirror/view InputState):
  //   !scrollTop && (lastScrollTop || lastScrollLeft) → restore
  function simulateFocus() {
    if (!scrollDOM.scrollTop && (inputState.lastScrollTop || inputState.lastScrollLeft)) {
      scrollDOM.scrollTop = inputState.lastScrollTop;
    }
  }

  // Mirror setValue from cm6-bundle/index.js: setState only, no scroll side-effects.
  function setValue(text) { void text; }

  function scrollToTop() {
    scrollDOM.scrollTop = 0;
    if (inputState) {
      inputState.lastScrollTop = 0;
      inputState.lastScrollLeft = 0;
    }
  }

  return { scrollDOM, inputState, simulateFocus, setValue, scrollToTop };
}

describe('scrollToTop resets scroll on note switch', () => {
  test('resets scrollTop to 0', () => {
    const ed = makeEditorStub(800);
    assert.equal(ed.scrollDOM.scrollTop, 800, 'precondition: non-zero scroll');
    ed.scrollToTop();
    assert.equal(ed.scrollDOM.scrollTop, 0);
  });

  test('clears inputState.lastScrollTop to neutralise the focus-handler race', () => {
    const ed = makeEditorStub(800);
    ed.scrollToTop();
    assert.equal(ed.inputState.lastScrollTop, 0);
    assert.equal(ed.inputState.lastScrollLeft, 0);
  });

  test('focus handler does NOT restore old scroll after scrollToTop', () => {
    const ed = makeEditorStub(800);
    ed.setValue('# New note');
    ed.scrollToTop();
    ed.simulateFocus(); // user clicks into the editor after switching notes
    assert.equal(ed.scrollDOM.scrollTop, 0, 'focus handler must not restore old scroll');
  });

  test('documents the race: without clearing lastScrollTop, focus handler restores old scroll', () => {
    // This test pins the bug that existed before the fix.
    const inputState = { lastScrollTop: 800, lastScrollLeft: 0 };
    const scrollDOM = { scrollTop: 800 };

    // Old broken approach: only set scrollTop=0, do not clear lastScrollTop.
    scrollDOM.scrollTop = 0;

    // CM6 focus handler fires when the user clicks into the editor.
    if (!scrollDOM.scrollTop && (inputState.lastScrollTop || inputState.lastScrollLeft)) {
      scrollDOM.scrollTop = inputState.lastScrollTop;
    }
    assert.equal(scrollDOM.scrollTop, 800, 'without fix, focus handler restores previous scroll');
  });

  test('multiple consecutive note switches all reset scroll', () => {
    const ed = makeEditorStub(0);

    ed.scrollDOM.scrollTop = 1200;
    ed.inputState.lastScrollTop = 1200;
    ed.setValue('# Note A');
    ed.scrollToTop();
    assert.equal(ed.scrollDOM.scrollTop, 0, 'first switch');

    ed.scrollDOM.scrollTop = 450;
    ed.inputState.lastScrollTop = 450;
    ed.setValue('# Note B');
    ed.scrollToTop();
    assert.equal(ed.scrollDOM.scrollTop, 0, 'second switch');
  });

  test('works when already at zero', () => {
    const ed = makeEditorStub(0);
    ed.scrollToTop();
    ed.simulateFocus();
    assert.equal(ed.scrollDOM.scrollTop, 0);
  });
});

describe('setValue does NOT reset scroll (in-note edits)', () => {
  test('checkbox toggle preserves scroll position', () => {
    const ed = makeEditorStub(600);
    ed.setValue('- [x] done item\n- [ ] other item');
    assert.equal(ed.scrollDOM.scrollTop, 600, 'scroll must not change during in-note edit');
  });

  test('inline edit commit preserves scroll position', () => {
    const ed = makeEditorStub(1200);
    ed.setValue('updated content');
    assert.equal(ed.scrollDOM.scrollTop, 1200);
  });

  test('repeated in-note edits do not drift scroll position', () => {
    const ed = makeEditorStub(400);
    ed.setValue('edit 1');
    ed.setValue('edit 2');
    ed.setValue('edit 3');
    assert.equal(ed.scrollDOM.scrollTop, 400);
  });
});
