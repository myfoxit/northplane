#!/usr/bin/env bash
# Northplane host access hardening — run as root on the Proxmox host, idempotent
# (safe to re-run).
#
#   ssh root@HOST 'bash -s' < deploy/harden-access.sh
#
# Design rule: security comes from IDENTITY (keys), never from the source IP.
# Nothing here pins an admin address, so a changing home/mobile IP can never
# lock you out. See "Anti-lockout" in deploy/README.md for the recovery paths.
#
# What it does:
#   1. refuses to run unless root already has an authorized SSH key
#   2. starts a second, independent sshd on a rescue port (own config file, so a
#      botched main config or a brute-force penalty cannot take away both)
#   3. hardens the main sshd: key-only, no passwords, no root password login
#   4. validates every config with `sshd -t` BEFORE reloading; reloads (never
#      restarts) so the session you are typing in survives a mistake
#
# Options:
#   --rescue-port N   port for the rescue sshd (default 2222)
#   --firewall        opt-in: enable the Proxmox firewall with PORT-based rules
#                     (never source-IP based) plus a 10-minute auto-rollback
#   --confirm         cancel that auto-rollback once you verified you are still in
#   --no-rescue       skip the rescue sshd (not recommended)
set -euo pipefail

RESCUE_PORT="${RESCUE_PORT:-2222}"
WANT_FIREWALL=0
WANT_RESCUE=1
DO_CONFIRM=0
ROLLBACK_UNIT="np-access-rollback"

while [ $# -gt 0 ]; do
	case "$1" in
	--rescue-port) RESCUE_PORT="$2"; shift 2 ;;
	--firewall) WANT_FIREWALL=1; shift ;;
	--confirm) DO_CONFIRM=1; shift ;;
	--no-rescue) WANT_RESCUE=0; shift ;;
	*) echo "unknown option: $1" >&2; exit 64 ;;
	esac
done

log() { printf '\n=== %s ===\n' "$1"; }
warn() { printf '  !! %s\n' "$1"; }

[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }

if [ "$DO_CONFIRM" -eq 1 ]; then
	systemctl stop "$ROLLBACK_UNIT.timer" 2>/dev/null || true
	systemctl reset-failed "$ROLLBACK_UNIT.timer" 2>/dev/null || true
	echo "auto-rollback cancelled — the firewall config stays as applied."
	exit 0
fi

# --- 1. preflight: never disable passwords before a key works -----------------
log "preflight"
AUTH=/root/.ssh/authorized_keys
KEYS=0
[ -s "$AUTH" ] && KEYS=$(grep -cvE '^\s*(#|$)' "$AUTH" || true)
if [ "$KEYS" -eq 0 ]; then
	cat >&2 <<-EOF
	ABORT: /root/.ssh/authorized_keys holds no key.

	Disabling password auth now would lock you out. From your workstation:
	    ssh-copy-id -i ~/.ssh/id_ed25519.pub root@<host>
	then re-run this script.
	EOF
	exit 1
fi
echo "authorized keys for root:"
ssh-keygen -lf "$AUTH" | sed 's/^/  /'
if [ "$KEYS" -lt 2 ]; then
	warn "only one key authorized. Add a second RECOVERY key kept offline"
	warn "(password manager / USB), so a lost laptop is not a lost server."
fi

SSHD_BIN=$(command -v sshd || echo /usr/sbin/sshd)
"$SSHD_BIN" -V 2>&1 | head -1 | sed 's/^/  sshd: /' || true

# Probe option support so this script also runs on older OpenSSH.
supports() { "$SSHD_BIN" -T -o "$1=$2" -f /dev/null >/dev/null 2>&1; }

