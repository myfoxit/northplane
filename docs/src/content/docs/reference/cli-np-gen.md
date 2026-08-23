---
title: np-gen CLI
description: np-gen is the developer scaffolding generator — what np-gen new-resource stamps, its flags, name derivation rules and the manual wiring checklist it prints.
sidebar:
  order: 4
---

`np-gen` is a **developer tool**, not something operators run. It stamps the boilerplate for a new configuration-resource type across the Go model, the storage kind registry, the REST CRUD registration and the frontend (type + admin page) from a single name, and then prints the few wiring steps a human still has to do. It is built by `make build` (`bin/np-gen`) but is not part of release tarballs or the Docker image. How a resource fits into the backend is described in [Backend](/docs/development/backend/) and [Frontend](/docs/development/frontend/).

## Usage

```text
np-gen — Northplane scaffolding generator

Usage:
  np-gen new-resource <Name> [--dry-run] [--force] [--root <dir>] [--out <dir>]

Commands:
  new-resource   Stamp the boilerplate for a new monitored-resource type
                 across the model, storage, API and frontend layers.

Run "np-gen new-resource <Name> --dry-run" to preview the plan.
```

`np-gen` with no arguments prints the usage and exits 2; `help`, `-h`, `--help` print it and exit 0; an unknown command prints `np-gen: unknown command "<x>"` plus the usage and exits 2. Errors inside `new-resource` are printed as `np-gen: <message>` with exit 1.

## new-resource

```bash
cd /path/to/northplane
go run ./cmd/np-gen new-resource MaintenanceWindow --dry-run     # preview
go run ./cmd/np-gen new-resource MaintenanceWindow               # write into the tree
go run ./cmd/np-gen new-resource maintenance-window --out /tmp/np-gen   # try it without touching the tree
```

| Flag | Meaning |
|---|---|
| `<Name>` | the resource name in any casing: `Foo`, `foo`, `foo-bar`, `fooBar`, `foo_bar`; one name only |
| `--dry-run` | print the plan (files + checklist) without writing |
| `--force` | overwrite existing files instead of refusing |
| `--root <dir>` | repository root to write into (default: current directory) |
| `--out <dir>` | write all generated files **flat** into `<dir>` (names prefixed with the layer directory, e.g. `internal_model__gen_widget.go`) instead of their real package directories |

Without `--force`, an existing target file aborts the run with `refusing to overwrite existing file <path> (use --force)` before anything is written; with `--dry-run` such files are listed as `(EXISTS — would skip without --force)`. Missing flag values (`--root`/`--out` without a value), unknown flags (`unknown flag "-x"`) and extra positional arguments are errors.

### Derived names

All casings are derived from the one input, and printed in the plan header so a wrong guess is visible:

| Derived form | Example for `ContactGroup` | Used for |
|---|---|---|
| Pascal | `ContactGroup` | Go type, bundle kind, TS interface |
| PascalPlural | `ContactGroups` | `register…()` function, page component |
| camel | `contactGroup` | JS identifiers |
| kebab | `contact-group` | storage kind value, REST singular |
| kebabPlural | `contact-groups` | REST path, `resourceApi` base, SSE invalidation key |
| snake | `contact_group` | Go file names |
| Title | `Contact Group` | human label |
| ConstName | `KindContactGroup` | storage kind constant |

Words are split on `-`, `_`, `.`, `/` and on camel-case boundaries (`HTTPProxy` → `http proxy`). Pluralisation uses a small English rule set: `…s/x/z/ch/sh` → `+es`, consonant + `y` → `ies`, otherwise `+s`.

### Files stamped

| File | Content |
|---|---|
| `internal/model/gen_<snake>.go` | Go struct `<Pascal>` with envelope fields (`id`, `tenantId`, `name`, `version`, `createdAt`, `updatedAt`) and two placeholder domain fields (`enabled`, `notes`) |
| `internal/storage/gen_<snake>_kind.go` | `const Kind<Pascal> = "<kebab>"` — a standalone declaration so the generator never edits the hand-maintained kind block |
| `internal/api/gen_<snake>.go` | `func (a *API) register<PascalPlural>()` calling `a.resourceCRUD("<kebabPlural>", storage.Kind<Pascal>, "config", model.<Pascal>{})` — the five canonical routes `GET/POST /api/v1/<kebabPlural>`, `GET/PUT/DELETE /api/v1/<kebabPlural>/{name}` with RFC 9457 problems, ETag/If-Match and audit logging, read permission `objects:read`, write permission `config:write` |
| `web/src/types/gen_<kebab>.ts` | `export interface <Pascal>` matching the stub fields |
| `web/src/pages/<PascalPlural>.tsx` | an admin page: table + create/edit dialog over `resourceApi('<kebabPlural>')`, optimistic locking aware, mirroring `web/src/pages/Templates.tsx` |

All files are written with mode 0644 (directories 0755).

### The checklist it prints

After writing, `np-gen` prints `Manual follow-up (paste these — np-gen does not edit hand-maintained registries):` with numbered steps:

1. Register the REST routes — `internal/api/api.go`, in `registerAll()`: `a.register<PascalPlural>()`.
2. Add the bundle kind to the apply-order vocabulary — `internal/bundle/bundle.go`, `KindOrder`: `"<Pascal>",` (slot it by dependency order).
3. Add the SSE invalidation key so live updates refresh the list — `web/src/api.ts`, `invalidations` map: add `['<kebabPlural>']` to the `config` event's list.
4. Wire the page into the router/nav and the i18n labels (e.g. `web/src/main.tsx`): `import { <PascalPlural>Page } from './pages/<PascalPlural>'`.
5. Fold the generated type into the canonical barrel — move the interface from `web/src/types/gen_<kebab>.ts` into `web/src/types.ts`, then delete the stub.
6. Edit the stubs to add your real fields: `internal/model/gen_<snake>.go` (domain fields) and `web/src/pages/<PascalPlural>.tsx` (form fields).

Optional: add a `validateResourceDoc()` case in `internal/api/objects.go` for kind-specific server-side validation (`storage.Kind<Pascal>`), and a Go test under `internal/api/` mirroring `resources_test.go`. Afterwards `go build ./...` and `cd web && npm run build` should stay green; regenerate the typed client and the docs' OpenAPI copy with `make types` ([API overview](/docs/reference/api-overview/#openapi-document-and-swagger-ui)).

## Example plan output

```text
np-gen new-resource MaintenanceWindow
  derived: Pascal=MaintenanceWindow plural=MaintenanceWindows camel=maintenanceWindow kebab=maintenance-window kebabPlural=maintenance-windows snake=maintenance_window kind="maintenance-window"

  Would write  internal/model/gen_maintenance_window.go               Go model struct (MaintenanceWindow)
  Would write  internal/storage/gen_maintenance_window_kind.go        storage resources-kind constant (KindMaintenanceWindow = "maintenance-window")
  Would write  internal/api/gen_maintenance_window.go                 REST CRUD registration (registerMaintenanceWindows)
  Would write  web/src/types/gen_maintenance-window.ts                frontend type (MaintenanceWindow)
  Would write  web/src/pages/MaintenanceWindows.tsx                   frontend admin page (MaintenanceWindowsPage)

Manual follow-up (paste these — np-gen does not edit hand-maintained registries):
  …
```
