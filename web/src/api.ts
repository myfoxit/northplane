// API client (SPEC P1: the UI consumes only the public, documented API)
// with RFC 9457 problem handling, ETag/If-Match support and the SSE
// live-update hook (SPEC §12.2: one multiplexed stream per tab, query
// cache patched per event instead of full refetches).

import { QueryClient } from '@tanstack/react-query'
import { useEffect, useRef } from 'react'

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 15_000, retry: 1, refetchOnWindowFocus: false },
  },
})

export class APIError extends Error {
  status: number
  code: string
  detail: string
  constructor(status: number, code: string, title: string, detail: string) {
    super(title)
    this.status = status
    this.code = code
    this.detail = detail
  }
}

async function parseError(res: Response): Promise<APIError> {
  try {
    const prob = await res.json()
    return new APIError(res.status, prob.code ?? 'unknown', prob.title ?? res.statusText, prob.detail ?? '')
  } catch {
    return new APIError(res.status, 'unknown', res.statusText, '')
  }
}

export async function api<T>(path: string, init?: RequestInit & { etag?: number }): Promise<T> {
  const headers: Record<string, string> = { ...(init?.headers as Record<string, string>) }
  if (init?.body && !headers['Content-Type']) headers['Content-Type'] = 'application/json'
  if (init?.etag) headers['If-Match'] = `"${init.etag}"`
  const res = await fetch(`/api/v1${path}`, { ...init, headers, credentials: 'same-origin' })
  if (res.status === 401) {
    window.location.href = '/login'
    throw new APIError(401, 'auth', 'login required', '')
  }
  if (!res.ok) throw await parseError(res)
  if (res.status === 204) return undefined as T
  return res.json()
}

export const get = <T,>(path: string) => api<T>(path)
export const post = <T,>(path: string, body?: unknown) =>
  api<T>(path, { method: 'POST', body: body === undefined ? undefined : JSON.stringify(body) })
export const put = <T,>(path: string, body: unknown, etag: number) =>
  api<T>(path, { method: 'PUT', body: JSON.stringify(body), etag })
export const del = (path: string) => api<void>(path, { method: 'DELETE' })

export interface ListResponse<T> {
  items: T[] | null
  nextCursor?: string
}

// Named-resource documents (templates, rules, channels, dashboards, …)
// carry their version in the ETag response header — GET must capture it
// so the subsequent PUT can send If-Match (SPEC §11.1 optimistic locking).
export interface Versioned<T> {
  data: T
  etag: number
}

export async function getWithEtag<T>(path: string): Promise<Versioned<T>> {
  const res = await fetch(`/api/v1${path}`, { credentials: 'same-origin' })
  if (res.status === 401) {
    window.location.href = '/login'
    throw new APIError(401, 'auth', 'login required', '')
  }
  if (!res.ok) throw await parseError(res)
  const etag = parseInt(res.headers.get('ETag')?.replaceAll('"', '') ?? '0', 10)
  return { data: await res.json(), etag: Number.isNaN(etag) ? 0 : etag }
}

// CRUD facade for the uniform named-resource endpoints
// (GET/POST /api/v1/<path>, GET/PUT/DELETE /api/v1/<path>/{name}).
export function resourceApi<T extends { name: string }>(base: string) {
  return {
    queryKey: ['resources', base] as const,
    list: () => get<ListResponse<T>>(`/${base}?limit=500`).then((r) => r.items ?? []),
    get: (name: string) => getWithEtag<T>(`/${base}/${encodeURIComponent(name)}`),
    create: (doc: T) => post<T>(`/${base}`, doc),
    update: (name: string, doc: T, etag: number) =>
      put<T>(`/${base}/${encodeURIComponent(name)}`, doc, etag),
    remove: (name: string) => del(`/${base}/${encodeURIComponent(name)}`),
  }
}

// Live updates: one EventSource per tab; events invalidate the matching
// query keys. Visibility throttling per SPEC §12.2.
const invalidations: Record<string, string[][]> = {
  state_change: [['problems'], ['objects'], ['overview'], ['events']],
  alert_opened: [['alerts'], ['overview'], ['events']],
  alert_resolved: [['alerts'], ['overview'], ['problems'], ['events']],
  ack: [['alerts'], ['problems'], ['events']],
  notification: [['events']],
  escalation: [['events']],
  incident_update: [['incidents'], ['overview']],
  downtime: [['downtimes'], ['problems']],
  silence: [['silences']],
  config: [['objects'], ['rules'], ['resources']],
  heartbeat_missed: [['heartbeats'], ['events']],
  flapping_start: [['problems'], ['objects']],
  flapping_end: [['problems'], ['objects']],
}

// Event types the client consumes — sent as ?types= so the server doesn't
// push (and buffer) unrelated tenant events, which is what arms a `resync`
// when the per-subscriber buffer overflows.
const streamTypes = Object.keys(invalidations).join(',')
// All live query keys, for a `resync` (missed-events signal): refresh just
// these through the throttled flush instead of nuking the whole query cache.
const liveKeys = Array.from(new Set(Object.values(invalidations).flat().map((k) => k[0])))

export function useLiveUpdates(onEvent?: (type: string, data: unknown) => void) {
  const handler = useRef(onEvent)
  handler.current = onEvent
  useEffect(() => {
    let es: EventSource | null = null
    let backoff = 1000
    let closed = false
    const pending = new Set<string>()
    let flushTimer: number | undefined

    const flush = () => {
      flushTimer = undefined
      if (document.hidden) return // throttle while invisible
      for (const key of pending) queryClient.invalidateQueries({ queryKey: [key] })
      pending.clear()
    }

    const connect = () => {
      if (closed) return
      es = new EventSource('/api/v1/stream?types=' + encodeURIComponent(streamTypes))
      es.onopen = () => { backoff = 1000 }
      es.onerror = () => {
        es?.close()
        if (!closed) setTimeout(connect, backoff = Math.min(backoff * 2, 30000))
      }
      for (const type of Object.keys(invalidations)) {
        es.addEventListener(type, (ev) => {
          for (const key of invalidations[type]) pending.add(key[0])
          if (!flushTimer) flushTimer = window.setTimeout(flush, 400)
          try { handler.current?.(type, JSON.parse((ev as MessageEvent).data)) } catch { /* ignore */ }
        })
      }
      es.addEventListener('resync', () => {
        // Missed events: refresh the live keys via the same throttled,
        // visibility-gated flush — not invalidateQueries() with no key,
        // which refetches every mounted query at once.
        for (const key of liveKeys) pending.add(key)
        if (!flushTimer) flushTimer = window.setTimeout(flush, 400)
      })
    }
    connect()
    const onVisible = () => { if (!document.hidden) flush() }
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      closed = true
      es?.close()
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [])
}

export function fmtTime(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'short', timeStyle: 'medium',
  }).format(d)
}

export function fmtAgo(iso?: string): string {
  if (!iso) return '—'
  const secs = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000)
  if (secs < 60) return `${Math.floor(secs)}s`
  if (secs < 3600) return `${Math.floor(secs / 60)}m`
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m`
  return `${Math.floor(secs / 86400)}d ${Math.floor((secs % 86400) / 3600)}h`
}
