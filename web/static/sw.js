/* thornotes — service worker */
'use strict';

const CACHE = 'thornotes-v0.19.2.0';

const STATIC_ASSETS = [
  '/',
  '/static/css/font-awesome.min.css',
  '/static/css/highlight-github.min.css',
  '/static/css/highlight-github-dark.min.css',
  '/static/fonts/fontawesome-webfont.woff2',
  '/static/js/app.js',
  '/static/js/share.js',
  '/static/js/vendor/codemirror6.min.js',
  '/static/js/vendor/marked.min.js',
  '/static/js/vendor/highlight.min.js',
  '/static/manifest.json',
  '/static/icons/icon-192.svg',
  '/static/icons/icon-512.svg',
];

// Install: cache all static assets.
self.addEventListener('install', e => {
  e.waitUntil(
    caches.open(CACHE)
      .then(c => c.addAll(STATIC_ASSETS))
      .then(() => self.skipWaiting())
  );
});

// Activate: remove old cache versions.
self.addEventListener('activate', e => {
  e.waitUntil(
    caches.keys()
      .then(keys => Promise.all(
        keys.filter(k => k !== CACHE).map(k => caches.delete(k))
      ))
      .then(() => self.clients.claim())
  );
});

self.addEventListener('fetch', e => {
  const url = new URL(e.request.url);

  // Never intercept API calls, SSE, or MCP — always go to network.
  if (
    url.pathname.startsWith('/api/') ||
    url.pathname.startsWith('/mcp') ||
    url.pathname.startsWith('/s/')
  ) {
    return;
  }

  // Cache-first for static assets (CSS, JS, fonts, icons).
  if (url.pathname.startsWith('/static/') || url.pathname === '/sw.js') {
    e.respondWith(
      caches.match(e.request).then(cached => {
        if (cached) return cached;
        return fetch(e.request).then(res => {
          const clone = res.clone();
          caches.open(CACHE).then(c => c.put(e.request, clone));
          return res;
        });
      })
    );
    return;
  }

  // Network-first for app shell — fall back to cached shell when offline.
  e.respondWith(
    fetch(e.request).catch(() => caches.match('/'))
  );
});
