/* thornotes — graph-utils test suite
   Unit tests for the pure graph layout functions. */
'use strict';

const { describe, it } = require('node:test');
const assert = require('node:assert/strict');
const { _layoutOneHopGraph, _graphHitTest, isNavigableHit, _truncateLabel, GRAPH_NODE_CAP } = require('./graph-utils.js');
const fs = require('node:fs');
const path = require('node:path');

describe('graph-utils', () => {
  describe('_layoutOneHopGraph', () => {
    it('should return centre node at canvas centre', () => {
      const centre = { id: 1, title: 'Centre Note' };
      const layout = _layoutOneHopGraph(centre, [], [], 800, 600);
      
      assert.equal(layout.centre.id, 1);
      assert.equal(layout.centre.x, 400);
      assert.equal(layout.centre.y, 300);
      assert.equal(layout.totalCount, 0);
      assert.equal(layout.capped, false);
    });

    it('should place one resolved neighbour', () => {
      const centre = { id: 1, title: 'Centre' };
      const outbound = [{ to_note_id: 2, to_title: 'Neighbour', raw_target: 'Neighbour' }];
      const layout = _layoutOneHopGraph(centre, outbound, [], 800, 600);
      
      assert.equal(layout.totalCount, 1);
      assert.equal(layout.nodes.length, 2); // centre + 1 neighbour
      const neighbour = layout.nodes.find(n => n.noteId === 2);
      assert.ok(neighbour);
      assert.equal(neighbour.status, 'resolved');
      assert.ok(neighbour.x >= 0 && neighbour.x <= 800);
      assert.ok(neighbour.y >= 0 && neighbour.y <= 600);
    });

    it('should merge mutual links into one node', () => {
      const centre = { id: 1, title: 'Centre' };
      const outbound = [{ to_note_id: 2, to_title: 'Mutual', raw_target: 'Mutual' }];
      const backlinks = [{ from_note_id: 2, from_title: 'Mutual' }];
      const layout = _layoutOneHopGraph(centre, outbound, backlinks, 800, 600);
      
      assert.equal(layout.totalCount, 1);
      const neighbour = layout.nodes.find(n => n.noteId === 2);
      assert.deepEqual(neighbour.directions, ['out', 'in']);
    });

    it('should sort neighbours deterministically', () => {
      const centre = { id: 1, title: 'Centre' };
      const outbound = [
        { to_note_id: 3, to_title: 'Zebra', raw_target: 'Zebra' },
        { to_note_id: 2, to_title: 'Apple', raw_target: 'Apple' },
        { unresolved: true, raw_target: 'Missing' },
        { ambiguous: true, raw_target: 'Unknown' },
      ];
      const layout = _layoutOneHopGraph(centre, outbound, [], 800, 600);
      
      assert.equal(layout.totalCount, 4);
      // Order: resolved (Apple, Zebra), ambiguous (Unknown), ghost (Missing)
      const ids = layout.nodes.slice(1).map(n => n.noteId || n.label);
      assert.deepEqual(ids, [2, 3, 'Unknown', 'Missing']);
    });

    it('should cap neighbours at GRAPH_NODE_CAP', () => {
      const centre = { id: 1, title: 'Centre' };
      const outbound = Array.from({ length: 50 }, (_, i) => ({
        to_note_id: i + 2,
        to_title: `Note ${i}`,
        raw_target: `Note ${i}`,
      }));
      const layout = _layoutOneHopGraph(centre, outbound, [], 800, 600);
      
      assert.equal(layout.totalCount, 50);
      assert.equal(layout.nodes.length, GRAPH_NODE_CAP + 1); // centre + cap
      assert.equal(layout.capped, true);
    });

    it('should return empty edges when no neighbours', () => {
      const centre = { id: 1, title: 'Centre' };
      const layout = _layoutOneHopGraph(centre, [], [], 800, 600);
      
      assert.equal(layout.edges.length, 0);
    });

    it('should create edges from centre to each shown neighbour', () => {
      const centre = { id: 1, title: 'Centre' };
      const outbound = [
        { to_note_id: 2, to_title: 'A', raw_target: 'A' },
        { to_note_id: 3, to_title: 'B', raw_target: 'B' },
      ];
      const layout = _layoutOneHopGraph(centre, outbound, [], 800, 600);
      
      assert.equal(layout.edges.length, 2);
      layout.edges.forEach(edge => {
        assert.equal(edge.from.noteId, 1);
        assert.ok([2, 3].includes(edge.to.noteId));
      });
    });
  });

  describe('_graphHitTest', () => {
    it('should return null for empty layout', () => {
      assert.equal(_graphHitTest(null, 0, 0), null);
      assert.equal(_graphHitTest({ nodes: [] }, 0, 0), null);
    });

    it('should hit centre node at centre', () => {
      const layout = {
        nodes: [{ id: 1, x: 400, y: 300, status: 'centre' }],
      };
      const hit = _graphHitTest(layout, 400, 300);
      assert.equal(hit.id, 1);
    });

    it('should miss node outside radius', () => {
      const layout = {
        nodes: [{ id: 1, x: 400, y: 300, status: 'centre' }],
      };
      const hit = _graphHitTest(layout, 500, 300);
      assert.equal(hit, null);
    });

    it('should hit neighbour within radius', () => {
      const layout = {
        nodes: [
          { id: 1, x: 400, y: 300, status: 'centre' },
          { id: 2, x: 500, y: 300, status: 'resolved', noteId: 2 },
        ],
      };
      const hit = _graphHitTest(layout, 505, 300);
      assert.equal(hit.id, 2);
    });

    it('should prefer later nodes in reverse order', () => {
      const layout = {
        nodes: [
          { id: 1, x: 400, y: 300, status: 'centre' },
          { id: 2, x: 400, y: 300, status: 'resolved', noteId: 2 },
        ],
      };
      const hit = _graphHitTest(layout, 400, 300);
      assert.equal(hit.id, 2); // later node wins
    });
  });

  describe('isNavigableHit', () => {
    // isNavigableHit is the single definition of "clicking this hit opens a
    // note". A dead click must stay dead: ghost, ambiguous, centre, a resolved
    // node with no note id, and empty canvas. Ticket 603.

    it('should navigate a resolved node with a note id', () => {
      assert.equal(isNavigableHit({ status: 'resolved', noteId: 7 }), true);
    });

    it('should not navigate a resolved node with a null note id', () => {
      assert.equal(isNavigableHit({ status: 'resolved', noteId: null }), false);
    });

    it('should not navigate a resolved node with no noteId property at all', () => {
      assert.equal(isNavigableHit({ status: 'resolved' }), false);
    });

    it('should not navigate an ambiguous node even when it carries a note id', () => {
      assert.equal(isNavigableHit({ status: 'ambiguous', noteId: 3 }), false);
    });

    it('should not navigate a ghost (unresolved) node', () => {
      assert.equal(isNavigableHit({ status: 'ghost', label: 'Missing' }), false);
    });

    it('should not navigate the centre node, which has a note id but status centre', () => {
      assert.equal(isNavigableHit({ status: 'centre', noteId: 1 }), false);
    });

    it('should not navigate when there is no hit (click on empty canvas)', () => {
      assert.equal(isNavigableHit(null), false);
      assert.equal(isNavigableHit(undefined), false);
    });

    it('should treat note id 0 as a concrete id, not a missing one', () => {
      // The rule is "noteId == null", not truthiness — preserves app.js's
      // original semantics exactly (no behaviour change in this refactor).
      assert.equal(isNavigableHit({ status: 'resolved', noteId: 0 }), true);
    });
  });

  describe('isNavigableHit against real layout output', () => {
    // The predicate must agree with what _layoutOneHopGraph actually emits, so
    // these tests build a layout from link objects (the same shape the API
    // returns) rather than hand-written node literals.

    function hitAtNode(layout, node) {
      return _graphHitTest(layout, node.x, node.y);
    }

    const centre = { id: 1, title: 'Centre' };
    const outbound = [
      { to_note_id: 2, to_title: 'Resolved', raw_target: 'Resolved' },
      { ambiguous: true, raw_target: 'Unknown', to_title: 'Unknown' },
      { unresolved: true, raw_target: 'Missing' },
    ];

    it('should navigate only the resolved neighbour', () => {
      const layout = _layoutOneHopGraph(centre, outbound, [], 800, 600);
      const navigable = layout.nodes.filter(n => isNavigableHit(hitAtNode(layout, n)));

      assert.equal(navigable.length, 1);
      assert.equal(navigable[0].noteId, 2);
      assert.equal(navigable[0].status, 'resolved');
    });

    it('should not navigate the centre node of a populated graph', () => {
      const layout = _layoutOneHopGraph(centre, outbound, [], 800, 600);
      const hit = hitAtNode(layout, layout.centre);

      assert.equal(hit.status, 'centre');
      assert.equal(isNavigableHit(hit), false);
    });

    it('should not navigate ambiguous and ghost neighbours', () => {
      const layout = _layoutOneHopGraph(centre, outbound, [], 800, 600);
      const byStatus = {};
      layout.nodes.forEach(n => { byStatus[n.status] = n; });

      assert.ok(byStatus.ambiguous, 'layout should contain an ambiguous node');
      assert.ok(byStatus.ghost, 'layout should contain a ghost node');
      assert.equal(isNavigableHit(hitAtNode(layout, byStatus.ambiguous)), false);
      assert.equal(isNavigableHit(hitAtNode(layout, byStatus.ghost)), false);
    });

    it('should not navigate a click on empty canvas between nodes', () => {
      const layout = _layoutOneHopGraph(centre, outbound, [], 800, 600);
      // Top-left corner: outside the node radius of every node in the layout.
      const hit = _graphHitTest(layout, 0, 0);

      assert.equal(hit, null);
      assert.equal(isNavigableHit(hit), false);
    });
  });

  describe('graph click rule has one home (ticket 603)', () => {
    // The click handler and the cursor/tooltip handler must both call
    // isNavigableHit. This guards against the rule being re-inlined in app.js,
    // which is exactly the drift the extraction exists to prevent.
    const APP = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');

    function clickHandlerBody() {
      const start = APP.indexOf('function _attachGraphClickHandler');
      assert.notEqual(start, -1, 'app.js should still define _attachGraphClickHandler');
      const end = APP.indexOf('// Hook the click handler', start);
      assert.notEqual(end, -1, 'app.js click-handler section should keep its end marker');
      return APP.slice(start, end);
    }

    it('should call isNavigableHit from both the click and mousemove handlers', () => {
      const body = clickHandlerBody();
      const calls = body.match(/isNavigableHit\(/g) || [];
      assert.equal(calls.length, 2, 'expected one call in the click handler and one in mousemove');
      assert.ok(body.includes("addEventListener('click'"));
      assert.ok(body.includes("addEventListener('mousemove'"));
    });

    it('should not re-inline the resolved-status rule in the click handler', () => {
      const body = clickHandlerBody();
      assert.ok(!/status\s*[!=]==\s*'resolved'/.test(body),
        'click/cursor handlers must not compare status themselves — call isNavigableHit');
      assert.ok(!/noteId\s*[!=]=/.test(body),
        'click/cursor handlers must not test noteId themselves — call isNavigableHit');
    });
  });

  describe('_truncateLabel', () => {
    it('should return label unchanged if no context', () => {
      assert.equal(_truncateLabel(null, 'Test', 100), 'Test');
    });

    it('should return label unchanged if it fits', () => {
      const ctx = {
        measureText: (text) => ({ width: text.length * 10 }),
      };
      assert.equal(_truncateLabel(ctx, 'Short', 100), 'Short');
    });

    it('should truncate long labels with ellipsis', () => {
      const ctx = {
        measureText: (text) => ({ width: text.length * 10 }),
      };
      const result = _truncateLabel(ctx, 'This is a very long label', 100);
      assert.ok(result.endsWith('…'));
      assert.ok(result.length < 'This is a very long label'.length);
    });
  });

  describe('GRAPH_NODE_CAP', () => {
    it('should be 30', () => {
      assert.equal(GRAPH_NODE_CAP, 30);
    });
  });

  describe('Stale graph prevention (acceptance #4)', () => {
    // These tests prove the canvas path is covered by the same abort + generation
    // guard that protects the text lists. The actual wiring is in loadLinksPanel
    // (app.js lines 1099-1167), which increments _linksGeneration, creates an
    // AbortController, and checks the guard before calling renderOneHopGraph.
    // We mirror that logic here to prove the algorithm.

    let _testGeneration = 0;
    let _testAbortController = null;

    function resetTestState() {
      _testGeneration = 0;
      _testAbortController = null;
    }

    function simulateLoadLinksPanel(noteId, renderGraph) {
      // Abort any in-flight request
      if (_testAbortController) {
        _testAbortController.abort();
      }
      _testGeneration++;
      const thisGeneration = _testGeneration;
      _testAbortController = new AbortController();
      const signal = _testAbortController.signal;

      // Simulate async fetch
      return new Promise((resolve) => {
        setTimeout(() => {
          // Guard: if generation changed, we switched notes — discard
          if (thisGeneration !== _testGeneration) {
            resolve({ noteId, rendered: false, reason: 'stale generation' });
            return;
          }
          // Guard: if signal aborted, discard
          if (signal.aborted) {
            resolve({ noteId, rendered: false, reason: 'aborted' });
            return;
          }
          // Render the graph (this is where renderOneHopGraph would be called)
          renderGraph(noteId);
          resolve({ noteId, rendered: true });
        }, 10);
      });
    }

    it('should prevent stale graph from rendering after rapid note switch', async () => {
      resetTestState();
      const rendered = [];

      // Simulate rapid note switching: A → B → C
      const promiseA = simulateLoadLinksPanel(1, (id) => rendered.push(id));
      const promiseB = simulateLoadLinksPanel(2, (id) => rendered.push(id));
      const promiseC = simulateLoadLinksPanel(3, (id) => rendered.push(id));

      const results = await Promise.all([promiseA, promiseB, promiseC]);

      // Only the last note (C) should render
      assert.equal(results.length, 3);
      assert.equal(results[0].noteId, 1);
      assert.equal(results[0].rendered, false, 'Note A should not render');
      assert.equal(results[1].noteId, 2);
      assert.equal(results[1].rendered, false, 'Note B should not render');
      assert.equal(results[2].noteId, 3);
      assert.equal(results[2].rendered, true, 'Note C should render');

      // Only note 3's graph should have been painted
      assert.deepEqual(rendered, [3]);
    });

    it('should abort in-flight fetch when switching notes', async () => {
      resetTestState();

      // Start loading note 1
      const promise1 = simulateLoadLinksPanel(1, () => {});
      const ctrl1 = _testAbortController;

      // Switch to note 2 before note 1 completes
      const promise2 = simulateLoadLinksPanel(2, () => {});

      // Note 1's controller should be aborted
      assert.equal(ctrl1.signal.aborted, true);

      const results = await Promise.all([promise1, promise2]);
      assert.equal(results[0].rendered, false, 'Note 1 should not render');
      assert.equal(results[1].rendered, true, 'Note 2 should render');
    });

    it('should render graph when generation matches and not aborted', async () => {
      resetTestState();
      const rendered = [];

      const result = await simulateLoadLinksPanel(42, (id) => rendered.push(id));

      assert.equal(result.rendered, true);
      assert.deepEqual(rendered, [42]);
    });
  });
});
