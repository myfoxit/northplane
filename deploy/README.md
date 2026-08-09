# Northplane — production deployment (Proxmox VM + central Caddy, CI/CD)

Continuous deployment to a NAT'd Proxmox VM. Push to `main` → CI runs → on
green, the **Deploy** workflow builds the image, pushes it to GHCR, and rolls
the Compose stack on the VM over SSH as an unprivileged `deploy` user.

```
GitHub: push main ─▶ CI (test/lint/e2e) ─▶ Deploy workflow
                                              │ build+push ghcr.io image
                                              ▼ ssh -p 2201 deploy@51.83.96.40
Proxmox host 51.83.96.40 ── DNAT :80/:443 ──▶ Caddy LXC 10.10.10.10 (TLS, all SaaS domains)
                        └── DNAT :2201    ──▶ saas1 VM 10.10.10.11:22 (this app)
Caddy LXC: doktrace.com ── reverse_proxy ──▶ 10.10.10.11:8443 (northplaned)
VM saas1:  /opt/northplane — docker compose, edge-proxied variant (no bundled caddy)
```

TLS terminates at the **central Caddy LXC** (one per Proxmox box, shared by
all SaaS VMs; domains are mapped with the `saas-domain` helper in the LXC).
The bundled-caddy compose file (`deploy/docker-compose.yml`) remains for
standalone single-box installs; production ships
`deploy/docker-compose.vm.yml` instead.

The same binary is a demo instance or a real instance depending on one
switch — see **Demo / real data switch** below.

---

## 1. One-time server provisioning

On the VM (Rocky Linux, login as `rocky`): run the idempotent provisioner as
root. It installs Docker CE, creates the `deploy` user, authorizes the CI
deploy key, and lays down the project dir + persistent `secret.key`:

```sh
# from a checkout, with the CI deploy PUBLIC key:
ssh -J root@51.83.96.40 rocky@10.10.10.11 \
  'DEPLOY_PUBKEY="ssh-ed25519 AAAA… northplane-ci-deploy" sudo -E bash -s' \
  < deploy/provision-server.sh
```

On the Proxmox host, forward a public SSH port to the VM for CI (persisted
as `post-up` rules in `/etc/network/interfaces`, next to the 80/443 rules):

```sh
iptables -t nat -A PREROUTING -i vmbr0 -p tcp --dport 2201 \
  -j DNAT --to-destination 10.10.10.11:22
```

Then pin the VM's SSH key for the `DEPLOY_KNOWN_HOSTS` variable:

```sh
ssh-keyscan -p 2201 -t ed25519 51.83.96.40
```

## 2. DNS + ingress

`doktrace.com` is a Cloudflare-proxied A record to `51.83.96.40`. In the
Caddy LXC, `saas-domain 1 doktrace.com 8443` maps the domain to this VM;
Caddy issues the certificate automatically.

## 3. GitHub configuration

Create these under **Settings → Secrets and variables → Actions**. (The
`info@myfoxit.com` admin set these automatically with `gh`; this table is the
source of truth if you ever need to recreate them.)

### Repository **Variables**  (non-secret)

| Variable | Value | Purpose |
|---|---|---|
| `DEPLOY_HOST` | `51.83.96.40` | Proxmox host address (SSH is DNAT-forwarded to the VM). |
| `DEPLOY_PORT` | `2201` | DNAT port on the host → VM `:22`. |
| `DEPLOY_USER` | `deploy` | Non-root SSH user the deploy runs as. |
| `DEPLOY_PATH` | `/opt/northplane` | Compose project directory on the VM. |
| `DEPLOY_KNOWN_HOSTS` | _output of `ssh-keyscan -p 2201`_ | Pins the VM host key (StrictHostKeyChecking=yes). |
| `DEPLOY_DOMAIN` | `doktrace.com` | Public hostname (mapped in the central Caddy LXC). |
| `NORTHPLANE_BASE_URL` | `https://doktrace.com` | App base URL (links, ack URLs, OIDC redirects). |
| `NP_DEFAULT_ADMIN_EMAIL` | `admin@doktrace.com` | Break-glass admin login. |
| `NORTHPLANE_DEMO` | `true` | **The demo/real switch** (`true` = demo data, `false` = clean). |

The rendered `.env` also sets `NORTHPLANE_ALLOW_SIGNUP=true` — the public
`/register` page (self-service viewer accounts) is part of the showcase.

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
