'use strict';
/* thornotes desktop — pure helpers.
   No Electron, no Node built-ins — safe to require() in Node for unit tests
   and to import() in an Electron renderer via the preload bridge. */

/**
 * Validate and normalise a thornotes server URL entered by the user.
 *
 * Rules:
 *  - Must be non-empty after trimming.
 *  - Must parse as a valid URL.
 *  - Protocol must be http: or https:.
 *  - Trailing slashes are removed from the normalised result.
 *
 * @param {string|null|undefined} raw
 * @returns {{ ok: true, url: string } | { ok: false, error: string }}
 */
function validateServerUrl(raw) {
  const url = (raw == null ? '' : String(raw)).trim().replace(/\/+$/, '');

  if (!url) {
    return { ok: false, error: 'Server URL is required.' };
  }

  let parsed;
  try {
    parsed = new URL(url);
  } catch {
    return { ok: false, error: 'Invalid URL — enter something like http://localhost:8080' };
  }

  if (!['http:', 'https:'].includes(parsed.protocol)) {
    return { ok: false, error: 'URL must start with http:// or https://' };
  }

  return { ok: true, url };
}

/**
 * Shallow-merge a partial update into an existing config object.
 * Returns a *new* object — neither argument is mutated.
 *
 * @param {object} existing
 * @param {object} update
 * @returns {object}
 */
function mergeConfig(existing, update) {
  return Object.assign({}, existing, update);
}

// CommonJS export — no-op in browser / Electron renderer contexts.
if (typeof module !== 'undefined') {
  module.exports = { validateServerUrl, mergeConfig };
}
