/* Editor scroll reset state machine - extracted from app.js
   Pure functions with no DOM dependencies. */
'use strict';

function shouldResetScroll(isNewNote) {
  return isNewNote;
}

function shouldClearLastScroll(scrollTopBefore, scrollTopAfter) {
  // Must clear lastScrollTop when scrollTop becomes 0 to prevent CM6's
  // focus handler from restoring the old scroll position
  return scrollTopBefore !== 0 && scrollTopAfter === 0;
}

function computeScrollToTopState(editorState) {
  const scrollTopBefore = editorState.scrollDOM?.scrollTop || 0;
  const scrollTopAfter = 0;
  
  return {
    ...editorState,
    scrollDOM: {
      ...editorState.scrollDOM,
      scrollTop: 0,
    },
    inputState: shouldClearLastScroll(scrollTopBefore, scrollTopAfter) ? {
      ...editorState.inputState,
      lastScrollTop: 0,
      lastScrollLeft: 0,
    } : editorState.inputState,
  };
}

if (typeof module !== 'undefined' && module.exports) {
  module.exports = {
    shouldResetScroll,
    shouldClearLastScroll,
    computeScrollToTopState,
  };
}
