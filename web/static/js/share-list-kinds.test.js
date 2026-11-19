'use strict';
// The shared-links panel reads GET /api/v1/shares, which now returns TWO kinds of
// link in one list (ticket 616, slice 3): a note link tagged kind:"note" with a
// note_id, and a folder link tagged kind:"folder" with a folder_id and a null
// note_id.
//
// The failure this file exists to prevent: the panel reading note_id blindly.
// Every folder row would then render a Revoke button that posts to
// /api/v1/notes/null/share — a 404 — and a recursive folder link would stay live
// while the person who pressed Revoke was told it was gone.
//
// The harness pulls the real functions out of app.js and evaluates them with
// stubbed esc / isExpired / formatExpiry / api / confirm, so the assertions run
// against shipped source rather than a copy that can drift.

const { describe, it, beforeEach } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const APP_JS_PATH = path.resolve(__dirname, 'app.js');
const APP_JS = fs.readFileSync(APP_JS_PATH, 'utf8');

// Pull a top-level function (async or not) out of app.js by walking braces.
function extractFunction(src, name) {
  let start = src.indexOf(`async function ${name}(`);
  if (start === -1) start = src.indexOf(`function ${name}(`);
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

function makeHarness() {
  const h = {
    currentNote: null,
    apiCalls: [],
    apiResponder: null,
    notifications: [],
    syncCalls: 0,
    confirmAnswer: true,
    confirmQuestions: [],
    modalRefreshes: 0,
  };
  h.api = async function (method, urlPath, body) {
    h.apiCalls.push({ method, path: urlPath, body });
    if (h.apiResponder) return h.apiResponder(method, urlPath, body);
    return { share_token: null };
  };
  h.confirm = function (question) {
    h.confirmQuestions.push(question);
    return h.confirmAnswer;
  };
  h.showNotification = function (msg) { h.notifications.push(msg); };
  h.syncShareControls = function () { h.syncCalls++; };
  h.showSharedLinksModal = async function () { h.modalRefreshes++; };
  h.esc = function (s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  };
  h.isExpired = function (iso) {
    if (!iso) return false;
    const t = Date.parse(iso);
    return Number.isFinite(t) && t <= Date.now();
  };
  h.formatExpiry = function (iso) { return String(iso); };
  return h;
}

// compile evaluates the named app.js functions against the harness. Every
// dependency is passed as a shim that reads the harness at call time, so a test
// can swap h.api or h.confirmAnswer after compiling.
function compile(h, ...names) {
  const preamble = names.map(n => extractFunction(APP_JS, n)).join('\n');
  const factory = new Function(
    'api', 'confirm', 'showNotification', 'syncShareControls', 'showSharedLinksModal',
    'currentNote', 'esc', 'isExpired', 'formatExpiry',
    `${preamble}
     return { ${names.map(n => `${n}: ${n}`).join(', ')} };`
  );
  return factory(
    (...args) => h.api(...args),
    (q) => h.confirm(q),
    (m) => h.showNotification(m),
    () => h.syncShareControls(),
    () => h.showSharedLinksModal(),
    h.currentNote,
    h.esc, h.isExpired, h.formatExpiry
  );
}

const FOLDER_ROW = {
  kind: 'folder',
  note_id: null,
  folder_id: 7,
  title: 'work',
  token_prefix: 'a1b2c3d4…',
  expires_at: null,
  last_viewed_at: null,
  updated_at: '2026-11-18T10:00:00Z',
};

const NOTE_ROW = {
  kind: 'note',
  note_id: 42,
  folder_id: 7,
  title: 'Alice private note',
  token_prefix: 'deadbeef…',
  expires_at: null,
  last_viewed_at: null,
  updated_at: '2026-11-18T11:00:00Z',
};

describe('sharedLinkTarget', () => {
  it('sends a folder row to the folder share route', () => {
    const h = makeHarness();
    const t = compile(h, 'sharedLinkTarget').sharedLinkTarget(FOLDER_ROW);
    assert.equal(t.kind, 'folder');
    assert.equal(t.id, 7);
    assert.equal(t.revokePath, '/api/v1/folders/7/share');
  });

  it('sends a note row to the note share route', () => {
    const h = makeHarness();
    const t = compile(h, 'sharedLinkTarget').sharedLinkTarget(NOTE_ROW);
    assert.equal(t.kind, 'note');
    assert.equal(t.id, 42);
    assert.equal(t.revokePath, '/api/v1/notes/42/share');
  });

  it('treats a row with no kind as a note row (the pre-folder response)', () => {
    const h = makeHarness();
    const t = compile(h, 'sharedLinkTarget').sharedLinkTarget({ note_id: 9, title: 'old' });
    assert.equal(t.kind, 'note');
    assert.equal(t.revokePath, '/api/v1/notes/9/share');
  });
});

describe('renderSharedLinkRow', () => {
  let h;
  let mod;
  beforeEach(() => {
    h = makeHarness();
    mod = compile(h, 'sharedLinkTarget', 'renderSharedLinkRow');
  });

  it('renders a folder row with a folder-kind revoke and no note id', () => {
    const html = mod.renderSharedLinkRow(FOLDER_ROW);
    assert.match(html, /data-kind="folder"/);
    assert.match(html, /data-folder-id="7"/);
    assert.match(html, /data-action="revoke"/);
    assert.doesNotMatch(html, /data-action="set-deadline"/,
      'a folder deadline is not edited from this panel');
    assert.doesNotMatch(html, /data-note-id="7"/,
      'the folder id must never be rendered as a note id');
    assert.match(html, /shared-link-kind/, 'the row says it is a folder link');
    assert.match(html, /a1b2c3d4…/, 'the prefix is still shown');
  });

  it('keeps the note row exactly as it was: deadline, no-expiry and revoke by note id', () => {
    const html = mod.renderSharedLinkRow(NOTE_ROW);
    assert.match(html, /data-kind="note"/);
    assert.match(html, /data-note-id="42"/);
    assert.match(html, /data-action="set-deadline"/);
    assert.match(html, /data-action="revoke"/);
    assert.match(html, /Deadline/);
    assert.doesNotMatch(html, /data-folder-id/, 'a note row offers no folder action');
    assert.doesNotMatch(html, /shared-link-kind/);
  });

  it('offers no actions on an expired folder row but still shows its deadline', () => {
    const expiredFolder = Object.assign({}, FOLDER_ROW, { expires_at: '2020-01-01T00:00:00Z' });
    const html = mod.renderSharedLinkRow(expiredFolder);
    assert.doesNotMatch(html, /data-action="revoke"/,
      'an expired link no longer resolves, so Revoke would be noise');
    assert.match(html, /shared-link-expired-badge/);
    assert.match(html, /Expired: 2020-01-01T00:00:00Z/,
      'the deadline stays visible: it is the only explanation for a dead URL');
  });

  it('escapes the folder name it displays', () => {
    const hostile = Object.assign({}, FOLDER_ROW, { title: '<img src=x onerror=alert(1)>' });
    const html = mod.renderSharedLinkRow(hostile);
    assert.doesNotMatch(html, /<img/);
    assert.match(html, /&lt;img/);
  });
});

describe('revokeShareFromPanel', () => {
  it('revokes a folder link through the folder route', async () => {
    const h = makeHarness();
    await compile(h, 'revokeShareFromPanel').revokeShareFromPanel('folder', 7);

    assert.equal(h.apiCalls.length, 1);
    assert.equal(h.apiCalls[0].method, 'POST');
    assert.equal(h.apiCalls[0].path, '/api/v1/folders/7/share');
    assert.equal(h.apiCalls[0].body.clear, true);
    assert.match(h.confirmQuestions[0], /folder/i,
      'the confirmation must say a folder link, and that it covers the subtree');
    assert.match(h.confirmQuestions[0], /subfolders/i,
      'a recursive grant revoked by a generic "revoke this link?" understates it');
  });

  it('revokes a note link through the note route', async () => {
    const h = makeHarness();
    await compile(h, 'revokeShareFromPanel').revokeShareFromPanel('note', 42);

    assert.equal(h.apiCalls[0].path, '/api/v1/notes/42/share');
    assert.equal(h.apiCalls[0].body.clear, true);
  });

  it('does nothing when the confirmation is declined', async () => {
    const h = makeHarness();
    h.confirmAnswer = false;
    await compile(h, 'revokeShareFromPanel').revokeShareFromPanel('folder', 7);
    assert.equal(h.apiCalls.length, 0);
  });

  it('refreshes the panel and reports success after a revoke', async () => {
    const h = makeHarness();
    await compile(h, 'revokeShareFromPanel').revokeShareFromPanel('folder', 7);
    assert.equal(h.modalRefreshes, 1);
    assert.match(h.notifications.join(' '), /revoked/i);
  });

  it('does not claim success when the server still reports a token', async () => {
    const h = makeHarness();
    h.apiResponder = () => ({ share_token: 'still-live' });
    await compile(h, 'revokeShareFromPanel').revokeShareFromPanel('folder', 7);
    assert.doesNotMatch(h.notifications.join(' '), /revoked/i);
    assert.match(h.notifications.join(' '), /still live/i);
  });
});

describe('the deadline guard reads note rows only', () => {
  it('setShareExpiryFromPanel refuses to revive an expired note link while a folder row shares the list', async () => {
    const h = makeHarness();
    h.apiResponder = (method) => {
      if (method === 'GET') {
        return [FOLDER_ROW, Object.assign({}, NOTE_ROW, { expires_at: '2020-01-01T00:00:00Z' })];
      }
      return { expires_at: '2099-01-01T00:00:00Z' };
    };
    await compile(h, 'setShareExpiryFromPanel').setShareExpiryFromPanel(42, 24);

    const patches = h.apiCalls.filter(c => c.method === 'PATCH');
    assert.equal(patches.length, 0, 'an expired note link must not be given a new deadline');
    assert.match(h.notifications.join(' '), /expired/i);
  });

  it('setShareExpiryFromPanel still sets a deadline on a live note link', async () => {
    const h = makeHarness();
    h.apiResponder = (method) => {
      if (method === 'GET') return [FOLDER_ROW, NOTE_ROW];
      return { expires_at: '2099-01-01T00:00:00Z' };
    };
    await compile(h, 'setShareExpiryFromPanel').setShareExpiryFromPanel(42, 24);

    const patches = h.apiCalls.filter(c => c.method === 'PATCH');
    assert.equal(patches.length, 1);
    assert.equal(patches[0].path, '/api/v1/notes/42/share');
  });
});
