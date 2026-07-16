// Colour-theme switch (Admin → Darstellung). The app ships one dark palette
// (Northplane: slate surfaces + blue accent, defined on :root in index.css);
// this store lets a user re-skin the accent (and, for the stock "shadcn"
// theme, the neutral surfaces) by toggling a `data-theme` attribute on
// <html>. Each theme's concrete token values live in index.css under
// :root[data-theme="…"]; here we only carry the id + display label + swatch
// so the switcher and the boot-time apply stay in sync with that CSS.
//
// Deliberately localStorage-only (unlike settings.ts, which is server-backed):
// a colour theme is a per-browser cosmetic choice, and keeping it off the
// shared /users/me/preferences document avoids two independent writers racing
// on the same doc (a PUT there replaces the whole document). Instant boot,
// no network, no contract change.
import { useSyncExternalStore } from 'react'
import type { TKey } from './i18n'

// ThemeId is the value written to <html data-theme>. 'northplane' is the
// built-in :root default and intentionally has NO override block in index.css
// (selecting it just falls back to the stock palette → current UX unchanged).
export type ThemeId =
  | 'northplane' | 'shadcn' | 'blue' | 'emerald' | 'violet' | 'rose'
  | 'orange' | 'amber' | 'red' | 'cyan' | 'zinc'

export interface ThemeDef {
  id: ThemeId
  labelKey: TKey // resolved through i18n t() in the switcher
  swatch: string // representative colour for the picker dot
}

// Order = display order in the switcher. Northplane (current default) first,
// then the stock shadcn neutral theme, then the accent colours.
export const THEMES: readonly ThemeDef[] = [
  { id: 'northplane', labelKey: 'themeNorthplane', swatch: '#3b82f6' },
  { id: 'shadcn', labelKey: 'themeShadcn', swatch: '#e5e5e5' },
  { id: 'blue', labelKey: 'themeBlue', swatch: '#3b82f6' },
  { id: 'emerald', labelKey: 'themeEmerald', swatch: '#10b981' },
  { id: 'cyan', labelKey: 'themeCyan', swatch: '#06b6d4' },
  { id: 'violet', labelKey: 'themeViolet', swatch: '#8b5cf6' },
  { id: 'rose', labelKey: 'themeRose', swatch: '#f43f5e' },
  { id: 'red', labelKey: 'themeRed', swatch: '#ef4444' },
  { id: 'orange', labelKey: 'themeOrange', swatch: '#f97316' },
  { id: 'amber', labelKey: 'themeAmber', swatch: '#f59e0b' },
  { id: 'zinc', labelKey: 'themeZinc', swatch: '#d4d4d8' },
] as const

const IDS = new Set<string>(THEMES.map((t) => t.id))
const KEY = 'np.theme'
const DEFAULT: ThemeId = 'northplane'

function readCache(): ThemeId {
  try {
    const raw = localStorage.getItem(KEY)
    if (raw && IDS.has(raw)) return raw as ThemeId
  } catch { /* localStorage may be unavailable (private mode) — fall back */ }
  return DEFAULT
}

// applyTheme reflects the choice onto <html>. Northplane is the :root default,
// so it clears the attribute rather than setting an (empty) override block.
function applyTheme(v: ThemeId): void {
  if (typeof document === 'undefined') return
  const root = document.documentElement
  if (v === DEFAULT) delete root.dataset.theme
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

// Reactive read: the switcher (and anything else) re-renders on change.
export function useTheme(): ThemeId {
  return useSyncExternalStore(subscribe, () => current, () => DEFAULT)
}
