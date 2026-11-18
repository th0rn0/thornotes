'use strict';
// Regression test: PATCH /api/v1/notes/:id/share must carry `clear_expiry`
// (not `clear`) when removing a deadline. The two field names differ by one
// word, but mean opposite things: `clear` on POST revokes the token, while
// `clear_expiry` on PATCH removes the deadline. Sending the wrong one is a
// security-relevant bug.
//
// The harness reads the real functions out of app.js and evaluates them with
// stubbed api / currentNote / showNotification / syncShareControls, so the
// assertions run against shipped source rather than a copy that can drift.

const { describe, it, beforeEach } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const APP_JS_PATH = path.resolve(__dirname, 'app.js');

// Pull a top-level `async function NAME(...)` out of app.js by walking braces.
function extractFunction(src, name) {
  const marker = `async function ${name}(`;
  const start = src.indexOf(marker);
  assert.notEqual(start, -1, `${name}() declaration not found in app.js`);
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
  return src.slice(start, i + 1);
}

function makeHarness(note) {
  const h = {
    currentNote: note,
    apiCalls: [],
    responders: [],
    notifications: [],
    syncCalls: 0,
  };
  h.api = async function (method, urlPath, body) {
    h.apiCalls.push({ method, path: urlPath, body });
    for (let i = 0; i < h.responders.length; i++) {
      const r = h.responders[i];
      if (r.method === method && r.path === urlPath) {
        h.responders.splice(i, 1);
        return typeof r.response === 'function' ? r.response(body) : r.response;
      }
    }
    throw new Error(`unstubbed api call: ${method} ${urlPath}`);
  };
  h.showNotification = function (msg) { h.notifications.push(msg); };
  h.syncShareControls = function () { h.syncCalls++; };
  return h;
}

// compile returns a function that, when called with args, runs the extracted
// function with those args and the stubbed environment.
function compile(h, name) {
  const src = fs.readFileSync(APP_JS_PATH, 'utf8');
  const fnSrc = extractFunction(src, name);
  // Extract the parameter list from the function signature.
  const sigMatch = fnSrc.match(/async function \w+\(([^)]*)\)/);
  const params = sigMatch ? sigMatch[1] : '';
  const bodyStart = fnSrc.indexOf('{');
  const body = fnSrc.slice(bodyStart + 1, -1);
  const wrapper = `
    return (async function(${params}) {
      const api = __h.api;
      const currentNote = __h.currentNote;
      const showNotification = __h.showNotification;
      const syncShareControls = __h.syncShareControls;
      ${body}
    });
  `;
  // eslint-disable-next-line no-new-func
  const fn = new Function('__h', wrapper)(h);
  return fn;
}

const NOTE_ID = 7;
const PATCH_PATH = `/api/v1/notes/${NOTE_ID}/share`;

