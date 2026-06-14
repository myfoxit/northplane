# Northplane — production deployment (Docker Compose + Caddy, CI/CD)

Continuous deployment to a single Linux host. Push to `main` → CI runs → on
green, the **Deploy** workflow builds the image, pushes it to GHCR, and rolls
the Compose stack on the server over SSH as an unprivileged `deploy` user.

```
GitHub: push main ─▶ CI (test/lint/e2e) ─▶ Deploy workflow
                                              │ build+push ghcr.io image
                                              ▼ ssh deploy@host
Server: Caddy :80/:443 ──TLS──▶ northplaned :8443 ──▶ northplane-data volume
        (Let's Encrypt for doktrace.com, self-signed for the bare IP)
```

The same binary is a demo instance or a real instance depending on one
switch — see **Demo / real data switch** below.

---

## 1. One-time server provisioning

Run the idempotent provisioner as root. It installs Docker CE, creates the
`deploy` user, authorizes the CI deploy key, and lays down the project dir +
persistent `secret.key`:

```sh
# from a checkout, with the CI deploy PUBLIC key:
ssh root@178.104.12.226 \
  'DEPLOY_PUBKEY="ssh-ed25519 AAAA… northplane-deploy" bash -s' \
  < deploy/provision-server.sh
```

Then pin the host's SSH key for the `DEPLOY_KNOWN_HOSTS` variable:

```sh
ssh-keyscan -t ed25519,rsa 178.104.12.226
```

## 2. DNS (required for the real Let's Encrypt cert)

Create an **A record**: `doktrace.com → 178.104.12.226` (and `www` if you
want it). Until it resolves, the site is reachable at
`https://178.104.12.226` with a self-signed cert (one-time browser warning);
Caddy upgrades to the real certificate automatically once the record is live —
no redeploy needed.

## 3. GitHub configuration

Create these under **Settings → Secrets and variables → Actions**. (The
`info@myfoxit.com` admin set these automatically with `gh`; this table is the
source of truth if you ever need to recreate them.)

### Repository **Variables**  (non-secret)

| Variable | Value | Purpose |
|---|---|---|
| `DEPLOY_HOST` | `178.104.12.226` | Server address (also the bare-IP TLS endpoint). |
| `DEPLOY_USER` | `deploy` | Non-root SSH user the deploy runs as. |
| `DEPLOY_PATH` | `/opt/northplane` | Compose project directory on the server. |
| `DEPLOY_KNOWN_HOSTS` | _output of `ssh-keyscan`_ | Pins the host key (StrictHostKeyChecking=yes). |
| `DEPLOY_DOMAIN` | `doktrace.com` | Hostname Caddy gets a Let's Encrypt cert for. |
| `ACME_EMAIL` | `admin@doktrace.com` | Let's Encrypt account email. |
| `NORTHPLANE_BASE_URL` | `https://doktrace.com` | App base URL (links, ack URLs, OIDC redirects). |
| `NP_DEFAULT_ADMIN_EMAIL` | `admin@doktrace.com` | Break-glass admin login. |
| `NORTHPLANE_DEMO` | `true` | **The demo/real switch** (`true` = demo data, `false` = clean). |

### Repository **Secrets**

| Secret | Value | Purpose |
|---|---|---|
| `DEPLOY_SSH_KEY` | _private key_ | CI → server SSH key (its public half is in step 1). |
| `NP_DEFAULT_ADMIN_PASSWORD` | _strong password_ | Initial admin password (set once; change it after first login). Leave empty to use the interactive `/setup` page instead. |

> GHCR pull auth needs no extra secret: the deploy job's built-in
> `GITHUB_TOKEN` is used for the `docker login` on the server and revoked
> right after the pull.

## 4. Deploy

- **Automatic:** merge to `main`. CI runs; on success the Deploy workflow ships.
- **Manual:** Actions → **Deploy** → *Run workflow*. The `demo` dropdown lets
  you override the switch for that run (`repo-default` / `true` / `false`).

---

## Demo / real data switch

`NORTHPLANE_DEMO` controls whether the showcase environment is seeded:

- **`true`** → seeds idempotent `demo-*` hosts, services, alerts, an
  escalation chain, on-call schedule, a BPI tree with an SLA, a dashboard, a
  scheduled report and two demo users. Great for showing the product working
  end-to-end.
- **`false`** → nothing is seeded. Log in as the break-glass admin (or via
  `/setup`) and add your real hosts.

**It's safe to flip.** The two modes use **separate data directories**
(`/var/lib/northplane/demo` vs `/var/lib/northplane/real`) inside the same
volume, so switching never mixes datasets and each side keeps its own data.
As a second guard, the server **refuses to seed demo data on top of a
database that already contains real (non-demo) hosts**.

To switch to real data:

1. Set the `NORTHPLANE_DEMO` repository variable to `false` (or use the manual
   *Run workflow* dropdown).
2. Re-deploy. The server now serves the empty `…/real` dataset; create your
   admin and add hosts.

Demo objects are all labelled `demo=true`, so they're easy to find/remove if
you ever seed them into a shared dataset by mistake.

---

## Operations

```sh
# on the server, as the deploy user:
cd /opt/northplane
docker compose ps
docker compose logs -f northplane     # find the seeded admin password on first boot
docker compose logs -f caddy          # watch ACME / cert issuance

# rollback to a previous image (immutable per-commit tags):
#   edit NORTHPLANE_IMAGE in .env to ghcr.io/myfoxit/northplane:main-<oldsha>
#   then: docker compose up -d
```

**Back up** `/opt/northplane/secret.key` (decrypts secrets-at-rest) and the
`northplane-data` Docker volume.
