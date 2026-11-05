'use strict';
const { describe, test } = require('node:test');
const assert = require('node:assert/strict');

// Tests for the stale-CSRF-token recovery in api() (web/static/js/app.js).
//
// Failure mode being guarded: the CSRF token is per-SESSION, not per-tab, and
// GET /api/v1/csrf rotates it server-side. With one token in a module-level
// variable, reloading tab A revoked tab B's copy, and tab B's next write was
// rejected with a bare 403 the UI could not read as "stale token" — so the
// edit was lost. The server now marks that rejection with
// {"code":"csrf_invalid"} and api() refetches + replays exactly once.
//
// What must stay true:
//   - 403 + code, then 200  -> two writes and one /api/v1/csrf, new token on
//                              the replay, call resolves.
//   - 403 without the code  -> one write, no refresh, error thrown (a real
//                              permission refusal must not be replayed).
//   - 403 + code twice      -> exactly one retry, never a loop.
//   - GET                   -> never retried (no token is sent anyway).
//   - refresh fails         -> the original 403 surfaces, not a refresh error.
//
// As with session-expiry.test.js and default-note-view.test.js, app.js is a
// browser script that cannot be required here, so the logic is mirrored in a
// self-contained client. Keep makeClient() in sync with api() in app.js.

const CSRF_ERROR_CODE = 'csrf_invalid';

// makeNet wires a fake transport. routes maps path -> list of responses; the
// last entry repeats, so an unexpected extra request shows up in net.log
// rather than as a confusing crash.
function makeNet(routes) {
  const log = [];
  async function fetch(path, opts = {}) {
    log.push({
      path,
      method: opts.method || 'GET',
      headers: Object.assign({}, opts.headers || {}),
      body: opts.body,
    });
    const seq = routes[path];
    if (!seq || seq.length === 0) throw new Error('unexpected request: ' + path);
    const r = seq.length > 1 ? seq.shift() : seq[0];
    return {
      ok: r.status >= 200 && r.status < 300,
      status: r.status,
      json: async () => r.body,
    };
  }
  return { fetch, log };
}

// makeClient mirrors api(), refreshCSRFToken(), setCSRFToken() and the 401 hook
// from app.js.
function makeClient(net, { startToken = 'token-old', loggedIn = true } = {}) {
  let csrfToken = '';
  let csrfFetchedAt = 0;
  let currentUser = loggedIn ? { username: 'u' } : null;
  let sessionExpired = false;
  let csrfFetches = 0;

  function setCSRFToken(token) {
    csrfToken = token;
    csrfFetchedAt = Date.now();
  }
  setCSRFToken(startToken);

  function handleSessionExpired() {
    if (sessionExpired) return;
    sessionExpired = true;
  }

  async function refreshCSRFToken() {
    csrfFetches++;
    const res = await net.fetch('/api/v1/csrf', { credentials: 'same-origin' });
    if (!res.ok) throw new Error('csrf refresh failed: HTTP ' + res.status);
    const data = await res.json();
    if (!data || !data.csrf_token) throw new Error('csrf refresh: no token in response');
    setCSRFToken(data.csrf_token);
    return data.csrf_token;
  }

  async function api(method, path, body) {
    const mutating = method !== 'GET' && method !== 'HEAD';

    const send = () => {
      const opts = {
        method,
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
      };
      if (csrfToken && mutating) {
        opts.headers['X-CSRF-Token'] = csrfToken;
      }
      if (body !== undefined) {
        opts.body = JSON.stringify(body);
      }
      return net.fetch(path, opts);
    };

    const readBody = async (res) => {
      try { return await res.json(); } catch { return {}; }
    };

    let res = await send();
    let data = await readBody(res);

    if (res.status === 403 && data && data.code === CSRF_ERROR_CODE && mutating) {
      try {
        await refreshCSRFToken();
        res = await send();
        data = await readBody(res);
      } catch (_) {
        // Fall through and report the original rejection.
      }
    }

    if (res.status === 401 && currentUser) {
      handleSessionExpired();
    }

    if (!res.ok) {
      const err = new Error(data.error || 'HTTP ' + res.status);
      err.status = res.status;
      throw err;
    }
    return data;
  }

  return {
    api,
    refreshCSRFToken,
    state: () => ({ csrfToken, csrfFetches, csrfFetchedAt, sessionExpired, currentUser }),
  };
}

const ok = (body) => ({ status: 200, body });
const csrfStale = { status: 403, body: { error: 'invalid csrf token', code: 'csrf_invalid' } };
const csrfBytes = { status: 200, body: { csrf_token: 'token-new' } };

