---
title: Branding and themes
description: "Instance-wide appearance of the Northplane console — colour theme and light/dark mode, the 31 themes, how the browser caches and adopts the setting, the favicon and logo, which pages are not branded, and the GET/PUT /api/v1/branding API."
sidebar:
  order: 10
---

Branding in Northplane is the **look of the console for this installation**: one colour theme and one light/dark mode, chosen by an administrator and seen by everyone who signs in. It is deliberately not per user and not per tenant. There is no logo, name or CSS upload.

![Admin → Appearance](../../../assets/screenshots/admin-appearance.webp)


## What can be branded

| Axis | Values | Default |
|---|---|---|
| Colour theme (`theme`) | One of the 31 theme ids below (`<html data-theme="…">`) | `obsidianFire` ("Obsidian & Fire") for a browser with no cached choice |
| Mode (`mode`) | `dark`, `light`, `system` (`system` follows the operating-system preference live via `prefers-color-scheme`) | `dark` |

Every theme exists in both modes (30 dark and 30 light CSS blocks plus the built-in `northplane` palette on `:root`). The sidebar logo, the tenant-switcher chip and the favicon are tinted with the active theme's accent.

## Set the instance appearance

Open **Admin → Appearance (Darstellung)**. The card shows a **Mode** row (System / Hell / Dunkel — "Light/dark — every theme comes in both modes") and a **Farbschema / Colour theme** radio grid with a swatch and label per theme. Picking a value applies it in your browser immediately and writes it to the server as the instance branding.

The controls are enabled only for principals whose permissions imply `config:write` (the built-in `admin` role; `operator` and `viewer` see a read-only banner: "Only administrators with config:write can change this instance's appearance"). The hint under the title states the scope: "Applies to this instance — every user sees it, and switching customer does not change it."

## Themes

| Id | Label | Id | Label |
|---|---|---|---|
| `northplane` | Northplane (Standard) | `terracotta` | Terracotta Warm |
| `currentRed` | Current (Red/Orange) | `steelViolet` | Steel & Violet |
| `warmAmber` | Warm Amber | `cloudPeach` | Cloud & Peach |
| `deepTeal` | Deep Teal + Coral | `carbonYellow` | Carbon & Yellow |
| `lavenderMint` | Lavender + Mint | `navyBronze` | Navy & Bronze |
| `forest` | Forest & Copper | `snowRuby` | Snow & Ruby |
| `midnightIndigo` | Midnight Indigo | `slateTangerine` | Slate & Tangerine |
| `sandOcean` | Sand & Ocean | `espressoTeal` | Espresso & Teal |
| `roseGold` | Rose Gold & Charcoal | `blushSage` | Blush & Sage |
| `electricDark` | Electric Blue Dark | `polarNight` | Polar Night |
| `mossStone` | Moss & Stone | `ivoryIndigo` | Ivory & Indigo |
| `obsidianFire` | Obsidian & Fire (product default) | `chalkMagenta` | Chalk & Magenta |
| `arcticBlue` | Arctic Blue | `volcanicAqua` | Volcanic & Aqua |
| `plumGold` | Plum & Gold | `linenOlive` | Linen & Olive |
| `neonMint` | Neon Mint Dark | `midnightRose` | Midnight Rose |
| | | `concreteOrange` | Concrete & Orange |

`northplane` is the base palette defined on `:root` (slate surfaces, blue accent); selecting it clears the `data-theme` attribute instead of applying an override block. The registry lives in `web/src/theme-data.ts`; unknown ids sent through the API are accepted by the server but ignored by the client.

## How the browser applies it

The SPA keeps the two axes as synchronous local stores so the first paint never flashes the wrong palette, and a separate module talks to the server:

1. On boot the SPA reads `localStorage` keys `np.theme` and `np.mode` and applies them to `<html>` before React renders (defaults `obsidianFire` / `dark` when nothing is cached). Other tabs pick up changes through the `storage` event.
2. Once the authenticated shell mounts, it fetches `GET /api/v1/branding` **once** and adopts `theme` and `mode` from the document — the server value wins over the local cache. A fetch failure (offline, 401) leaves the cached look in place. The document is **not** re-fetched when you switch tenants.
3. A user-driven change in the Appearance tab updates the local store and `PUT`s the whole document `{theme, mode}` back. A caller without `config:write` gets a 403 that the UI swallows — which is why the controls are locked for them in the first place.

:::note[There is no real per-user theme]
What looks like a per-user override is only the per-browser `localStorage` cache: a value that differs from the instance document survives until the next shell mount, when the server document is adopted again. If the instance document is empty (`{}` — nobody has ever set branding), every browser simply keeps its own cached choice or the defaults. Language is likewise not a preference (it follows `navigator.language`). The only stored per-user setting is the refresh interval — see [Users, roles and permissions](/docs/administration/users-roles-permissions/).
:::

## Favicon and logo

- The sidebar shows the lucide `radar` glyph next to the "Northplane" wordmark.
- Inside the SPA the browser-tab icon is the same glyph drawn at runtime into a data-URI SVG, tinted with the live `--sidebar-primary` colour (fallback `--primary`, then `#FF5C3A`) and re-rendered whenever theme or mode changes — so switching branding recolours the tab too.
- `public/favicon.svg` carries the same glyph in the default accent (`#FF5C3A`, the Obsidian & Fire accent) for the server-rendered pages that never boot the SPA.
- `meta name="theme-color"` is fixed to `#020617`.

None of these can be replaced through configuration in this version.

## Where branding does not apply

- `/login`, `/setup`, `/register` and the public status pages `/status/{slug}` are server-rendered static dark HTML with a "▲ Northplane" heading and the static favicon — they ignore theme and mode (and are German-only, see [Authentication](/docs/administration/authentication/)).
- The documentation under `/docs/` has its own Starlight theme.
- The REST API (`/api/…`) is unaffected, and the tenant header is ignored by the branding endpoints (below).

## API

Branding is a single document (`kind: branding`, name `instance`) stored **under the Default tenant**; `X-Northplane-Tenant` is ignored on both calls.

| Endpoint | Permission | Behaviour |
|---|---|---|
| [`GET /api/v1/branding`](/docs/reference/api/operations/get_branding/) | none, but a login is required (401 when anonymous) | `{"theme": "…", "mode": "…"}` — `{}` when never set |
| [`PUT /api/v1/branding`](/docs/reference/api/operations/put_branding/) | `config:write` | Body `{"theme": "<id>", "mode": "light\|dark\|system"}`; `mode` is validated (`422 mode must be one of light, dark, system`), `theme` is stored unvalidated; audit `branding.update` with before/after |

```bash
curl -s -X PUT https://monitoring.example.net/api/v1/branding \
  -H "Authorization: Bearer np_<48 hex>" -H "Content-Type: application/json" \
  -d '{"theme":"deepTeal","mode":"system"}'
```

The `PUT` replaces the whole document, so always send both fields. Setting branding in a [config bundle](/docs/administration/config-bundles/) is not possible (branding is not a bundle kind); use the API or the Appearance tab. For the UI side of theming (tokens, Tailwind variant, adding a theme) see [Frontend](/docs/development/frontend/).
