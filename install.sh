#!/bin/sh
# Northplane installer — downloads the latest release binaries.
#
#   curl -fsSL https://raw.githubusercontent.com/northplane/northplane/main/install.sh | sh
#
# Installs northplaned, np, and np-agent to /usr/local/bin (with sudo when
# needed) or ~/.local/bin as a fallback. Safe to re-run; existing binaries
# are replaced. Linux and macOS, amd64 and arm64.
set -eu

REPO="northplane/northplane"
BINARIES="northplaned np np-agent"
API="https://api.github.com/repos/${REPO}/releases/latest"

err() { printf 'install.sh: %s\n' "$1" >&2; exit 1; }

# --- platform ---------------------------------------------------------------
case "$(uname -s)" in
  Linux)  os=linux ;;
  Darwin) os=darwin ;;
  *) err "unsupported OS '$(uname -s)' (supported: Linux, macOS) — see https://github.com/${REPO} for building from source" ;;
esac
case "$(uname -m)" in
  x86_64|amd64)  arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) err "unsupported architecture '$(uname -m)' (supported: amd64, arm64)" ;;
esac

command -v curl >/dev/null 2>&1 || err "curl is required"
if command -v sha256sum >/dev/null 2>&1; then
  sha() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  sha() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  err "sha256sum or shasum is required to verify the download"
fi

# --- resolve latest release -------------------------------------------------
tag=$(curl -fsSL "$API" | grep -m1 '"tag_name"' | sed 's/.*"tag_name"[^"]*"\([^"]*\)".*/\1/') ||
  err "could not query the latest release from ${API}"
[ -n "$tag" ] || err "no release found for ${REPO} — has a version been published yet?"

asset="northplane_${tag}_${os}_${arch}.tar.gz"
base="https://github.com/${REPO}/releases/download/${tag}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

printf 'Downloading Northplane %s (%s/%s) ...\n' "$tag" "$os" "$arch"
curl -fsSL -o "${tmp}/${asset}" "${base}/${asset}" ||
  err "download failed: ${base}/${asset}"
curl -fsSL -o "${tmp}/checksums.txt" "${base}/checksums.txt" ||
  err "download failed: ${base}/checksums.txt"

# --- verify -----------------------------------------------------------------
want=$(grep " ${asset}\$" "${tmp}/checksums.txt" | awk '{print $1}')
[ -n "$want" ] || err "${asset} not found in checksums.txt"
got=$(sha "${tmp}/${asset}")
[ "$want" = "$got" ] || err "checksum mismatch for ${asset}: expected ${want}, got ${got}"

tar -xzf "${tmp}/${asset}" -C "$tmp"

# --- install ----------------------------------------------------------------
dest=/usr/local/bin
sudo=""
if [ ! -w "$dest" ]; then
  if command -v sudo >/dev/null 2>&1 && [ -t 0 ]; then
    sudo="sudo"
  else
    dest="${HOME}/.local/bin"
    mkdir -p "$dest"
  fi
fi
for b in $BINARIES; do
  [ -f "${tmp}/${b}" ] || err "binary ${b} missing from ${asset}"
  $sudo install -m 0755 "${tmp}/${b}" "${dest}/${b}"
done

case ":$PATH:" in
  *":${dest}:"*) ;;
  *) printf 'note: %s is not on your PATH\n' "$dest" ;;
esac

cat <<EOF

Installed to ${dest}: ${BINARIES}

Get started:
  northplaned serve          # then open the /setup URL printed in the log
                             # and create your admin account in the browser

Production (Linux, systemd):
  sudo northplaned init      # writes config.yaml, secret.key and a systemd unit

Docs: https://github.com/${REPO}
EOF
