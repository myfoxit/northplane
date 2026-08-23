# Contributing to Northplane

Thanks for helping. Northplane is one Go binary (`cmd/`, `internal/`), a React UI (`web/`) and an
Astro Starlight manual (`docs/`). Everything below is the short version — the long one is the
[development guide](https://doktrace.com/docs/development/setup/) in the manual.

## Set up

```bash
git clone https://github.com/myfoxit/northplane.git && cd northplane   # Go 1.25, Node 22
make dev            # Vite HMR on :5173 + auto-rebuilt backend on 127.0.0.1:8443 (demo data seeded)
make docs-dev       # the manual with live reload at http://localhost:4321/docs/
```

## Before you open a pull request

| Check | Command |
|---|---|
| Go formatting, vet, tests (CI adds `-race`) | `gofmt -l cmd internal` · `make test` |
| Static analysis (the CI gate is stricter than `go vet`) | `golangci-lint run ./...` |
| Frontend lint, unit tests, types | `cd web && npm run lint && npm test && npx tsc -b --noEmit` |
| API changed? regenerate the TypeScript types + the docs' OpenAPI copy | `make types` (CI fails on drift: `make types-check`) |
| UI or behaviour changed? end-to-end | `make e2e` (Playwright, German locale — keep the specs asserting German) |
| Docs changed? build = link validation | `cd docs && npm run build` |

Keep pull requests focused, write commit subjects for the release notes (they become the changelog),
and update the matching manual page in the same PR — the docs are part of the product and ship
inside the binary at `/docs/`.

## Conventions in brief

- API: every capability is a REST endpoint first (`internal/api`, `a.handle(...)` registers route +
  permission + OpenAPI metadata); RFC 9457 problem documents; `If-Match` on updates; UUIDv7 ids.
- Storage: SQLite and PostgreSQL must both work; add schema changes as a new migration.
- UI: German is the reference language — add strings to both catalogs in `web/src/i18n.ts`.
- Tests live next to the code (`_test.go`, `*.test.ts(x)`, `web/e2e/*.spec.ts`).

## Reporting bugs and proposing features

Use the [issue templates](https://github.com/myfoxit/northplane/issues/new/choose). For anything
security-relevant follow [SECURITY.md](SECURITY.md) instead of a public issue.

By contributing you agree that your contributions are licensed under the [MIT License](LICENSE).
