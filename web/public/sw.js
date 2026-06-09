// Northplane PWA service worker (ADR-12): caches immutable, content-hashed
// build assets for fast repeat loads + delivers Web Push alerts.
//
// Deliberately NARROW. It only ever touches same-origin /assets/<hash>.<ext>
// files, which are content-addressed (a new build ⇒ new filenames), so the
// cache can never serve a stale asset. The SPA shell (index.html / every
// navigation), the API and the SSE stream are NEVER intercepted — they
// always hit the network. The previous version precached "/" under a static
// cache name and, on any fetch failure, returned that cached index.html for
// *every* request: after a redeploy the shell pointed at chunk names that no
// longer existed, the 404s fell back to HTML, and the browser tried to
// execute HTML as a JS module — wedging routes/modals (black screens, multi-
// second hangs) until the worker was manually unregistered. Bumping CACHE
// purges that old cache on activate so existing installs self-heal.
const CACHE = 'np-assets-v2'

self.addEventListener('install', () => self.skipWaiting())

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  )
})

self.addEventListener('fetch', (event) => {
  if (event.request.method !== 'GET') return
  const url = new URL(event.request.url)
  // Cache-first ONLY for same-origin hashed build assets; everything else
  // (shell, navigations, /api, /auth, the stream, the manifest) goes straight
  // to the network so nothing stale is ever served.
  if (url.origin !== self.location.origin || !url.pathname.startsWith('/assets/')) return
  event.respondWith(
    caches.match(event.request).then((hit) =>
      hit || fetch(event.request).then((res) => {
        if (res.ok) {
          const copy = res.clone()
          caches.open(CACHE).then((c) => c.put(event.request, copy))
        }
        return res
      }),
    ),
  )
})

self.addEventListener('push', (event) => {
  let data = {}
  try { data = event.data.json() } catch { data = { title: 'Northplane', body: event.data?.text() } }
  const actions = []
  if (data.ackUrl) actions.push({ action: 'ack', title: 'Quittieren' })
  event.waitUntil(self.registration.showNotification(data.title || 'Northplane', {
    body: data.body || '',
    tag: data.url || 'northplane',
    data,
    actions,
    badge: undefined,
    requireInteraction: data.severity === 'critical',
  }))
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const data = event.notification.data || {}
  if (event.action === 'ack' && data.ackUrl) {
    event.waitUntil(fetch(data.ackUrl))
    return
  }
  if (data.url) event.waitUntil(clients.openWindow(data.url))
})
