#!/usr/bin/env bash
# Runs INSIDE the PoC builder container. Executes the ticket's proposed
# procedure and reports empirical facts the spike needs:
#   - which extracted formats update-ca-trust produces
#   - their sizes (Secret 1 MiB budget)
#   - whether the custom anchor lands in EACH format (pem + java cacerts)
#   - the directory-hash symlink problem
#   - that the blocklist actually removes a trusted CA
# NB: no `pipefail` - we deliberately use `| head` which closes pipes early (SIGPIPE).
set -eu

EXTRACTED=/etc/pki/ca-trust/extracted
CERTS_D=/etc/pki/tls/certs
CUSTOM_CN="RHOSO Internal Root CA (cert-manager)"
line() { printf '\n=== %s ===\n' "$1"; }

line "ca-certificates version in this image"
rpm -q ca-certificates openssl || true

line "STEP 1: custom CAs staged in /etc/pki/ca-trust/source/anchors"
ls -1 /etc/pki/ca-trust/source/anchors/

line "STEP 2: run update-ca-trust (regenerates ALL formats)"
update-ca-trust
echo "exit=$?"

line "STEP 3: generated files, sizes and types"
# Show the whole extracted tree + the legacy certs dir, with sizes and file(1) type.
find "$EXTRACTED" -maxdepth 2 -type f -o -type l | sort | while read -r f; do
  if [ -L "$f" ]; then
    printf '%-70s SYMLINK -> %s\n' "$f" "$(readlink "$f")"
  else
    printf '%-70s %8s bytes  %s\n' "$f" "$(stat -c%s "$f")" "$(file -b "$f")"
  fi
done
echo "--- directory-hash dir (first 6 entries) ---"
ls -l "$EXTRACTED/pem/directory-hash" | head -8
echo "--- total extracted size (all formats) ---"
du -sh "$EXTRACTED"
echo "--- size of just the single-file bundles we'd put in a Secret ---"
du -ch "$EXTRACTED/pem/tls-ca-bundle.pem" \
       "$EXTRACTED/java/cacerts" \
       "$EXTRACTED/edk2/cacerts.bin" 2>/dev/null | tail -1

line "STEP 4a: is the custom anchor in the PEM bundle?"
if grep -q "$CUSTOM_CN" "$EXTRACTED/pem/tls-ca-bundle.pem"; then
  echo "YES - '$CUSTOM_CN' present in tls-ca-bundle.pem"
else
  echo "NO"
fi

line "STEP 4b: is the custom anchor in the JAVA cacerts keystore?"
# keytool default password for cacerts is 'changeit'
if keytool -list -keystore "$EXTRACTED/java/cacerts" -storepass changeit 2>/dev/null \
     | grep -qi "cert-manager"; then
  echo "YES - custom CA present in java/cacerts:"
  keytool -list -keystore "$EXTRACTED/java/cacerts" -storepass changeit 2>/dev/null \
    | grep -i "cert-manager" || true
else
  echo "NO - custom CA NOT found in java/cacerts"
fi
echo "total certs in java/cacerts: $(keytool -list -keystore "$EXTRACTED/java/cacerts" -storepass changeit 2>/dev/null | grep -c 'trustedCertEntry' || echo '?')"

line "STEP 5: BLOCKLIST demo - distrust an already-trusted public CA"
# Pick a real trusted root currently in the bundle, blocklist it, re-run.
TARGET=$(awk '/^# / && !/cert-manager/ {print; exit}' "$EXTRACTED/pem/tls-ca-bundle.pem" | sed 's/^# //')
echo "before: '$TARGET' present -> $(grep -cF "# $TARGET" "$EXTRACTED/pem/tls-ca-bundle.pem")"
# Extract that CA's PEM into the blocklist source and re-run.
trust dump --filter "pkcs11:object=$(printf '%s' "$TARGET" | sed 's/ /%20/g')" 2>/dev/null | \
  awk '/BEGIN CERT/{p=1} p; /END CERT/{p=0}' > /etc/pki/ca-trust/source/blocklist/distrust.crt || true
if [ ! -s /etc/pki/ca-trust/source/blocklist/distrust.crt ]; then
  # Fallback: split the bundle and grab the block under that comment.
  awk -v t="# $TARGET" '$0==t{f=1} f&&/BEGIN CERT/{p=1} p; f&&/END CERT/{print;exit}' \
    "$EXTRACTED/pem/tls-ca-bundle.pem" > /etc/pki/ca-trust/source/blocklist/distrust.crt
fi
echo "blocklist file bytes: $(stat -c%s /etc/pki/ca-trust/source/blocklist/distrust.crt 2>/dev/null || echo 0)"
update-ca-trust
echo "after : '$TARGET' present -> $(grep -cF "# $TARGET" "$EXTRACTED/pem/tls-ca-bundle.pem")"

line "STEP 6: export single-file bundles for Secret packaging (if /out mounted)"
if [ -d /out ]; then
  cp "$EXTRACTED/pem/tls-ca-bundle.pem"   /out/tls-ca-bundle.pem
  cp "$EXTRACTED/java/cacerts"            /out/cacerts
  cp "$EXTRACTED/edk2/cacerts.bin"        /out/cacerts.bin
  echo "exported to /out: $(ls -1 /out | tr '\n' ' ')"
else
  echo "(no /out mount; skipping export)"
fi

line "DONE"
