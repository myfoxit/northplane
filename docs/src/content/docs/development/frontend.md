---
title: Frontend
description: The React SPA in web/ — stack and layout, routing, the API client and typed codegen, zod boundaries, i18n, theming, permissions, the shared component kit, lint and TypeScript settings, building and embedding, and the Stept widget with its CSP hash.
sidebar:
  order: 4
---

The UI is a single-page React application in `web/` that talks only to `/api/v1/` with the session cookie. It is built by Vite into `web/dist`, copied to `internal/web/dist` and embedded into `northplaned` with `go:embed`, so the binary always ships the UI it was built with. The server renders only four pages itself — login, first-run setup, self-registration and the public status page — because those must work without JavaScript or before a session exists (`internal/web/web.go`). What each page does for users is documented in the [User interface](/docs/ui/navigation/) section; this page is about the code.

## Stack

| Area | Packages |
|---|---|
| Build | `vite` 8, `@vitejs/plugin-react` 6, `typescript` 6 (`~6.0`), `@tailwindcss/vite` |
| Runtime | `react` 19, `react-dom` 19 |
| Routing and data | `@tanstack/react-router`, `@tanstack/react-query` 5, `@tanstack/react-virtual` (Objects list only) |
| Styling | Tailwind CSS v4 (`index.css` `@theme`), `tw-animate-css`, `class-variance-authority`, `clsx` + `tailwind-merge` (`cn()` in `lib/utils.ts`) |
| Components | `radix-ui` (monolithic package), shadcn/ui files generated into `src/components/ui/` (style `new-york`, base colour `neutral`, CSS variables; see `components.json`), `cmdk` (command palette), `lucide-react` (icons) |
| Charts | `uplot` behind `components/Chart.tsx`, split into its own `charts` chunk |
| Validation | `zod` 4 (`schemas.ts`) |
| Markdown | `react-markdown` + `remark-gfm` (agent chat only) |
| Codegen | `openapi-typescript` 7 (dev) |
| Tests | `vitest` 4, `jsdom`, `@testing-library/react`, `@testing-library/user-event`, `@testing-library/jest-dom`, `msw` 2, `@vitest/coverage-v8`, `@playwright/test` |
| Lint | `eslint` 10, `typescript-eslint`, `eslint-plugin-react-hooks` 7 (React Compiler rules), `eslint-plugin-react-refresh` |

Declared but unused in `src`: `next-themes` and the `sonner` toaster wrapper (the app uses inline page-local banners, not toasts).

## Project layout

```text
web/
├── index.html                 entry; lang="de"; Stept bootstrap snippet
├── public/                    favicon.svg, sw.js (service-worker kill switch)
├── e2e/                       Playwright specs, global-setup/teardown, lib/roles.ts
├── src/
│   ├── main.tsx               QueryClientProvider + TanStack router, lazy routes, 404/error components
│   ├── api.ts                 fetch wrapper, APIError, resourceApi, queryClient, fmtTime/fmtAgo
│   ├── types.ts               curated domain types + state helpers (hand-written)
│   ├── types.gen.ts           generated from the OpenAPI spec (`make types`) — DO NOT EDIT
│   ├── types.gen.conformance.ts  compile-time guard: types.ts refines types.gen.ts
│   ├── schemas.ts             zod schemas for the riskiest payloads
│   ├── i18n.ts                `de` (reference) and `en` catalogs, t()
│   ├── theme.ts / theme-data.ts / mode.ts / branding.ts / favicon.ts   look & feel
│   ├── settings.ts            refresh interval (localStorage + /users/me/preferences)
│   ├── tenant.ts              active tenant (X-Northplane-Tenant)
│   ├── permissions.ts         permImplies / hasPermission
│   ├── index.css              Tailwind, tokens, 30 theme blocks × light/dark, a11y rules
│   ├── pages/                 one file per route (Overview, Problems, Objects, Alerts, …)
│   ├── components/
│   │   ├── ui/                shadcn primitives (do not hand-edit style)
│   │   ├── kit.tsx            the app component kit (Spinner, Empty, ErrorState, Field, …)
│   │   ├── Layout.tsx, CommandPalette.tsx, TenantSwitcher.tsx, RefreshControl.tsx, AISidebar.tsx
│   │   ├── admin/ agent/ alerting/ dash/ objects/   feature components
│   ├── hooks/useSave.ts       mutation wrapper (invalidate + onDone + error)
│   ├── lib/                   duration, humanize, perfdata, redact, uistream, utils
│   └── test/                  setup.ts, msw.ts, render.tsx
├── vite.config.ts  vitest.config.ts  playwright.config.ts  eslint.config.js
├── tsconfig.json  tsconfig.app.json  tsconfig.node.json  tsconfig.test.json
└── components.json            shadcn configuration (aliases @/components, @/lib, @/hooks)
```

