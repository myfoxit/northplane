---
title: Testing
description: Go unit and race tests with the PostgreSQL matrix, the golangci-lint configuration, Vitest component tests with MSW, the Playwright end-to-end suite against a demo server, and how CI maps onto each layer.
sidebar:
  order: 2
---

Northplane is tested at four layers. Each has one command you run locally and one CI job that gates merges; the e2e layer additionally gates the production deploy because the Deploy workflow only runs after a fully green CI run on `main`.

| Layer | What it proves | Local command | CI job |
|---|---|---|---|
| Go unit/integration tests | packages, storage on SQLite, API handlers via `httptest`, server wiring | `make test` / `make race` | `test` (ubuntu + macos) |
| Storage on PostgreSQL | the dual-backend store against a real PostgreSQL 16 | `NORTHPLANE_TEST_PG_DSN=… go test ./internal/storage/...` | `postgres` (non-blocking) |
| Static analysis | gofmt, go vet, golangci-lint | `make fmt`, `golangci-lint run ./...` | `lint` |
| Frontend unit/component | TypeScript, ESLint, Vitest + Testing Library + MSW in jsdom | `cd web && npm run lint && npm test && npm run build` | `ui` |
| Typed codegen drift | generated `types.gen.ts` and `docs/src/assets/openapi.json` match the Go API | `make types-check` | `types` |
| End-to-end | the real `northplaned --demo` binary with the embedded SPA driven through Chromium | `make e2e` | `e2e` |
| Docs build | the Starlight site compiles and has no broken links | `cd docs && npm run build` | `docs` |

## Go tests

```bash
make test        # go vet ./... && go test ./...
make race        # go test -race ./...  (what CI runs)
go test ./internal/storage/... -run TestAuditChain -v
```

There are 92 `_test.go` files under `cmd/` and `internal/`. Conventions you will meet:

- **Stores in a temp dir.** Storage and API tests open a real SQLite store in `t.TempDir()`; nothing touches your data directory.
- **HTTP through `httptest`.** API tests build an `api.API` and exercise handlers over `httptest` servers (for example `internal/api/objects_test.go` for the ETag/If-Match flow, `tenant_switch_test.go` for the `X-Northplane-Tenant` rules, `openapi_spec_test.go` and `openapi_docs_test.go` for the generated spec and Swagger UI). `internal/server/server_test.go` boots the whole process (TLS policy, security headers, `/api/openapi.json`).
- **Embedded assets compile in tests.** `internal/web` and `internal/docs` embed `dist/` with `go:embed all:dist`; both directories exist in a fresh checkout (the committed UI snapshot, and `internal/docs/dist/.gitkeep`), and `make clean` recreates the stubs, so `go test ./...` never needs `make web` or `make docs` first. `internal/docs/docs_test.go` drives the handler against an in-memory tree (`HandlerFS`) and covers routing, the canonical `index.html` redirect, pre-compressed serving, write rejection and the "not embedded" 501.
- **Fuzzing.** `internal/nagios` has `FuzzParsePerfdata`; run it with `go test ./internal/nagios -run '^$' -fuzz FuzzParsePerfdata -fuzztime 30s`. Generated corpora land in `testdata/fuzz/`, which is git-ignored.

### PostgreSQL matrix

The store is dual-backend, and the storage suite runs every test against both when a DSN is present:

- `testStores(t)` (`internal/storage/storage_test.go`) always opens SQLite in a temp dir; when `NORTHPLANE_TEST_PG_DSN` is set it also opens PostgreSQL, and `matrix(t, fn)` runs the body once per backend.
- `resetPostgres` executes `DROP SCHEMA public CASCADE; CREATE SCHEMA public;` before each PostgreSQL-backed test so the shared database starts pristine (without it audit chains grew across tests and object identities collided). **Point the DSN at a throwaway database only.**

