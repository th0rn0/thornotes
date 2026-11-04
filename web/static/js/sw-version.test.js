'use strict';
const { describe, test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Tests for the version wiring in sw.js.
//
// Failure mode being guarded: the service worker carried a hardcoded cache name
// ('thornotes-v1.5.14.0') and precached the shell plus unversioned /static/
// URLs cache-first. Its bytes never changed between releases, so the browser
// never re-ran install(), the cache never rotated, and a deploy could not reach
// a returning browser at all — curl showed the new app.js while the page kept
// executing the old one.
//
// The worker is a browser script and cannot be required, but it is readable
// source. These tests load the real file with __ASSET_VERSION__ substituted the
// way the server does, and exercise the pure helpers it exposes. No test hooks
// are added to the worker itself: keep this file in sync with sw.js.

// sw.js lives one directory up; the tests sit beside the other JS suites.
const SOURCE = fs.readFileSync(path.join(__dirname, '..', 'sw.js'), 'utf8');

// loadWorker evaluates sw.js with the server-side substitution applied, in a
// context that stubs just enough of a ServiceWorkerGlobalScope to let the module
// register its listeners, then hands back its pure functions and constants.
function loadWorker(version) {
  const src = SOURCE.replace(/__ASSET_VERSION__/g, version);
  const listeners = {};
  const context = {
    self: {
      addEventListener: (type, fn) => { listeners[type] = fn; },
      location: { origin: 'https://notes.example.test' },
      skipWaiting: () => {},
      clients: { claim: () => {} },
    },
    caches: {},
    URL,
  };
  vm.createContext(context);
  const api = vm.runInContext(
    src + '\n;({ ASSET_VERSION, CACHE, STATIC_ASSETS, SHELL_KEY, cacheNameFor, assetUrl, isCurrentVersion });',
    context
  );
  return { api, listeners, substituted: src };
}

describe('sw.js asset version', () => {
  test('the server-side placeholder is the only source of the version', () => {
    assert.match(SOURCE, /const ASSET_VERSION = '__ASSET_VERSION__';/);
    // The raw file must never carry a usable version: if it did, the bytes
    // served by two releases would be identical and nothing would reinstall.
    assert.match(SOURCE, /__ASSET_VERSION__/);
  });

  test('cache name carries the version and moves when it does', () => {
    const a = loadWorker('aaaabbbb').api;
    const b = loadWorker('ccccdddd').api;

    assert.equal(a.CACHE, 'thornotes-aaaabbbb');
    assert.equal(b.CACHE, 'thornotes-ccccdddd');
    assert.notEqual(a.CACHE, b.CACHE);
    assert.equal(a.cacheNameFor('0123abcd'), 'thornotes-0123abcd');
  });

  test('every precached URL is versioned and the shell is not precached', () => {
    const { api, substituted } = loadWorker('aaaabbbb');

    assert.ok(api.STATIC_ASSETS.length > 0);
    for (const url of api.STATIC_ASSETS) {
      assert.ok(url.startsWith('/static/'), `unexpected precache entry: ${url}`);
      assert.ok(url.endsWith('?v=aaaabbbb'), `unversioned precache entry: ${url}`);
    }
    assert.ok(!api.STATIC_ASSETS.includes('/'), 'the shell must not be precached');
    assert.doesNotMatch(substituted, /^\s*'\/',\s*$/m, "bare '/' left in the precache list");
    assert.equal(api.SHELL_KEY, '/');
  });

  test('assetUrl appends the query to an otherwise unchanged path', () => {
    const { api } = loadWorker('aaaabbbb');
    assert.equal(api.assetUrl('/static/js/app.js'), '/static/js/app.js?v=aaaabbbb');
  });

  test('only this version counts as current', () => {
    const { api } = loadWorker('aaaabbbb');
    const url = (u) => new URL(u, 'https://notes.example.test');

    assert.equal(api.isCurrentVersion(url('/static/js/app.js?v=aaaabbbb')), true);
    // Unversioned: the old shell's request, and what an old cache must not answer.
    assert.equal(api.isCurrentVersion(url('/static/js/app.js')), false);
    // Stale: a previous release's version.
    assert.equal(api.isCurrentVersion(url('/static/js/app.js?v=99999999')), false);
  });

  test('the worker registers the handlers the fetch policy depends on', () => {
    const { api, listeners } = loadWorker('aaaabbbb');
    assert.equal(typeof listeners.install, 'function');
    assert.equal(typeof listeners.activate, 'function');
    assert.equal(typeof listeners.fetch, 'function');
    assert.equal(api.ASSET_VERSION, 'aaaabbbb');
  });
});
