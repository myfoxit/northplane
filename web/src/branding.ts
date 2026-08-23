// Branding — the console's look (colour theme + light/dark mode) as a property
// of THIS INSTALLATION.
//
// Scope, and why it is not the obvious one:
//   • Not per user. An admin brands the instance once; everyone signing into
//     it sees that look. Nobody can quietly re-skin the console for a
//     colleague, so the write is gated on config:write (the API enforces it —
//     the UI only hides the controls).
//   • Not per tenant. GET/PUT /branding always resolve the document under the
//     installation's default tenant, ignoring the X-Northplane-Tenant header,
//     so a cross-tenant operator switching customers in the console does NOT
//     watch the UI re-skin under them. A customer running its own Northplane
//     instance brands that instance instead.
//
// This module is the ONLY thing that talks to /branding. theme.ts and mode.ts
// stay synchronous local stores (localStorage cache + <html> attributes,
// applied before first paint so boot never flashes); this subscribes to them,
// writes changes through, and adopts the server document on sign-in.
import { api } from './api'
import { getTheme, onThemeChange, setTheme, type ThemeId } from './theme'
import { getMode, onModeChange, setMode, type Mode } from './mode'

// Mirrors model.Branding (types.gen.ts Branding).
export interface Branding { theme?: string; mode?: string }

// Last known server document. Compared against before every write so adopting
// a value — or a second tab echoing one through the `storage` event — does not
// bounce a redundant PUT back at the server.
let doc: Branding = {}

// True while applying a server value, so the change listeners below can tell
// "the user picked this" from "the server told us this".
let adopting = false

const isMode = (v: string | undefined): v is Mode =>
  v === 'light' || v === 'dark' || v === 'system'

// syncBrandingFromServer adopts the instance branding. Called once from the
// authenticated shell; a failure (offline, or a session that 401s) leaves the
// cached local look in place — branding is never worth blocking the UI over.
export async function syncBrandingFromServer(): Promise<void> {
  try {
    doc = (await api<Branding>('/branding')) ?? {}
  } catch {
    return // keep whatever the localStorage cache booted with
  }
  adopting = true
  try {
    if (doc.theme) setTheme(doc.theme as ThemeId) // unknown ids are ignored by theme.ts
    if (isMode(doc.mode)) setMode(doc.mode)
  } finally {
    adopting = false
  }
}

// save writes the current local look back as the instance branding. Callers
// without config:write get a 403, which is swallowed: the UI already hides the
// controls, and a stray write must not surface as an error toast.
function save(): void {
  const next: Branding = { theme: getTheme(), mode: getMode() }
  if (next.theme === doc.theme && next.mode === doc.mode) return
  doc = next
  void api('/branding', { method: 'PUT', body: JSON.stringify(next) })
    .catch(() => { /* not permitted, or offline — the local look still applies */ })
}

// A user-driven change to either axis persists the whole document (both axes
// live in one record, so a partial write would drop the other one).
onThemeChange(() => { if (!adopting) save() })
onModeChange(() => { if (!adopting) save() })
