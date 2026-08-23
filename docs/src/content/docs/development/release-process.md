---
title: Release process
description: How a Northplane version is stamped and shipped — ldflags versioning, the tag-triggered Release workflow (tarballs, Windows zip, checksums, multi-arch image), the main-branch Deploy workflow, the install.sh asset-name contract, release notes and the checklists.
sidebar:
  order: 5
---

There are two shipping paths. **Every green CI run on `main`** publishes a container image tagged with the commit and rolls production forward (the Deploy workflow). **A `v*` tag** produces a GitHub Release with per-platform archives, a `checksums.txt` and a multi-arch image with semver tags (the Release workflow). Both embed the current UI and this documentation into the binary. Nothing here is manual except creating the tag.

## Versioning

The version is a plain string compiled into each binary:

| Where | Detail |
|---|---|
| Definition | `var version = "1.0.0-dev"` in `package main` of `cmd/northplaned` (the CLI and agent carry the same variable) |
| Injection | `-ldflags "-X main.version=<value>"`; the Makefile uses `VERSION ?= 1.0.0-dev` (`make build VERSION=1.2.3`); the Dockerfile has `ARG VERSION=docker` |
| CD image | `VERSION=main-<12-char commit sha>` (Deploy workflow) |
| Release archives | `VERSION="${GITHUB_REF_NAME#v}"` — the tag **without** the `v` (`v1.2.3` → `1.2.3`) |
| Release image | `VERSION=${{ github.ref_name }}` — the tag **with** the `v` (`v1.2.3`) |

