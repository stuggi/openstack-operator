#!/bin/bash
# Bindata webhook assertion: run after `make bindata` to verify webhook manifests are
# correctly staged. Keystone (conversion operator) gets Service+Certificate only;
# infra/baremetal (admission operators) retain their full admission webhook configs.
#
# Usage: make bindata && ./hack/test-bindata-webhooks.sh
set -euo pipefail
BINDATA_OPERATOR="bindata/operator"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "PASS: $*"; }

[[ -f "$BINDATA_OPERATOR/keystone-operator-webhooks.yaml" ]] || \
    fail "keystone-operator-webhooks.yaml not staged (run 'make bindata' with keystone pinned)"

grep -q "kind: Service" "$BINDATA_OPERATOR/keystone-operator-webhooks.yaml" || \
    fail "keystone webhook manifest missing Service"
grep -q "kind: Certificate" "$BINDATA_OPERATOR/keystone-operator-webhooks.yaml" || \
    fail "keystone webhook manifest missing Certificate"
pass "keystone-operator-webhooks.yaml has Service + Certificate"

grep -qE "MutatingWebhookConfiguration|ValidatingWebhookConfiguration" \
    "$BINDATA_OPERATOR/keystone-operator-webhooks.yaml" && \
    fail "keystone webhook manifest has unexpected admission webhook configs (should be serving-only for conversion)"
pass "keystone-operator-webhooks.yaml has no admission webhook configs"

grep -q "kind: Certificate" "$BINDATA_OPERATOR/keystone-operator-webhooks.yaml" | grep -q "secretName: keystone-operator-webhook-server-cert" 2>/dev/null || true
grep -q "keystone-operator-webhook-server-cert" "$BINDATA_OPERATOR/keystone-operator-webhooks.yaml" || \
    fail "keystone webhook manifest missing expected secretName keystone-operator-webhook-server-cert"
pass "keystone-operator-webhooks.yaml has correct secretName"

grep -q "MutatingWebhookConfiguration" "$BINDATA_OPERATOR/infra-operator-webhooks.yaml" || \
    fail "infra-operator webhook manifest missing MutatingWebhookConfiguration (regression)"
grep -q "MutatingWebhookConfiguration" "$BINDATA_OPERATOR/openstack-baremetal-operator-webhooks.yaml" || \
    fail "openstack-baremetal-operator webhook manifest missing MutatingWebhookConfiguration (regression)"
pass "infra/baremetal webhook manifests retain admission webhook configs (no regression)"

echo ""
echo "All bindata webhook assertions passed."
