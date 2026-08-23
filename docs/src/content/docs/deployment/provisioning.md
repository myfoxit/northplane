---
title: Provisioning a new host
description: What deploy/provision-server.sh does, the one-time steps that turn a fresh Rocky/RHEL VM or standalone box into a CI deploy target, and the checklist for re-creating np-02.
sidebar:
  order: 5
---

A host becomes a deploy target in three moves: run the idempotent provisioner as root, make the host reachable for the CI deploy user, and tell GitHub about it (variables, secrets, host key). Everything after that is the Deploy workflow's job — see [CI/CD](/docs/deployment/ci-cd/).

## What `provision-server.sh` does

`deploy/provision-server.sh` is a one-shot, **idempotent** root script, tested on Rocky Linux 10 (RHEL 10 family); the Rocky 9 production VM was provisioned with it through the jump host (see below). Re-running it is safe.

| Variable | Default | Meaning |
|---|---|---|
| `DEPLOY_PUBKEY` | **required** | the GitHub Actions deploy *public* key (`ssh-ed25519 AAAA… comment`); the private half becomes the `DEPLOY_SSH_KEY` / `HETZNER_SSH_KEY` secret |
| `DEPLOY_USER` | `deploy` | non-root user the pipeline logs in as |
| `DEPLOY_PATH` | `/opt/northplane` | compose project directory |
| `CONTAINER_UID` | `65532` | the distroless `nonroot` uid the app runs as — owner of `secret.key` |

Steps, in order:

