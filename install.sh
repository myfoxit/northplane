#!/bin/sh
# Northplane installer — downloads a release build and installs the binaries.
#
#   curl -fsSL https://raw.githubusercontent.com/myfoxit/northplane/main/install.sh | sh
#
# Installs northplaned (server), np (CLI) and np-agent (host agent) to
# /usr/local/bin — via sudo when needed — or to ~/.local/bin as a fallback.
# Safe to re-run; existing binaries are replaced. Linux and macOS, amd64 and
# arm64. Downloads are verified against the release's checksums.txt.
#
# Knobs (environment variables):
#   NP_VERSION=v1.2.3      install a specific release tag (default: newest)
#   NP_INSTALL_DIR=/path   install directory (default: /usr/local/bin, else ~/.local/bin)
#   NP_BINARIES="np-agent" subset of binaries, e.g. for a monitored host
set -eu

REPO="myfoxit/northplane"
BINARIES="${NP_BINARIES:-northplaned np np-agent}"
API="https://api.github.com/repos/${REPO}/releases"

err() { printf 'install.sh: %s\n' "$1" >&2; exit 1; }
say() { printf '%s\n' "$1"; }

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
command -v tar  >/dev/null 2>&1 || err "tar is required"
if command -v sha256sum >/dev/null 2>&1; then
  sha() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  sha() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  err "sha256sum or shasum is required to verify the download"
fi

# --- resolve the release ----------------------------------------------------
tag_from() { grep -m1 '"tag_name"' | sed 's/.*"tag_name"[^"]*"\([^"]*\)".*/\1/'; }
if [ -n "${NP_VERSION:-}" ]; then
  tag="$NP_VERSION"
  case "$tag" in v*) ;; *) tag="v$tag" ;; esac
else
  # releases/latest ignores pre-releases; fall back to the newest release of
  # any kind so the installer also works while only release candidates exist.
  tag=$(curl -fsSL "${API}/latest" 2>/dev/null | tag_from || true)
  [ -n "$tag" ] || tag=$(curl -fsSL "${API}?per_page=1" 2>/dev/null | tag_from || true)
  [ -n "$tag" ] || err "could not determine the latest release of ${REPO} (network? rate limit?) — set NP_VERSION=vX.Y.Z"
fi

asset="northplane_${tag}_${os}_${arch}.tar.gz"
base="https://github.com/${REPO}/releases/download/${tag}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

say "Downloading Northplane ${tag} (${os}/${arch}) …"
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
dest="${NP_INSTALL_DIR:-/usr/local/bin}"
sudo=""
if [ ! -w "$dest" ] && [ ! -w "$(dirname "$dest")" ]; then
  # sudo prompts on /dev/tty, so it also works for `curl … | sh`
  if [ "$(id -u)" != "0" ] && command -v sudo >/dev/null 2>&1 && [ -e /dev/tty ]; then
    sudo="sudo"
  elif [ -z "${NP_INSTALL_DIR:-}" ]; then
    dest="${HOME}/.local/bin"
  else
    err "cannot write to ${dest}"
  fi
fi
$sudo mkdir -p "$dest"
for b in $BINARIES; do
  [ -f "${tmp}/${b}" ] || err "binary ${b} missing from ${asset}"
  $sudo install -m 0755 "${tmp}/${b}" "${dest}/${b}"
done

case ":$PATH:" in
  *":${dest}:"*) ;;
  *) say "note: ${dest} is not on your PATH" ;;
esac

say ""
say "Installed Northplane ${tag} to ${dest}: ${BINARIES}"
case " $BINARIES " in
  *" northplaned "*)
    cat <<EOM

Try it now (loopback, no TLS needed):
  northplaned serve                   # then open http://127.0.0.1:8443/setup

Run it as a service (Linux, systemd):
  sudo northplaned init               # config.yaml + secret key + service user + unit
  sudo systemctl enable --now northplaned

Docs: https://github.com/${REPO}#readme  ·  after install: http://127.0.0.1:8443/docs/
EOM
    ;;
  *)
    say "Configure the agent: see https://github.com/${REPO}#readme (Admin → Agents in your Northplane generates agent.yaml)"
    ;;
esac
