// API client (SPEC P1: the UI consumes only the public, documented API)
// with RFC 9457 problem handling and ETag/If-Match support. Live freshness is
// interval polling (see settings.ts / useRefreshInterval), not server push —
// the old SSE stream held a browser connection open and starved other fetches.

import { QueryClient } from '@tanstack/react-query'
import type { ZodType } from 'zod'

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

export async function parseError(res: Response): Promise<APIError> {
  try {
    const prob = await res.json()
    return new APIError(res.status, prob.code ?? 'unknown', prob.title ?? res.statusText, prob.detail ?? '')
  } catch {
    return new APIError(res.status, 'unknown', res.statusText, '')
  }
}

// Runtime-validation boundary: when a caller passes a zod schema we parse the
// decoded JSON through it instead of trusting the bare `as T` cast. A schema
// failure means the *server sent us something we can't model* — surfaced as a
// 502-shaped APIError so the UI's existing ErrorState path renders it like any
// other transport failure (rather than crashing deep inside a component).
// No schema → original behaviour (unchecked cast), so every existing call site
// is untouched.
export function validate<T>(json: unknown, schema?: ZodType<T>): T {
  if (!schema) return json as T
  const r = schema.safeParse(json)
  if (!r.success) {
    const detail = r.error.issues
      .slice(0, 5)
      .map((i) => `${i.path.join('.') || '(root)'}: ${i.message}`)
      .join('; ')
    throw new APIError(502, 'invalid_response', 'invalid server response', detail)
  }
  return r.data
}

export async function api<T>(
  path: string,
  init?: RequestInit & { etag?: number; schema?: ZodType<T> },
): Promise<T> {
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
  return validate(await res.json(), init?.schema)
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

// Pure ETag header → numeric version. Strips the quoting (and the optional
// weak-validator W/ prefix), defaults to 0 for missing/garbage so a later PUT
// still sends a well-formed If-Match. Exported for direct unit testing.
export function parseEtag(header: string | null): number {
  const n = parseInt(header?.replace(/^W\//, '').replaceAll('"', '') ?? '0', 10)
  return Number.isNaN(n) ? 0 : n
}

export async function getWithEtag<T>(path: string, schema?: ZodType<T>): Promise<Versioned<T>> {
  const res = await fetch(`/api/v1${path}`, { credentials: 'same-origin' })
  if (res.status === 401) {
    window.location.href = '/login'
    throw new APIError(401, 'auth', 'login required', '')
  }
  if (!res.ok) throw await parseError(res)
  const etag = parseEtag(res.headers.get('ETag'))
  return { data: validate(await res.json(), schema), etag }
}

// CRUD facade for the uniform named-resource endpoints
// (GET/POST /api/v1/<path>, GET/PUT/DELETE /api/v1/<path>/{name}).
// An optional `schema` validates the single-doc read (`get`) at the boundary —
// used for the dashboard doc, whose `spec` is opaque frontend-owned JSON most
// likely to come back malformed. List/create/update stay on the bare cast.
export function resourceApi<T extends { name: string }>(base: string, schema?: ZodType<T>) {
  return {
    queryKey: ['resources', base] as const,
    list: () => get<ListResponse<T>>(`/${base}?limit=500`).then((r) => r.items ?? []),
    get: (name: string) => getWithEtag<T>(`/${base}/${encodeURIComponent(name)}`, schema),
    create: (doc: T) => post<T>(`/${base}`, doc),
    update: (name: string, doc: T, etag: number) =>
      put<T>(`/${base}/${encodeURIComponent(name)}`, doc, etag),
    remove: (name: string) => del(`/${base}/${encodeURIComponent(name)}`),
  }
}

// Live freshness is interval polling, not server push. Each live view sets
// React Query's refetchInterval from the user's refresh setting (settings.ts).
// The previous SSE EventSource was removed: on HTTP/1.1 it held one of the
// browser's ~6 per-origin connections open for the tab's lifetime, so once a
// few accumulated across navigation every other fetch queued behind them and
// the whole UI hung. Polling has no held-open connection to leak.

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