Conventions: pages in `src/pages/*.tsx`; feature components under `src/components/{admin,agent,alerting,dash,objects}`; generic primitives only from `@/components/ui/*`; app-level building blocks from `@/components/kit`. German copy in code comments is common; identifiers are English.

## Routing and pages

`main.tsx` builds a TanStack router: a root route renders `Layout` with a `Suspense` spinner around `Outlet`; every page is `lazy()`-loaded so each route is its own chunk. Routes: `/` (Overview), `/agent`, `/problems`, `/objects` (+ `/objects/$id`), `/alerts`, `/incidents`, `/events`, `/oncall`, `/alerting`, `/dashboards` (+ `/dashboards/$name`), `/reports`, `/business`, `/discovery`, `/maintenance`, `/templates`, `/admin`. `defaultNotFoundComponent` renders a real 404; `defaultErrorComponent` renders the shared `ErrorState` with Retry.

Filters that should be linkable live in URL search params and are validated in the route (`validateSearch`): `/objects` takes `selector`, `q`, `state`, `kind`; `/alerts` takes `status`, `severity`. If `wallboard` is present in the search, `Layout` renders the page without the sidebar.

To add a page: create `src/pages/Things.tsx` exporting `ThingsPage`, add a `lazy()` import and a `createRoute({ getParentRoute: () => rootRoute, path: '/things', component: ThingsPage })` entry in `main.tsx`, add a sidebar item to the nav list in `components/Layout.tsx` and the `de`/`en` labels in `i18n.ts`. The command palette's static page entries in `CommandPalette.tsx` are a separate, English-only list.

## API client (`src/api.ts`)

- Base URL is relative: `fetch('/api/v1' + path, { credentials: 'same-origin' })` — the browser session cookie `np_session` authenticates; the SPA never holds a bearer token.
- Headers: `Content-Type: application/json` when a body is present; `If-Match: "<etag>"` when `etag` is given; `X-Northplane-Tenant` from `activeTenantId()` (`tenant.ts`) when an admin has switched tenant.
- 401 → `window.location.href = '/login'` (hard redirect) and an `APIError(401, 'auth')`.
- Errors: RFC 9457 bodies become `APIError { status, code, message (title), detail }`; non-JSON errors get code `unknown`. 204/202 bodies resolve to `undefined`.
- Helpers: `get`, `post`, `put(path, body, etag)`, `del`, `getWithEtag(path, schema?)` → `{ data, etag }` (`parseEtag` strips `W/` and quotes), `ListResponse<T> { items, nextCursor? }`.
- `resourceApi<T>(base, schema?)` is the CRUD facade for named-resource endpoints: `queryKey: ['resources', base]`, `list()` (`?limit=500`), `get(name)` (with ETag), `create(doc)`, `update(name, doc, etag)` (`If-Match`), `remove(name)`.
- `queryClient` defaults: `staleTime` 15 s, `retry` 1, `refetchOnWindowFocus` false. Live lists poll: each passes `useRefreshInterval()` (from `settings.ts`; presets 5/10/30/60 s or off, default 30 s, persisted in `localStorage` `np.refreshInterval` and server-side `/users/me/preferences`) as React Query's `refetchInterval`, with `placeholderData: keepPreviousData`. There is no SSE in the UI — `lib/uistream.ts` is the AI-chat stream client, not a live-data channel.
- Mutations go through `useSave(fn, { invalidate, onDone })` (`hooks/useSave.ts`); `FormError` renders `APIError` title + detail; 409/412 are shown as `Konflikt — bitte neu laden.` / "Conflict — please reload". `DeleteButton` is a two-click inline confirm (no native `confirm()`).

