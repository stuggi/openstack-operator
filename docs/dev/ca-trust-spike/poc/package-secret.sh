#!/usr/bin/env bash
# Host-side driver for the OSPRH-36600 PoC.
#   1. builds the builder image (Containerfile)
#   2. runs it -> update-ca-trust regenerates all formats
#   3. collects the single-file bundles it can ship
#   4. renders a Kubernetes Secret and checks it against the 1 MiB object limit
#
# This demonstrates the ticket's proposed procedure end-to-end and answers the
# central feasibility question: "does the output fit in a Secret?"
set -euo pipefail
cd "$(dirname "$0")"

IMG=ca-trust-spike:poc
OUT=./out
SECRET=combined-ca-trust.secret.yaml
K8S_LIMIT=$((1024 * 1024)) # 1 MiB hard object limit

echo "== build =="
podman build -q -t "$IMG" -f Containerfile . >/dev/null
echo "built $IMG"

echo "== run update-ca-trust in builder, export bundles =="
rm -rf "$OUT"; mkdir -p "$OUT"
podman run --rm -v "$PWD/$OUT:/out:z" "$IMG" >/dev/null
ls -l "$OUT"

echo "== render Secret =="
{
  echo "apiVersion: v1"
  echo "kind: Secret"
  echo "metadata:"
  echo "  name: combined-ca-trust"
  echo "  labels:"
  echo "    combined-ca-bundle: \"\""
  echo "type: Opaque"
  echo "data:"
  # binary-safe: base64 each file onto one line
  printf '  tls-ca-bundle.pem: %s\n' "$(base64 -w0 "$OUT/tls-ca-bundle.pem")"
  printf '  cacerts: %s\n'          "$(base64 -w0 "$OUT/cacerts")"
  printf '  cacerts.bin: %s\n'      "$(base64 -w0 "$OUT/cacerts.bin")"
} > "$SECRET"

RAW=$(cat "$OUT/tls-ca-bundle.pem" "$OUT/cacerts" "$OUT/cacerts.bin" | wc -c)
YAML=$(wc -c < "$SECRET")
echo
echo "raw bundle bytes (pem+java+edk2) : $RAW"
echo "rendered Secret YAML bytes       : $YAML"
echo "k8s object limit                 : $K8S_LIMIT (1 MiB)"
if [ "$YAML" -lt "$K8S_LIMIT" ]; then
  echo "RESULT: fits (headroom $(( (K8S_LIMIT - YAML) / 1024 )) KiB)"
else
  echo "RESULT: EXCEEDS limit by $(( (YAML - K8S_LIMIT) / 1024 )) KiB"
fi
echo
echo "wrote $SECRET"
