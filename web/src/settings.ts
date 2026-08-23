// Live-data refresh cadence — the polling interval that replaced the old SSE
// push (which held a browser connection open and starved every other request).
// Each "live" list view feeds this into React Query's refetchInterval, so the
// UI stays fresh on a cadence the user controls instead of a fragile stream.
//
// The setting is server-backed (P1 parity: PUT /users/me/preferences — the
// same knob an API client or MCP agent can set) with localStorage as an
// instant-boot cache: the UI starts on the cached value, adopts the server
// value once syncPreferencesFromServer() resolves (Layout mount), and every
// local change is written through to both.
//
// This module also owns the SHARED preferences document. A PUT replaces the
// whole doc, so anything else that wants to persist a per-user setting (the
// branding axes in theme.ts / mode.ts) goes through setExtraPref() here rather
// than PUTting on its own — one writer, one merged document, no lost keys.
import { useSyncExternalStore } from 'react'
import { api } from './api'
import { t } from './i18n'

export type RefreshValue = number | false // milliseconds, or false = off (manual refresh only)

export const REFRESH_PRESETS: { label: string; value: RefreshValue }[] = [
  { label: '5s', value: 5_000 },
  { label: '10s', value: 10_000 },
  { label: '30s', value: 30_000 },
  { label: '60s', value: 60_000 },
  { label: t('off'), value: false },
]

// Mirrors model.Preferences (types.gen.ts Preferences).
type Preferences = { refreshIntervalMs?: number; extra?: Record<string, string> }

const KEY = 'np.refreshInterval'
const DEFAULT: RefreshValue = 30_000

function readCache(): RefreshValue {
  try {
    const raw = localStorage.getItem(KEY)
    if (raw === 'off') return false
    if (raw != null) {
      const n = Number(raw)
      if (Number.isFinite(n) && n >= 1_000) return n
    }
  } catch { /* localStorage may be unavailable (private mode) — fall back */ }
  return DEFAULT
}

function writeCache(v: RefreshValue): void {
  try { localStorage.setItem(KEY, v === false ? 'off' : String(v)) } catch { /* ignore */ }
}

let current: RefreshValue = readCache()
// Last server document — preserved on write so refreshIntervalMs updates
// don't clobber other preference keys (PUT replaces the whole doc).
let serverPrefs: Preferences = {}
const listeners = new Set<() => void>()
const emit = () => { for (const l of listeners) l() }

// wire-format mapping: 0 = off (false); undefined = not set on the server.
const fromWire = (ms: number | undefined | null): RefreshValue | undefined =>
  ms === undefined || ms === null ? undefined : ms === 0 ? false : ms
const toWire = (v: RefreshValue): number => (v === false ? 0 : v)

// syncPreferencesFromServer adopts the authoritative server value (e.g. set
// by an admin or an MCP agent). Called once from the authenticated shell;
// failures (offline, demo) keep the cached value — never disruptive.
export async function syncPreferencesFromServer(): Promise<void> {
  try {
    serverPrefs = (await api<Preferences>('/users/me/preferences')) ?? {}
    const v = fromWire(serverPrefs.refreshIntervalMs)
    if (v !== undefined && v !== current) {
      current = v
      writeCache(v)
      emit()
    }
    synced = true
    const extra = serverPrefs.extra ?? {}
    for (const adopt of adopters) adopt(extra)
  } catch { /* keep cache */ }
}

// ── shared `extra` bag ────────────────────────────────────────────────────
// Free-form string settings that need no schema change (model.Preferences
// .Extra). Used by the branding axes so a user's look follows them to every
// browser and device instead of living only in one localStorage.

type Adopter = (extra: Record<string, string>) => void
const adopters = new Set<Adopter>()
let synced = false

// onPreferencesSynced registers a callback fired once the authoritative
// server document arrives. Modules use it to adopt a value saved elsewhere;
// they must apply it WITHOUT writing back (nothing changed server-side).
export function onPreferencesSynced(adopt: Adopter): void {
  adopters.add(adopt)
  // A module imported after the sync already resolved still needs the value.
  if (synced) adopt(serverPrefs.extra ?? {})
}

// setExtraPref writes one `extra` key through to the server, preserving every
// other key in the document (PUT replaces the whole thing). Fire-and-forget:
// the caller has already applied the value locally, so a failed write only
// means other devices won't pick it up until the next change.
export function setExtraPref(key: string, value: string): void {
  const extra = { ...(serverPrefs.extra ?? {}), [key]: value }
  if (serverPrefs.extra?.[key] === value) return
  serverPrefs = { ...serverPrefs, extra }
  void api('/users/me/preferences', { method: 'PUT', body: JSON.stringify(serverPrefs) })
    .catch(() => { /* offline — localStorage still holds the value */ })
}

export function setRefreshInterval(v: RefreshValue): void {
  current = v
  writeCache(v)
  emit()
  // write-through to the server; fire-and-forget (the local value already
  // applied — a failed write just means other devices won't pick it up).
  serverPrefs = { ...serverPrefs, refreshIntervalMs: toWire(v) }
  void api('/users/me/preferences', { method: 'PUT', body: JSON.stringify(serverPrefs) })
    .catch(() => { /* offline — cache still holds the value */ })
}

// Adopt a change made in another tab so every open view shares one cadence.
if (typeof window !== 'undefined') {
  window.addEventListener('storage', (e) => {
    if (e.key === KEY) { current = readCache(); emit() }
  })
}

function subscribe(cb: () => void): () => void {
  listeners.add(cb)
  return () => { listeners.delete(cb) }
}

// Reactive read: components re-render (and React Query picks up the new
// interval) the moment the setting changes.
export function useRefreshInterval(): RefreshValue {
  return useSyncExternalStore(subscribe, () => current, () => DEFAULT)
}
