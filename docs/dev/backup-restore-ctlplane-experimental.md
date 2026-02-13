# OpenStack Control Plane Backup and Restore - Experimental Scenarios

⚠️ **WARNING**: The scenarios documented here have **NOT been tested**. Use at your own risk and verify each step carefully.

⚠️ **OUTDATED PROCEDURES**: These scenarios do not yet incorporate the refined filtering and error handling from Scenario 1 (smart filtering of secrets/configmaps, RabbitMQUser CRD approach, etc.). They need to be updated to match the tested approach before use.

This document contains experimental restore scenarios that are theoretically possible but have not been validated:

- **Scenario 2**: Restore to Different Namespace (Same Cluster)
- **Scenario 3**: Restore to Different Cluster

For the **tested** restore procedure with refined filtering and error handling, see [backup-restore-ctlplane.md](backup-restore-ctlplane.md) for Scenario 1 (Restore to Same Namespace).

---

## Scenario 2: Restore to Different Namespace (Same Cluster)

⚠️ **NOTE**: This scenario has not been tested yet. Use with caution and verify each step.

⚠️ **CRITICAL - EDPM Hostname Requirements**: If you have EDPM nodes (compute/network nodes), **the hostnames MUST NOT change!** Nova-compute registers with a hostname, and all running VM instances are associated with that hostname. Changing hostnames will cause you to lose the ability to manage existing instances.

⚠️ **EDPM Deployment Required**: Since this scenario changes the namespace, DNS names change (e.g., `rabbitmq.openstack.svc` → `rabbitmq.openstack-restored.svc`). You MUST run an EDPM deployment to update node configurations before nodes can reconnect to the control plane.

**Prerequisites:**
- **Operator versions match the backup** (same cluster, so this should already be true)
- New namespace will be created
- Operator managing the new namespace
- Storage classes available
- **EDPM node hostnames must remain the same** (see warning above)

**Steps:**

### 1. Verify Operator Versions and Extract Backup

```bash
# Extract backup
tar -xzf openstack-ctlplane-backup-*.tar.gz
cd openstack-ctlplane-backup-*/

# Review operator versions from backup (should match since same cluster)
cat operator-versions.txt

# Verify current versions match
oc get deployment openstack-operator-controller-manager -n openstack-operators -o jsonpath='{.spec.template.spec.containers[0].image}'
```

### 2. Create New Namespace

```bash
NEW_NAMESPACE="openstack-restored"

# In OpenShift, create a new project (which creates a namespace)
oc new-project ${NEW_NAMESPACE}

# Or create namespace directly
# oc create namespace ${NEW_NAMESPACE}

# Label appropriately
oc label namespace ${NEW_NAMESPACE} openstack.org/name=${NEW_NAMESPACE}
```

### 2. Prepare Backup Files

```bash
# Backup already extracted in step 1
# cd openstack-ctlplane-backup-*/

# Update namespace in all backup files
OLD_NAMESPACE="openstack"
NEW_NAMESPACE="openstack-restored"

# Update namespace in all JSON backup files
for file in *-backup.json; do
    jq --arg old "${OLD_NAMESPACE}" --arg new "${NEW_NAMESPACE}" \
       '(.. | objects | select(has("namespace")) | .namespace) |= (if . == $old then $new else . end)' \
       ${file} > ${file}.tmp && mv ${file}.tmp ${file}
done
```

### 3. Restore in New Namespace with Staged Deployment

**Follow the correct restore order using staged deployment:**