## Typed codegen

The Go API is the single source of truth for wire shapes. `make types` runs `northplaned openapi` (no server needed), writes the spec to `/tmp/np-openapi.json`, copies it to `docs/src/assets/openapi.json`, and runs `openapi-typescript` into `src/types.gen.ts` (header: "Code generated by `make types` … DO NOT EDIT"), which exports `paths`, `components['schemas']` and `operations`. CI's `types` job runs `make types-check` and fails on any diff — see [Testing](/docs/development/testing/).

`src/types.ts` stays hand-written on purpose: it is narrower than the wire DTOs (semantic unions such as `Severity`, `AlertStatus`, `Kind`, `StateType`, numeric state enums) and omits transport-only fields, plus helpers `stateLabel`/`stateIcon`/`stateColor`/`sevColor`/`eventBadge`, `svcStates`, `hostStates`. `src/types.gen.conformance.ts` asserts at compile time (`ConformsDeep`) that `Alert`, `Incident`, `CheckState`, `NPObject` (vs `ObjectView`) and `Overview` refine their generated counterparts: every field the frontend declares must exist on the DTO with a compatible type; omitting wire-only fields is fine, null and undefined are interchangeable. Because that file is part of `tsc -b` (not a test), backend drift fails `npm run build` and CI. `types.gen.test.ts` gives it a runtime touchpoint for coverage.

Workflow after a backend change: `make types` → fix `types.ts`/consumers until `npm run build` is green → commit `types.gen.ts` and the docs' `openapi.json` together with the Go change.

## Runtime validation (zod)

`src/schemas.ts` holds zod schemas for the payloads most likely to come back malformed: `dashboardWidgetSchema`/`dashboardDocSchema` (the dashboard `spec` is frontend-owned JSON), plus alert, npObject, overview and incident schemas, each cross-checked against `types.ts` with `Expect<Equal<…>>` type tests. `api()` and `getWithEtag()` accept an optional `schema`; a failed parse becomes a 502-shaped `APIError('invalid_response')` that the normal `ErrorState` path renders instead of a crash deep in a component. `resourceApi('dashboards', dashboardDocSchema)` validates the single-document read; lists and writes stay on the bare cast.

## i18n

- One typed catalog in `src/i18n.ts`, no i18next: `const de = { … } as const` is the **reference** language, `const en: Record<keyof typeof de, string>` the fallback. The `Record` type makes a missing English key a compile error.
- Language is chosen once at module load: `navigator.language.startsWith('de') ? de : en` — no user preference, no switcher, no server setting. `index.html` declares `lang="de"`; the server-rendered pages are German only.
- Use `t('key')` (`TKey = keyof typeof de`). Adding a string = add the key to `de` **and** `en`. A few labels are intentionally literal (command palette page names, "Logout", "Raw JSON", "ICS").
- Dates: `Intl.DateTimeFormat(undefined, { dateStyle: 'short', timeStyle: 'medium' })` in the browser locale (`fmtTime`); ages via `fmtAgo` (`12s`, `5m`, `2h 3m`, `1d 4h`).
- The e2e suite is pinned to `de-DE`; Vitest sees jsdom's `en-US` — see [Testing](/docs/development/testing/).

## Theming, mode, branding, favicon

Two independent axes:

| Axis | Module | DOM | Values |
|---|---|---|---|
| colour theme | `theme.ts` (+ `theme-data.ts` registry) | `<html data-theme="…">` | 31 ids: `northplane` (the built-in `:root` palette — selecting it clears the attribute) plus 30 generated themes (`obsidianFire`, `currentRed`, `warmAmber`, `deepTeal`, …); default for a fresh user is **`obsidianFire`** |
| light/dark mode | `mode.ts` | `<html class="light">` | `system` / `light` / `dark`; default **`dark`**; `system` follows `prefers-color-scheme` live |