describe('api() stale-CSRF replay', () => {
  test('403 with code: refetches and replays once with the new token', async () => {
    const net = makeNet({
      '/api/v1/notes/7': [csrfStale, ok({ content_hash: 'sha-2' })],
      '/api/v1/csrf': [csrfBytes],
    });
    const client = makeClient(net);

    const result = await client.api('PATCH', '/api/v1/notes/7', { content: 'hi', content_hash: 'sha-1' });

    assert.deepEqual(result, { content_hash: 'sha-2' });
    assert.equal(net.log.length, 3, 'write, token refetch, write — nothing else');
    assert.deepEqual(net.log.map((c) => c.path), ['/api/v1/notes/7', '/api/v1/csrf', '/api/v1/notes/7']);

    // The stale token went out first, the fresh one on the replay.
    assert.equal(net.log[0].headers['X-CSRF-Token'], 'token-old');
    assert.equal(net.log[2].headers['X-CSRF-Token'], 'token-new');
    assert.equal(client.state().csrfToken, 'token-new');

    // The body is replayed byte for byte — a PATCH must not lose its content.
    assert.equal(net.log[2].method, 'PATCH');
    assert.equal(net.log[2].body, net.log[0].body);

    assert.equal(client.state().csrfFetches, 1);
  });

  test('POST create is replayed too (CSRF is rejected before the handler writes)', async () => {
    const net = makeNet({
      '/api/v1/notes': [csrfStale, ok({ id: 42, content_hash: 'sha-1' })],
      '/api/v1/csrf': [csrfBytes],
    });
    const client = makeClient(net);

    const created = await client.api('POST', '/api/v1/notes', { title: 'new', content: '' });

    assert.equal(created.id, 42);
    // Two POSTs reached the network, but only the second one ever got past the
    // middleware to the handler — the first was rejected before create ran.
    const posts = net.log.filter((c) => c.method === 'POST' && c.path === '/api/v1/notes');
    assert.equal(posts.length, 2);
  });

  test('403 without the code: one request, no refresh, error surfaces', async () => {
    const net = makeNet({
      '/api/v1/notes/7': [{ status: 403, body: { error: 'forbidden' } }],
      '/api/v1/csrf': [csrfBytes],
    });
    const client = makeClient(net);

    await assert.rejects(
      () => client.api('PATCH', '/api/v1/notes/7', { content: 'hi' }),
      (err) => {
        assert.equal(err.status, 403);
        assert.equal(err.message, 'forbidden');
        return true;
      },
    );

    assert.equal(net.log.length, 1, 'no refetch, no replay');
    assert.equal(client.state().csrfFetches, 0);
  });

  test('a second 403 with code is not retried again (no loop)', async () => {
    const net = makeNet({
      '/api/v1/notes/7': [csrfStale, csrfStale],
      '/api/v1/csrf': [csrfBytes],
    });
    const client = makeClient(net);

    await assert.rejects(() => client.api('PATCH', '/api/v1/notes/7', { content: 'hi' }));

    const writes = net.log.filter((c) => c.path === '/api/v1/notes/7');
    assert.equal(writes.length, 2, 'original attempt + exactly one replay');
    assert.equal(client.state().csrfFetches, 1);
  });

  test('GET is never replayed', async () => {
    const net = makeNet({
      '/api/v1/notes/7': [{ status: 403, body: { error: 'invalid csrf token', code: 'csrf_invalid' } }],
      '/api/v1/csrf': [csrfBytes],
    });
    const client = makeClient(net);

    await assert.rejects(() => client.api('GET', '/api/v1/notes/7'));
    assert.equal(net.log.length, 1);
    assert.equal(client.state().csrfFetches, 0);
  });

  test('a failed refresh reports the original 403, not a refresh error', async () => {
    const net = makeNet({
      '/api/v1/notes/7': [csrfStale],
      '/api/v1/csrf': [{ status: 401, body: { error: 'unauthorized' } }],
    });
    const client = makeClient(net);

    await assert.rejects(
      () => client.api('PATCH', '/api/v1/notes/7', { content: 'hi' }),
      (err) => {
        assert.equal(err.status, 403);
        assert.equal(err.message, 'invalid csrf token');
        return true;
      },
    );

    // One write + one failed refresh attempt; no replay against a dead session.
    assert.deepEqual(net.log.map((c) => c.path), ['/api/v1/notes/7', '/api/v1/csrf']);
  });

  test('a successful request never touches the token endpoint', async () => {
    const net = makeNet({
      '/api/v1/notes/7': [ok({ content_hash: 'sha-1' })],
      '/api/v1/csrf': [csrfBytes],
    });
    const client = makeClient(net);

    await client.api('PATCH', '/api/v1/notes/7', { content: 'hi' });
    assert.equal(net.log.length, 1);
    assert.equal(client.state().csrfFetches, 0);
  });
});

describe('token freshness clock', () => {
  // The focus path is intentionally not exercised here (it needs a DOM); this
  // pins the invariant it relies on: every path that installs a token also
  // resets csrfFetchedAt, so "age" can never be measured from a stale clock.
  test('refreshCSRFToken stamps the fetch time', async () => {
    const net = makeNet({ '/api/v1/csrf': [csrfBytes] });
    const client = makeClient(net, { startToken: '' });

    const before = client.state().csrfFetchedAt;
    await client.refreshCSRFToken();

    assert.equal(client.state().csrfToken, 'token-new');
    assert.ok(client.state().csrfFetchedAt >= before);
    assert.ok(Date.now() - client.state().csrfFetchedAt < 5000);
  });
});
