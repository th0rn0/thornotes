'use strict';
const { describe, test } = require('node:test');
const assert = require('node:assert/strict');
const { validateServerUrl, mergeConfig } = require('./lib.js');

// ── validateServerUrl ─────────────────────────────────────────────────────────

describe('validateServerUrl', () => {
  // Happy paths
  test('plain http URL with port', () => {
    const r = validateServerUrl('http://localhost:8080');
    assert.equal(r.ok, true);
    assert.equal(r.url, 'http://localhost:8080');
  });

  test('https URL accepted', () => {
    const r = validateServerUrl('https://notes.example.com');
    assert.equal(r.ok, true);
    assert.equal(r.url, 'https://notes.example.com');
  });

  test('http URL with sub-path accepted', () => {
    const r = validateServerUrl('http://example.com/thornotes');
    assert.equal(r.ok, true);
    assert.equal(r.url, 'http://example.com/thornotes');
  });

  test('IP address with port accepted', () => {
    const r = validateServerUrl('http://192.168.1.42:3000');
    assert.equal(r.ok, true);
    assert.equal(r.url, 'http://192.168.1.42:3000');
  });

  test('subdomain accepted', () => {
    const r = validateServerUrl('https://notes.myserver.io');
    assert.equal(r.ok, true);
  });

  // Normalisation
  test('trailing slash is stripped', () => {
    const r = validateServerUrl('http://localhost:8080/');
    assert.equal(r.ok, true);
    assert.equal(r.url, 'http://localhost:8080');
  });

  test('multiple trailing slashes are stripped', () => {
    const r = validateServerUrl('http://localhost:8080///');
    assert.equal(r.ok, true);
    assert.equal(r.url, 'http://localhost:8080');
  });

  test('leading and trailing whitespace is trimmed', () => {
    const r = validateServerUrl('  http://localhost:8080  ');
    assert.equal(r.ok, true);
    assert.equal(r.url, 'http://localhost:8080');
  });

  test('whitespace + trailing slash both handled', () => {
    const r = validateServerUrl('  http://localhost:8080/  ');
    assert.equal(r.ok, true);
    assert.equal(r.url, 'http://localhost:8080');
  });

  // Empty / null / undefined
  test('empty string returns error', () => {
    const r = validateServerUrl('');
    assert.equal(r.ok, false);
    assert.match(r.error, /required/i);
  });

  test('whitespace-only string returns error', () => {
    const r = validateServerUrl('   ');
    assert.equal(r.ok, false);
    assert.match(r.error, /required/i);
  });

  test('null returns error', () => {
    const r = validateServerUrl(null);
    assert.equal(r.ok, false);
    assert.match(r.error, /required/i);
  });

  test('undefined returns error', () => {
    const r = validateServerUrl(undefined);
    assert.equal(r.ok, false);
    assert.match(r.error, /required/i);
  });

  // Protocol gating
  test('ftp:// is rejected', () => {
    const r = validateServerUrl('ftp://files.example.com');
    assert.equal(r.ok, false);
    assert.match(r.error, /http/i);
  });

  test('file:// is rejected', () => {
    const r = validateServerUrl('file:///home/user/notes');
    assert.equal(r.ok, false);
    assert.match(r.error, /http/i);
  });

  test('ws:// (websocket) is rejected', () => {
    const r = validateServerUrl('ws://localhost:8080');
    assert.equal(r.ok, false);
    assert.match(r.error, /http/i);
  });

  // Malformed inputs
  test('bare hostname without protocol is rejected', () => {
    const r = validateServerUrl('localhost:8080');
    assert.equal(r.ok, false);
  });

  test('hostname with no scheme at all is rejected', () => {
    const r = validateServerUrl('localhost');
    assert.equal(r.ok, false);
  });

  test('double-slash-only string is rejected', () => {
    const r = validateServerUrl('//');
    assert.equal(r.ok, false);
  });

  test('gibberish is rejected', () => {
    const r = validateServerUrl('not a url at all!!!');
    assert.equal(r.ok, false);
  });

  // Return shape guarantee
  test('success result has ok=true and url string', () => {
    const r = validateServerUrl('http://localhost:8080');
    assert.equal(typeof r.ok, 'boolean');
    assert.equal(typeof r.url, 'string');
    assert.equal(r.error, undefined);
  });

  test('failure result has ok=false and error string', () => {
    const r = validateServerUrl('bad');
    assert.equal(typeof r.ok, 'boolean');
    assert.equal(typeof r.error, 'string');
    assert.equal(r.url, undefined);
  });
});

// ── mergeConfig ───────────────────────────────────────────────────────────────

describe('mergeConfig', () => {
  test('adds new keys from update', () => {
    const result = mergeConfig({ a: 1 }, { b: 2 });
    assert.deepEqual(result, { a: 1, b: 2 });
  });

  test('overwrites existing key with update value', () => {
    const result = mergeConfig({ serverUrl: 'http://old' }, { serverUrl: 'http://new' });
    assert.equal(result.serverUrl, 'http://new');
  });

  test('does not mutate the existing config', () => {
    const orig = { serverUrl: 'http://localhost:8080' };
    mergeConfig(orig, { serverUrl: 'http://example.com' });
    assert.equal(orig.serverUrl, 'http://localhost:8080');
  });

  test('does not mutate the update object', () => {
    const upd = { serverUrl: 'http://example.com' };
    mergeConfig({}, upd);
    assert.equal(upd.serverUrl, 'http://example.com');
  });

  test('empty update returns copy of existing', () => {
    const orig = { serverUrl: 'http://localhost' };
    const result = mergeConfig(orig, {});
    assert.deepEqual(result, orig);
    assert.notEqual(result, orig); // new object
  });

  test('empty existing with update returns copy of update', () => {
    const result = mergeConfig({}, { serverUrl: 'http://localhost' });
    assert.equal(result.serverUrl, 'http://localhost');
  });

  test('both empty returns empty object', () => {
    const result = mergeConfig({}, {});
    assert.deepEqual(result, {});
  });

  test('later update keys win (last-write-wins)', () => {
    const result = mergeConfig({ x: 1, y: 2 }, { y: 99, z: 3 });
    assert.equal(result.x, 1);
    assert.equal(result.y, 99);
    assert.equal(result.z, 3);
  });

  test('returns a new object, not the same reference as either arg', () => {
    const existing = { a: 1 };
    const update   = { b: 2 };
    const result   = mergeConfig(existing, update);
    assert.notEqual(result, existing);
    assert.notEqual(result, update);
  });
});
