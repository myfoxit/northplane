#!/bin/bash
# Test for harden-access.sh. Runs INSIDE a throwaway container against a real
# OpenSSH install — never on a host you care about, it rewrites sshd config.
#
#   docker run --rm -v "$PWD/deploy:/work:ro" debian:trixie bash /work/harden-access.test.sh
#
# debian:trixie on purpose: same OpenSSH build as the Proxmox 9 host.
# Proves: key login works, password login is refused, and a broken drop-in in
# the main config cannot take the rescue daemon down with it.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

apt-get update -qq >/dev/null
apt-get install -y -qq openssh-server openssh-client iproute2 procps >/dev/null
ssh-keygen -A >/dev/null

# a key to log in with
ssh-keygen -q -t ed25519 -N '' -f /root/test_key
install -d -m 700 /root/.ssh
cp /root/test_key.pub /root/.ssh/authorized_keys
chmod 600 /root/.ssh/authorized_keys
mkdir -p /run/sshd

# stub systemd bits the container does not have
cat >/usr/local/sbin/systemctl <<'EOF'
#!/bin/sh
echo "[stub systemctl] $*" >>/tmp/systemctl.log
exit 0
EOF
cat >/usr/local/sbin/systemd-run <<'EOF'
#!/bin/sh
echo "[stub systemd-run] $*" >>/tmp/systemctl.log
exit 0
EOF
chmod +x /usr/local/sbin/systemctl /usr/local/sbin/systemd-run

echo "##### running harden-access.sh #####"
bash /work/harden-access.sh

echo "##### assertions #####"
fail=0
# capture first: `sshd -T | grep -q` dies of SIGPIPE under pipefail
EFF=$(sshd -T)
assert_cfg() {
	if printf '%s\n' "$EFF" | grep -qiE "^$1$"; then echo "  ok   $1"; else echo "  FAIL $1"; fail=1; fi
}
assert_cfg "passwordauthentication no"
assert_cfg "permitrootlogin (prohibit-password|without-password)"
assert_cfg "pubkeyauthentication yes"
assert_cfg "maxauthtries 6"
echo "  PerSourcePenalties in effective config: $(sshd -T | grep -i persourcepenalties || echo '(unsupported)')"

# main sshd (port 22) with the drop-in applied
/usr/sbin/sshd -D -e >/tmp/sshd-main.log 2>&1 &
# rescue sshd (port 2222) from its standalone config
/usr/sbin/sshd -D -e -f /etc/ssh/sshd_config.rescue >/tmp/sshd-rescue.log 2>&1 &
sleep 2
ss -tln | grep -E ':(22|2222)\b' | sed 's/^/  listening: /'

SSHOPT="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5"
for port in 22 2222; do
	if ssh $SSHOPT -i /root/test_key -p $port root@127.0.0.1 'echo LOGIN_OK' 2>/dev/null | grep -q LOGIN_OK; then
		echo "  ok   key login on :$port"
	else
		echo "  FAIL key login on :$port"; fail=1
	fi
done

# password auth must be refused outright on both doors
for port in 22 2222; do
	out=$(ssh $SSHOPT -o PreferredAuthentications=password -o PubkeyAuthentication=no \
		-p $port root@127.0.0.1 'echo SHOULD_NOT_HAPPEN' 2>&1 || true)
	if echo "$out" | grep -q "Permission denied (publickey)"; then
		echo "  ok   password login refused on :$port"
	else
		echo "  FAIL password login not cleanly refused on :$port -> $out"; fail=1
	fi
done

# a broken drop-in must NOT be able to take out the rescue door
echo "GARBAGE_DIRECTIVE yes" >/etc/ssh/sshd_config.d/99-broken.conf
if sshd -t 2>/dev/null; then echo "  FAIL main config should now be invalid"; fail=1; else echo "  ok   main config now rejected by sshd -t"; fi
if sshd -t -f /etc/ssh/sshd_config.rescue; then echo "  ok   rescue config still valid"; else echo "  FAIL rescue config broke too"; fail=1; fi
rm -f /etc/ssh/sshd_config.d/99-broken.conf

# re-run must be a no-op (idempotence)
echo "##### re-run (idempotence) #####"
bash /work/harden-access.sh >/tmp/rerun.log 2>&1 && echo "  ok   second run succeeded" || { echo "  FAIL second run"; fail=1; }

echo "##### result #####"
[ "$fail" -eq 0 ] && echo "ALL CHECKS PASSED" || echo "SOME CHECKS FAILED"
exit "$fail"
