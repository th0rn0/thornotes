'use strict';
// Folder share UI tests (ticket 616, slice 1 UI half).
//
// The confirmation copy is part of the deliverable, not decoration. Contract
// note 616, decision 1: the link is live and recursive — a note added later
// is published, an edit is published, a subfolder is included, and there is
// no way to exclude one note. Re-sharing rotates the token and kills the old
// URL. The sentence the user confirms must say that, in that many words.
//
// This file pins those words so a later edit cannot quietly weaken the
// promise. If any of the required words go missing, the tests fail.

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

// Extract a top-level const declaration.
function extractConst(src, name) {
  const marker = `const ${name} =`;
  const start = src.indexOf(marker);
  assert.notEqual(start, -1, `${name} declaration not found in app.js`);
  const valueStart = start + marker.length;
  // Find the end of the statement (semicolon or next const/function/let/var).
  let depth = 0;
  let inString = false;
  let stringChar = null;
  let i = valueStart;
  for (; i < src.length; i++) {
    const c = src[i];
    if (inString) {
      if (c === stringChar && src[i - 1] !== '\\') inString = false;
    } else {
      if (c === '"' || c === "'" || c === '`') {
        inString = true;
        stringChar = c;
      } else if (c === '(' || c === '[' || c === '{') {
        depth++;
      } else if (c === ')' || c === ']' || c === '}') {
        depth--;
        if (depth < 0) break;
      } else if (c === ';' && depth === 0) {
        break;
      }
    }
  }
  return src.slice(valueStart, i).trim();
}