# --- 2. rescue sshd: a second door with its own hinges ------------------------
if [ "$WANT_RESCUE" -eq 1 ]; then
	log "rescue sshd on port $RESCUE_PORT"
	SFTP=/usr/lib/openssh/sftp-server
	[ -x "$SFTP" ] || SFTP=/usr/libexec/openssh/sftp-server
	cat >/etc/ssh/sshd_config.rescue <<-EOF
	# Northplane rescue sshd — deliberately standalone: it does NOT include
	# /etc/ssh/sshd_config.d/, so a broken drop-in cannot disable this door too.
	# Same keys, same rules, different daemon and different port.
	Port $RESCUE_PORT
	PermitRootLogin prohibit-password
	PubkeyAuthentication yes
	PasswordAuthentication no
	KbdInteractiveAuthentication no
	AuthorizedKeysFile .ssh/authorized_keys
	UsePAM yes
	X11Forwarding no
	LoginGraceTime 30
	# Generous on purpose: ssh offers every key in your agent one by one, and a
	# tight limit turns "many keys loaded" into a failed login.
	MaxAuthTries 6
	LogLevel VERBOSE
	PidFile /run/sshd-rescue.pid
	Subsystem sftp $SFTP
	EOF
	"$SSHD_BIN" -t -f /etc/ssh/sshd_config.rescue
	cat >/etc/systemd/system/sshd-rescue.service <<-EOF
	[Unit]
	Description=Rescue SSH daemon (independent config, port $RESCUE_PORT)
	Documentation=file:/etc/ssh/sshd_config.rescue
	After=network.target

	[Service]
	Type=exec
	ExecStartPre=$SSHD_BIN -t -f /etc/ssh/sshd_config.rescue
	ExecStart=$SSHD_BIN -D -f /etc/ssh/sshd_config.rescue
	ExecReload=/bin/kill -HUP \$MAINPID
	KillMode=process
	Restart=on-failure
	RestartSec=10s

	[Install]
	WantedBy=multi-user.target
	EOF
	systemctl daemon-reload
	systemctl enable --now sshd-rescue.service
	systemctl is-active --quiet sshd-rescue.service ||
		{ echo "rescue sshd failed to start — aborting before touching the main sshd" >&2; exit 1; }
	echo "  rescue sshd up on :$RESCUE_PORT"
fi

# --- 3. harden the main sshd --------------------------------------------------
log "main sshd hardening"
DROPIN=/etc/ssh/sshd_config.d/10-northplane-hardening.conf
install -d -m 755 /etc/ssh/sshd_config.d
{
	cat <<-'EOF'
	# Managed by deploy/harden-access.sh — edit there, not here.
	#
	# DELIBERATELY ABSENT: any `AllowUsers user@1.2.3.4`, `ListenAddress` limit
	# or `Match Address` block. Pinning the admin's source IP is what locked us
	# out last time; a key you hold is just as strong and travels with you.
	PubkeyAuthentication yes
	PermitRootLogin prohibit-password
	PasswordAuthentication no
	KbdInteractiveAuthentication no
	PermitEmptyPasswords no
	AuthenticationMethods publickey
	X11Forwarding no
	PrintMotd no
	LoginGraceTime 30
	# See the rescue config: high enough that a loaded agent cannot fail itself out.
	MaxAuthTries 6
	ClientAliveInterval 300
	ClientAliveCountMax 2
	LogLevel VERBOSE
	EOF
	# OpenSSH >= 9.8 throttles misbehaving sources itself, which replaces
	# fail2ban here. Penalties are per-daemon, so the rescue port stays open
	# even if this one is busy penalising a noisy scanner.
	if supports PerSourcePenalties yes; then
		echo "PerSourcePenalties yes"
	else
		warn "sshd too old for PerSourcePenalties — consider fail2ban" >&2
	fi
} >"$DROPIN"
chmod 644 "$DROPIN"

# Debian ships an `Include /etc/ssh/sshd_config.d/*.conf` at the very top of
# sshd_config and sshd takes the FIRST value it sees for each keyword, so the
# 10- prefix makes this file win. Verify rather than trust.
"$SSHD_BIN" -t
EFFECTIVE=$("$SSHD_BIN" -T)
check() {
	local want="$1"
	printf '%s\n' "$EFFECTIVE" | grep -qiE "^$want$" ||
		{ echo "SANITY CHECK FAILED: expected '$want' in effective config" >&2; exit 1; }
}
check "passwordauthentication no"
# sshd -T renders prohibit-password under its historical name.
check "permitrootlogin (prohibit-password|without-password)"
check "pubkeyauthentication yes"
check "authenticationmethods publickey"

