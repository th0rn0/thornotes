/* thornotes — graph layout utilities
   Pure functions; no DOM dependencies so they run under Node for testing. */
'use strict';

// Graph node cap: maximum number of neighbours to display before truncation.
const GRAPH_NODE_CAP = 30;

/**
 * _layoutOneHopGraph: pure layout for a one-hop neighbourhood.
 *
 * @param {Object} centre - { id, title }
 * @param {Array} outbound - array of outgoing link objects from API
 * @param {Array} backlinks - array of backlink objects from API
 * @param {number} width - canvas width in CSS pixels
 * @param {number} height - canvas height in CSS pixels
 * @param {number} [cap] - max neighbours to show (default: GRAPH_NODE_CAP)
 * @returns {Object} layout object with { nodes, edges, shownCount, totalCount, capped }
 *
 * Returns a deterministic layout: same inputs → same outputs every time.
 * No Math.random(), no Date.now(), no side effects.
 */
function _layoutOneHopGraph(centre, outbound, backlinks, width, height, cap) {
  if (cap === undefined) cap = GRAPH_NODE_CAP;
  const cx = width / 2;
  const cy = height / 2;

  const neighbours = new Map();
  const ensure = (key, seed) => {
    let n = neighbours.get(key);
    if (!n) { n = Object.assign({ directions: [] }, seed); neighbours.set(key, n); }
    return n;
  };

  (outbound || []).forEach(link => {
    let key, status, label, noteId = null;
    if (link.ambiguous) {
      key = 'amb:' + link.raw_target;
      status = 'ambiguous';
      label = link.to_title || link.raw_target;
    } else if (link.unresolved) {
      key = 'ghost:' + link.raw_target;
      status = 'ghost';
      label = link.raw_target;
    } else if (link.to_note_id != null) {
      key = 'n:' + link.to_note_id;
      status = 'resolved';
      label = link.to_title || link.raw_target;
      noteId = link.to_note_id;
    } else {
      return;
    }
    const n = ensure(key, { key, status, label, noteId });
    if (n.directions.indexOf('out') === -1) n.directions.push('out');
  });

  (backlinks || []).forEach(bl => {
    if (!bl || bl.from_note_id == null) return;
    const key = 'n:' + bl.from_note_id;
    const n = ensure(key, {
      key, status: 'resolved',
      label: bl.from_title || '', noteId: bl.from_note_id,
    });
    if (n.directions.indexOf('in') === -1) n.directions.push('in');
  });

  const centreNode = {
    id: centre.id, key: 'n:' + centre.id, status: 'centre',
    label: centre.title || '', noteId: centre.id,
    directions: [], x: cx, y: cy,
  };

  const statusRank = { resolved: 0, ambiguous: 1, ghost: 2 };
  const all = Array.from(neighbours.values());
  all.sort((a, b) => {
    const sa = statusRank[a.status] ?? 9;
    const sb = statusRank[b.status] ?? 9;
    if (sa !== sb) return sa - sb;
    const la = (a.label || '').toLowerCase();
    const lb = (b.label || '').toLowerCase();
    if (la < lb) return -1;
    if (la > lb) return 1;
    const da = a.directions.join('');
    const db = b.directions.join('');
    return da < db ? -1 : da > db ? 1 : 0;
  });

  const totalCount = all.length;
  const shown = all.slice(0, cap);
  const capped = totalCount > shown.length;

  const rx = Math.max(40, width / 2 - 70);
  const ry = Math.max(30, height / 2 - 30);
  const n = shown.length;
  shown.forEach((node, i) => {
    const angle = -Math.PI / 2 + (2 * Math.PI * i) / Math.max(n, 1);
    node.x = cx + rx * Math.cos(angle);
    node.y = cy + ry * Math.sin(angle);
  });

  const nodes = [centreNode].concat(shown);
  const edges = shown.map(node => ({
    from: centreNode, to: node,
    bidirectional: node.directions.indexOf('out') !== -1 &&
                   node.directions.indexOf('in') !== -1,
  }));

  return {
    centre: centreNode,
    nodes: nodes,
    edges: edges,
    shownCount: shown.length,
    totalCount: totalCount,
    capped: capped,
    width: width,
    height: height,
  };
}

/**
 * _graphHitTest: pure hit-testing against a layout.
 *
 * @param {Object} layout - layout object from _layoutOneHopGraph
 * @param {number} x - mouse x in CSS pixels
 * @param {number} y - mouse y in CSS pixels
 * @returns {Object|null} the node at (x, y) or null
 *
 * No DOM, no side effects.
 */
function _graphHitTest(layout, x, y) {
  if (!layout || !layout.nodes) return null;
  const r = 14; // matches the drawn node radius in renderOneHopGraph
  for (let i = layout.nodes.length - 1; i >= 0; i--) {
    const n = layout.nodes[i];
    const dx = x - n.x;
    const dy = y - n.y;
    if (dx * dx + dy * dy <= r * r) return n;
  }
  return null;
}

/**
 * _truncateLabel: truncate a label to fit maxPx width using ctx.measureText.
 *
 * @param {CanvasRenderingContext2D} ctx - canvas context (for measureText)
 * @param {string} label - the label to truncate
 * @param {number} maxPx - maximum width in pixels
 * @returns {string} the (possibly truncated) label
 *
 * Pure against ctx.measureText — no DOM, no side effects.
 */
function _truncateLabel(ctx, label, maxPx) {
  if (!ctx || !ctx.measureText) return label;
  if (ctx.measureText(label).width <= maxPx) return label;
  let lo = 1, hi = label.length;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (ctx.measureText(label.slice(0, mid) + '…').width <= maxPx) lo = mid;
    else hi = mid - 1;
  }
  return label.slice(0, lo) + '…';
}

// CommonJS export for the Node.js test runner — no-op in browsers.
if (typeof module !== 'undefined') {
  module.exports = { _layoutOneHopGraph, _graphHitTest, _truncateLabel, GRAPH_NODE_CAP };
}