describe('share expiry — PATCH body carries clear_expiry, not clear', () => {
  let h;

  beforeEach(() => {
    h = makeHarness({ id: NOTE_ID, share_token: 'tok-live', expires_at: '2099-01-01T00:00:00Z' });
  });

  it('clearShareExpiry sends PATCH with {clear_expiry: true}', async () => {
    h.responders.push({ method: 'PATCH', path: PATCH_PATH, response: { expires_at: null } });

    const fn = compile(h, 'clearShareExpiry');
    await fn();

    assert.equal(h.apiCalls.length, 1, 'clearShareExpiry must make exactly one request');
    assert.equal(h.apiCalls[0].method, 'PATCH', 'must use PATCH, not POST');
    assert.equal(h.apiCalls[0].path, PATCH_PATH);
    assert.equal(h.apiCalls[0].body.clear_expiry, true, 'body must carry clear_expiry: true');
    assert.equal(h.apiCalls[0].body.clear, undefined, 'body must NOT carry clear (that means revoke on POST)');
    assert.equal(h.currentNote.expires_at, null, 'local state must reflect the cleared expiry');
    assert.equal(h.syncCalls, 1, 'syncShareControls must be called');
  });

  it('clearShareExpiry does not claim success when expires_at is not null', async () => {
    h.responders.push({ method: 'PATCH', path: PATCH_PATH, response: { expires_at: '2099-01-01T00:00:00Z' } });

    const fn = compile(h, 'clearShareExpiry');
    await fn();

    assert.equal(h.currentNote.expires_at, '2099-01-01T00:00:00Z', 'local state must not change on unexpected response');
    assert.doesNotMatch(h.notifications.join(' '), /deadline removed/i, 'success must not be claimed');
  });

  it('setShareExpiry sends PATCH with {expires_at: ISO}', async () => {
    h.responders.push({ method: 'PATCH', path: PATCH_PATH, response: { expires_at: '2099-06-01T00:00:00Z' } });

    const fn = compile(h, 'setShareExpiry');
    await fn(24);

    assert.equal(h.apiCalls.length, 1);
    assert.equal(h.apiCalls[0].method, 'PATCH');
    assert.ok(h.apiCalls[0].body.expires_at, 'body must carry expires_at');
    assert.equal(typeof h.apiCalls[0].body.expires_at, 'string', 'expires_at must be an ISO string');
    assert.equal(h.apiCalls[0].body.clear_expiry, undefined, 'set must not carry clear_expiry');
    assert.equal(h.currentNote.expires_at, '2099-06-01T00:00:00Z');
  });

  it('setShareExpiry rejects non-positive hours without calling the API', async () => {
    const fn = compile(h, 'setShareExpiry');
    await fn(-1);

    assert.equal(h.apiCalls.length, 0, 'must not call the API with invalid input');
    assert.match(h.notifications.join(' '), /positive number/i);
  });

  it('clearShareExpiry is a no-op when the note has no share token', async () => {
    h.currentNote = { id: NOTE_ID, share_token: null };

    const fn = compile(h, 'clearShareExpiry');
    await fn();

    assert.equal(h.apiCalls.length, 0);
  });

  it('clearShareExpiry surfaces request failures honestly', async () => {
    // No responder registered, so api() throws.
    const fn = compile(h, 'clearShareExpiry');
    await fn();

    assert.match(h.notifications.join(' '), /could not clear deadline/i, '403 or other errors must surface');
  });
});

// ── New controls: wiring, not re-implementation ────────────────────────────
// The panel and editor controls must call the existing setShareExpiry /
// clearShareExpiry / setShareExpiryFromPanel / clearShareExpiryFromPanel
// functions rather than re-implementing the request bodies. This pins the
// contract: if someone rewrites the controls inline, these tests fail.

