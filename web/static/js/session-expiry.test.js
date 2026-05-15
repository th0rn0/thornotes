'use strict';
const { describe, test } = require('node:test');
const assert = require('node:assert/strict');

// Tests for the stale-session detection logic in app.js.
//
// Failure mode being guarded: a notes tab is left open past the 7-day session
// TTL. The user keeps editing; every autosave PATCH returns 401; the old code
// only flipped a tiny "Error" chip, so edits were lost silently.
//
// The fix has three moving parts in app.js, all mirrored below:
//   - api()               raises handleSessionExpired() on a 401, but only
//                          when currentUser is set (a failed LOGIN is also a
//                          401 and must not trip the banner).
//   - handleSessionExpired() idempotent terminal state: banner up, save status
//                          frozen on "error", pending autosave cancelled, note
//                          text left untouched so it can be copied out.
//   - scheduleAutoSave()   / autoSave() become no-ops once expired, so the
//                          editor stops hammering the server with doomed 401s.
//
// As with default-note-view.test.js, app.js is a browser script and cannot be
// required directly, so the logic is mirrored in a self-contained state
// machine. Keep makeSessionState() in sync with app.js.

function makeSessionState({ loggedIn = true, noteContent = 'draft text' } = {}) {
  let currentUser = loggedIn ? { username: 'u' } : null;
  let sessionExpired = false;
  let saveStatus = 'saved';
  let bannerVisible = false;
  let saveTimerArmed = false;
  let networkSaves = 0;       // autosaves that actually reached the network
  let handledCount = 0;       // times handleSessionExpired did real work

  function setSaveStatus(s) { saveStatus = s; }

  // Mirror of handleSessionExpired() in app.js.
  function handleSessionExpired() {
    if (sessionExpired) return;
    sessionExpired = true;
    handledCount++;
    saveTimerArmed = false;       // clearTimeout(saveTimer)
    setSaveStatus('error');
    bannerVisible = true;
    // note text is deliberately NOT touched
  }

  // Mirror of the api() helper: the 401 hook, then throw-on-non-2xx.
  function api(status) {
    if (status === 401 && currentUser) {
      handleSessionExpired();
    }
    if (status < 200 || status >= 300) {
      const err = new Error('HTTP ' + status);
      err.status = status;
      throw err;
    }
    return {};
  }

  // Mirror of scheduleAutoSave() in app.js.
  function scheduleAutoSave() {
    if (sessionExpired) return;
    setSaveStatus('saving');
    saveTimerArmed = true;
  }

  // Mirror of autoSave() in app.js, reduced to the session-relevant parts.
  // serverStatus is the status the PATCH would return.
  function autoSave(serverStatus) {
    if (sessionExpired) return;       // belt-and-suspenders guard
    saveTimerArmed = false;
    networkSaves++;
    try {
      api(serverStatus);
      setSaveStatus('saved');
    } catch (e) {
      setSaveStatus('error');
    }
  }

  // Mirror of login(): a fresh app where /auth/login itself returns 401.
  function attemptLogin(loginStatus) {
    try {
      api(loginStatus);
      currentUser = { username: 'u' };
    } catch (e) {
      // login() shows an inline error; nothing else happens.
    }
  }

  return {
    get sessionExpired() { return sessionExpired; },
    get saveStatus() { return saveStatus; },
    get bannerVisible() { return bannerVisible; },
    get saveTimerArmed() { return saveTimerArmed; },
    get networkSaves() { return networkSaves; },
    get handledCount() { return handledCount; },
    get noteContent() { return noteContent; },
    api, scheduleAutoSave, autoSave, handleSessionExpired, attemptLogin,
  };
}

// ── A 401 from autosave raises the banner ────────────────────────────────────

describe('autosave hits an expired session', () => {
  test('a 401 sets the expired flag, banner, and error status', () => {
    const s = makeSessionState();
    s.autoSave(401);
    assert.equal(s.sessionExpired, true, 'session must be marked expired');
    assert.equal(s.bannerVisible, true, 'banner must be shown');
    assert.equal(s.saveStatus, 'error', 'save status must read error');
  });

  test('the pending autosave timer is cancelled on expiry', () => {
    const s = makeSessionState();
    s.scheduleAutoSave();
    assert.equal(s.saveTimerArmed, true, 'precondition: timer armed');
    s.autoSave(401);
    assert.equal(s.saveTimerArmed, false, 'timer must be cancelled on expiry');
  });

  test('the note text is left untouched so it can be copied out', () => {
    const s = makeSessionState({ noteContent: 'unsaved paragraph' });
    s.autoSave(401);
    assert.equal(s.noteContent, 'unsaved paragraph', 'editor content must survive');
  });
});

