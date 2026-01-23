# Building an OpenShift Catalog with Additional Versions for Upgrade Testing

This guide explains how to create a custom OLM catalog with additional operator versions to test upgrade paths using manual InstallPlan approval.

## Prerequisites

- `podman` installed
- `opm` (Operator Package Manager) installed
- Access to a container registry (e.g., quay.io)
- An existing catalog image to extend

## Step 1: Extract the Existing Catalog

Pull and extract the original catalog:

```bash
# Pull the original catalog
podman pull registry-proxy.engineering.redhat.com/rh-osbs/iib:<iib-number>

# Create container and extract configs
podman create --name temp-catalog registry-proxy.engineering.redhat.com/rh-osbs/iib:<iib-number>
podman cp temp-catalog:/configs ./openstack-catalog
podman rm temp-catalog

# See existing versions
cat openstack-catalog/openstack-operator/catalog.json | \
  jq 'select(.schema == "olm.bundle") | .name'
```

## Step 2: Build and Push Your Test Bundle

From the openstack-operator directory:

```bash
VERSION=1.0.19-test make bundle bundle-build bundle-push \
  BUNDLE_IMG=quay.io/<your-user>/openstack-operator-bundle:v1.0.19-test
```

## Step 3: Add the New Bundle to the Catalog

Render and append your bundle to the catalog:

```bash
opm render quay.io/<your-user>/openstack-operator-bundle:v1.0.19-test --output=json \
  >> openstack-catalog/openstack-operator/catalog.json
```

## Step 4: Update the Channel with skipRange

Add your version to the channel with both `replaces` (for chain integrity) and `skipRange` (to allow skipping intermediate versions):

```bash
# Get the latest existing version
LATEST=$(cat openstack-catalog/openstack-operator/catalog.json | \
  jq -r 'select(.schema == "olm.channel") | .entries[-1].name')
echo "Latest version: $LATEST"

# Add the new entry with replaces + skipRange
cat openstack-catalog/openstack-operator/catalog.json | jq -c '
  if .schema == "olm.channel" then
    .entries += [{
      "name": "openstack-operator.v1.0.19-test",
      "replaces": "openstack-operator.v1.0.18",
      "skipRange": ">=1.0.0 <1.0.19"
    }]
  else
    .
  end
' > /tmp/catalog.json.new

mv /tmp/catalog.json.new openstack-catalog/openstack-operator/catalog.json

# Verify
grep "1.0.19-test" openstack-catalog/openstack-operator/catalog.json
```

### Channel Entry Fields

| Field | Purpose |
|-------|---------|
| `name` | The bundle name (must match the rendered bundle) |
| `replaces` | The previous version in the upgrade chain (required for valid channel) |
| `skipRange` | Semver range allowing direct upgrades from any matching version |
| `skips` | List of specific versions to skip (alternative to skipRange) |

## Step 5: Validate, Build, and Push the Catalog

```bash
# Validate the catalog
opm validate openstack-catalog

# Generate Dockerfile (only needed once)
opm generate dockerfile openstack-catalog

# Build the catalog image
podman build -t quay.io/<your-user>/openstack-operator-index:1.0.19-test \
  -f openstack-catalog.Dockerfile .

# Push to registry
podman push quay.io/<your-user>/openstack-operator-index:1.0.19-test
```

## Step 6: Deploy CatalogSource to OpenShift

```bash
cat <<EOF | oc apply -f -
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: openstack-operator-index
  namespace: openstack-operators
spec:
  displayName: OpenStack Test Catalog
  image: quay.io/<your-user>/openstack-operator-index:1.0.19-test
  publisher: Testing Team
  sourceType: grpc
EOF
```

## Step 7: Create/Update Subscription with Manual Approval

```bash
cat <<EOF | oc apply -f -
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: openstack
  namespace: openstack-operators
spec:
  channel: stable-v1.0
  name: openstack-operator
  source: openstack-operator-index
  sourceNamespace: openstack-operators
  installPlanApproval: Manual
  startingCSV: openstack-operator.v1.0.7
EOF
```

### Subscription Fields

| Field | Purpose |
|-------|---------|
| `channel` | The channel to subscribe to |
| `source` | Name of the CatalogSource |
| `sourceNamespace` | Namespace where CatalogSource is deployed |
| `installPlanApproval` | `Manual` requires approval, `Automatic` auto-approves |
| `startingCSV` | Install a specific version (useful for testing upgrades from older versions) |

## Step 8: Test the Upgrade

```bash
# Check for pending InstallPlan
oc get installplan -n openstack-operators

# Approve upgrade to new version
oc patch installplan <installplan-name> -n openstack-operators \
  --type merge -p '{"spec":{"approved":true}}'

# Watch the upgrade progress
oc get csv -n openstack-operators -w
```

## Refreshing After Catalog Updates

If you rebuild and push an updated catalog image:

```bash
# Push updated image (same tag)
podman push quay.io/<your-user>/openstack-operator-index:1.0.19-test

# Force catalog pod to re-pull
oc delete pod -n openstack-operators -l olm.catalogSource=openstack-operator-index

# Delete old InstallPlan if stuck
oc delete installplan <old-installplan> -n openstack-operators

# Force subscription resync
oc patch subscription openstack -n openstack-operators --type merge \
  -p '{"metadata":{"annotations":{"force-resync":"'$(date +%s)'"}}}'

# Check for new InstallPlan
oc get installplan -n openstack-operators
```

## Troubleshooting

### Check catalog contents in cluster

```bash
# Verify bundle exists in running catalog
oc exec -n openstack-operators -l olm.catalogSource=openstack-operator-index -- \
  cat /configs/openstack-operator/catalog.json | jq 'select(.schema == "olm.bundle") | .name'

# Check channel entries
oc exec -n openstack-operators -l olm.catalogSource=openstack-operator-index -- \
  cat /configs/openstack-operator/catalog.json | jq 'select(.schema == "olm.channel")'
```

### Check catalog and OLM logs

```bash
# Catalog pod logs
oc logs -n openstack-operators -l olm.catalogSource=openstack-operator-index

# OLM operator logs
oc logs -n openshift-operator-lifecycle-manager deploy/olm-operator --tail=50

# Catalog operator logs
oc logs -n openshift-operator-lifecycle-manager deploy/catalog-operator --tail=50
```

### Common validation errors

**"multiple channel heads found"**: Your channel has versions without a proper `replaces` chain. Every version except the first must have `replaces` pointing to another version in the channel.

**"stranded bundles"**: A bundle exists but isn't referenced in any channel entry.

## Example: Complete Channel Structure

```json
{
  "schema": "olm.channel",
  "name": "stable-v1.0",
  "package": "openstack-operator",
  "entries": [
    {"name": "openstack-operator.v1.0.7", "skipRange": "<1.0.7"},
    {"name": "openstack-operator.v1.0.8", "replaces": "openstack-operator.v1.0.7"},
    {"name": "openstack-operator.v1.0.18", "replaces": "openstack-operator.v1.0.17", "skipRange": "<1.0.18"},
    {"name": "openstack-operator.v1.0.19-test", "replaces": "openstack-operator.v1.0.18", "skipRange": ">=1.0.0 <1.0.19"}
  ]
}
```

This allows:
- Sequential upgrades: v1.0.7 → v1.0.8 → ... → v1.0.18 → v1.0.19-test
- Skip upgrades: v1.0.7 → v1.0.19-test (via skipRange)
