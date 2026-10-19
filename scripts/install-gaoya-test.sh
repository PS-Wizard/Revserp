#!/bin/sh
set -eu

SRC="$(dirname "$0")/install-gaoya.sh"
PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); echo "ok: $1"; }
bad() { FAIL=$((FAIL + 1)); echo "FAIL: $1" >&2; }

T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT INT TERM HUP
printf '#!/bin/sh\necho gaoya-fixture-ok\n' > "$T/fixture"
HASH="$(sha256sum "$T/fixture" | awk '{print $1}')"

cat > "$T/fakebin-curl" <<EOF
#!/bin/sh
out=""
prev=""
for a in "\$@"; do
	if [ "\$prev" = "-o" ]; then out="\$a"; fi
	prev="\$a"
done
[ -n "\$out" ] || exit 1
cp "$T/fixture" "\$out"
EOF
mkdir -p "$T/fakebin"
mv "$T/fakebin-curl" "$T/fakebin/curl"
chmod +x "$T/fakebin/curl"
export PATH="$T/fakebin:$PATH"

pinned_copy() {
	sed -e "s/^AMD64_SHA256=.*/AMD64_SHA256=\"$HASH\"/" \
	    -e "s/^ARM64_SHA256=.*/ARM64_SHA256=\"$HASH\"/" \
		"$SRC" > "$1"
}

pinned_copy "$T/gaoya-ok.sh"
if sh "$T/gaoya-ok.sh" amd64 "$T/a/revserp-gaoya" >/dev/null 2>&1 \
	&& [ -x "$T/a/revserp-gaoya" ] \
	&& [ "$("$T/a/revserp-gaoya")" = "gaoya-fixture-ok" ]; then
	ok "valid amd64 fixture installs and runs"
else
	bad "valid amd64 fixture installs and runs"
fi

if sh "$T/gaoya-ok.sh" arm64 "$T/b/revserp-gaoya" >/dev/null 2>&1 \
	&& [ -x "$T/b/revserp-gaoya" ] \
	&& [ "$(sha256sum "$T/b/revserp-gaoya" | awk '{print $1}')" = "$HASH" ]; then
	ok "valid arm64 fixture installs"
else
	bad "valid arm64 fixture installs"
fi

sed -e 's/^AMD64_SHA256=.*/AMD64_SHA256="0000000000000000000000000000000000000000000000000000000000000000"/' \
	"$T/gaoya-ok.sh" > "$T/gaoya-evil.sh"
if sh "$T/gaoya-evil.sh" amd64 "$T/evil/revserp-gaoya" >/dev/null 2>&1; then
	bad "bad digest fails (installer succeeded, want failure)"
elif [ ! -e "$T/evil/revserp-gaoya" ]; then
	ok "bad digest fails and installs nothing"
else
	bad "bad digest fails but left a file behind"
fi

if sh "$T/gaoya-ok.sh" riscv64 "$T/unknown/revserp-gaoya" >/dev/null 2>&1; then
	bad "unknown arch fails (installer succeeded, want failure)"
else
	ok "unknown arch fails"
fi

sed 's/^AMD64_SHA256=.*/AMD64_SHA256="not-pinned"/' "$SRC" > "$T/gaoya-unpinned.sh"
if sh "$T/gaoya-unpinned.sh" amd64 "$T/pending/revserp-gaoya" >/dev/null 2>&1; then
	bad "unpinned installer fails closed (installer succeeded, want failure)"
elif [ ! -e "$T/pending/revserp-gaoya" ]; then
	ok "unpinned installer fails closed"
else
	bad "unpinned installer left a file behind"
fi

echo "---"
echo "pass=$PASS fail=$FAIL"
[ "$FAIL" -eq 0 ]