describe('panel controls delegate to the FromPanel functions', () => {
  let h;

  beforeEach(() => {
    h = makeHarness({ id: NOTE_ID, share_token: 'tok-live', expires_at: '2099-01-01T00:00:00Z' });
  });

  it('setShareExpiryFromPanel sends PATCH with {expires_at: ISO} and refreshes', async () => {
    h.responders.push({ method: 'PATCH', path: PATCH_PATH, response: { expires_at: '2099-06-01T00:00:00Z' } });
    // showSharedLinksModal calls api('GET', '/api/v1/shares'); stub it.
    h.responders.push({ method: 'GET', path: '/api/v1/shares', response: [] });

    const src = fs.readFileSync(APP_JS_PATH, 'utf8');
    const fnSrc = extractFunction(src, 'setShareExpiryFromPanel');
    // The function calls showSharedLinksModal, which we must stub.
    const sigMatch = fnSrc.match(/async function \w+\(([^)]*)\)/);
    const params = sigMatch ? sigMatch[1] : '';
    const bodyStart = fnSrc.indexOf('{');
    const body = fnSrc.slice(bodyStart + 1, -1);
    const wrapper = `
      return (async function(${params}) {
        const api = __h.api;
        const currentNote = __h.currentNote;
        const showNotification = __h.showNotification;
        const syncShareControls = __h.syncShareControls;
        const formatExpiry = (iso) => iso;
        const showSharedLinksModal = async () => { await api('GET', '/api/v1/shares'); };
        ${body}
      });
    `;
    const fn = new Function('__h', wrapper)(h);
    await fn(NOTE_ID, 24);

    assert.equal(h.apiCalls.length, 2, 'PATCH then GET /api/v1/shares to refresh');
    assert.equal(h.apiCalls[0].method, 'PATCH');
    assert.ok(h.apiCalls[0].body.expires_at, 'body must carry expires_at');
    assert.equal(h.apiCalls[1].method, 'GET');
    assert.equal(h.apiCalls[1].path, '/api/v1/shares', 'panel must refresh after set');
  });

  it('clearShareExpiryFromPanel sends PATCH with {clear_expiry: true} and refreshes', async () => {
    h.responders.push({ method: 'PATCH', path: PATCH_PATH, response: { expires_at: null } });
    h.responders.push({ method: 'GET', path: '/api/v1/shares', response: [] });

    const src = fs.readFileSync(APP_JS_PATH, 'utf8');
    const fnSrc = extractFunction(src, 'clearShareExpiryFromPanel');
    const sigMatch = fnSrc.match(/async function \w+\(([^)]*)\)/);
    const params = sigMatch ? sigMatch[1] : '';
    const bodyStart = fnSrc.indexOf('{');
    const body = fnSrc.slice(bodyStart + 1, -1);
    const wrapper = `
      return (async function(${params}) {
        const api = __h.api;
        const currentNote = __h.currentNote;
        const showNotification = __h.showNotification;
        const syncShareControls = __h.syncShareControls;
        const showSharedLinksModal = async () => { await api('GET', '/api/v1/shares'); };
        ${body}
      });
    `;
    const fn = new Function('__h', wrapper)(h);
    await fn(NOTE_ID);

    assert.equal(h.apiCalls.length, 2, 'PATCH then GET /api/v1/shares to refresh');
    assert.equal(h.apiCalls[0].method, 'PATCH');
    assert.equal(h.apiCalls[0].body.clear_expiry, true, 'body must carry clear_expiry: true');
    assert.equal(h.apiCalls[0].body.clear, undefined, 'body must NOT carry clear');
    assert.equal(h.apiCalls[1].method, 'GET');
    assert.equal(h.apiCalls[1].path, '/api/v1/shares', 'panel must refresh after clear');
  });

  it('setShareExpiryFromPanel rejects non-positive hours without calling the API', async () => {
    const src = fs.readFileSync(APP_JS_PATH, 'utf8');
    const fnSrc = extractFunction(src, 'setShareExpiryFromPanel');
    const sigMatch = fnSrc.match(/async function \w+\(([^)]*)\)/);
    const params = sigMatch ? sigMatch[1] : '';
    const bodyStart = fnSrc.indexOf('{');
    const body = fnSrc.slice(bodyStart + 1, -1);
    const wrapper = `
      return (async function(${params}) {
        const api = __h.api;
        const showNotification = __h.showNotification;
        ${body}
      });
    `;
    const fn = new Function('__h', wrapper)(h);
    await fn(NOTE_ID, -5);

    assert.equal(h.apiCalls.length, 0, 'must not call the API with invalid input');
    assert.match(h.notifications.join(' '), /positive number/i);
  });
});

describe('isExpired helper', () => {
  it('returns false for null/undefined', () => {
    const src = fs.readFileSync(APP_JS_PATH, 'utf8');
    const isExpiredSrc = src.match(/function isExpired\(iso\) \{[\s\S]*?\n\}/)[0];
    const fn = new Function('return ' + isExpiredSrc)();
    assert.equal(fn(null), false);
    assert.equal(fn(undefined), false);
  });

  it('returns true for past dates', () => {
    const src = fs.readFileSync(APP_JS_PATH, 'utf8');
    const isExpiredSrc = src.match(/function isExpired\(iso\) \{[\s\S]*?\n\}/)[0];
    const fn = new Function('return ' + isExpiredSrc)();
    assert.equal(fn('2020-01-01T00:00:00Z'), true);
  });

  it('returns false for future dates', () => {
    const src = fs.readFileSync(APP_JS_PATH, 'utf8');
    const isExpiredSrc = src.match(/function isExpired\(iso\) \{[\s\S]*?\n\}/)[0];
    const fn = new Function('return ' + isExpiredSrc)();
    assert.equal(fn('2099-01-01T00:00:00Z'), false);
  });
});
