'use strict';
// M-03: the share controls must tell the truth about the link.
//
// Two behaviours are pinned here.
//
// 1. Share issues a token in ONE request. A note has a single share_token
//    column and issuing replaces it in one UPDATE, so the previous link stops
//    working the moment the new one exists. A clear-then-issue pair would open
//    a window where the note is unshared, and a failure in the second request
//    would kill the existing link without replacing it.
//
// 2. Revoke stops sharing and leaves it stopped. It never issues a token, and
//    it does not claim success when the response still carries one.
//
// The harness reads the real functions out of app.js and evaluates them with
// stubbed api / currentNote / location / confirm / copyToClipboard /
// showNotification / syncShareControls, so the assertions run against shipped
// source rather than a copy that can drift.

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
    confirmResults: [],
    notifications: [],
    copiedUrls: [],
    syncCalls: 0,
    locationOrigin: 'https://notes.example.com',
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
  h.copyToClipboard = async function (text) { h.copiedUrls.push(text); };
  h.showNotification = function (msg) { h.notifications.push(msg); };
  h.confirm = function () { return h.confirmResults.shift() !== false; };
  h.syncShareControls = function () { h.syncCalls++; };
  h.location = { origin: h.locationOrigin };
  return h;
}

function compile(h, name) {
  const src = fs.readFileSync(APP_JS_PATH, 'utf8');
  const fnSrc = extractFunction(src, name);
  const body = fnSrc.replace(new RegExp(`async function ${name}\\([^)]*\\)`), '');
  const wrapper = `
    return (async function ${name}() {
      const api = __h.api;
      const currentNote = __h.currentNote;
      const copyToClipboard = __h.copyToClipboard;
      const showNotification = __h.showNotification;
      const confirm = __h.confirm;
      const location = __h.location;
      const syncShareControls = __h.syncShareControls;
      ${body}
    })();
  `;
  // eslint-disable-next-line no-new-func
  return new Function('__h', wrapper)(h);
}

const NOTE_ID = 7;
const SHARE_PATH = `/api/v1/notes/${NOTE_ID}/share`;

describe('M-03 — Share issues a token, Unshare revokes it', () => {
  let h;

  beforeEach(() => {
    h = makeHarness({ id: NOTE_ID, share_token: undefined });
  });

  it('share with no existing token sends exactly one POST and copies the URL', async () => {
    h.responders.push({ method: 'POST', path: SHARE_PATH, response: { share_token: 'tok-new' } });

    await compile(h, 'shareNote');

    assert.equal(h.apiCalls.length, 1, 'share must be a single request');
    assert.deepEqual(h.apiCalls[0].body, {}, 'share sends an empty object');
    assert.equal(h.apiCalls[0].body.clear, undefined, 'share must not send clear');
    assert.equal(h.currentNote.share_token, 'tok-new');
    assert.deepEqual(h.copiedUrls, ['https://notes.example.com/s/tok-new']);
    assert.equal(h.syncCalls, 1, 'the control reflects the new share state');
  });

  it('share on an already-shared note warns, then sends exactly ONE request', async () => {
    h.currentNote = { id: NOTE_ID, share_token: 'tok-old' };
    h.confirmResults.push(true);
    h.responders.push({ method: 'POST', path: SHARE_PATH, response: { share_token: 'tok-new' } });

    await compile(h, 'shareNote');

    // The count is the assertion that matters: a clear-then-issue pair passes a
    // payload check while opening a window where the note is unshared.
    assert.equal(h.apiCalls.length, 1, 'rotation must be one atomic request, not clear-then-issue');
    assert.equal(h.apiCalls[0].body.clear, undefined, 'share never sends clear');
    assert.equal(h.currentNote.share_token, 'tok-new');
  });

  it('share does nothing when the user cancels the warning', async () => {
    h.currentNote = { id: NOTE_ID, share_token: 'tok-old' };
    h.confirmResults.push(false);

    await compile(h, 'shareNote');

    assert.equal(h.apiCalls.length, 0, 'cancel must not touch the server');
    assert.equal(h.currentNote.share_token, 'tok-old', 'the old link survives a cancel');
  });

  it('share does not report success when the response carries no token', async () => {
    h.responders.push({ method: 'POST', path: SHARE_PATH, response: { share_token: null } });

    await compile(h, 'shareNote');

    assert.deepEqual(h.copiedUrls, [], 'nothing is copied when there is no link');
    assert.equal(h.currentNote.share_token, undefined);
    assert.match(h.notifications.join(' '), /could not be created/i);
  });

  it('revoke sends {clear:true}, clears the token, and never issues one', async () => {
    h.currentNote = { id: NOTE_ID, share_token: 'tok-old' };
    h.confirmResults.push(true);
    h.responders.push({ method: 'POST', path: SHARE_PATH, response: { share_token: null } });

    await compile(h, 'revokeShare');

    assert.equal(h.apiCalls.length, 1, 'revoke must be a single request');
    assert.equal(h.apiCalls[0].body.clear, true, 'revoke asks for a clear');
    assert.equal(h.currentNote.share_token, null, 'the note is no longer shared');
    assert.equal(h.syncCalls, 1);
    assert.match(h.notifications.join(' '), /revoked/i);
  });

  it('revoke reports failure when the response still carries a token', async () => {
    h.currentNote = { id: NOTE_ID, share_token: 'tok-old' };
    h.confirmResults.push(true);
    h.responders.push({ method: 'POST', path: SHARE_PATH, response: { share_token: 'tok-old' } });

    await compile(h, 'revokeShare');

    assert.equal(h.currentNote.share_token, 'tok-old', 'a live link is not cleared locally');
    assert.match(h.notifications.join(' '), /did not take effect/i);
    assert.doesNotMatch(h.notifications.join(' '), /Share link revoked/i, 'success is not claimed');
  });

  it('revoke surfaces a request failure instead of claiming success', async () => {
    h.currentNote = { id: NOTE_ID, share_token: 'tok-old' };
    h.confirmResults.push(true);
    // No responder registered, so api() throws.
    await compile(h, 'revokeShare');

    assert.equal(h.currentNote.share_token, 'tok-old');
    assert.match(h.notifications.join(' '), /could not revoke/i);
  });

  it('revoke is a no-op when the note has no share token', async () => {
    h.currentNote = { id: NOTE_ID, share_token: null };

    await compile(h, 'revokeShare');

    assert.equal(h.apiCalls.length, 0);
    assert.deepEqual(h.notifications, []);
  });
});