```bash
# 1. Restore NetworkAttachmentDefinitions
oc apply -f network-attachment-definitions-backup.json -n ${NEW_NAMESPACE}

# 2. Restore Secrets (filtered)
jq '.items |= map(
  select((.metadata.name | startswith("rabbitmq-")) | not) |
  select(.metadata.labels."service-cert" | not) |
  select(
    (.metadata.ownerReferences == null) or
    (.metadata.name | startswith("rootca-")) or
    (.metadata.name | contains("-db-password"))
  )
)' secrets-all-backup.json | oc apply -f - -n ${NEW_NAMESPACE}

# 3. Restore ConfigMaps (user-provided only)
jq '.items |= map(select(.metadata.ownerReferences == null))' configmaps-all-backup.json | \
  oc apply -f - -n ${NEW_NAMESPACE}

# 4. Restore TLS Issuers
oc apply -f issuer-backup.json -n ${NEW_NAMESPACE}

# 5. Restore MariaDB CRs
oc apply -f mariadbdatabase-backup.json -n ${NEW_NAMESPACE}
oc apply -f mariadbaccount-backup.json -n ${NEW_NAMESPACE}

# 6. Restore Related CRs
oc apply -f openstackversion-backup.json -n ${NEW_NAMESPACE} 2>/dev/null || true
oc apply -f topology-backup.json -n ${NEW_NAMESPACE} 2>/dev/null || true

# 7. Restore OpenStackControlPlane CR with staged deployment
jq '.items[0].metadata.annotations["core.openstack.org/deployment-stage"] = "infrastructure-only"' \
  openstackcontrolplane-backup.json > openstackcontrolplane-staged.json

oc apply -f openstackcontrolplane-staged.json -n ${NEW_NAMESPACE}

# Wait for infrastructure ready
oc wait --for=condition=OpenStackControlPlaneInfrastructureReady \
  openstackcontrolplane/$(jq -r '.items[0].metadata.name' openstackcontrolplane-backup.json) \
  -n ${NEW_NAMESPACE} --timeout=20m
```

### 4. Restore Database Contents and PVCs

```bash
# Restore databases (MariaDB, OVN) while services are paused
# Follow separate database restore procedures

# Restore PVCs if applicable (OADP or other method)
```

### 5. Restore RabbitMQ User Credentials

Follow the RabbitMQUser CRD approach from Scenario 1:

```bash
# Get list of running RabbitMQ clusters
echo "Finding RabbitMQ clusters..."
RABBITMQ_CLUSTERS=$(oc get rabbitmqcluster -n ${NEW_NAMESPACE} -o jsonpath='{.items[*].metadata.name}')

echo "RabbitMQ clusters found: ${RABBITMQ_CLUSTERS}"

# For each RabbitMQ cluster, restore the user credentials
for CLUSTER_NAME in ${RABBITMQ_CLUSTERS}; do
  # Determine the secret name pattern (e.g., rabbitmq -> rabbitmq-default-user)
  ORIGINAL_SECRET_NAME="${CLUSTER_NAME}-default-user"
  RESTORED_SECRET_NAME="${CLUSTER_NAME}-restored-user"

  echo ""
  echo "Processing cluster: ${CLUSTER_NAME}"
  echo "Looking for secret: ${ORIGINAL_SECRET_NAME} in backup"

  # Check if secret exists in backup
  SECRET_EXISTS=$(jq -r ".items[] | select(.metadata.name==\"${ORIGINAL_SECRET_NAME}\") | .metadata.name" secrets-all-backup.json 2>/dev/null)

  if [ -z "${SECRET_EXISTS}" ]; then
    echo "  ERROR: Secret ${ORIGINAL_SECRET_NAME} not found in backup!"
    echo "  Cannot restore RabbitMQ credentials for cluster ${CLUSTER_NAME}"
    exit 1
  fi

  # Check if the restored secret already exists
  if oc get secret "${RESTORED_SECRET_NAME}" -n ${NEW_NAMESPACE} &>/dev/null; then
    echo "  ERROR: Secret ${RESTORED_SECRET_NAME} already exists!"
    echo "  This may indicate a previous restore attempt. Please investigate and delete if needed:"
    echo "    oc delete secret ${RESTORED_SECRET_NAME} -n ${NEW_NAMESPACE}"
    exit 1
  fi

  # Restore the secret with a new name to avoid conflict
  jq ".items[] | select(.metadata.name==\"${ORIGINAL_SECRET_NAME}\") | .metadata.name=\"${RESTORED_SECRET_NAME}\" | del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp, .metadata.ownerReferences)" \
    secrets-all-backup.json | oc apply -f - -n ${NEW_NAMESPACE}

  echo "  ✓ Restored secret as ${RESTORED_SECRET_NAME}"

  # Create RabbitMQUser CR that references the restored secret
  cat <<EOF | oc apply -f -
apiVersion: rabbitmq.openstack.org/v1beta1
kind: RabbitMQUser
metadata:
  name: ${CLUSTER_NAME}-restored-user
  namespace: ${NEW_NAMESPACE}
spec:
  rabbitmqClusterName: ${CLUSTER_NAME}
  secret: ${RESTORED_SECRET_NAME}
  tags:
    - administrator
  permissions:
    configure: ".*"
    read: ".*"
    write: ".*"
EOF

  echo "  ✓ Created RabbitMQUser CR for ${CLUSTER_NAME}"
done

echo ""
echo "Waiting for RabbitMQUser CRs to be ready..."
sleep 5

# Verify RabbitMQUser CRs are ready
echo ""
echo "RabbitMQUser status:"
oc get rabbitmquser -n ${NEW_NAMESPACE}

echo ""
echo "RabbitMQ user credentials restored successfully using RabbitMQUser CRs!"
```

