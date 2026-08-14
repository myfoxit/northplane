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