SSH_UNIT=ssh.service
systemctl list-unit-files sshd.service >/dev/null 2>&1 &&
	systemctl cat sshd.service >/dev/null 2>&1 && SSH_UNIT=sshd.service
# reload, not restart: your current session stays alive even if something is off.
systemctl reload "$SSH_UNIT" || systemctl restart "$SSH_UNIT"
echo "  main sshd reloaded ($SSH_UNIT) — key-only from now on"

# --- 4. optional: port-based firewall with a dead man's switch ----------------
if [ "$WANT_FIREWALL" -eq 1 ]; then
	log "Proxmox firewall (port-based, 10-minute auto-rollback)"
	if [ ! -d /etc/pve/firewall ]; then
		warn "/etc/pve/firewall missing — not a Proxmox node? skipping firewall."
	else
		[ -f /etc/pve/firewall/cluster.fw ] &&
			cp -a /etc/pve/firewall/cluster.fw "/etc/pve/firewall/cluster.fw.bak-$(date +%Y%m%d%H%M%S)"
		cat >/etc/pve/firewall/cluster.fw <<-EOF
		[OPTIONS]
		enable: 1

		[RULES]
		# Ports, never source IPs — an admin on a new IP must still get in.
		IN ACCEPT -p tcp -dport 22 -log nolog     # ssh
		IN ACCEPT -p tcp -dport $RESCUE_PORT -log nolog   # rescue ssh
		IN ACCEPT -p tcp -dport 8006 -log nolog   # proxmox web ui (protect with 2FA)
		IN ACCEPT -p tcp -dport 3128 -log nolog   # spice proxy (console in the ui)
		IN ACCEPT -p tcp -dport 5900:5999 -log nolog # novnc console
		IN ACCEPT -p tcp -dport 80 -log nolog
		IN ACCEPT -p tcp -dport 443 -log nolog
		EOF
		systemd-run --unit="$ROLLBACK_UNIT" --on-active=10min \
			/bin/sh -c "sed -i 's/^enable: 1/enable: 0/' /etc/pve/firewall/cluster.fw; pve-firewall compile >/dev/null 2>&1; pve-firewall restart" >/dev/null
		pve-firewall restart
		cat <<-EOF

		  Firewall is ON and will DISABLE ITSELF in 10 minutes.
		  Open a SECOND terminal now and confirm you can still reach the box:
		      ssh root@<host>            # port 22
		      ssh -p $RESCUE_PORT root@<host>   # rescue port
		  Then keep it:
		      bash harden-access.sh --confirm
		  Do nothing and it rolls back on its own.
		EOF
	fi
fi

# --- 5. report ----------------------------------------------------------------
log "listening"
ss -tlnp 2>/dev/null | grep -E ':(22|'"$RESCUE_PORT"'|8006|3128)\b' | sed 's/^/  /' || true

cat <<EOF

=== access hardening complete ===
  main ssh    : port 22, publickey only, root allowed by key
  rescue ssh  : port $RESCUE_PORT, separate daemon + separate config file
  source IPs  : NOT restricted anywhere — a new IP always gets a login prompt

If you are ever locked out, in this order:
  1. rescue port          ssh -p $RESCUE_PORT root@<host>
  2. Proxmox web UI       https://<host>:8006  → node → Shell
  3. OVH KVM/IPMI console (works even with sshd down)
  4. OVH rescue mode      boot the rescue system, mount the disk, fix the config

Still to do by hand (cannot be scripted safely):
  * Proxmox 2FA — ENROLL FIRST, then require it, or you lock yourself out of the UI:
      UI → Datacenter → Permissions → Two Factor → Add → TOTP  (scan, verify)
      only once that works:  pveum realm modify pam --tfa type=oath
  * put a second, offline RECOVERY key in /root/.ssh/authorized_keys
EOF
