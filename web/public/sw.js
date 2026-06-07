// Northplane PWA service worker (ADR-12): app-shell cache for offline
// display + Web Push notifications with ack action.
const CACHE = 'np-shell-v1'

self.addEventListener('install', (event) => {
  event.waitUntil(caches.open(CACHE).then((c) => c.addAll(['/'])))
  self.skipWaiting()
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)))),
  )
  self.clients.claim()
})

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url)
  // never cache API or auth — network only
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/auth/')) return
  if (event.request.method !== 'GET') return
  event.respondWith(
    fetch(event.request)
      .then((res) => {
        if (res.ok && url.pathname.startsWith('/assets/')) {
          const copy = res.clone()
          caches.open(CACHE).then((c) => c.put(event.request, copy))
        }
        return res
      })
      .catch(() => caches.match(event.request).then((hit) => hit ?? caches.match('/'))),
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