```bash
docker run --rm -d --name np-pg \
  -e POSTGRES_USER=np -e POSTGRES_PASSWORD=np -e POSTGRES_DB=northplane \
  -p 5432:5432 postgres:16
NORTHPLANE_TEST_PG_DSN='postgres://np:np@localhost:5432/northplane?sslmode=disable' \
  go test ./internal/storage/...
```

:::caution[Known failure on PostgreSQL]
`TestAuditChain` cannot verify the audit hash chain on PostgreSQL: `before_json`/`after_json` are stored as `jsonb`, which normalises the JSON text, while the tamper-evident row hash is computed over the original text; the audit timestamp also round-trips at microsecond precision on `timestamptz`. The CI job `postgres` is therefore `continue-on-error: true` — it keeps the PostgreSQL signal visible without blocking the pipeline. The shipped product runs on SQLite, which is fully green. See [Storage](/docs/administration/storage/) for the operator-facing caveat.
:::

## golangci-lint

CI runs golangci-lint **v2.12.2** with the committed `.golangci.yml` (v2 format). Install the same version locally and run it from the repository root:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
golangci-lint run ./...
```

CI installs it with `go install` rather than downloading a prebuilt binary: the prebuilt releases are compiled with an older Go and abort against a `go.mod` that targets a newer one ("the Go language version used to build golangci-lint is lower than the targeted version").

What the configuration says:

| Section | Setting | Effect |
|---|---|---|
| `issues` | `max-issues-per-linter: 0`, `max-same-issues: 0` | report every finding (no 50-per-linter / 3-identical caps) |
| `linters.default` | `standard` | errcheck, govet, ineffassign, staticcheck, unused |
| `linters.enable` | `misspell`, `unconvert`, `bodyclose` | high-signal extras that pass cleanly |
| `errcheck.exclude-functions` | `fmt.Fprint`, `fmt.Fprintf`, `fmt.Fprintln`, `fmt.Sscanf`, `(*bufio.Writer).Write`/`WriteByte`/`WriteString`/`Flush`, `(*text/tabwriter.Writer).Flush`, `(*database/sql.Rows).Close`, `(io.ReadCloser).Close` | value-less returns only (stdout/stderr/builders, sticky buffered-writer errors surfaced at the checked `Flush()`, read-side closes); real I/O errors are never excluded |
| `exclusions.paths` | `web/node_modules` | vendored third-party Go under node_modules is not linted |
| `exclusions.rules` | `_test.go`: errcheck on `Close`/`Unsetenv`; `_test.go`: bodyclose | teardown closes and the test request helpers that already close `resp.Body` |

The `lint` job also fails when `gofmt -l cmd internal` lists any file; `make fmt` fixes that.

## Frontend unit and component tests (Vitest)

```bash
cd web
npm test             # vitest run
npm run test:watch   # vitest (watch mode)
npm run test:cov     # vitest run --coverage (v8)
```

Configuration lives in `web/vitest.config.ts`, separate from `vite.config.ts` so the app build stays untouched:

| Setting | Value |
|---|---|
| `environment` | `jsdom` |
| `globals` | `true` (`describe`/`it`/`expect`/`vi` without imports; ESLint registers the Vitest globals for test files) |
| `setupFiles` | `./src/test/setup.ts` |
| `css` | `false` |
| `include` | `src/**/*.test.ts`, `src/**/*.test.tsx` |
| `coverage` | provider `v8`, `src/**/*.{ts,tsx}` minus `src/test/**`, `*.test.*` and `src/main.tsx` |
| alias | `@` → `./src` (same as the app) |

The harness in `web/src/test/`:

- `setup.ts` — registers jest-dom matchers; starts the MSW server with `onUnhandledRequest: 'error'` (an unexpected fetch fails the test), resets handlers after each test and closes the server afterwards; polyfills jsdom gaps: an in-memory `localStorage` (Node ≥ 22 ships its own that shadows jsdom's), `matchMedia`, `ResizeObserver`, `scrollIntoView`, `window.scrollTo`.
- `msw.ts` — exports `server`, `handlers` and `sampleProblems`. Default handlers answer `GET /api/v1/problems` (two sample rows), `GET /api/v1/alerts` (empty) and `GET /api/v1/events` (empty). Override per test with `server.use(...)`.
- `render.tsx` — `renderWithProviders(ui)` renders inside a fresh `QueryClient` (retries off, `gcTime` 0) and an in-memory TanStack router with stub routes `/objects`, `/objects/$id`, `/alerts`, `/incidents`.

A page test looks like this (trimmed from `src/pages/Problems.test.tsx`):

```tsx
import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen } from '@testing-library/react'
import { server } from '../test/msw'
import { renderWithProviders } from '../test/render'
import { ProblemsPage } from './Problems'

describe('<ProblemsPage />', () => {
  it('renders a row per problem', async () => {
    renderWithProviders(<ProblemsPage />)
    expect(await screen.findByText(/web-01 \/ http/)).toBeInTheDocument()
  })

  it('shows the all-green empty state when there are no problems', async () => {
    server.use(http.get('/api/v1/problems', () => HttpResponse.json({ items: [] })))
    renderWithProviders(<ProblemsPage />)
    expect(await screen.findByText(/all green|alles grün/i)).toBeInTheDocument()
  })
})
```

Note the bilingual regex: the existing tests tolerate both catalogs where a label is language-dependent.

There are 31 test files: `api`, `branding`, `permissions`, `schemas`, `settings`, `tenant`, `types`, `types.gen` at `src/*.test.ts`; components `DualListPicker`, `TenantSwitcher`, `admin/{Agents,Bundles,Channels,DeadLetters,MCP}`, `alerting/IVRMenus`, `dash/{grid,series,util}`, `objects/ObjectForm`; libs `duration`, `humanize`, `perfdata`, `redact`, `uistream`; pages `Alerts`, `Events`, `Maintenance`, `Overview`, `Problems`, `Templates`.

Two type-level checks run as part of `npm run build` (`tsc -b`), not Vitest: `src/types.gen.conformance.ts` asserts at compile time that the curated types in `types.ts` refine the generated DTOs in `types.gen.ts` (renamed or retyped backend fields fail the build), and `tsconfig.app.json` excludes `*.test.ts(x)` and `src/test` from that strict build — Vitest type-checks those on its own. Details are on the [Frontend](/docs/development/frontend/) page.

:::note[Which language do component tests see?]
The UI picks its catalog once at module load from `navigator.language` (`startsWith('de')` → German, otherwise English). jsdom reports `en-US`, so Vitest component tests assert the **English** catalog (`Overview.test.tsx` looks for `Active problems`, `Hosts UP`). The e2e suite, by contrast, is pinned to `de-DE` and asserts German (below). Check the existing tests next to yours before writing text assertions.
:::

## End-to-end tests (Playwright)

The e2e suite drives the **real** `northplaned --demo` binary — Go backend plus the embedded SPA — through Chromium. It is the one suite that proves the product works through a browser, which is why the `e2e` CI job is part of the gate in front of the production deploy.

```bash
make e2e                              # make web, build bin/northplaned, install Chromium, run
cd web && npm run test:e2e            # re-run against the existing bin/northplaned
cd web && npm run test:e2e:report     # open the HTML report
```

`make e2e` rebuilds the UI and the binary first because the tests exercise the **embedded** SPA, not Vite — a stale `bin/northplaned` tests stale UI. Running `npm run test:e2e` directly requires `bin/northplaned` to exist (global setup fails with "Build it first: make build" otherwise).

### Configuration (`web/playwright.config.ts`)

| Setting | Value | Why |
|---|---|---|
| `testDir` / `testMatch` | `./e2e`, `**/*.spec.ts` | |
| `workers` / `fullyParallel` | `1` / `false` | one shared demo database; tests run serially and never race on mutations |
| `timeout` / `expect.timeout` | 30 s / 7 s | |
| `actionTimeout` / `navigationTimeout` | 10 s / 15 s | |
| `retries` | `1` in CI, `0` locally | |
| `forbidOnly` | in CI | a stray `test.only` fails the run |
| `reporter` | `list` (+ `html` with `open: 'never'` in CI) | |
| `globalSetup` / `globalTeardown` | `./e2e/global-setup.ts`, `./e2e/global-teardown.ts` | boot/teardown the isolated demo server |
| `baseURL` | `http://127.0.0.1:18973` (`PW_PORT` overrides) | |
| `locale` / `timezoneId` | **`de-DE`** / `Europe/Vienna` | deterministic German selectors regardless of the machine locale |
| `storageState` | the `operator` role by default; `test.use({ storageState: authFile('admin') })` to switch | |
| `trace` / `screenshot` / `video` | retain on failure / only on failure / retain on failure | |
| `projects` | `chromium` (Desktop Chrome) | |