### 6. Resume Deployment

```bash
# Remove the staged deployment annotation
CTLPLANE_NAME=$(jq -r '.items[0].metadata.name' openstackcontrolplane-backup.json)
oc annotate openstackcontrolplane ${CTLPLANE_NAME} -n ${NEW_NAMESPACE} \
  core.openstack.org/deployment-stage-

# Wait for control plane ready
oc wait --for=condition=Ready openstackcontrolplane/${CTLPLANE_NAME} \
  -n ${NEW_NAMESPACE} --timeout=30m
```

### 7. Post-Restore Configuration

**IMPORTANT**: Since you changed namespace, DNS names will change:
- Old: `keystone.openstack.svc.cluster.local`
- New: `keystone.openstack-restored.svc.cluster.local`

**This is OK for stateless services** because:
- No database to update (excluded from scope)
- Services will register with new DNS names
- External clients need to be reconfigured

**EDPM/Data Plane Updates Required:**

If you have EDPM nodes (compute, network), you **MUST** run EDPM deployment to update their configurations:

```bash
# EDPM node configurations still reference the old namespace DNS names
# Example: transport_url points to rabbitmq-cell1.openstack.svc
# Must update to: rabbitmq-cell1.openstack-restored.svc

# Run EDPM deployment to update all node configurations
oc apply -f <your-edpm-deployment-cr.yaml>

# Monitor EDPM deployment
oc get openstackdataplanedeployment -n ${NEW_NAMESPACE} --watch

# Verify data plane nodes are updated and functional
oc get openstackdataplanenodeset -n ${NEW_NAMESPACE}
```

**What gets updated on EDPM nodes:**
- RabbitMQ transport URLs (cell, notifications)
- Service endpoints (Keystone, Nova, Neutron, Glance)
- OVN database connections
- Any other control plane service references

Without running EDPM deployment, data plane nodes will continue trying to connect to the old namespace endpoints and fail.

---

## Scenario 3: Restore to Different Cluster

⚠️ **NOTE**: This scenario has not been tested yet. Use with caution and verify each step.

⚠️ **CRITICAL - EDPM Hostname Requirements**: If you have EDPM nodes (compute/network nodes), **the hostnames MUST NOT change!** Nova-compute registers with a hostname, and all running VM instances are associated with that hostname. Changing hostnames will cause you to lose the ability to manage existing instances.

⚠️ **EDPM Deployment May Be Required**: If the namespace, control plane endpoint IPs, or DNS server endpoint IP change, you MUST run an EDPM deployment to update node configurations before nodes can reconnect to the control plane.

**Prerequisites:**
- Target cluster has **EXACT same operator versions installed** as source cluster
- Target cluster has required storage classes
- Network connectivity for external access
- Compatible OpenShift version
- **EDPM node hostnames must remain the same** (see warning above)

**Steps:**

### 1. Verify Target Cluster and Operator Versions

**CRITICAL**: This is the most important step for cross-cluster restore.

