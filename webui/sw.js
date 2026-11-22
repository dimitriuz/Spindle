'use strict';
// Spindle service worker: lets the page be installed as an app, shows the
// "now playing" notifications, and keeps the app shell for a flaky network.
// The player's own data (/api/*) is never cached.
const CACHE = 'spindle-shell-v1';
const SHELL = ['/', 'app.css', 'app.js', 'themes.js', 'icons.js', 'icon.svg', 'icon-192.png', 'icon-512.png', 'PressStart2P.woff2', 'manifest.json'];

self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()).catch(() => self.skipWaiting()));
});

self.addEventListener('activate', (e) => {
  e.waitUntil(caches.keys().then((ks) => Promise.all(ks.filter((k) => k !== CACHE).map((k) => caches.delete(k)))).then(() => self.clients.claim()));
});

// network first, so a new build is picked up at once; the cache answers only when the network does not
self.addEventListener('fetch', (e) => {
  const u = new URL(e.request.url);
  if (e.request.method !== 'GET' || u.origin !== self.location.origin || u.pathname.startsWith('/api/')) return;
  e.respondWith(fetch(e.request).then((r) => {
    if (r.ok) { const copy = r.clone(); caches.open(CACHE).then((c) => c.put(e.request, copy)); }
    return r;
  }).catch(() => caches.match(e.request).then((m) => m || caches.match('/'))));
});

// tapping a notification brings the app forward
self.addEventListener('notificationclick', (e) => {
  e.notification.close();
  e.waitUntil(self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((cs) => {
    for (const c of cs) if ('focus' in c) return c.focus();
    return self.clients.openWindow('/#now');
  }));
});
