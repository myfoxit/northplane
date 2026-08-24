// Colour-theme switch (Admin → Darstellung). The app ships one dark palette
// (Northplane: slate surfaces + blue accent, defined on :root in index.css);
// this store lets a user pick a completely different palette — the stock
// shadcn neutral theme or any of the tweakcn presets — by toggling a
// `data-theme` attribute on <html>. Each theme re-skins the full token
// surface via the :root[data-theme="…"] blocks in index.css; the registry of
// ids + labels + swatch previews lives in theme-data.ts (generated).
//
// This module is the LOCAL half of the theme: <html data-theme> plus a
// localStorage cache, applied on import so boot never flashes the wrong
// palette. It deliberately knows nothing about the server. The colour theme is
// an INSTANCE-wide setting (branding.ts owns GET/PUT /branding and is the only
// writer); keeping the transport there means this store stays synchronous and
// importable from anywhere, and there is exactly one module talking to the
// branding document.
import { useSyncExternalStore } from 'react'
import { THEMES } from './theme-data'

export { THEMES } from './theme-data'
export type { ThemeDef } from './theme-data'

// The attribute value written to <html data-theme>. 'northplane' is the
// built-in :root default and intentionally has NO override block in index.css
// (selecting it just falls back to the stock palette → current UX unchanged).
export type ThemeId = string

const IDS = new Set<string>(THEMES.map((t) => t.id))
const KEY = 'np.theme'
// BASE is the :root fallback (Northplane) — selecting it clears the attribute.
// INITIAL is what a user with no saved preference gets: since the Polaris
// redesign the base palette IS the product default. (Kept distinct so BASE
// stays the attribute-cleared sentinel.)
const BASE: ThemeId = 'northplane'
const INITIAL: ThemeId = BASE

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

export function setTheme(v: ThemeId): void {
  if (!IDS.has(v)) return
  current = v
  try { localStorage.setItem(KEY, v) } catch { /* ignore — attribute still applied */ }
  applyTheme(v)
  for (const l of listeners) l()
}

// Adopt a change made in another tab so every open view shares one theme.
if (typeof window !== 'undefined') {
  window.addEventListener('storage', (e) => {
    if (e.key === KEY) { current = readCache(); applyTheme(current); for (const l of listeners) l() }
  })
}

function subscribe(cb: () => void): () => void {
  listeners.add(cb)
  return () => { listeners.delete(cb) }
}

// onThemeChange is subscribe() for non-React consumers (branding.ts persists
// the change; favicon.ts repaints the tab icon in the new accent).
export const onThemeChange = subscribe

// getTheme is the plain (non-hook) read, for the same consumers.
export function getTheme(): ThemeId {
  return current
}

// Reactive read: the switcher (and anything else) re-renders on change.
export function useTheme(): ThemeId {
  return useSyncExternalStore(subscribe, () => current, () => INITIAL)
}
