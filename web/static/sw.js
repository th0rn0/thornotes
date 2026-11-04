/* thornotes — service worker */
'use strict';

// ASSET_VERSION is substituted server-side on every response with a digest of
// the embedded static tree. It has to appear in these bytes: a browser only
// re-runs install() when the script changes, so a version baked in at authoring
// time would mean a deploy never rotates Cache Storage — the bug this worker is
// written against.
const ASSET_VERSION = '__ASSET_VERSION__';

// One cache per asset version. A cache name that does not move with the bytes
// is exactly what let a released bundle outlive several deploys.
function cacheNameFor(version) {
  return 'thornotes-' + version;
}

// Versioned URL for a static path. The URL changes when the assets do, which is
// what makes a long max-age safe and a stale body impossible.
function assetUrl(path) {
  return path + '?v=' + ASSET_VERSION;
}

// True when a static URL carries this worker's version. Anything else is an
// unversioned or stale URL and must go to the network.
function isCurrentVersion(url) {
  return url.searchParams.get('v') === ASSET_VERSION;
}

const CACHE = cacheNameFor(ASSET_VERSION);

// Precache versioned asset URLs only. The shell ('/') is deliberately absent:
// it must come from the network so it can pin this version's asset URLs.
const STATIC_ASSETS = [
  assetUrl('/static/css/font-awesome.min.css'),
  assetUrl('/static/css/highlight-github.min.css'),
  assetUrl('/static/css/highlight-github-dark.min.css'),
  assetUrl('/static/css/highlight-catppuccin-mocha.min.css'),
  assetUrl('/static/fonts/fontawesome-webfont.woff2'),
  assetUrl('/static/js/app.js'),
  assetUrl('/static/js/share.js'),
  assetUrl('/static/js/vendor/codemirror6.min.js'),
  assetUrl('/static/js/vendor/marked.min.js'),
  assetUrl('/static/js/vendor/highlight.min.js'),
  assetUrl('/static/manifest.json'),
  assetUrl('/static/icons/icon-192.svg'),
  assetUrl('/static/icons/icon-512.svg'),
];

// Cache key for the last shell fetched from the network. One synthetic key is
// shared by every navigation, whatever its deep-link URL.
const SHELL_KEY = '/';

// Install: cache this version's assets only.
self.addEventListener('install', e => {
  e.waitUntil(
    caches.open(CACHE)
      .then(c => c.addAll(STATIC_ASSETS))
      .then(() => self.skipWaiting())
  );
});

// Activate: drop every cache that is not this version's.
self.addEventListener('activate', e => {
  e.waitUntil(
    caches.keys()
      .then(keys => Promise.all(
        keys.filter(k => k !== CACHE).map(k => caches.delete(k))
      ))
      .then(() => self.clients.claim())
  );
});

// Cache-first, but only ever reached for a URL that already carries this
// version, so the cached copy and the network copy are the same bytes.
function cacheFirst(e) {
  return caches.match(e.request).then(cached => {
    if (cached) return cached;
    return fetch(e.request).then(res => {
      if (res && res.ok) {
        const clone = res.clone();
        caches.open(CACHE).then(c => c.put(e.request, clone)).catch(() => {});
      }
      return res;
    });
  });
}

// Network-first with a cached-shell fallback: online the shell is always fresh,
// offline the last one still opens the app.
function networkFirstShell(req) {
  return caches.open(CACHE).then(cache =>
    fetch(req)
      .then(res => {
        if (res && res.ok) {
          const clone = res.clone();
          cache.put(SHELL_KEY, clone).catch(() => {});
        }
        return res;
      })
      .catch(err => cache.match(SHELL_KEY).then(cached => {
        if (cached) return cached;
        throw err;
      }))
  );
}

self.addEventListener('fetch', e => {
  // Only same-origin GETs are ours to answer.
  if (e.request.method !== 'GET') return;

  const url = new URL(e.request.url);
  if (url.origin !== self.location.origin) return;

  // Never intercept API calls, SSE, MCP or shared-note views — always network.
  if (
    url.pathname.startsWith('/api/') ||
    url.pathname.startsWith('/mcp') ||
    url.pathname.startsWith('/s/')
  ) {
    return;
  }

  if (url.pathname.startsWith('/static/')) {
    // Unversioned or stale: leave it to the network. Serving it out of this
    // cache is how an old bundle survived a deploy.
    if (!isCurrentVersion(url)) return;
    e.respondWith(cacheFirst(e));
    return;
  }

  // The worker script itself is never served from the cache: the browser has
  // to see new bytes in order to update.
  if (url.pathname === '/sw.js') return;

  // Navigations (the shell, including deep links) are network-first.
  if (e.request.mode === 'navigate' || url.pathname === SHELL_KEY) {
    e.respondWith(networkFirstShell(e.request));
  }
});
