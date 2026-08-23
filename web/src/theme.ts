// Colour-theme switch (Admin → Darstellung). The app ships one dark palette
// (Northplane: slate surfaces + blue accent, defined on :root in index.css);
// this store lets a user pick a completely different palette — the stock
// shadcn neutral theme or any of the tweakcn presets — by toggling a
// `data-theme` attribute on <html>. Each theme re-skins the full token
// surface via the :root[data-theme="…"] blocks in index.css; the registry of
// ids + labels + swatch previews lives in theme-data.ts (generated).
//
// PERSISTED per user, not per browser: the choice is written through to
// /users/me/preferences (extra["theme"]) so the branding follows the user to
// every browser and device. localStorage stays as the instant-boot cache —
// the saved theme is applied before first paint, and the server value is
// adopted once the shell has synced. settings.ts owns the shared preferences
// document (a PUT replaces the whole thing), so the write goes through
// setExtraPref() there rather than a second, racing writer here.
import { useSyncExternalStore } from 'react'
import { THEMES } from './theme-data'
import { onPreferencesSynced, setExtraPref } from './settings'

export { THEMES } from './theme-data'
export type { ThemeDef } from './theme-data'

// The attribute value written to <html data-theme>. 'northplane' is the
// built-in :root default and intentionally has NO override block in index.css
// (selecting it just falls back to the stock palette → current UX unchanged).
export type ThemeId = string

const IDS = new Set<string>(THEMES.map((t) => t.id))
const KEY = 'np.theme'
// Key inside the server-side preferences `extra` bag.
const PREF = 'theme'
// BASE is the :root fallback (Northplane) — selecting it clears the attribute.
// INITIAL is what a user with no saved preference gets: Obsidian & Fire is the
// product default. (Kept distinct so BASE stays the attribute-cleared sentinel.)
const BASE: ThemeId = 'northplane'
const INITIAL: ThemeId = 'obsidianFire'

function readCache(): ThemeId {
  try {
    const raw = localStorage.getItem(KEY)
    if (raw && IDS.has(raw)) return raw
  } catch { /* localStorage may be unavailable (private mode) — fall back */ }
  return INITIAL
}

// applyTheme reflects the choice onto <html>. Northplane (BASE) is the :root
// default, so it clears the attribute rather than setting an override block.
function applyTheme(v: ThemeId): void {
  if (typeof document === 'undefined') return
  const root = document.documentElement
  if (v === BASE) delete root.dataset.theme
  else root.dataset.theme = v
}

let current: ThemeId = readCache()
const listeners = new Set<() => void>()

// Apply the cached theme as soon as this module loads (imported from main.tsx
// before React renders) so there is no flash of the default palette.
applyTheme(current)

// apply stores + reflects a theme locally. persist=false is the adopt path
// (the value CAME from the server, so writing it back would be a pointless
// PUT). The cache and <html> are reconciled unconditionally — adopting must
// converge on the server's value even when this tab already believed it was
// the current one — while subscribers are only woken on a real change.
function apply(v: ThemeId, persist: boolean): void {
  if (!IDS.has(v)) return
  const changed = v !== current
  current = v
  try { localStorage.setItem(KEY, v) } catch { /* ignore — attribute still applied */ }
  applyTheme(v)
  if (persist) setExtraPref(PREF, v) // no-ops when the account already has it
  if (changed) for (const l of listeners) l()
}

export function setTheme(v: ThemeId): void {
  apply(v, true)
}

// Adopt the theme saved on the user's account once the shell has synced, so a
// fresh browser (or a colleague's machine) lands on the same branding.
onPreferencesSynced((extra) => {
  const v = extra[PREF]
  if (v) apply(v, false)
})

// Adopt a change made in another tab so every open view shares one theme.
if (typeof window !== 'undefined') {
  window.addEventListener('storage', (e) => {
    if (e.key === KEY) apply(readCache(), false)
  })
}

function subscribe(cb: () => void): () => void {
  listeners.add(cb)
  return () => { listeners.delete(cb) }
}

// onThemeChange is subscribe() for non-React consumers (favicon.ts).
export const onThemeChange = subscribe

// Reactive read: the switcher (and anything else) re-renders on change.
export function useTheme(): ThemeId {
  return useSyncExternalStore(subscribe, () => current, () => INITIAL)
}
