/* WSMS Gateway admin PWA service worker.

   IMPORTANT: authenticated /admin pages are NEVER cached. They can contain one-time
   secret reveals (new API keys, webhook/signing secrets) and PII (message log, unmasked
   MSISDNs). Writing those into persistent Cache Storage would defeat the one-time flash
   design, survive logout, and sit outside the server's PII purge. So only static,
   non-sensitive assets are cached; page navigations always go to the network. */
const CACHE = 'wsms-admin-v2';
const STATIC = [
  '/admin/static/admin.css',
  '/admin/static/htmx.min.js',
  '/admin/static/logo.svg',
  '/admin/static/icon-maskable.svg',
  '/admin/manifest.webmanifest',
];

self.addEventListener('install', function (e) {
  self.skipWaiting();
  e.waitUntil(caches.open(CACHE).then(function (c) { return c.addAll(STATIC).catch(function () {}); }));
});

self.addEventListener('activate', function (e) {
  e.waitUntil(
    caches.keys().then(function (keys) {
      return Promise.all(keys.filter(function (k) { return k !== CACHE; }).map(function (k) { return caches.delete(k); }));
    }).then(function () { return self.clients.claim(); })
  );
});

self.addEventListener('fetch', function (e) {
  var req = e.request;
  if (req.method !== 'GET') return;
  var url = new URL(req.url);
  if (url.origin !== self.location.origin) return;

  var isStatic = url.pathname.indexOf('/admin/static/') === 0 || url.pathname === '/admin/manifest.webmanifest';

  // Static assets only: stale-while-revalidate (serve cache fast, refresh in background).
  if (isStatic) {
    e.respondWith(
      caches.match(req).then(function (hit) {
        var net = fetch(req).then(function (res) {
          if (res && res.ok) { var cp = res.clone(); caches.open(CACHE).then(function (c) { c.put(req, cp); }); }
          return res;
        }).catch(function () { return hit; });
        return hit || net;
      })
    );
    return;
  }

  // Everything else under /admin (authed pages, POSTs' redirects, secret reveals, PII):
  // network only. Never cached, never replayed. Leave to the browser's default handling.
});
