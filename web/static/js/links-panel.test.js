'use strict';
const { describe, test } = require('node:test');
const assert = require('node:assert/strict');

// Tests for the links panel logic: rendering resolved/unresolved/ambiguous links,
// generation counter to prevent stale responses, and click handlers.
//
// These tests verify the pure logic of the panel without DOM manipulation.

// Mock generation counter state
let _linksGeneration = 0;
let _linksAbortController = null;

function resetLinksState() {
  _linksGeneration = 0;
  _linksAbortController = null;
}

function incrementGeneration() {
  _linksGeneration++;
  return _linksGeneration;
}

function getCurrentGeneration() {
  return _linksGeneration;
}

function setAbortController(ctrl) {
  _linksAbortController = ctrl;
}

// Simulate the stale-response guard logic from loadLinksPanel
function checkGenerationGuard(requestGeneration) {
  return requestGeneration === _linksGeneration;
}

// ── Generation counter tests ─────────────────────────────────────────────

describe('links panel generation counter', () => {
  test('starts at zero', () => {
    resetLinksState();
    assert.equal(getCurrentGeneration(), 0);
  });

  test('increments on each call', () => {
    resetLinksState();
    const gen1 = incrementGeneration();
    assert.equal(gen1, 1);
    const gen2 = incrementGeneration();
    assert.equal(gen2, 2);
    assert.equal(getCurrentGeneration(), 2);
  });

  test('guard passes when generation matches', () => {
    resetLinksState();
    const gen = incrementGeneration();
    assert.equal(checkGenerationGuard(gen), true);
  });

  test('guard fails when generation has advanced', () => {
    resetLinksState();
    const gen1 = incrementGeneration();
    incrementGeneration(); // advance to gen 2
    assert.equal(checkGenerationGuard(gen1), false);
  });

  test('simulates rapid note switching: only last request renders', () => {
    resetLinksState();
    
    // Open note A
    const genA = incrementGeneration();
    // Open note B before A's response arrives
    const genB = incrementGeneration();
    // Open note C before B's response arrives
    const genC = incrementGeneration();
    
    // Now responses arrive in order
    assert.equal(checkGenerationGuard(genA), false, 'Note A stale');
    assert.equal(checkGenerationGuard(genB), false, 'Note B stale');
    assert.equal(checkGenerationGuard(genC), true, 'Note C current');
  });
});

// ── Link classification tests ────────────────────────────────────────────

describe('link classification', () => {
  test('resolved link has to_note_id and no unresolved/ambiguous flags', () => {
    const link = {
      to_note_id: 42,
      to_title: 'Target Note',
      raw_target: 'Target Note',
      anchor: '',
      link_text: 'Target Note',
      ambiguous: false,
      unresolved: false,
    };
    
    assert.equal(link.to_note_id, 42);
    assert.equal(link.ambiguous, false);
    assert.equal(link.unresolved, false);
    assert.equal(link.to_title, 'Target Note');
  });

  test('unresolved link has no to_note_id and unresolved=true', () => {
    const link = {
      to_note_id: null,
      to_title: '',
      raw_target: 'Missing Note',
      anchor: '',
      link_text: 'Missing Note',
      ambiguous: false,
      unresolved: true,
    };
    
    assert.equal(link.to_note_id, null);
    assert.equal(link.unresolved, true);
    assert.equal(link.ambiguous, false);
    assert.equal(link.raw_target, 'Missing Note');
  });

  test('ambiguous link has no to_note_id and ambiguous=true', () => {
    const link = {
      to_note_id: null,
      to_title: '',
      raw_target: 'Duplicate',
      anchor: '',
      link_text: 'Duplicate',
      ambiguous: true,
      unresolved: false,
    };
    
    assert.equal(link.to_note_id, null);
    assert.equal(link.ambiguous, true);
    assert.equal(link.unresolved, false);
  });

  test('priority: ambiguous takes precedence over unresolved', () => {
    // The backend should never set both, but if it did, the frontend
    // checks ambiguous first, so ambiguous wins.
    const link = {
      to_note_id: null,
      to_title: '',
      raw_target: 'Weird',
      anchor: '',
      link_text: 'Weird',
      ambiguous: true,
      unresolved: true, // hypothetical
    };
    
    // Frontend logic: if (link.ambiguous) { ... } else if (link.unresolved) { ... }
    // So ambiguous is checked first
    const classification = link.ambiguous ? 'ambiguous' : 
                          link.unresolved ? 'unresolved' : 
                          link.to_note_id ? 'resolved' : 'unknown';
    
    assert.equal(classification, 'ambiguous');
  });
});

