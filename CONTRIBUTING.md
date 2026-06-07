# Contributing to Northplane

Thanks for your interest! Northplane is a Go single-binary monitoring & alerting
system with an embedded React UI. This guide gets you productive fast.

## Prerequisites

- Go ≥ 1.25
- Node ≥ 22 (only to rebuild the UI)
- Optional: PostgreSQL ≥ 15 to exercise the second storage backend

## Build & run

```bash
make                 # build the UI + all three binaries into ./bin

# Dev server (no root, SQLite under a per-user data dir, loopback HTTP):
./bin/northplaned serve
./bin/northplaned bootstrap-admin       # prints an admin token (once)
export NP_TOKEN=np_…  NP_SERVER=http://127.0.0.1:8443
./bin/np get hosts
```

The Go build embeds `internal/web/dist` via `go:embed`. That directory is
committed, so `go build ./...` works without Node. Run `make web` after changing
anything under `web/`.

## Tests

```bash
make test                                       # go vet + all suites (SQLite)
go test -race ./...                             # race detector
NORTHPLANE_TEST_PG_DSN=postgres://np:np@localhost:5432/northplane?sslmode=disable \
  go test ./internal/storage/...                # PostgreSQL backend matrix
go test -fuzz=FuzzParsePerfdata ./internal/nagios/
```

CI runs vet + `-race` tests on Linux and macOS, the storage suite against
PostgreSQL, and cross-compiles for linux/amd64, linux/arm64 and darwin/arm64.
Please make sure `go vet ./...` and `go test -race ./...` are clean before
opening a PR.

## Conventions

- **Standard library first.** Each external dependency must earn its place
  (see SPEC §7.9). Prefer the stdlib and the few vetted deps already present.
- **Both storage backends are first-class.** Any storage change must work on
  SQLite *and* PostgreSQL; add/extend a test in `internal/storage` (it runs the
  same cases against both via the `matrix` helper).
- **Match the surrounding code** — naming, comment density, error wrapping.
- Keep functions auditable; security-sensitive paths (auth, secrets, exec,
  parsers) deserve a test for the failure mode, not just the happy path.

## Architecture

See the README ("Architecture") and [SPEC.md](SPEC.md) for the full design and
the decision register (ADRs).

## License

By contributing you agree that your contributions are licensed under the
project's [MIT License](LICENSE).