### What global setup does

1. Requires `bin/northplaned`; creates a temp data dir `np-e2e-*` with a random 32-byte `secret.key` and a `config.yaml` (`listen 127.0.0.1:<PORT>`, `baseUrl`, `dataDir`, `logLevel warn`).
2. Runs `northplaned bootstrap-admin -config …` and parses the `np_…` token.
3. Spawns `northplaned serve -config … --demo` detached with `NP_DEFAULT_ADMIN_DISABLED=1`, waits for `/readyz`.
4. Creates the admin user `admin@e2e.local` / `e2e-admin-pass-2026` through the API; the operator (`operator@demo.local` / `operator-demo-2026!`) and viewer (`viewer@demo.local` / `viewer-demo-2026!`) come from the demo seed.
5. Form-logs-in each role via `POST /login` and saves the `np_session` cookie as a Playwright storage state in `web/e2e/.auth-<PORT>/<role>.json`; writes `web/e2e/.runtime-<PORT>.json` with pid, data dir and port.

Teardown sends SIGTERM to the process group and deletes the runtime file, the temp data dir and the auth dir. Because the port is part of every file name, several suites can run side by side with different `PW_PORT` values (parallel worktrees, authoring agents). Git ignores `web/test-results/`, `web/playwright-report/`, `web/blob-report/`, `web/playwright/.cache/`, `web/e2e/.auth-*/` and `web/e2e/.runtime-*.json`.

