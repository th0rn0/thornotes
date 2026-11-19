'use strict';
// esc() must neutralise the five characters that are special in HTML:
// & < > " '. The first four have always been required; the apostrophe was
// added so that a future aria-label='…' sink cannot become an XSS vector
// without the escaper itself being at fault. The invariant lives with the
// function that promises it.

const { describe, it } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const APP_JS_PATH = path.resolve(__dirname, 'app.js');

// Extract esc() from app.js source and evaluate it in isolation. This keeps
// the test pinned to the shipped implementation rather than a copy that can
// drift.
function loadEsc() {
  const src = fs.readFileSync(APP_JS_PATH, 'utf8');
  const marker = 'function esc(';
  const start = src.indexOf(marker);
  assert.notEqual(start, -1, 'esc() declaration not found in app.js');
  const bodyStart = src.indexOf('{', start);
  let depth = 0;
  let i = bodyStart;
  for (; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') {
      depth--;
      if (depth === 0) break;
    }
  }
  const fnSrc = src.slice(start, i + 1);
  const ctx = { String };
  vm.createContext(ctx);
  vm.runInContext(fnSrc, ctx);
  return ctx.esc;
}

const esc = loadEsc();

describe('esc — HTML-entity escaping', () => {
  it('escapes ampersand', () => {
    assert.equal(esc('a&b'), 'a&amp;b');
  });

  it('escapes less-than', () => {
    assert.equal(esc('a<b'), 'a&lt;b');
  });

  it('escapes greater-than', () => {
    assert.equal(esc('a>b'), 'a&gt;b');
  });

  it('escapes double quote', () => {
    assert.equal(esc('a"b'), 'a&quot;b');
  });

  it('escapes single quote (apostrophe)', () => {
    assert.equal(esc("a'b"), 'a&#39;b');
  });

  it('escapes all five special characters together', () => {
    assert.equal(
      esc(`<tag attr="v" attr2='v'>&</tag>`),
      '&lt;tag attr=&quot;v&quot; attr2=&#39;v&#39;&gt;&amp;&lt;/tag&gt;'
    );
  });

  it('returns an empty string for empty input', () => {
    assert.equal(esc(''), '');
  });

  it('coerces non-string input via String()', () => {
    assert.equal(esc(0), '0');
    assert.equal(esc(null), 'null');
    assert.equal(esc(undefined), 'undefined');
  });

  it('leaves safe strings unchanged', () => {
    assert.equal(esc('hello world'), 'hello world');
  });
});