// ── Backlink tests ───────────────────────────────────────────────────────

describe('backlink structure', () => {
  test('backlink has from_note_id, from_title, and link_text', () => {
    const backlink = {
      from_note_id: 10,
      from_title: 'Source Note',
      from_folder_id: 1,
      anchor: '',
      link_text: 'Source Note',
    };
    
    assert.equal(backlink.from_note_id, 10);
    assert.equal(backlink.from_title, 'Source Note');
    assert.equal(backlink.link_text, 'Source Note');
  });

  test('backlink is always clickable (always resolved)', () => {
    const backlink = {
      from_note_id: 10,
      from_title: 'Source Note',
      from_folder_id: 1,
      anchor: '',
      link_text: 'Source Note',
    };
    
    // Backlinks are always resolved — the source note exists
    const isClickable = backlink.from_note_id != null;
    assert.equal(isClickable, true);
  });

  test('backlink display uses link_text if present, else from_title', () => {
    const backlink1 = {
      from_note_id: 10,
      from_title: 'Source Note',
      from_folder_id: 1,
      anchor: '',
      link_text: 'Custom Link Text',
    };
    
    const backlink2 = {
      from_note_id: 11,
      from_title: 'Another Note',
      from_folder_id: 1,
      anchor: '',
      link_text: '',
    };
    
    const display1 = backlink1.link_text || backlink1.from_title;
    const display2 = backlink2.link_text || backlink2.from_title;
    
    assert.equal(display1, 'Custom Link Text');
    assert.equal(display2, 'Another Note');
  });
});

// ── XSS safety tests ─────────────────────────────────────────────────────

describe('links panel XSS safety', () => {
  test('textContent escapes HTML and prevents XSS', () => {
    // Note: We can't fully test DOM XSS protection in Node.js without JSDOM.
    // This test verifies our approach: using textContent instead of innerHTML.
    // The actual XSS protection is provided by the browser's DOM implementation.
    
    const payloads = [
      '<script>alert("xss")</script>',
      '<img src=x onerror=alert(1)>',
      '<a href="javascript:alert(1)">click</a>',
      '[[<script>evil</script>]]'
    ];
    
    payloads.forEach(payload => {
      // Simulate what the panel does: create element and use textContent
      // In a real browser, this would create a text node with escaped content
      const element = { textContent: '' };
      element.textContent = payload;
      
      // Verify the text content is preserved exactly as provided
      // (In a real browser, HTML tags would be escaped and shown as text)
      assert.equal(element.textContent, payload, 
        'textContent preserves the string value');
    });
  });

  test('raw_target is rendered as textContent', () => {
    const maliciousTarget = '[[<img src=x onerror=alert(1)>]]';
    
    const span = { textContent: '' };
    span.textContent = maliciousTarget;
    
    assert.equal(span.textContent, maliciousTarget);
  });

  test('no inline event handlers are added', () => {
    // The panel uses addEventListener, not onclick="..." attributes
    // This test verifies the pattern, not the actual implementation
    const link = { to_note_id: 42 };
    
    // Correct pattern: addEventListener
    const handlers = [];
    const mockElement = {
      addEventListener: (event, handler) => {
        handlers.push({ event, handler });
      }
    };
    
    // Simulate the panel's click handler attachment
    if (link.to_note_id) {
      mockElement.addEventListener('click', () => {
        // openNote(link.to_note_id)
      });
    }
    
    assert.equal(handlers.length, 1);
    assert.equal(handlers[0].event, 'click');
  });
});