### Spec files

| File | Tests | Covers |
|---|---|---|
| `smoke.spec.ts` | 4 | operator lands on the shell; Objects shows demo objects; anonymous is redirected to `/login`; viewer can load the app |
| `navigation.spec.ts` | 12 | every sidebar route renders without `ErrorState`; active link; overview tiles/incidents/on-call; KPI drill-down; problems list + handled toggle; ⌘K palette; BPI tree → SLA; reports; on-call; ⌘I assistant |
| `objects.spec.ts` | 7 | demo hosts/services; kind and state filters ↔ URL; full-text search; host create/edit/delete; service create + delete; "Jetzt prüfen" |
| `alerts-events.spec.ts` | 9 | alerts list; status/severity filters drive the URL; incidents; ack + resolve round-trip on a minted CRITICAL alert; events list, type filter, `types=` param, NDJSON export link |
| `alerting-rules.spec.ts` | 8 | rules + inline tester; admin CRUD rule; escalations + simulate; admin CRUD policy; groups |
| `alerting-windows.spec.ts` | 5 | seeded recurring downtime; fixed downtime create/delete; silence create/delete; maintenance tabs; on-call rotation |
| `dashboards.spec.ts` | 6 | shared demo dashboard; all seeded widgets render; wallboard mode; admin lifecycle; free layout + persisted time/refresh; delete |
| `admin-users.spec.ts` | 10 | users CRUD, disable/enable, set-password, last-admin guard; roles; tenants; secrets |
| `admin-comms.spec.ts` | 6 | contacts, contact groups, channels (create, test-send, delete), event sources, outgoing webhooks, heartbeats |
| `admin-mcp-agents.spec.ts` | 5 | MCP endpoint + snippets; agents install one-liner + `agent.yaml`; dead letters; bundle dry-run plan |
| `agent-chat.spec.ts` | 2 | agent page empty state + provider connection creation (Ollama, no key); admin policy tab |

