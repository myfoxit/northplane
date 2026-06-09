// Live-data refresh cadence — the polling interval that replaced the old SSE
// push (which held a browser connection open and starved every other request).
// Each "live" list view feeds this into React Query's refetchInterval, so the
// UI stays fresh on a cadence the user controls instead of a fragile stream.
//
// It's a plain client setting today (persisted in localStorage, synced across
// tabs). Because every feature should be AI/MCP-configurable, the intended
// follow-up is to back this with server-side config so an agent can set it;
// keep all reads/writes going through this module so that swap stays local.
import { useSyncExternalStore } from 'react'

export type RefreshValue = number | false // milliseconds, or false = off (manual refresh only)

export const REFRESH_PRESETS: { label: string; value: RefreshValue }[] = [
  { label: '5s', value: 5_000 },
  { label: '10s', value: 10_000 },
  { label: '30s', value: 30_000 },
  { label: '60s', value: 60_000 },
  { label: 'Aus', value: false },
]

const KEY = 'np.refreshInterval'
const DEFAULT: RefreshValue = 30_000

function read(): RefreshValue {
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

let current: RefreshValue = read()
const listeners = new Set<() => void>()
const emit = () => { for (const l of listeners) l() }

export function setRefreshInterval(v: RefreshValue): void {
  current = v
  try { localStorage.setItem(KEY, v === false ? 'off' : String(v)) } catch { /* ignore */ }
  emit()
}

// Adopt a change made in another tab so every open view shares one cadence.
if (typeof window !== 'undefined') {
  window.addEventListener('storage', (e) => {
    if (e.key === KEY) { current = read(); emit() }
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
