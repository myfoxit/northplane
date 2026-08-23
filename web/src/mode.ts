// Light/dark MODE — the second theming axis alongside the colour theme
// (theme.ts). Mode toggles a `.light` class on <html>; the app's original look
// is DARK (values on :root, no class), so light mode is the opt-in class and
// the dark default is byte-for-byte unchanged. 'system' follows the OS via
// matchMedia. Like theme.ts the choice is persisted per USER (written through
// to /users/me/preferences via settings.ts) with localStorage as the
// instant-boot cache, applied on module load (imported early in main.tsx) so
// there's no flash on boot.
import { useSyncExternalStore } from 'react'
import { onPreferencesSynced, setExtraPref } from './settings'

export type Mode = 'light' | 'dark' | 'system'
export const MODES: Mode[] = ['system', 'light', 'dark']

const KEY = 'np.mode'
// Key inside the server-side preferences `extra` bag.
const PREF = 'mode'
const DEFAULT: Mode = 'dark' // preserves the app's original dark-only look

const prefersLight = (): boolean =>
  typeof window !== 'undefined' && !!window.matchMedia?.('(prefers-color-scheme: light)').matches

// The concrete light/dark a mode resolves to right now.
export function resolveMode(m: Mode): 'light' | 'dark' {
  return m === 'system' ? (prefersLight() ? 'light' : 'dark') : m
}

function readCache(): Mode {
  try {
    const raw = localStorage.getItem(KEY)
    if (raw === 'light' || raw === 'dark' || raw === 'system') return raw
  } catch { /* private mode — fall back */ }
  return DEFAULT
}

function apply(m: Mode): void {
  if (typeof document === 'undefined') return
  document.documentElement.classList.toggle('light', resolveMode(m) === 'light')
}

let current: Mode = readCache()
const listeners = new Set<() => void>()
const emit = () => { for (const l of listeners) l() }

apply(current)

// When mode is 'system', re-apply as the OS preference flips live.
if (typeof window !== 'undefined' && window.matchMedia) {
  window.matchMedia('(prefers-color-scheme: light)').addEventListener('change', () => {
    if (current === 'system') { apply(current); emit() }
  })
  // Adopt a change made in another tab.
  window.addEventListener('storage', (e) => {
    if (e.key === KEY) store(readCache(), false)
  })
}

// store persists + reflects a mode locally. persist=false is the adopt path
// (the value CAME from the server, so writing it back would be a pointless
// PUT). Cache and <html> are reconciled unconditionally — see theme.ts — while
// subscribers are only woken on a real change.
function store(m: Mode, persist: boolean): void {
  const changed = m !== current
  current = m
  try { localStorage.setItem(KEY, m) } catch { /* ignore — class still applied */ }
  apply(m)
  if (persist) setExtraPref(PREF, m) // no-ops when the account already has it
  if (changed) emit()
}

export function setMode(m: Mode): void {
  store(m, true)
}

// Adopt the mode saved on the user's account once the shell has synced.
onPreferencesSynced((extra) => {
  const v = extra[PREF]
  if (v === 'light' || v === 'dark' || v === 'system') store(v, false)
})

function subscribe(cb: () => void): () => void {
  listeners.add(cb)
  return () => { listeners.delete(cb) }
}

// onModeChange is subscribe() for non-React consumers (favicon.ts).
export const onModeChange = subscribe

export function useMode(): Mode {
  return useSyncExternalStore(subscribe, () => current, () => DEFAULT)
}

// Resolved light/dark for consumers that need the concrete value (e.g. toasts).
// useMode() already re-renders on OS-preference changes while in 'system'.
export function useResolvedMode(): 'light' | 'dark' {
  return resolveMode(useMode())
}