:::caution[Keep the specs German]
The suite is pinned to `de-DE`, so selectors and text assertions use the German catalog (`Objekte`, `Jetzt prüfen`, `Konflikt — bitte neu laden.` …). A new spec that asserts English labels passes on your machine only if it forces `en`; in CI it fails and blocks the deploy. Write assertions against the `de` strings in `web/src/i18n.ts`.
:::

## CI mapping

All jobs are in `.github/workflows/ci.yml` and run on pushes and pull requests to `main`:

| Job | Runs | Needs | Blocking |
|---|---|---|---|
| `ui` | `npm ci`, `npm run lint`, `npm test`, `npm run build`; uploads `ui-dist` (1 day) | — | yes |
| `docs` | `npm ci`, `npm run build` in `docs/` (fails on broken links); uploads `docs-dist` | — | yes |
| `types` | `make types-check` (Go 1.25 + Node 22) | — | yes |
| `lint` | gofmt check, golangci-lint v2.12.2 | — | yes |
| `test` | `go vet`, `go test -race`, builds the three binaries; matrix `ubuntu-latest` + `macos-latest` | `ui` | yes |
| `e2e` | builds `bin/northplaned` with the `ui-dist` artifact, `npx playwright install --with-deps chromium`, `npm run test:e2e`; uploads `playwright-report` (7 days) unless cancelled | `ui` | yes |
| `postgres` | `go test ./internal/storage/...` with `NORTHPLANE_TEST_PG_DSN` against a `postgres:16` service | `ui` | **no** (`continue-on-error`) |
| `cross-build` | `go build` for linux/amd64, linux/arm64, darwin/arm64, windows/amd64 (`northplaned` skipped on windows) | `ui` | yes |

The Deploy workflow triggers on a completed CI run for `main` and proceeds only when that run concluded `success`, so every blocking job above gates production (see [CI/CD](/docs/deployment/ci-cd/)).

## Flakiness notes

- **e2e is serial on purpose.** `workers: 1` and a single demo database keep mutations from racing; the price is runtime, not reliability. Do not add `fullyParallel` to a spec.
- **Retries hide real failures.** CI retries each e2e test once; if a test only passes on retry, fix the wait (prefer `await expect(locator).toBeVisible()` over fixed sleeps) — the timeouts above are generous already.
- **Stale binary.** A green Vite session says nothing about the embedded SPA. Before `npm run test:e2e`, rebuild with `make web && make build` (or simply `make e2e`).
- **Readiness, not liveness.** Both the dev loop and global setup wait for `/readyz`, which goes green only after storage, the event bus and the scheduler are up; `/healthz` answers as soon as the listener binds and is the wrong probe for "can I log in yet".
- **PostgreSQL matrix.** Beyond the known `TestAuditChain` failure, flakes there historically came from state leaking between tests; `resetPostgres` fixed that. If you see new ones, check that the test goes through `matrix()` and does not assume SQLite-only behaviour (for example `UNIQUE constraint failed` error text versus SQLSTATE `23505`).
- **Default admin seeding.** Any test that boots `serve` and wants the first-run `/setup` page must set `NP_DEFAULT_ADMIN_DISABLED=1` (global setup does), otherwise the break-glass admin closes the gate before the first request.
- **Locale.** Vitest runs with jsdom's default locale, Playwright with `de-DE` — see the two notes above before asserting on text.
