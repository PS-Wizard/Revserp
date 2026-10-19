#!/bin/sh
set -eu

AMD64_SHA256="9a2793b84cc7f80e2ec84f4c0f49deeee4319d0164eaa03e2524d8adb90900a1"
ARM64_SHA256="cf762b8727d034a70ce5ec65dde559661f4d34e580a53952998b142ce890804a"

ARCH="${1:-${TARGETARCH:-}}"
DEST="${2:-/usr/local/bin/revserp-gaoya}"
case "$ARCH" in
	amd64) TARGET="x86_64-unknown-linux-musl"; EXPECTED="$AMD64_SHA256" ;;
	arm64) TARGET="aarch64-unknown-linux-musl"; EXPECTED="$ARM64_SHA256" ;;
	*) echo "install-gaoya: want amd64|arm64, got '$ARCH'" >&2; exit 1 ;;
esac

PINNED=no
case "$EXPECTED" in
	*[!0-9a-f]*|"") PINNED=no ;;
	*) if [ "${#EXPECTED}" -eq 64 ]; then PINNED=yes; fi ;;
esac
[ "$PINNED" = yes ] || { echo "install-gaoya: pinned sha256 missing for $ARCH" >&2; exit 1; }

URL="https://github.com/PS-Wizard/Revserp/releases/download/gaoya-v0.1.0/revserp-gaoya-${TARGET}"
TMPD="$(mktemp -d)"
trap 'rm -rf "$TMPD"' EXIT INT TERM HUP
curl -fsSL --proto '=https' --proto-redir '=https' \
	--retry 3 --retry-delay 2 --retry-max-time 60 \
	--connect-timeout 15 --max-time 300 \
	-o "$TMPD/revserp-gaoya" "$URL"
ACTUAL="$(sha256sum "$TMPD/revserp-gaoya" | awk '{print $1}')"
[ "$ACTUAL" = "$EXPECTED" ] || { echo "install-gaoya: sha256 mismatch for $ARCH" >&2; exit 1; }
mkdir -p "$(dirname "$DEST")"
cp "$TMPD/revserp-gaoya" "$DEST"
chmod 0755 "$DEST"