// ── Idempotency ──────────────────────────────────────────────────────────────

describe('handleSessionExpired is idempotent', () => {
  test('repeated 401s only do the work once', () => {
    const s = makeSessionState();
    s.autoSave(401);
    s.handleSessionExpired();
    s.handleSessionExpired();
    assert.equal(s.handledCount, 1, 'only the first 401 should do real work');
  });

  test('the banner stays up across further 401s', () => {
    const s = makeSessionState();
    s.autoSave(401);
    s.handleSessionExpired();
    assert.equal(s.bannerVisible, true);
    assert.equal(s.sessionExpired, true);
  });
});

// ── A failed login must NOT trip the banner ──────────────────────────────────

describe('a 401 before login does not trigger the banner', () => {
  test('wrong-password login (401) leaves the session state clean', () => {
    const s = makeSessionState({ loggedIn: false });
    s.attemptLogin(401);
    assert.equal(s.sessionExpired, false, 'a failed login is not an expired session');
    assert.equal(s.bannerVisible, false, 'the banner must not appear on the login screen');
  });

  test('a successful login leaves the session healthy', () => {
    const s = makeSessionState({ loggedIn: false });
    s.attemptLogin(200);
    assert.equal(s.sessionExpired, false);
    assert.equal(s.bannerVisible, false);
  });
});

// ── Other error statuses must not be mistaken for expiry ─────────────────────

describe('non-401 save failures are not treated as session expiry', () => {
  for (const status of [409, 500, 507]) {
    test(`a ${status} leaves the session unexpired`, () => {
      const s = makeSessionState();
      s.autoSave(status);
      assert.equal(s.sessionExpired, false, `${status} must not expire the session`);
      assert.equal(s.bannerVisible, false, `${status} must not raise the banner`);
    });
  }

  test('a successful save keeps the session healthy', () => {
    const s = makeSessionState();
    s.autoSave(200);
    assert.equal(s.sessionExpired, false);
    assert.equal(s.saveStatus, 'saved');
  });
});

// ── The retry loop stops once the session is dead ────────────────────────────

describe('editing after expiry does not hammer the server', () => {
  test('scheduleAutoSave is a no-op once expired', () => {
    const s = makeSessionState();
    s.autoSave(401);
    s.scheduleAutoSave();
    assert.equal(s.saveTimerArmed, false, 'no new timer may be armed after expiry');
    assert.equal(s.saveStatus, 'error', 'status must stay error, not flip to saving');
  });

  test('autoSave is a no-op once expired — no further network calls', () => {
    const s = makeSessionState();
    s.autoSave(401);
    const before = s.networkSaves;
    s.autoSave(401);
    s.autoSave(401);
    assert.equal(s.networkSaves, before, 'no autosave may reach the network after expiry');
  });

  test('a full type-edit cycle after expiry stays silent', () => {
    const s = makeSessionState();
    s.autoSave(401);                       // first failed save expires the session
    for (let i = 0; i < 5; i++) {          // user keeps typing
      s.scheduleAutoSave();
      s.autoSave(401);
    }
    assert.equal(s.networkSaves, 1, 'only the original save should ever hit the network');
    assert.equal(s.handledCount, 1, 'expiry handled exactly once');
  });
});

// ── The 401 hook is central — every caller is covered ────────────────────────

describe('any authenticated request surfaces expiry, not just autosave', () => {
  test('a 401 from a title/tag edit also raises the banner', () => {
    const s = makeSessionState();
    // onTitleChange / onTagsChange call api() directly and swallow the error.
    try { s.api(401); } catch (e) { /* swallowed by .catch(() => null) */ }
    assert.equal(s.sessionExpired, true, 'a non-autosave 401 must still be caught');
    assert.equal(s.bannerVisible, true);
  });
});
