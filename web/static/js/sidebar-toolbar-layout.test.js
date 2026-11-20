'use strict';
// The sidebar toolbar holds five actions (Note, Folder, Import, Board, Links) in
// a 220px column. It was `display: flex` with `flex: 1` per button, which put
// all five in one row at about 40px each: "+ Folder" and "↑ Import" wrapped onto
// two lines inside their button and the row overflowed, clipping the last
// button. A grid of three columns fixed it, and this test reads the stylesheet
// back so a future layout change cannot quietly reintroduce the crush.
//
// The assertions are about the rules that prevent the two failures, not about a
// pixel result: one row of five (flex:1 with no wrap), and a label that wraps.

const { test } = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');

const TEMPLATE = path.resolve(__dirname, '..', '..', 'templates', 'app.html');

function rule(css, selector) {
  // First declaration block for an exact selector, e.g. ".sidebar-toolbar {".
  const start = css.indexOf(selector + ' {');
  assert.notEqual(start, -1, `no rule for ${selector} in app.html`);
  return css.slice(start, css.indexOf('}', start));
}

const css = fs.readFileSync(TEMPLATE, 'utf8');

test('the sidebar toolbar lays its buttons out on a grid, not one flex row', () => {
  const bar = rule(css, '.sidebar-toolbar');
  assert.match(bar, /display:\s*grid/,
    'the toolbar must be a grid: five buttons in one flex row is what crushed them');
  assert.doesNotMatch(bar, /display:\s*flex/);
});

test('the grid is three equal columns, so buttons in both rows are the same width', () => {
  const bar = rule(css, '.sidebar-toolbar');
  assert.match(bar, /grid-template-columns:\s*repeat\(3,\s*minmax\(0,\s*1fr\)\)/,
    'three equal columns is what keeps the two-button second row the same width as the first');
});

test('a button label cannot wrap onto a second line', () => {
  const btn = rule(css, '.sidebar-toolbar button');
  assert.match(btn, /white-space:\s*nowrap/,
    'a wrapping label is the two-line "+ Folder" the fix removed');
  assert.match(btn, /min-width:\s*0/,
    'without min-width:0 a grid item refuses to shrink below its content and overflows the column');
});

test('every toolbar button is a direct child of the toolbar (grid, not wrapped)', () => {
  const start = css.indexOf('<div class="sidebar-toolbar">');
  assert.notEqual(start, -1);
  const markup = css.slice(start, css.indexOf('</div>', start));
  const ids = [...markup.matchAll(/<button[^>]*id="([^"]+)"/g)].map((m) => m[1]);
  assert.deepStrictEqual(ids, [
    'create-note-btn', 'create-folder-btn', 'import-btn', 'board-toggle-btn', 'shared-links-btn',
  ], 'the five actions the grid was sized for, in the order the toolbar draws them');
});