function makeHarness() {
  const h = {
    currentFolderViewId: null,
    folders: [],
    folderShares: {},
    apiCalls: [],
    apiResponder: null,
    notifications: [],
    syncCalls: 0,
    confirmAnswer: true,
    confirmQuestions: [],
    promptAnswer: '',
    promptCancelled: false,
    copiedUrls: [],
    locationOrigin: 'https://notes.example.com',
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
  h.prompt = function () {
    if (h.promptCancelled) return null;
    return h.promptAnswer;
  };
  h.showNotification = function (msg) { h.notifications.push(msg); };
  h.syncFolderShareControls = function () { h.syncCalls++; };
  h.copyToClipboard = async function (text) { h.copiedUrls.push(text); };
  h.isExpired = function (iso) {
    if (!iso) return false;
    const t = Date.parse(iso);
    return Number.isFinite(t) && t <= Date.now();
  };
  h.formatExpiry = function (iso) { return String(iso); };
  h.location = { origin: h.locationOrigin };
  return h;
}

function compile(h, ...names) {
  // Extract the constants the functions reference.
  const confirmationCopy = extractConst(APP_JS, 'FOLDER_SHARE_CONFIRMATION_COPY');
  const firstCopy = extractConst(APP_JS, 'FOLDER_SHARE_FIRST_COPY');
  const preamble = names.map(n => extractFunction(APP_JS, n)).join('\n');
  const factory = new Function(
    'api', 'confirm', 'prompt', 'showNotification', 'syncFolderShareControls',
    'copyToClipboard', 'currentFolderViewId', 'folders', 'folderShares',
    'esc', 'isExpired', 'formatExpiry', 'location',
    `const FOLDER_SHARE_CONFIRMATION_COPY = ${confirmationCopy};
     const FOLDER_SHARE_FIRST_COPY = ${firstCopy};
     ${preamble}
     return { ${names.map(n => `${n}: ${n}`).join(', ')} };`
  );
  return factory(
    (...args) => h.api(...args),
    (q) => h.confirm(q),
    () => h.prompt(),
    (m) => h.showNotification(m),
    () => h.syncFolderShareControls(),
    (t) => h.copyToClipboard(t),
    h.currentFolderViewId,
    h.folders,
    h.folderShares,
    (s) => String(s == null ? '' : s),
    h.isExpired,
    h.formatExpiry,
    h.location
  );
}

describe('FOLDER_SHARE_CONFIRMATION_COPY pins the promise', () => {
  it('contains the word "live"', () => {
    const copy = extractConst(APP_JS, 'FOLDER_SHARE_CONFIRMATION_COPY');
    assert.match(copy, /live/i, 'the copy must say the link is live');
  });

  it('contains the word "recursive"', () => {
    const copy = extractConst(APP_JS, 'FOLDER_SHARE_CONFIRMATION_COPY');
    assert.match(copy, /recursive/i, 'the copy must say the link is recursive');
  });

  it('mentions subfolders', () => {
    const copy = extractConst(APP_JS, 'FOLDER_SHARE_CONFIRMATION_COPY');
    assert.match(copy, /subfolder/i, 'the copy must mention subfolders');
  });

  it('says notes added later are published', () => {
    const copy = extractConst(APP_JS, 'FOLDER_SHARE_CONFIRMATION_COPY');
    assert.match(copy, /added.*later|later.*appears/i, 'the copy must say notes added later appear');
  });

  it('says edits are published', () => {
    const copy = extractConst(APP_JS, 'FOLDER_SHARE_CONFIRMATION_COPY');
    assert.match(copy, /edit/i, 'the copy must mention edits');
  });

  it('says there is no way to exclude one note', () => {
    const copy = extractConst(APP_JS, 'FOLDER_SHARE_CONFIRMATION_COPY');
    assert.match(copy, /no way to exclude|cannot exclude/i, 'the copy must say no exclusion is possible');
  });

  it('warns that re-sharing rotates the token and kills the old URL', () => {
    const copy = extractConst(APP_JS, 'FOLDER_SHARE_CONFIRMATION_COPY');
    assert.match(copy, /replaces|stops working|old.*link/i, 'the copy must warn about token rotation');
  });
});

describe('FOLDER_SHARE_FIRST_COPY pins the promise for first-time shares', () => {
  it('contains the word "live"', () => {
    const copy = extractConst(APP_JS, 'FOLDER_SHARE_FIRST_COPY');
    assert.match(copy, /live/i, 'the copy must say the link is live');
  });

  it('contains the word "recursive"', () => {
    const copy = extractConst(APP_JS, 'FOLDER_SHARE_FIRST_COPY');
    assert.match(copy, /recursive/i, 'the copy must say the link is recursive');
  });

  it('mentions subfolders', () => {
    const copy = extractConst(APP_JS, 'FOLDER_SHARE_FIRST_COPY');
    assert.match(copy, /subfolder/i, 'the copy must mention subfolders');
  });

  it('warns that re-sharing rotates the token', () => {
    const copy = extractConst(APP_JS, 'FOLDER_SHARE_FIRST_COPY');
    assert.match(copy, /rotate|kill.*old|old.*URL/i, 'the copy must warn about future token rotation');
  });
});

describe('shareFolder', () => {
  it('posts to the folder share route', async () => {
    const h = makeHarness();
    h.currentFolderViewId = 7;
    h.folders = [{ id: 7, name: 'work' }];
    h.apiResponder = () => ({ share_token: 'tok_abc123' });
    await compile(h, 'shareFolder', 'refreshFolderSharesFromServer').shareFolder();

    assert.equal(h.apiCalls.length, 1);
    assert.equal(h.apiCalls[0].method, 'POST');
    assert.equal(h.apiCalls[0].path, '/api/v1/folders/7/share');
  });

  it('shows the rotation confirmation when a link already exists', async () => {
    const h = makeHarness();
    h.currentFolderViewId = 7;
    h.folders = [{ id: 7, name: 'work' }];
    h.folderShares = { 7: { share_token: 'old_token', expires_at: null } };
    h.apiResponder = () => ({ share_token: 'new_token' });
    await compile(h, 'shareFolder', 'refreshFolderSharesFromServer').shareFolder();

    assert.match(h.confirmQuestions[0], /already has a share link/i,
      'the rotation case must say the folder already has a link');
    assert.match(h.confirmQuestions[0], /replaces|stops working/i,
      'the rotation case must warn the old link dies');
  });

  it('shows the first-time confirmation when no link exists', async () => {
    const h = makeHarness();
    h.currentFolderViewId = 7;
    h.folders = [{ id: 7, name: 'work' }];
    h.apiResponder = () => ({ share_token: 'tok_abc123' });
    await compile(h, 'shareFolder', 'refreshFolderSharesFromServer').shareFolder();

    assert.match(h.confirmQuestions[0], /live and recursive/i,
      'the first-time case must say the link is live and recursive');
    assert.match(h.confirmQuestions[0], /subfolder/i,
      'the first-time case must mention subfolders');
    assert.match(h.confirmQuestions[0], /rotate|kill.*old/i,
      'the first-time case must warn about future rotation');
  });

  it('does nothing when the confirmation is declined', async () => {
    const h = makeHarness();
    h.currentFolderViewId = 7;
    h.folders = [{ id: 7, name: 'work' }];
    h.confirmAnswer = false;
    await compile(h, 'shareFolder', 'refreshFolderSharesFromServer').shareFolder();
    assert.equal(h.apiCalls.length, 0);
  });

  it('copies the folder share URL to clipboard', async () => {
    const h = makeHarness();
    h.currentFolderViewId = 7;
    h.folders = [{ id: 7, name: 'work' }];
    h.apiResponder = () => ({ share_token: 'tok_abc123' });
    await compile(h, 'shareFolder', 'refreshFolderSharesFromServer').shareFolder();

    assert.equal(h.copiedUrls.length, 1);
    assert.match(h.copiedUrls[0], /\/f\/tok_abc123/, 'the URL must use /f/ prefix for folders');
  });

  it('surfaces a 409 server message rather than a generic failure', async () => {
    const h = makeHarness();
    h.currentFolderViewId = 7;
    h.folders = [{ id: 7, name: 'work' }];
    const err = new Error('This folder share link has expired');
    err.status = 409;
    h.apiResponder = () => { throw err; };
    await compile(h, 'shareFolder', 'refreshFolderSharesFromServer').shareFolder();

    assert.match(h.notifications.join(' '), /expired/i,
      'a 409 must surface the expiry message');
    assert.doesNotMatch(h.notifications.join(' '), /request failed/i,
      'a 409 must not fall through to the generic failure');
  });
});

describe('revokeFolderShare', () => {
  it('posts to the folder share route with clear:true', async () => {
    const h = makeHarness();
    h.currentFolderViewId = 7;
    h.folderShares = { 7: { share_token: 'tok_abc123', expires_at: null } };
    h.apiResponder = () => ({ share_token: null });
    await compile(h, 'revokeFolderShare', 'refreshFolderSharesFromServer').revokeFolderShare();

    assert.equal(h.apiCalls.length, 1);
    assert.equal(h.apiCalls[0].method, 'POST');
    assert.equal(h.apiCalls[0].path, '/api/v1/folders/7/share');
    assert.equal(h.apiCalls[0].body.clear, true);
  });

  it('mentions subfolders in the revoke confirmation', async () => {
    const h = makeHarness();
    h.currentFolderViewId = 7;
    h.folderShares = { 7: { share_token: 'tok_abc123', expires_at: null } };
    h.apiResponder = () => ({ share_token: null });
    await compile(h, 'revokeFolderShare', 'refreshFolderSharesFromServer').revokeFolderShare();

    assert.match(h.confirmQuestions[0], /subfolder/i,
      'the revoke confirmation must mention subfolders');
  });

  it('removes the folder from folderShares on success', async () => {
    const h = makeHarness();
    h.currentFolderViewId = 7;
    h.folderShares = { 7: { share_token: 'tok_abc123', expires_at: null } };
    h.apiResponder = () => ({ share_token: null });
    const mod = compile(h, 'revokeFolderShare', 'refreshFolderSharesFromServer');
    await mod.revokeFolderShare();
    assert.equal(h.folderShares[7], undefined, 'the folder share should be removed');
  });

  it('does not claim success when the server still reports a token', async () => {
    const h = makeHarness();
    h.currentFolderViewId = 7;
    h.folderShares = { 7: { share_token: 'tok_abc123', expires_at: null } };
    h.apiResponder = () => ({ share_token: 'still-live' });
    await compile(h, 'revokeFolderShare', 'refreshFolderSharesFromServer').revokeFolderShare();

    assert.doesNotMatch(h.notifications.join(' '), /revoked/i);
    assert.match(h.notifications.join(' '), /still live/i);
  });
});

describe('syncFolderShareControls', () => {
  it('hides all controls when no folder is selected', () => {
    const h = makeHarness();
    h.currentFolderViewId = null;
    // Mock DOM elements
    const elements = {};
    const getElementById = (id) => {
      if (!elements[id]) elements[id] = { hidden: false, textContent: '', disabled: false };
      return elements[id];
    };
    global.document = { getElementById };
    try {
      compile(h, 'syncFolderShareControls').syncFolderShareControls();
      assert.equal(elements['folder-share-btn'].hidden, true,
        'share should be hidden when no folder is selected');
      assert.equal(elements['folder-unshare-btn'].hidden, true,
        'unshare should be hidden when no folder is selected');
      assert.equal(elements['folder-share-expiry-info'].hidden, true,
        'expiry info should be hidden when no folder is selected');
      assert.equal(elements['folder-share-expiry-controls'].hidden, true,
        'expiry controls should be hidden when no folder is selected');
    } finally {
      delete global.document;
    }
  });

  it('hides unshare when there is no share link', () => {
    const h = makeHarness();
    h.currentFolderViewId = 7;
    // Mock DOM elements
    const elements = {};
    const getElementById = (id) => {
      if (!elements[id]) elements[id] = { hidden: false, textContent: '', disabled: false };
      return elements[id];
    };
    global.document = { getElementById };
    try {
      compile(h, 'syncFolderShareControls').syncFolderShareControls();
      assert.equal(elements['folder-unshare-btn'].hidden, true,
        'unshare should be hidden when no share exists');
      // Share button should be visible (hidden is false or not set to true)
      assert.notEqual(elements['folder-share-btn'].hidden, true,
        'share should be visible when no share exists');
    } finally {
      delete global.document;
    }
  });

  it('shows unshare and hides share when a live link exists', () => {
    const h = makeHarness();
    h.currentFolderViewId = 7;
    h.folderShares = { 7: { share_token: 'tok_abc123', expires_at: null } };
    const elements = {};
    const getElementById = (id) => {
      if (!elements[id]) elements[id] = { hidden: false, textContent: '', disabled: false };
      return elements[id];
    };
    global.document = { getElementById };
    try {
      compile(h, 'syncFolderShareControls').syncFolderShareControls();
      assert.equal(elements['folder-unshare-btn'].hidden, false,
        'unshare should be visible when a live share exists');
      assert.equal(elements['folder-share-btn'].hidden, true,
        'share should be hidden when a live share exists');
    } finally {
      delete global.document;
    }
  });

  it('hides unshare but shows share when the link is expired', () => {
    const h = makeHarness();
    h.currentFolderViewId = 7;
    h.folderShares = { 7: { share_token: 'tok_abc123', expires_at: '2020-01-01T00:00:00Z' } };
    const elements = {};
    const getElementById = (id) => {
      if (!elements[id]) elements[id] = { hidden: false, textContent: '', disabled: false };
      return elements[id];
    };
    global.document = { getElementById };
    try {
      compile(h, 'syncFolderShareControls').syncFolderShareControls();
      // When expired, the unshare button is hidden (nothing to revoke)
      assert.equal(elements['folder-unshare-btn'].hidden, true,
        'unshare should be hidden on expired link');
      // But the share button is visible so the user can create a new link
      assert.notEqual(elements['folder-share-btn'].hidden, true,
        'share should be visible on expired link so user can re-share');
    } finally {
      delete global.document;
    }
  });
});