Tokens are the shadcn set (`--background` … `--ring`) plus `--success`, `--warning`, `--danger`, `--chart-1..5`, `--sidebar*`, exposed through `@theme inline` in `index.css`. Each generated theme has two CSS blocks — `:root[data-theme="id"]` (dark) and `:root.light[data-theme="id"]` (light) — and Tailwind's `dark:` variant is bound to `:root:not(.light)`. Both side-effect modules are imported first in `main.tsx` so the attribute and class are set before first paint.

Persistence: `localStorage` keys `np.theme` and `np.mode` (instant boot, cross-tab `storage` sync) **and** the instance-wide document `GET/PUT /api/v1/branding {theme, mode}` owned by `branding.ts` (the only writer). The branding is adopted once on shell mount; writing needs `config:write` (a 403 is swallowed); branding is per instance, not per user or tenant. `favicon.ts` repaints the radar glyph as a data-URI SVG in the live `--sidebar-primary` colour on every theme/mode change; `public/favicon.svg` (tinted `#FF5C3A`) serves the server-rendered pages. Operator view: [Branding and themes](/docs/administration/branding-and-themes/).

:::note[Adding a theme]
`theme-data.ts` is marked GENERATED and names a generator script (`scratchpad/gen-modes.mjs`) that is not in the repository. Add a theme by hand consistently: an entry `{ id, label, swatch }` in `THEMES` and the two `[data-theme="id"]` blocks (dark and `.light`) in `index.css`.
:::

## Permissions (`src/permissions.ts`)

A port of the backend's `model.Permission.Implies`: `permImplies(have, want)` is true for an exact match, `*`, `*:*`, or resource/action wildcards (`admin:*`, `*:read`); `hasPermission(perms, want)` folds over `whoami.permissions`. The UI uses it to **hide** controls — the tenant switcher (`admin:tenants`), the Appearance controls (`config:write`) — never to authorise anything; the API enforces. Permission names and roles: [Users, roles and permissions](/docs/administration/users-roles-permissions/).

## Component kit

`src/components/kit.tsx` is the shared vocabulary: `Spinner`, `Empty`, `ErrorState` (icon + "Laden fehlgeschlagen." + detail + Retry), `Tile`, `LabelChips`, `Field` (label/hint/required), `DurationInput` (validates Go durations via `lib/duration.ts`), `KVEditor`, `ListEditor` (with suggestions), `FormError`, `SubmitRow`, `DeleteButton`; it re-exports `useSave` and `isDuration`. Other shared pieces: `DualListPicker` (two-pane transfer list), `MultiSelect` (chips + typeahead), `dash/pickers.tsx` (`ObjectPicker` = `GET /objects?q=&limit=50`, `MetricPicker` = `GET /objects/{id}/metrics`), `alerting/common.tsx` (`DateTimeInput`, `ChannelPicker`, `SeverityField`, `ToggleRow`), `admin/common.tsx` (`StatusBadge`, `TypeBadge`, `TableActions`, `RowActions`). Libraries under `lib/`: `humanize` (SNMP sysUpTime), `perfdata` (Nagios perfdata parser), `redact` (masks token-like values `•••` in effective config), `duration`, `utils` (`cn`).

Two quirks worth knowing: Radix `Select` cannot hold an empty-string value, so sentinels (`__all__`, `__none__`, `__both__`, `__home__`, `__root__`, `__default__`) are mapped back to empty/undefined at the edges; and accessibility rules are enforced in code — a single `:focus-visible` outline, status badges never colour-only, `aria-label` on icon buttons, `prefers-reduced-motion` disables overlay animations.

## ESLint and TypeScript

`eslint.config.js` (flat config): `js.recommended`, `tseslint.recommended`, `react-hooks` flat recommended (incl. React Compiler rules), `react-refresh` Vite preset, browser globals; `react-refresh/only-export-components` is an error with `allowConstantExport`, switched off for `src/components/ui/**` (shadcn exports cva variants), `src/components/kit.tsx` (re-exports helpers), `src/main.tsx` and tests; `react-hooks/incompatible-library` is off for `src/pages/Objects.tsx` (TanStack Virtual); test files get Vitest + Node globals; `dist` and `coverage` are ignored. Run with `npm run lint`.