Where the value surfaces at runtime: `northplaned version` (and the usage header), `GET /api/v1/system/info` (`version`, anonymous), `info.version` of `/api/openapi.json`, the MCP server implementation version, the footer of the login/setup/register pages, the Admin → System health tab, the federation site heartbeat (Admin → Sites shows each edge's version) and `manifest.json` of a backup. Operators use these to confirm a rollout — see [Upgrades](/docs/administration/upgrades/).

:::note[The `v` differs between the two release artefacts]
From the same tag `v1.2.3`, the tarball binaries report `northplaned 1.2.3` while the container image reports `northplaned v1.2.3`, because `release.yml` strips the prefix for the archives but passes `github.ref_name` verbatim as the Docker build argument. Compare with that in mind when verifying a release.
:::

There is no version bump commit, no `VERSION` file and no CHANGELOG in the repository; the tag is the version.

## Tag → Release workflow (`.github/workflows/release.yml`)

Triggered by `push` of a tag matching `v*`. Permissions: `contents: write`, `packages: write`.

| Job | Needs | What it does |
|---|---|---|
| `ui` | — | Node 22 (npm cache keyed on both lockfiles): `cd web && npm ci && npm run build`, copies `web/dist` → `internal/web/dist`, uploads artifact `ui-dist`; then `cd docs && npm ci && npm run build:embed` and uploads `docs-dist` (both 1-day retention) |
| `binaries` | `ui` | matrix `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64` (`fail-fast: false`); Go 1.25; downloads `ui-dist` into `internal/web/dist` and `docs-dist` into `internal/docs/dist`; builds `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}"`; packages and computes `sha256sum` per asset; uploads `archive-<os>-<arch>` |
| `release` | `binaries` | downloads all `archive-*`, concatenates the `.sha256` files into `checksums.txt`, creates the GitHub Release with `softprops/action-gh-release@v2` (`files`: `northplane_*.tar.gz`, `northplane_*.zip`, `checksums.txt`; `generate_release_notes: true`) |
| `docker` | — (runs in parallel) | QEMU + buildx, `docker/login-action` to `ghcr.io` with `GITHUB_TOKEN`, `docker/metadata-action` tags `type=semver,pattern={{version}}`, `type=semver,pattern={{major}}.{{minor}}`, `type=raw,value=latest`; `docker/build-push-action` with `platforms: linux/amd64,linux/arm64`, `build-args: VERSION=${{ github.ref_name }}`, GHA layer cache |

What ends up on the release page for tag `v1.2.3`:

| Asset | Contents |
|---|---|
| `northplane_v1.2.3_linux_amd64.tar.gz`, `…_linux_arm64.tar.gz`, `…_darwin_amd64.tar.gz`, `…_darwin_arm64.tar.gz` | `northplaned`, `np`, `np-agent`, `LICENSE` |
| `northplane_v1.2.3_windows_amd64.zip` | `np.exe`, `np-agent.exe`, `LICENSE` — **no `northplaned`**: it needs Unix process groups for plugin execution |
| `checksums.txt` | one `sha256sum` line per asset: `<sha256>  <asset>` |
| auto-generated release notes | see [Release notes](#release-notes) |

And on `ghcr.io/myfoxit/northplane`: `1.2.3`, `1.2`, `latest` for `linux/amd64` and `linux/arm64`. The Dockerfile is self-contained (it builds the UI and the docs in its own stages), so the image does not depend on the `ui` job's artifacts; the compose files' `build: .` fallback keeps working for the same reason.

Note the naming contract: archive names use the tag **with** `v` (`${GITHUB_REF_NAME}`), which is what `install.sh` expects.

## main → Deploy workflow (`.github/workflows/deploy.yml`)

Triggered by `workflow_run` of the CI workflow on `main` (proceeds only if CI concluded `success`) or by manual `workflow_dispatch` with a `demo` input (`repo-default` | `true` | `false`). Concurrency group `deploy-production`, never cancelled in flight.

1. `publish` — checks out the CI run's `head_sha`, builds the image once and pushes `ghcr.io/myfoxit/northplane:main-<sha12>` **and** `:latest` (`VERSION=main-<sha12>`).
2. `deploy` (np-01, production) and `deploy-hetzner` (np-02) run in parallel: render `.env`, ship the compose file, `docker login ghcr.io` with the run's ephemeral `GITHUB_TOKEN`, `docker compose pull`, `docker logout`, `docker compose up -d --remove-orphans`, then verify up to 12 × 5 s that the running container image equals the wanted tag **and** `/healthz` answers `ok`; on failure restore `.env.previous` and `docker compose up -d`.

The image that reaches production therefore carries the commit id, not a semver; `latest` is moved by **both** workflows, so it points at whichever ran last (a main-branch build or a tagged release). Use explicit tags in anything you deploy by hand. Details, variables and secrets: [CI/CD](/docs/deployment/ci-cd/); the current state of each host: [Environments](/docs/deployment/environments/).

:::caution[Merging to main is a deploy]
As long as CI is green, a merge to `main` rolls the production instance forward within the next CI + Deploy run. Keep unfinished work on feature branches (see the worktree convention in [Development setup](/docs/development/setup/)). A red Deploy run is not automatically a failed production rollout — check the per-job results; the `deploy-hetzner` job has been failing because its host no longer exists while `deploy` succeeds.
:::

## The `install.sh` contract

`install.sh` at the repository root (meant for `curl -fsSL https://raw.githubusercontent.com/myfoxit/northplane/main/install.sh | sh`) encodes the release asset layout, so changing `release.yml` means changing the script too:

- It resolves the tag from `https://api.github.com/repos/myfoxit/northplane/releases/latest` (`tag_name`), maps `uname` to `linux`/`darwin` and `amd64`/`arm64`, and downloads `northplane_<tag>_<os>_<arch>.tar.gz` plus `checksums.txt` from `https://github.com/myfoxit/northplane/releases/download/<tag>/`.
- It verifies the archive against the matching `checksums.txt` line (`grep " <asset>$"`, `sha256sum` or `shasum -a 256`) and aborts on mismatch.
- It installs `northplaned`, `np`, `np-agent` with mode 0755 into `/usr/local/bin` (using `sudo` when the directory is not writable and a TTY is present) or `~/.local/bin`, warns when the destination is not on `PATH`, and prints the `northplaned serve` / `sudo northplaned init` hints.
- Windows is not covered by the script (the zip is for manual download); unsupported OS/arch abort with a pointer to building from source.

Two facts worth knowing when you test it: the script resolves the newest release through GitHub's `releases/latest` endpoint, which only lists full releases — when none exists yet it falls back to the newest release of any kind (pre-releases included); and `NP_VERSION=vX.Y.Z` pins a specific tag, `NP_INSTALL_DIR` and `NP_BINARIES` control where and what is installed.

## Release notes

Nothing generates a changelog in the repository; the Release workflow passes `generate_release_notes: true`, so GitHub composes the notes from the pull requests and commits between the previous release and the new tag. That makes commit subjects and PR titles the release notes — write them for a reader of the notes ("Email smtp: announce a real EHLO name, bare envelope addresses"), and edit the generated text on the release page afterwards if you want a summary or upgrade hints on top.

## Checklists

### Before tagging

1. `main` is green: the latest CI run (all blocking jobs — `ui`, `docs`, `types`, `lint`, `test`, `e2e`, `cross-build`) succeeded, and the production instance deployed from that commit is healthy (`/api/v1/system/info` shows `main-<sha12>` of the commit you are about to tag).
2. `make types-check` is clean and `web/src/types.gen.ts` + `docs/src/assets/openapi.json` are committed (a release builds the docs from the committed spec copy).
3. New schema migrations are forward-only and additive where possible; `northplaned migrate` against a copy of a production-like database succeeds on SQLite and, if you support it, PostgreSQL (see [Testing](/docs/development/testing/)).
4. The documentation builds (`cd docs && npm run build`) — the Release `ui` job runs `npm run build:embed`, which also runs the links validator, so a broken link fails the release.
5. Decide the version: semver `vMAJOR.MINOR.PATCH`; pre-releases as `v1.2.0-rc1` (remember the `releases/latest` rule above).

### Cutting the release

```bash
git checkout main && git pull --ff-only
git tag -a v1.2.3 -m "Northplane 1.2.3"
git push origin v1.2.3
```

Then watch the **Release** workflow: `ui` → `binaries` (5 legs) → `release`, with `docker` in parallel. Total runtime is dominated by the multi-arch image build.

### After the workflow is green

1. On the release page: five archives, `checksums.txt`, generated notes. Spot-check one checksum: `sha256sum -c --ignore-missing checksums.txt` next to a downloaded archive.
2. `docker pull ghcr.io/myfoxit/northplane:1.2.3` and `docker run --rm ghcr.io/myfoxit/northplane:1.2.3 version` → `northplaned v1.2.3`; `docker manifest inspect` shows `linux/amd64` and `linux/arm64`.
3. Unpack a tarball, run `./northplaned version` → `northplaned 1.2.3`; `./northplaned serve` on a throwaway data dir and open `/docs/` — the manual must be embedded (a 501 there means the `docs-dist` artifact did not reach the build).
4. Test `install.sh` on a clean Linux or macOS machine (`NP_VERSION=v1.2.3` to target the new release explicitly).
5. If production should run the tagged image instead of `main-<sha>`, set `NORTHPLANE_IMAGE` on the host accordingly (the Deploy workflow overwrites `.env` on its next run — see [Operations](/docs/deployment/operations/)).

### Hotfixes and rollback

- A hotfix is a normal commit on `main` (CI → Deploy) followed by a new patch tag; there is no release branch.
- Rolling a host back means starting the previous image (`.env.previous` holds the previous `NORTHPLANE_IMAGE`; the Deploy workflow restores it automatically when verification fails). Schema migrations are not reversible: an older binary will not downgrade a database that a newer one migrated — take a backup before upgrading (`northplaned backup`, see [Storage](/docs/administration/storage/)).
