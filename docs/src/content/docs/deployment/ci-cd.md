---
title: CI/CD pipeline
description: How a merge to main becomes a production deploy — the CI jobs, the Deploy workflow (publish, deploy, deploy-hetzner, verify, rollback), GitHub variables and secrets, image tags, the release workflow and how the docs ship.
sidebar:
  order: 4
---

The repository `myfoxit/northplane` (private, default branch `main`) has three GitHub Actions workflows:

| Workflow | File | Trigger | Result |
|---|---|---|---|
| **CI** | `.github/workflows/ci.yml` | push to `main`/`master`, pull requests | quality gate: UI, docs, typed-codegen drift, lint, tests, e2e, PostgreSQL matrix, cross-build |
| **Deploy** | `.github/workflows/deploy.yml` | CI completed successfully on `main`, or manual dispatch | image `ghcr.io/myfoxit/northplane:main-<sha12>` + `latest`; rollout to `np-01` (job `deploy`) and `np-02` (job `deploy-hetzner`) |
| **Release** | `.github/workflows/release.yml` | tag push `v*` | GitHub Release with tarballs + `checksums.txt`; multi-arch image with semver tags |

:::note[A push to main is a production deploy]
There is no staging gate between `main` and `doktrace.com`. Merge only what you are willing to run in production, and treat a red Deploy run as described under [Reading a red run](/docs/deployment/ci-cd/#reading-a-red-run).
:::

## From merge to live: the timeline

1. **Push/merge to `main`.** CI starts; all jobs run in parallel where possible (`test`, `e2e`, `postgres` and `cross-build` wait for `ui`).
2. **CI finishes green.** The `workflow_run` trigger starts **Deploy** (`publish` runs only when `github.event.workflow_run.conclusion == 'success'`; a manual dispatch always proceeds). Concurrency group `deploy-production`, `cancel-in-progress: false` — one rollout at a time, never cancelled mid-flight.
3. **`publish`** checks out the CI run's `head_sha`, builds the Dockerfile with buildx for `linux/amd64` and `linux/arm64` (`VERSION=main-<sha12>`, GHA layer cache; the Dockerfile cross-compiles from the build platform, so no QEMU) and pushes `ghcr.io/myfoxit/northplane:main-<sha12>` and `:latest`.
4. **`deploy`** (np-01) and **`deploy-hetzner`** (np-02) run in parallel after `publish`: SSH set-up → render `.env` → ship the compose stack → `docker compose pull`/`up -d` → verify → roll back on failure → summary line.
5. The verified run on 2026-08-23 (run `32629242562`, merge `daa6dc5`, 08:48 UTC) ended with `publish` and `deploy` succeeded, `deploy-hetzner` failed; the container on `np-01` started at 08:50:57 UTC — under three minutes after the Deploy run began.

## CI jobs

| Job | Needs | What it does | Gate |
|---|---|---|---|
| `ui` | — | Node 22: `npm ci`, `npm run lint`, `npm test` (vitest), `npm run build` in `web/`; uploads `internal/web/dist` as artifact `ui-dist` | blocking |
| `docs` | — | Node 22: `npm ci` and `npm run build` in `docs/` — the Starlight build **fails on any broken internal link or anchor** (starlight-links-validator) and on a page that does not compile; uploads `docs-dist` | blocking |
| `types` | — | Go 1.25 + Node 22: `make types-check` — regenerates `web/src/types.gen.ts` and `docs/src/assets/openapi.json` from `northplaned openapi` and fails on a diff | blocking |
| `lint` | — | `gofmt -l cmd internal` must be empty; `golangci-lint` v2.12.2 (`go install`ed, per `.golangci.yml`) | blocking |
| `test` | `ui` | matrix `ubuntu-latest` + `macos-latest`: `go vet ./...`, `go test -race ./...`, builds `northplaned`, `np`, `np-agent` | blocking |
| `e2e` | `ui` | builds `northplaned` with the embedded UI, installs Playwright Chromium, `npm run test:e2e` against `northplaned --demo` (locale `de-DE`); uploads `playwright-report` | blocking |
| `postgres` | `ui` | `go test ./internal/storage/...` with `NORTHPLANE_TEST_PG_DSN` against a `postgres:16` service | **non-blocking** (`continue-on-error: true`) — known `TestAuditChain` failure on PostgreSQL (jsonb normalises the hashed JSON) |
| `cross-build` | `ui` | `CGO_ENABLED=0` builds for linux/amd64, linux/arm64, darwin/arm64, windows/amd64 — `northplaned` is skipped on windows (needs unix process groups), `np` and `np-agent` build everywhere | blocking |

Locally: `make test`, `make race`, `make e2e`, `make types-check`, `cd docs && npm run build`. See [Testing](/docs/development/testing/).

## The Deploy workflow

### Triggers and inputs

```yaml title=".github/workflows/deploy.yml (excerpt)"
on:
  workflow_run:
    workflows: ["CI"]
    types: [completed]
    branches: [main]
  workflow_dispatch:
    inputs:
      demo:
        description: "Demo/real data switch for this run"
        type: choice
        options: [repo-default, "true", "false"]
        default: repo-default

concurrency:
  group: deploy-production
  cancel-in-progress: false

permissions:
  contents: read
  packages: write # push the image to ghcr.io
```

Manual dispatch (**Actions → Deploy → Run workflow**) re-publishes the image for the current `main` and rolls it out; the `demo` dropdown overrides the repository variable `NORTHPLANE_DEMO` **for np-01 and for that run only** (`repo-default` keeps the variable). The `deploy-hetzner` job ignores the dropdown — np-02 is always a real-data instance.

### Job `publish`

```yaml title="deploy.yml — image reference"
- name: Resolve image ref
  id: img
  run: |
    SHA="${{ github.event.workflow_run.head_sha || github.sha }}"
    echo "ver=main-${SHA::12}" >> "$GITHUB_OUTPUT"
- name: Build and push image
  uses: docker/build-push-action@v6
  with:
    context: .
    push: true
    build-args: VERSION=${{ steps.img.outputs.ver }}
    tags: |
      ghcr.io/${{ github.repository }}:${{ steps.img.outputs.ver }}
      ghcr.io/${{ github.repository }}:latest
    cache-from: type=gha
    cache-to: type=gha,mode=max
```

The `VERSION` build arg becomes `main.version` in the binary, so `GET /api/v1/system/info` on the instance reports exactly the image tag (`"version":"main-daa6dc518a2b"`).

### Job `deploy` (np-01)

| Step | What happens on the runner / the VM |
|---|---|
| Set up SSH | writes `secrets.DEPLOY_SSH_KEY` to `~/.ssh/deploy_key`, `vars.DEPLOY_KNOWN_HOSTS` to `~/.ssh/known_hosts`, and an ssh config `Host prod` = `vars.DEPLOY_HOST`:`vars.DEPLOY_PORT` as `vars.DEPLOY_USER`, `StrictHostKeyChecking yes` |
| Render `.env` | `DEMO` = `vars.NORTHPLANE_DEMO`, overridden by the dispatch input unless `repo-default`; must be `true` or `false`; `MODE_DIR` = `demo` or `real`. Writes: `NORTHPLANE_IMAGE=ghcr.io/<repo>:<ver>`, `NORTHPLANE_BASE_URL=<vars>`, `NORTHPLANE_DEMO=$DEMO`, `NORTHPLANE_ALLOW_SIGNUP=$SIGNUP` (`true` only when the repo variable `NORTHPLANE_ALLOW_SIGNUP` is `true`, otherwise `false`), `NORTHPLANE_DATA_DIR=/var/lib/northplane/$MODE_DIR`, `NP_DEFAULT_ADMIN_EMAIL=<vars>`, `NP_DEFAULT_ADMIN_PASSWORD=<secret>` |
| Ship compose stack | on the VM: `cp .env .env.previous`; then `scp deploy/docker-compose.vm.yml prod:$DEPLOY_PATH/docker-compose.yml` and the rendered `.env` |
| Pull and roll forward | `docker login ghcr.io` with the run's ephemeral `GITHUB_TOKEN` (the server never stores a registry credential), `docker compose pull -q`, `docker logout ghcr.io`, `docker compose up -d --remove-orphans` |
| Verify rollout | up to 12 × 5 s: `docker inspect --format '{{.Config.Image}}' northplane-northplane-1` must equal the wanted image **and** `curl -fsS -m 5 http://localhost:8443/healthz` must return `ok` |
| Rollback on failure | only if verify failed: `mv .env.previous .env && docker compose up -d` — the previous image tag comes back |
| Summary | `Deployed ghcr.io/…:<ver> to <DEPLOY_HOST>:2201 (VM saas1) — live at <NORTHPLANE_BASE_URL>.` in the job summary |

```bash title="deploy.yml — the verify loop (np-01)"
WANT="ghcr.io/${{ github.repository }}:${{ needs.publish.outputs.ver }}"
for i in $(seq 1 12); do
  RUNNING=$(ssh prod "docker inspect --format '{{.Config.Image}}' northplane-northplane-1 2>/dev/null" || true)
  HEALTH=$(ssh prod "curl -fsS -m 5 http://localhost:8443/healthz" || true)
  if [ "$RUNNING" = "$WANT" ] && [ "$HEALTH" = "ok" ]; then
    echo "live: $RUNNING (healthz ok)"; exit 0
  fi
  echo "waiting ($i/12): image='$RUNNING' health='$HEALTH'"; sleep 5
done
echo "rollout did not become healthy"; exit 1
```

### Job `deploy-hetzner` (np-02)

Same shape, different target and files: `Host np-02` = `vars.HETZNER_HOST`, user `deploy`, key `secrets.HETZNER_SSH_KEY`, host key `vars.HETZNER_KNOWN_HOSTS`; ships `deploy/docker-compose.yml` **and** `deploy/Caddyfile` to `/opt/northplane`; renders `.env` with `DOMAIN=localhost`, `SERVER_IP=<HETZNER_HOST>`, `ACME_EMAIL=admin@doktrace.com`, `NORTHPLANE_BASE_URL=https://<HETZNER_HOST>`, `NORTHPLANE_DEMO=false`, `NORTHPLANE_DATA_DIR=/var/lib/northplane/real`, `NP_DEFAULT_ADMIN_EMAIL=root@localhost`, `NP_DEFAULT_ADMIN_PASSWORD=<secrets.HETZNER_ADMIN_PASSWORD>`. Because the app container is distroless and not published on the host, the health probe goes through Caddy: `docker compose exec -T caddy wget -qO- http://northplane:8443/healthz`.

:::caution[np-02 is gone — this job fails on every run]
Verified 2026-08-23: `91.98.92.10` no longer answers on port 22 and serves a parking page on 443 — the Hetzner box was reclaimed and the IP reassigned. `deploy-hetzner` fails at "Ship compose stack" (`ssh: connect to host 91.98.92.10 port 22: Connection timed out`) on every run (2026-08-14, 2026-08-21, 2026-08-23). `HETZNER_HOST` and `HETZNER_KNOWN_HOSTS` now point at a stranger's host. Either re-create the box and rotate the `HETZNER_*` variables and secrets ([recreation checklist](/docs/deployment/provisioning/#np-02-recreation-checklist)) or remove the job.
:::

## Reading a red run

The `deploy` and `deploy-hetzner` jobs are independent (`needs: publish` each). A red Deploy run therefore does **not** mean production missed the rollout:

1. Open the run and look at the per-job conclusions. `publish` green + `deploy` green = `np-01` is live on the new image, whatever `deploy-hetzner` says.
2. Confirm on the instance: `curl -s https://doktrace.com/api/v1/system/info` — `version` must equal `main-<sha12>` of the merge commit.
3. If `deploy` itself is red: the "Verify rollout" step tells you whether the image never changed (pull/login problem) or the app never returned `ok` on `/healthz` (it crashed on start — read `docker compose logs northplane` on the VM). The rollback step has already restored `.env.previous`; production is on the previous tag.
4. If CI was red, Deploy never started (`workflow_run` with a non-success conclusion skips `publish`); fix `main` or re-run the failed CI jobs (`gh run rerun --failed <id>`). Known flaky/non-blocking: the `postgres` job.

## Image tagging

| Tag | Produced by | Meaning |
|---|---|---|
| `ghcr.io/myfoxit/northplane:main-<sha12>` | Deploy `publish` | immutable per green build of `main`; `linux/amd64` + `linux/arm64`; `VERSION` baked in; what production pins in `.env` |
| `ghcr.io/myfoxit/northplane:latest` | Deploy `publish` **and** Release `docker` | moving; whichever ran last. Fine for trials, never for production pins |
| `ghcr.io/myfoxit/northplane:<major>.<minor>.<patch>` and `<major>.<minor>` | Release `docker` (tag `v*`) | semver, `linux/amd64` + `linux/arm64`, `VERSION=<tag>` |

The package is public; the deploy jobs nevertheless log in with the run's `GITHUB_TOKEN` (`packages: read`) for the pull and log out immediately after — the servers never store a registry credential.

## GitHub configuration

Created under **Settings → Secrets and variables → Actions** (values verified with `gh variable list` on 2026-08-23).

### Repository variables

| Variable | Value | Used by |
|---|---|---|
| `DEPLOY_HOST` | `51.83.96.40` | `deploy` — ssh target (the hypervisor; DNAT forwards to the VM) |
| `DEPLOY_PORT` | `2201` | `deploy` — the DNAT port → VM `:22` |
| `DEPLOY_USER` | `deploy` | `deploy` |
| `DEPLOY_PATH` | `/opt/northplane` | `deploy` — compose project dir on the VM |
| `DEPLOY_KNOWN_HOSTS` | `[51.83.96.40]:2201 ssh-ed25519 …` | `deploy` — pins the VM host key (`StrictHostKeyChecking yes`) |
| `DEPLOY_DOMAIN` | `doktrace.com` | informational — **not referenced by any workflow** |
| `NORTHPLANE_BASE_URL` | `https://doktrace.com` | `deploy` — `.env` |
| `NORTHPLANE_DEMO` | `false` (set 2026-08-20) | `deploy` — the demo/real switch |
| `NP_DEFAULT_ADMIN_EMAIL` | `admin@doktrace.com` | `deploy` — `.env` |
| `HETZNER_HOST` | `91.98.92.10` | `deploy-hetzner` — **stale, host is gone** |
| `HETZNER_KNOWN_HOSTS` | `91.98.92.10 ssh-ed25519 …` | `deploy-hetzner` — stale |

### Repository secrets (names only)

| Secret | Purpose |
|---|---|
| `DEPLOY_SSH_KEY` | private half of the CI deploy key authorised for `deploy@VM101` by `provision-server.sh` |
| `NP_DEFAULT_ADMIN_PASSWORD` | break-glass admin password for np-01 (`admin@doktrace.com`); change the password in the UI after first login — the secret only seeds |
| `HETZNER_SSH_KEY` | CI deploy key for np-02 (stale) |
| `HETZNER_ADMIN_PASSWORD` | np-02 break-glass admin (`root@localhost`) (stale) |

No registry secret exists — GHCR pulls on the servers use the workflow's `GITHUB_TOKEN`.

## The Release workflow (tags `v*`)

| Job | What it does |
|---|---|
| `ui` | builds the UI (`web/`) **and** the docs (`docs/`, `npm run build:embed`); uploads `ui-dist` and `docs-dist` |
| `binaries` | matrix linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64; `VERSION="${GITHUB_REF_NAME#v}"` (tag without `v`) via ldflags, `CGO_ENABLED=0 -trimpath -ldflags "-s -w"`; downloads `ui-dist` → `internal/web/dist` and `docs-dist` → `internal/docs/dist` so the binaries embed both; packs `northplane_<tag>_<os>_<arch>.tar.gz` (`northplaned np np-agent LICENSE`) or `northplane_<tag>_windows_amd64.zip` (`np.exe np-agent.exe LICENSE` — no `northplaned` on Windows) plus a `.sha256` each |
| `release` | aggregates `checksums.txt`, creates the GitHub Release with `softprops/action-gh-release` and `generate_release_notes: true` |
| `docker` | buildx + QEMU, pushes `ghcr.io/myfoxit/northplane` for `linux/amd64,linux/arm64` with tags `{{version}}`, `{{major}}.{{minor}}`, `latest`, `VERSION=<tag>` |

Asset names are a contract with `install.sh` (`northplane_<tag>_<os>_<arch>.tar.gz` — note the tag keeps its `v`; the embedded version string drops it). The only release so far is the pre-release `v0.0.0-rc1` (2026-06-07) with linux/darwin tarballs and `checksums.txt`. Release mechanics and checklists: [Release process](/docs/development/release-process/).

## How the documentation ships

The pages you are reading are built from `docs/` and embedded into `northplaned`, which serves them at `/docs/` on every instance — public, no login, with their own Content-Security-Policy.

| Where | How |
|---|---|
| Dockerfile | stage `docs` (`node:22-alpine`, `npm ci --legacy-peer-deps`, `npm run build:embed`) → `COPY --from=docs /docs/dist ./internal/docs/dist` before the Go build → every CD image carries the manual matching its commit |
| Release binaries | the `ui` job's `docs-dist` artifact is placed in `internal/docs/dist` before `go build` |
| CI | the `docs` job is the quality gate (build + link validation); its artifact is not embedded into the CI test binaries |
| Local | `make docs` builds and stages `docs/dist` into `internal/docs/dist` (git-ignored except `.gitkeep`); a plain `go build` without staging still compiles, and `/docs/` then answers `501 documentation not embedded in this build — run make docs` |
| OpenAPI | `docs/src/assets/openapi.json` is a copy of `northplaned openapi`, refreshed by `make types` and drift-checked by `make types-check` (CI job `types`); the REST reference pages under `/docs/reference/api/` are generated from it |

The image verified on np-01 on 2026-08-23 (`main-daa6dc518a2b`) was built **before** the `docs` stage existed, so `https://doktrace.com/docs/` serves the documentation only from the first deploy that includes the docs build. Authoring workflow: [Documentation](/docs/development/documentation/).

Related: [Provisioning](/docs/deployment/provisioning/) (first deploy to a new host), [Operations](/docs/deployment/operations/) (redeploy, rollback, switching modes), [Upgrades](/docs/administration/upgrades/).