`tsconfig.app.json`: target/lib ES2023 + DOM, `strict`, `noUncheckedIndexedAccess`, `noUnusedLocals`, `noUnusedParameters`, `erasableSyntaxOnly`, `noFallthroughCasesInSwitch`, `moduleResolution: bundler`, `verbatimModuleSyntax`, `jsx: react-jsx`, `paths { "@/*": ["./src/*"] }` (TypeScript 6: relative to the tsconfig, no `baseUrl`); it excludes `*.test.ts(x)` and `src/test`, which `tsconfig.test.json` covers with vitest/jest-dom/node types. `tsconfig.node.json` types `vite.config.ts`.

## Building and embedding

```bash
cd web && npm run build      # tsc -b && vite build  → web/dist
make web                     # the above + copy web/dist → internal/web/dist
make build                   # go build embeds internal/web/dist
```

`vite.config.ts`: plugins `react()` and `tailwindcss()`; alias `@` → `./src`; `optimizeDeps.include` pre-bundles react, TanStack, lucide, uplot, zod (avoids mid-session re-optimisation in dev); `build.target` `es2022`; `manualChunks` puts anything containing `uplot` into `charts` and all other `node_modules` into `vendor`, so the output is per-page chunks + `vendor` + `charts` + `index-*.js/css`. The dev proxy is described under [`make dev`](/docs/development/setup/).

On the Go side `internal/web/web.go` embeds `dist/` with `//go:embed all:dist` and serves it as the catch-all: `/assets/*` with `Cache-Control: public, max-age=31536000, immutable` (hashed names), everything else `no-cache`, unknown paths fall back to `index.html` for client routing; `GateSPA` redirects unauthenticated **document** navigations (GET/HEAD with `Accept: text/html`, not under `/assets/`) to `/login` so the shell never flashes before the client's own 401 redirect. If the embed is only the stub, the server answers 501 `UI not embedded in this build — run make web before building`.

:::caution[The committed dist is a snapshot]
`internal/web/dist` is committed so `go build`/`go test` work without Node, but it is only refreshed when someone runs `make web` and commits the result — at any given commit it can lag behind `web/src`. CI (`ui` job) and the Dockerfile always rebuild it before the Go build, so shipped images and release binaries carry the current UI; a local `go build` without `make web` embeds whatever is committed.
:::

## The Stept widget and the CSP hash

The SPA and the server-rendered login and register pages embed the Stept assistant (chat widget + product tours): an inline bootstrap script sets `window.SteptSettings = { workspaceKey: "wk_…" }` and an async `<script>` loads `https://app.stepped.ai/widget-assets/loader.js`. The snippet exists twice with the same hard-coded key — in `web/index.html` and as `steptSnippet` in `internal/web/web.go` — and there is no configuration switch.

The server's Content-Security-Policy for non-`/api/` paths allows exactly that: `app.stepped.ai` in `img-src`, `script-src`, `connect-src` (https + wss) and `frame-src`, plus the inline bootstrap script by SHA-256 hash, which keeps `script-src` free of `'unsafe-inline'`:

```text
script-src 'self' https://app.stepped.ai 'sha256-HlAiISfjqhgIiTh24Wt2L3bd5wG1TYbHlnpS0PMuIA8='
```

If you change a single byte of the inline script (for example a new workspace key), recompute the hash and update it in `internal/server/server.go`:

```bash
printf '%s' '<script-body>' | openssl dgst -sha256 -binary | openssl base64
```

Removing the widget means deleting the two `<script>` lines in `web/index.html`, the `steptSnippet` constant and its uses in `web.go`, and the Stept origins + hash from the CSP, then `make web`. The documentation at `/docs/` uses its own CSP without Stept (see [Documentation](/docs/development/documentation/)); the full header set is in [TLS and proxying](/docs/administration/tls-and-proxy/).