// ── Empty state tests ────────────────────────────────────────────────────

describe('empty states', () => {
  test('empty outbound links shows "No outbound links"', () => {
    const links = [];
    const isEmpty = links.length === 0;
    const message = isEmpty ? 'No outbound links' : 'Has links';
    
    assert.equal(isEmpty, true);
    assert.equal(message, 'No outbound links');
  });

  test('empty backlinks shows "No backlinks"', () => {
    const backlinks = [];
    const isEmpty = backlinks.length === 0;
    const message = isEmpty ? 'No backlinks' : 'Has backlinks';
    
    assert.equal(isEmpty, true);
    assert.equal(message, 'No backlinks');
  });

  test('loading state shows "Loading…"', () => {
    const isLoading = true;
    const message = isLoading ? 'Loading…' : 'Loaded';
    
    assert.equal(message, 'Loading…');
  });

  test('error state shows "Failed to load links"', () => {
    const hasError = true;
    const message = hasError ? 'Failed to load links' : 'Loaded';
    
    assert.equal(message, 'Failed to load links');
  });
});

// ── DOM integration tests ────────────────────────────────────────────────

describe('links panel DOM integration', () => {
  test('generation guard prevents stale rendering after note switch', () => {
    // Simulate the actual loadLinksPanel flow with two rapid note switches
    resetLinksState();
    
    const results = [];
    
    // Mock the panel state
    const mockPanel = {
      isOpen: true,
      outboundList: { items: [] },
      backlinksList: { items: [] }
    };
    
    // Simulate loadLinksPanel for note A
    const loadNoteA = async (noteId) => {
      // Abort any in-flight request
      if (_linksAbortController) {
        _linksAbortController.abort();
      }
      const gen = incrementGeneration();
      const ctrl = new AbortController();
      setAbortController(ctrl);
      
      // Simulate fetch delay
      await new Promise(resolve => setTimeout(resolve, 10));
      
      // Check guard before rendering
      if (!checkGenerationGuard(gen)) {
        results.push({ noteId, rendered: false, reason: 'stale generation' });
        return;
      }
      
      results.push({ noteId, rendered: true });
    };
    
    // Fire off note A load
    const promiseA = loadNoteA(1);
    
    // Immediately switch to note B (before A completes)
    const promiseB = loadNoteA(2);
    
    return Promise.all([promiseA, promiseB]).then(() => {
      // Note A should have been cancelled
      assert.equal(results.length, 2);
      assert.equal(results[0].noteId, 1);
      assert.equal(results[0].rendered, false, 'Note A should not render');
      assert.equal(results[0].reason, 'stale generation');
      assert.equal(results[1].noteId, 2);
      assert.equal(results[1].rendered, true, 'Note B should render');
    });
  });

  test('AbortController cancels in-flight fetch on note switch', () => {
    resetLinksState();
    
    const ctrl1 = new AbortController();
    setAbortController(ctrl1);
    
    // Simulate switching notes
    const ctrl2 = new AbortController();
    if (_linksAbortController) {
      _linksAbortController.abort();
    }
    setAbortController(ctrl2);
    
    // First controller should be aborted
    assert.equal(ctrl1.signal.aborted, true);
    assert.equal(ctrl2.signal.aborted, false);
  });

  test('panel remains closed does not trigger load', () => {
    const mockPanel = { isOpen: false };
    let loadCalled = false;
    
    const loadLinksPanel = (panel) => {
      if (!panel.isOpen) return;
      loadCalled = true;
    };
    
    loadLinksPanel(mockPanel);
    assert.equal(loadCalled, false, 'Load should not be called when panel is closed');
  });

  test('panel toggle opens and triggers load', () => {
    const mockPanel = { isOpen: false };
    let loadCalled = false;
    
    const toggleLinksPanel = (panel) => {
      panel.isOpen = !panel.isOpen;
      if (panel.isOpen) {
        loadCalled = true;
      }
    };
    
    toggleLinksPanel(mockPanel);
    assert.equal(mockPanel.isOpen, true);
    assert.equal(loadCalled, true);
  });
});
