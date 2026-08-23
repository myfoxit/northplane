// The browser tab shows the app LOGO — lucide's `radar` glyph, the same mark
// the sidebar renders next to the "Northplane" wordmark (Layout.tsx). The tab
// icon used to be an unrelated blue triangle baked into index.html.
//
// Because the mark is drawn here rather than loaded as a fixed image, it is
// tinted with the LIVE accent of the active colour theme (--sidebar-primary),
// and re-rendered whenever the theme or the light/dark mode changes. Switching
// branding therefore recolours the tab too. public/favicon.svg carries the
// same glyph in the default accent for the server-rendered pages (login,
// setup, register, status), which never boot the SPA.
import { onThemeChange } from './theme'
import { onModeChange } from './mode'

// lucide `radar`, v1.17.0 — kept as raw path data so the icon can be
// serialised into a data URI (the React component cannot). Stroke width is a
// touch heavier than the on-screen icon so the glyph survives 16px.
const PATHS = [
  'M19.07 4.93A10 10 0 0 0 6.99 3.34',
  'M4 6h.01',
  'M2.29 9.62A10 10 0 1 0 21.31 8.35',
  'M16.24 7.76A6 6 0 1 0 8.23 16.67',
  'M12 18h.01',
  'M17.99 11.66A6 6 0 0 1 15.77 16.67',
  'm13.41 10.59 5.66-5.66',
]

const FALLBACK = '#FF5C3A' // default theme accent, = public/favicon.svg

// brand reads the accent the sidebar logo is painted in. Falls back through
// --primary to a literal, so a stylesheet that has not applied yet (or a
// browser without custom-property support in getComputedStyle) still yields
// a coloured icon rather than a black one.
function brand(): string {
  try {
    const cs = getComputedStyle(document.documentElement)
    const v = cs.getPropertyValue('--sidebar-primary').trim()
      || cs.getPropertyValue('--primary').trim()
    if (v) return v
  } catch { /* fall through */ }
  return FALLBACK
}

function svg(color: string): string {
  const body = PATHS.map((d) => `<path d="${d}"/>`).join('')
  return '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none"'
    + ` stroke="${color}" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round">`
    + `${body}<circle cx="12" cy="12" r="2"/></svg>`
}

function paint(): void {
  if (typeof document === 'undefined') return
  const href = `data:image/svg+xml,${encodeURIComponent(svg(brand()))}`
  let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
  if (!link) {
    link = document.createElement('link')
    link.rel = 'icon'
    document.head.appendChild(link)
  }
  link.type = 'image/svg+xml'
  if (link.href !== href) link.href = href
}

// First paint plus one deferred retry: in dev, Vite injects the stylesheet
// after this module evaluates, so the very first brand() read can miss the
// tokens. The retry costs nothing and lands on the real colour.
paint()
if (typeof requestAnimationFrame === 'function') requestAnimationFrame(paint)

onThemeChange(paint)
onModeChange(paint)
