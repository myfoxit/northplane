// Service-worker KILL SWITCH (intentionally does nothing useful).
//
// Northplane no longer ships a service worker: a monitoring console has no
// offline use case, and the previous hand-rolled caching worker repeatedly
// wedged the app by serving a stale app shell (old chunk hashes → 404 →
// HTML returned for JS → black screens / multi-second hangs). A worker lives
// in the browser, so simply deleting this file would NOT remove already-
// installed copies. This stub stays so any browser still running an old
// worker fetches it on its next update check, unregisters itself, drops every
// cache, and reloads — cleaning up automatically. No fetch/push handlers.
self.addEventListener('install', () => self.skipWaiting())

self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    try { await self.registration.unregister() } catch { /* best effort */ }
    const keys = await caches.keys()
    await Promise.all(keys.map((k) => caches.delete(k)))
    const clients = await self.clients.matchAll({ type: 'window' })
    for (const client of clients) client.navigate(client.url)
  })())
})