```bash
# Login to target OpenShift cluster
oc login https://api.target-cluster.example.com:6443 --username <username> --password <password>

# Extract backup to review metadata
tar -xzf openstack-ctlplane-backup-*.tar.gz
cd openstack-ctlplane-backup-*/

# Display required operator versions from backup
echo "========================================="
echo "REQUIRED OPERATOR VERSIONS (from backup):"
echo "========================================="
cat operator-versions.txt | grep -A 10 "Operator Versions:"
echo ""

# Check current operator version on target cluster
echo "========================================="
echo "CURRENT OPERATOR VERSION (target cluster):"
echo "========================================="
echo "OpenStack Operator:"
oc get deployment openstack-operator-controller-manager -n openstack-operators -o jsonpath='{.spec.template.spec.containers[0].image}'
echo ""

echo "========================================="
echo "MANUAL VERIFICATION REQUIRED!"
echo "========================================="
echo "Compare the version above with operator-versions.txt. They MUST match exactly."
echo "If they don't match, STOP and install matching operator version."
echo "Note: Installing the correct openstack-operator will automatically install the matching infra-operator."
echo ""

# Verify storage classes exist
# Note: Check both global and service-specific storage classes
STORAGE_CLASS=$(jq -r '.items[0].spec.storageClass // .spec.storageClass' openstackcontrolplane-backup.json)
echo "Global storage class: ${STORAGE_CLASS}"
oc get storageclass ${STORAGE_CLASS} || echo "WARNING: Global storage class not found!"
echo ""
echo "Note: Individual services may have overridden storage classes in their templates."
echo "Review the backup file for service-specific storageClass or storageClassName parameters."
```

**STOP HERE if operator versions don't match!**

Install the exact operator versions from the backup before proceeding. Consult your operator installation documentation for version-specific installation.

### 2. Create Namespace

```bash
# In OpenShift, create a new project (which creates a namespace)
oc new-project openstack

# Or create namespace directly
# oc create namespace openstack

# Label appropriately
oc label namespace openstack openstack.org/name=openstack
```

### 3. Restore Resources with Staged Deployment

**Follow the correct restore order** (see "Important: Restore Order Matters" section in the main documentation):

