'use strict';
// M-03: "Clear share token" actually revokes.
//
// The bug: shareNote() in app.js POSTed `{}` unconditionally. The backend
// treats a missing `clear` field as "issue a token", so clicking Share on a
// note with an existing link rotated it instead of revoking — the UI told
// the user the old link was dead while the same token kept resolving.
//
// This test reads the real shareNote source from app.js and evaluates it in
// a harness with stubbed api / currentNote / location / copyToClipboard /
// showNotification / confirm. It then verifies that when currentNote has a
// live share_token the function sends {clear: true} and does NOT accept a
// response where the server returns the *same* token (the exact shape of
// the bug — a 200 with an unchanged token would look identical in the
// network tab).
//
// Failing without the fix: before the fix shareNote always sends `{}`, so
// apiCalls[0].body.clear is undefined → assertion fails.
//
// Keep in sync with the mirror of shareNote in app.js. If you change the
// clear flow, update both.

const { describe, it, beforeEach } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const APP_JS_PATH = path.resolve(__dirname, 'app.js');

// Extract the shareNote function source from app.js by scanning for the
// `async function shareNote()` declaration and walking balanced braces.
function extractShareNoteSource(src) {
  const marker = 'async function shareNote()';
  const start = src.indexOf(marker);
  assert.notEqual(start, -1, 'shareNote() declaration not found in app.js');
  const bodyStart = src.indexOf('{', start);
  let depth = 0;
  let i = bodyStart;
  for (; i < src.length; i++) {
    const ch = src[i];
    if (ch === '{') depth++;
    else if (ch === '}') {
      depth--;
      if (depth === 0) break;
    }
  }
  return src.slice(start, i + 1);
}

function makeHarness() {
  const h = {
    currentNote: null,
    apiCalls: [],
    // Stub responses per (method, path) — last-registered wins. Tests push
    // responders in order; each call pops the first matching one.
    responders: [],
    confirmResults: [],
    notifications: [],
    copiedUrls: [],
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
  h.confirm = function () {
    return h.confirmResults.shift() !== false;
  };
  h.location = { origin: h.locationOrigin };
  return h;
}

// Build a function from the real app.js source bound to the harness stubs.
function compileShareNote(h) {
  const src = fs.readFileSync(APP_JS_PATH, 'utf8');
  const fnSrc = extractShareNoteSource(src);
  // Wrap in an async IIFE that captures the harness as free variables.
  const wrapper = `
    return (async function shareNote() {
      const api = __h.api;
      const currentNote = __h.currentNote;
      const copyToClipboard = __h.copyToClipboard;
      const showNotification = __h.showNotification;
      const confirm = __h.confirm;
      const location = __h.location;
      ${fnSrc.replace('async function shareNote()', '')}
    })();
  `;
  // eslint-disable-next-line no-new-func
  return new Function('__h', wrapper);
}

describe('M-03 — "Clear share token" actually revokes', () => {
  let h, run;
  beforeEach(() => {
    h = makeHarness();
    run = () => compileShareNote(h)(h);
  });

  it('does nothing when no note is open', async () => {
    h.currentNote = null;
    await run();
    assert.equal(h.apiCalls.length, 0);
  });

  it('generates a fresh token when the note has no share_token yet', async () => {
    h.currentNote = { id: 11, share_token: null };
    h.responders.push({
      method: 'POST',
      path: '/api/v1/notes/11/share',
      response: { share_token: 'fresh-token-abc' },
    });
    await run();
    assert.equal(h.apiCalls.length, 1);
    // Fresh-issue path: body is the existing `{}` — fine for new tokens.
    assert.deepEqual(h.apiCalls[0].body, {});
    assert.equal(h.copiedUrls[0], 'https://notes.example.com/s/fresh-token-abc');
  });

  it('sends {clear: true} when the note already has a share_token', async () => {
    h.currentNote = { id: 42, share_token: 'live-token-xyz' };
    h.confirmResults.push(true); // user confirms
    // First call: revoke — must return {share_token: null}
    h.responders.push({
      method: 'POST',
      path: '/api/v1/notes/42/share',
      response: (body) => {
        // The whole point of the test: if the body does NOT carry clear:true,
        // this is the bug. Refuse to behave like the backend would when it
        // receives `{}` (which is "issue a new token").
        assert.ok(
          body && body.clear === true,
          'clear path must send {clear: true}; got ' + JSON.stringify(body),
        );
        // After revocation the note's share_token must be cleared locally so
        // the UI does not keep advertising a dead link.
        h.currentNote.share_token = null;
        return { share_token: null };
      },
    });
    // Second call: issue the replacement token.
    h.responders.push({
      method: 'POST',
      path: '/api/v1/notes/42/share',
      response: { share_token: 'new-token-def' },
    });

    await run();

    // The first call MUST be the clear — never a bare `{}` rotation.
    assert.equal(h.apiCalls.length, 2);
    assert.deepEqual(h.apiCalls[0].body, { clear: true });
    assert.deepEqual(h.apiCalls[1].body, {});
    // And the link handed to the user must be the NEW token, not the old one.
    assert.equal(h.copiedUrls[0], 'https://notes.example.com/s/new-token-def');
    assert.notEqual(h.copiedUrls[0].includes('live-token-xyz'), true);
  });

  it('rejects a "cleared" response that still carries the old token', async () => {
    // This is the exact lie the bug produced: 200 OK, body says the link is
    // gone, but the token value is unchanged so the public URL still works.
    // shareNote must now verify the outcome, not just the status code — if
    // the server returns a non-null token from a clear, we throw.
    h.currentNote = { id: 7, share_token: 'still-live' };
    h.confirmResults.push(true);
    h.responders.push({
      method: 'POST',
      path: '/api/v1/notes/7/share',
      // Malicious/laggy server response: the clear didn't take.
      response: () => ({ share_token: 'still-live' }),
    });

    await assert.rejects(
      () => run(),
      /clear did not revoke/,
    );
    // The clear call was still issued with the right body before the throw.
    assert.deepEqual(h.apiCalls[0].body, { clear: true });
    // And we never copied a URL — the lie was surfaced to the user.
    assert.equal(h.copiedUrls.length, 0);
  });
});