1. **Docker CE + compose plugin** — if `docker` is missing: `dnf config-manager --add-repo …/docker-ce.repo`, `dnf -y install docker-ce docker-ce-cli containerd.io docker-compose-plugin`.
2. **RHEL 10 quirks** — when the legacy `xt_addrtype` module is absent and `/etc/docker/daemon.json` is empty, writes `{"firewall-backend": "nftables"}` (RHEL 10 ships no `xt_*` iptables modules, and dockerd's default backend needs them); writes `net.ipv4.ip_forward = 1` to `/etc/sysctl.d/99-docker-forward.conf` and applies it (cloud images default forwarding off, and dockerd refuses to start without it). Then `systemctl enable --now docker`.
3. **Deploy user** — `useradd --create-home --shell /bin/bash deploy` (if missing) and `usermod -aG docker deploy`. The docker group is root-equivalent; acceptable on a dedicated, single-purpose host.
4. **Authorised key** — `~deploy/.ssh` (0700), appends `DEPLOY_PUBKEY` to `authorized_keys` if not already present (0600).
5. **Project directory** — `install -d -m 750 -o deploy -g deploy /opt/northplane`.
6. **`secret.key`** — if missing or empty: `openssl rand -hex 32` (fallback `/dev/urandom` via `od`), then `chown 65532:65532`, `chmod 600`. The read-only bind mount must be readable *inside* the distroless container, hence the container uid as owner.
7. **Firewall** — if `firewalld` is active: `--add-service=http`, `--add-service=https`, reload. Otherwise prints a reminder to allow TCP 80 and 443 at the cloud provider.
8. Prints a summary and the next step: pin the host key for the `DEPLOY_KNOWN_HOSTS` variable with `ssh-keyscan`.

The script does **not** install the compose stack, render `.env`, pull the image or start anything — the first Deploy run (or you, by hand) does that. It also does not open 8443: on the NAT'd VM the port is only reachable on the private bridge, and on a standalone box Caddy publishes 80/443 instead.

```bash title="deploy/provision-server.sh"
#!/usr/bin/env bash
# Northplane server provisioning — run ONCE as root, idempotent (safe to
# re-run). Installs Docker CE, creates a non-root `deploy` user wired for the
# GitHub Actions deploy, and prepares the project directory.
#
#   ssh root@HOST 'DEPLOY_PUBKEY="ssh-ed25519 AAAA… deploy" bash -s' \
#       < deploy/provision-server.sh
#
# Tested on Rocky Linux 10 (RHEL 10 family). Variables (all optional except
# DEPLOY_PUBKEY):
#   DEPLOY_USER   (default: deploy)
#   DEPLOY_PATH   (default: /opt/northplane)
#   CONTAINER_UID (default: 65532 — the distroless nonroot uid the app runs as)
#   DEPLOY_PUBKEY (required — the GitHub Actions deploy public key)
set -euo pipefail

DEPLOY_USER="${DEPLOY_USER:-deploy}"
DEPLOY_PATH="${DEPLOY_PATH:-/opt/northplane}"
CONTAINER_UID="${CONTAINER_UID:-65532}"
: "${DEPLOY_PUBKEY:?set DEPLOY_PUBKEY to the GitHub Actions deploy public key}"

log() { printf '\n=== %s ===\n' "$1"; }

log "Docker CE + compose plugin"
if ! command -v docker >/dev/null 2>&1; then
	dnf -y install dnf-plugins-core >/dev/null
	dnf config-manager --add-repo https://download.docker.com/linux/centos/docker-ce.repo >/dev/null 2>&1 || true
	dnf -y install docker-ce docker-ce-cli containerd.io docker-compose-plugin
fi
# RHEL 10 family ships no legacy xt_* iptables kernel modules (dockerd's
# default firewall backend needs xt_addrtype) — switch to the native
# nftables backend there. dockerd also refuses to start while IPv4
# forwarding is off, and cloud images default it off.
if ! modinfo xt_addrtype >/dev/null 2>&1 && [ ! -s /etc/docker/daemon.json ]; then
	install -d -m 755 /etc/docker
	printf '{\n  "firewall-backend": "nftables"\n}\n' >/etc/docker/daemon.json
fi
printf 'net.ipv4.ip_forward = 1\n' >/etc/sysctl.d/99-docker-forward.conf
sysctl -q -p /etc/sysctl.d/99-docker-forward.conf
systemctl enable --now docker
docker --version
docker compose version

log "deploy user '$DEPLOY_USER'"
id "$DEPLOY_USER" >/dev/null 2>&1 || useradd --create-home --shell /bin/bash "$DEPLOY_USER"
# Membership in the docker group lets the deploy user manage containers
# without sudo. That group is root-equivalent — acceptable on a dedicated,
# single-purpose deploy host; do not add untrusted users to it.
usermod -aG docker "$DEPLOY_USER"

log "authorize the GitHub Actions deploy key"
install -d -m 700 -o "$DEPLOY_USER" -g "$DEPLOY_USER" "/home/$DEPLOY_USER/.ssh"
AUTH="/home/$DEPLOY_USER/.ssh/authorized_keys"
touch "$AUTH"
grep -qxF "$DEPLOY_PUBKEY" "$AUTH" || printf '%s\n' "$DEPLOY_PUBKEY" >>"$AUTH"
chmod 600 "$AUTH"
chown "$DEPLOY_USER:$DEPLOY_USER" "$AUTH"

log "project directory $DEPLOY_PATH"
install -d -m 750 -o "$DEPLOY_USER" -g "$DEPLOY_USER" "$DEPLOY_PATH"

log "secret.key (AES-256 master key — persists across deploys & mode flips)"
KEY="$DEPLOY_PATH/secret.key"
if [ ! -s "$KEY" ]; then
	{ openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'; } >"$KEY"
fi
# Owned by the container's runtime uid so the read-only bind mount is
# readable inside the distroless image; 0600 keeps it private otherwise.
chown "$CONTAINER_UID:$CONTAINER_UID" "$KEY"
chmod 600 "$KEY"

log "firewall"
if systemctl is-active --quiet firewalld; then
	firewall-cmd --permanent --add-service=http
	firewall-cmd --permanent --add-service=https
	firewall-cmd --reload
	echo "opened 80/443 in firewalld"
else
	echo "firewalld inactive — nothing to open at the OS level."
	echo "NOTE: ensure any cloud-provider external firewall allows TCP 80 + 443."
fi

cat <<EOF

=== provisioning complete ===
  deploy user : $DEPLOY_USER  (in docker group)
  project dir : $DEPLOY_PATH
  secret.key  : $KEY  (back this up — it decrypts secrets-at-rest)

Next: the GitHub Actions deploy workflow will scp the compose stack here and
run 'docker compose up -d'. Pin this host's SSH key in the DEPLOY_KNOWN_HOSTS
GitHub variable with:  ssh-keyscan -t ed25519,rsa <host>
EOF
```

## One-time steps: a NAT'd VM behind the Proxmox host

This is how `np-01` (VM101 `saas1`) was wired; repeat it for another slot. Details of the topology are on the [Proxmox VM](/docs/deployment/proxmox-vm/) page.

1. **Generate the CI key pair** on your workstation (never reuse a personal key):
   ```bash
   ssh-keygen -t ed25519 -C northplane-ci-deploy -f ./northplane-ci-deploy -N ''
   ```
2. **Run the provisioner on the VM** through the hypervisor as jump host (VM user `rocky`, sudo):
   ```bash
   ssh -J root@51.83.96.40 rocky@10.10.10.11 \
     'DEPLOY_PUBKEY="ssh-ed25519 AAAA… northplane-ci-deploy" sudo -E bash -s' \
     < deploy/provision-server.sh
   ```
3. **Forward a public SSH port to the VM** on the hypervisor and persist it as a `post-up` rule in `/etc/network/interfaces`, next to the 80/443 rules:
   ```bash
   iptables -t nat -A PREROUTING -i vmbr0 -p tcp --dport 2201 \
     -j DNAT --to-destination 10.10.10.11:22
   ```
4. **Pin the VM's host key** (this is the value of `DEPLOY_KNOWN_HOSTS`):
   ```bash
   ssh-keyscan -p 2201 -t ed25519 51.83.96.40
   # → [51.83.96.40]:2201 ssh-ed25519 AAAA…
   ```
5. **Map the domain in the Caddy LXC** and point DNS at the hypervisor:
   ```bash
   ssh root@51.83.96.40 -t 'pct enter 100'
   saas-domain 1 doktrace.com 8443        # slot 1 = 10.10.10.11
   ```
   Create the (Cloudflare-proxied or plain) A record for the domain → `51.83.96.40`. Caddy issues the Let's Encrypt certificate via HTTP-01 as soon as the name resolves.
6. **Create the GitHub configuration** (`gh variable set` / `gh secret set`, or the repository settings UI): variables `DEPLOY_HOST=51.83.96.40`, `DEPLOY_PORT=2201`, `DEPLOY_USER=deploy`, `DEPLOY_PATH=/opt/northplane`, `DEPLOY_KNOWN_HOSTS=<keyscan line>`, `NORTHPLANE_BASE_URL=https://doktrace.com`, `NORTHPLANE_DEMO=false`, `NP_DEFAULT_ADMIN_EMAIL=admin@doktrace.com`; secrets `DEPLOY_SSH_KEY` (the private key) and `NP_DEFAULT_ADMIN_PASSWORD`. The full tables are in [CI/CD → GitHub configuration](/docs/deployment/ci-cd/#github-configuration).
7. **First deploy** — **Actions → Deploy → Run workflow** (or merge to `main`). Watch the `deploy` job: "Ship compose stack" proves SSH + host key, "Pull and roll forward" proves the GHCR login, "Verify rollout" proves the app answers `ok` on `/healthz`.
8. **Verify from outside**: `curl -s https://doktrace.com/api/v1/system/info` shows the new `version`; log in as the break-glass admin and change the password.

## One-time steps: a standalone box with bundled Caddy

This is how `np-02` was built (the box no longer exists — see below). The same provisioner runs **directly as root** on the box, and the bundled-Caddy stack terminates TLS on the box itself.

1. Generate a dedicated CI key pair (as above).
2. Run the provisioner as root:
   ```bash
   ssh root@<new-ip> \
     'DEPLOY_PUBKEY="ssh-ed25519 AAAA… northplane-deploy-ci" bash -s' \
     < deploy/provision-server.sh
   ```
   On Rocky 10 the script switches dockerd to the nftables backend and enables IPv4 forwarding; on a cloud provider make sure the external firewall allows TCP 80 and 443.
3. Pin the host key: `ssh-keyscan -t ed25519 <new-ip>` → the `HETZNER_KNOWN_HOSTS` value.
4. Set `HETZNER_HOST=<new-ip>`, `HETZNER_KNOWN_HOSTS`, and the secrets `HETZNER_SSH_KEY` (private key) and `HETZNER_ADMIN_PASSWORD` (break-glass admin `root@localhost`).
5. Run the Deploy workflow. The `deploy-hetzner` job ships `deploy/docker-compose.yml` + `deploy/Caddyfile` + `.env` (`DOMAIN=localhost`, `SERVER_IP=<new-ip>`, real-data mode) and verifies `/healthz` through the Caddy container. The instance is live at `https://<new-ip>` with Caddy's internal certificate.
6. Optional domain: create an A record → the box, then change `DOMAIN=localhost` in the job's "Render .env" step (it is hard-coded in `deploy.yml`, not a variable) and re-run — Caddy obtains the Let's Encrypt certificate.

If you do this by hand instead of via CI, follow [Docker Compose → Running the stack by hand](/docs/deployment/docker-compose/#running-the-stack-by-hand).

## np-02 recreation checklist

Verified 2026-08-23: the Hetzner box `91.98.92.10` is gone — port 22 times out and the IP serves a parking page, so Hetzner has reassigned it. `deploy-hetzner` fails on every Deploy run and the `HETZNER_*` variables point at a stranger's host. To restore a second, standalone instance:

1. Order a new box (Rocky 10 or another RHEL-family image; Debian/Ubuntu would need the `dnf` lines adapted). Note its IPv4.
2. **Rotate the CI key**: generate a fresh key pair and retire the old one — the old public key is authorised on a host you no longer control. (Nothing leaked: the failed runs time out at the TCP level, and `StrictHostKeyChecking yes` would refuse an unknown host key anyway.)
3. Provision the box as root with `DEPLOY_PUBKEY=<new public key>` (steps above).
4. Update GitHub: `HETZNER_HOST=<new-ip>`, `HETZNER_KNOWN_HOSTS=<ssh-keyscan -t ed25519 <new-ip>>`, `HETZNER_SSH_KEY=<new private key>`, a fresh `HETZNER_ADMIN_PASSWORD`.
5. Run **Deploy** manually and confirm `deploy-hetzner` goes green; open `https://<new-ip>` (internal certificate) and log in as `root@localhost`, then change the password.
6. Decide on a domain: either keep the bare-IP endpoint or set `DOMAIN` in `deploy.yml` once DNS points at the box.
7. Document the new box on [Environments](/docs/deployment/environments/).

Alternative: if a second instance is not wanted, delete the `deploy-hetzner` job from `deploy.yml` and remove the four `HETZNER_*` variables/secrets — Deploy runs turn green again and nothing else changes for `np-01`.

## Verifying a provisioned host

```bash
# as root on the host
docker --version && docker compose version
id deploy                                  # … groups=…,docker
ls -la /opt/northplane                     # drwxr-x--- deploy deploy; secret.key -rw------- 65532 65532
wc -c /opt/northplane/secret.key           # 65 (64 hex chars + newline)
sysctl net.ipv4.ip_forward                 # = 1
cat /etc/docker/daemon.json                # {"firewall-backend": "nftables"} on RHEL 10 only
firewall-cmd --list-services 2>/dev/null   # http https (when firewalld is active)

# from your workstation, as the CI would
ssh -i ./northplane-ci-deploy -p 2201 deploy@51.83.96.40 'docker ps'   # NAT'd VM
ssh -i ./northplane-ci-deploy deploy@<new-ip> 'docker ps'              # standalone box
```

Back up `/opt/northplane/secret.key` as soon as it exists — it is the only copy of the key that decrypts stored secrets (channel credentials, AI keys, MQTT passwords). See [Secrets](/docs/administration/secrets/) and the [operations runbook](/docs/deployment/operations/).