```bash
# 1. Restore NetworkAttachmentDefinitions
oc apply -f network-attachment-definitions-backup.json

# 2. Restore TLS Issuers
oc apply -f issuer-backup.json

# 3. Restore Secrets
oc apply -f secrets-all-backup.json

# 4. Restore ConfigMaps
oc apply -f configmaps-all-backup.json

# 5. Restore MariaDB CRs
oc apply -f mariadbdatabase-backup.json
oc apply -f mariadbaccount-backup.json

# 6. Restore Related CRs
oc apply -f openstackversion-backup.json 2>/dev/null || true
oc apply -f netconfig-backup.json
oc apply -f topology-backup.json 2>/dev/null || true

# 7. Restore OpenStackControlPlane CR with staged deployment annotation
jq '.items[0].metadata.annotations["core.openstack.org/deployment-stage"] = "infrastructure-only"' \
  openstackcontrolplane-backup.json > openstackcontrolplane-staged.json

oc apply -f openstackcontrolplane-staged.json

# Wait for OpenStackControlPlaneInfrastructureReady condition
oc wait --for=condition=OpenStackControlPlaneInfrastructureReady openstackcontrolplane/openstack -n openstack --timeout=20m

# 8. Restore Database Contents (MariaDB and OVN)
# Follow separate database restore procedures while services are NOT running

# 9. Restore RabbitMQ User Credentials using RabbitMQUser CRD
# Get list of running RabbitMQ clusters
echo "Finding RabbitMQ clusters..."
RABBITMQ_CLUSTERS=$(oc get rabbitmqcluster -n openstack -o jsonpath='{.items[*].metadata.name}')

echo "RabbitMQ clusters found: ${RABBITMQ_CLUSTERS}"

# For each RabbitMQ cluster, restore the user credentials
for CLUSTER_NAME in ${RABBITMQ_CLUSTERS}; do
  ORIGINAL_SECRET_NAME="${CLUSTER_NAME}-default-user"
  RESTORED_SECRET_NAME="${CLUSTER_NAME}-restored-user"

  echo ""
  echo "Processing cluster: ${CLUSTER_NAME}"
  echo "Looking for secret: ${ORIGINAL_SECRET_NAME} in backup"

  # Check if secret exists in backup
  SECRET_EXISTS=$(jq -r ".items[] | select(.metadata.name==\"${ORIGINAL_SECRET_NAME}\") | .metadata.name" secrets-all-backup.json 2>/dev/null)

  if [ -z "${SECRET_EXISTS}" ]; then
    echo "  ERROR: Secret ${ORIGINAL_SECRET_NAME} not found in backup!"
    echo "  Cannot restore RabbitMQ credentials for cluster ${CLUSTER_NAME}"
    exit 1
  fi

  # Check if the restored secret already exists
  if oc get secret "${RESTORED_SECRET_NAME}" -n openstack &>/dev/null; then
    echo "  ERROR: Secret ${RESTORED_SECRET_NAME} already exists!"
    echo "  This may indicate a previous restore attempt. Please investigate and delete if needed:"
    echo "    oc delete secret ${RESTORED_SECRET_NAME} -n openstack"
    exit 1
  fi

  # Restore the secret with a new name to avoid conflict
  jq ".items[] | select(.metadata.name==\"${ORIGINAL_SECRET_NAME}\") | .metadata.name=\"${RESTORED_SECRET_NAME}\" | del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp, .metadata.ownerReferences)" \
    secrets-all-backup.json | oc apply -f -

  echo "  ✓ Restored secret as ${RESTORED_SECRET_NAME}"

  # Create RabbitMQUser CR that references the restored secret
  cat <<EOF | oc apply -f -
apiVersion: rabbitmq.openstack.org/v1beta1
kind: RabbitMQUser
metadata:
  name: ${CLUSTER_NAME}-restored-user
  namespace: openstack
spec:
  rabbitmqClusterName: ${CLUSTER_NAME}
  secret: ${RESTORED_SECRET_NAME}
  tags:
    - administrator
  permissions:
    configure: ".*"
    read: ".*"
    write: ".*"
EOF

  echo "  ✓ Created RabbitMQUser CR for ${CLUSTER_NAME}"
done

echo ""
echo "Waiting for RabbitMQUser CRs to be ready..."
sleep 5

# Verify RabbitMQUser CRs are ready
echo ""
echo "RabbitMQUser status:"
oc get rabbitmquser -n openstack

echo ""
echo "RabbitMQ user credentials restored successfully using RabbitMQUser CRs!"

# 10. Resume Deployment (Remove staged deployment annotation)
oc annotate openstackcontrolplane openstack -n openstack \
  core.openstack.org/deployment-stage-

# Wait for control plane to be ready
oc wait --for=condition=Ready openstackcontrolplane/openstack -n openstack --timeout=30m

# Monitor services being created
oc get openstackcontrolplane -n openstack --watch
```

### 4. Post-Restore Configuration

**Update External Access:**

Since this is a new cluster, update:
- Load balancer configurations
- DNS records for external API endpoints
- Firewall rules
- Client configurations

**EDPM/Data Plane Updates Required:**

If you have EDPM nodes (compute, network), you **MUST** run EDPM deployment to update their configurations for the new cluster:

```bash
# EDPM node configurations reference the old cluster endpoints
# Must update to new cluster endpoints

# Run EDPM deployment to update all node configurations
oc apply -f <your-edpm-deployment-cr.yaml>

# Monitor EDPM deployment
oc get openstackdataplanedeployment -n openstack --watch

# Verify data plane nodes are updated and functional
oc get openstackdataplanenodeset -n openstack
```

**What gets updated on EDPM nodes:**
- RabbitMQ transport URLs (pointing to new cluster)
- Service endpoints (Keystone, Nova, Neutron, Glance on new cluster)
- OVN database connections (new cluster)
- TLS certificates (new cluster CA)
- Any other control plane service references

Without running EDPM deployment, data plane nodes will continue trying to connect to the old cluster endpoints and fail.
