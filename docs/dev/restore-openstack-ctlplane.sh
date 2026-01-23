#!/bin/bash
set -e

# OpenStack Control Plane Restore Script
# This script performs a restore of the stateless OpenStack control plane
# following the documented procedure in backup-restore-stateless.md
#
# CRITICAL: This script follows the correct restore order:
# 1. NetworkAttachmentDefinitions
# 2. Secrets (CA secrets needed by Issuers, exclude rabbitmq-*)
# 3. ConfigMaps (exclude rabbitmq-*)
# 4. TLS Issuers (cert-manager authority, needs CA secrets)
# 5. MariaDBDatabase CRs (needs database password secrets)
# 6. MariaDBAccount CRs (needs MariaDBDatabase CRs)
# 7. Related CRs (NetConfig, OpenStackVersion, Topology)
# 8. OpenStackControlPlane CR (triggers operator reconciliation)
# 9. Operators create Certificate CRs → cert-manager issues fresh certificates
# 10. Manual RabbitMQ user restoration
#
# NOTE: Certificate CRs and certificate secrets are NOT restored.
#       Operators recreate Certificate CRs during reconciliation, and cert-manager
#       issues fresh certificates from the same CA (preserving trust chain).

NAMESPACE="${NAMESPACE:-openstack}"
BACKUP_FILE="${1}"
SKIP_CLEANUP="${SKIP_CLEANUP:-false}"
SKIP_RABBITMQ_RESTORE="${SKIP_RABBITMQ_RESTORE:-false}"

# Print usage
usage() {
    cat <<EOF
Usage: $0 <backup-archive.tar.gz>

Environment Variables:
  NAMESPACE               Target namespace (default: openstack)
  SKIP_CLEANUP            Skip cleanup of existing resources (default: false)
  SKIP_RABBITMQ_RESTORE   Skip RabbitMQ user restoration (default: false)

Example:
  NAMESPACE=openstack ./restore-openstack-ctlplane.sh openstack-ctlplane-backup-20260119-120000.tar.gz

EOF
    exit 1
}

# Check if backup file is provided
if [ -z "${BACKUP_FILE}" ]; then
    echo "Error: Backup archive file is required"
    usage
fi

if [ ! -f "${BACKUP_FILE}" ]; then
    echo "Error: Backup file not found: ${BACKUP_FILE}"
    exit 1
fi

echo "========================================"
echo "OpenStack Control Plane Restore"
echo "========================================"
echo "Target Namespace: ${NAMESPACE}"
echo "Backup File: ${BACKUP_FILE}"
echo "Skip Cleanup: ${SKIP_CLEANUP}"
echo "Skip RabbitMQ Restore: ${SKIP_RABBITMQ_RESTORE}"
echo ""

# Extract backup
WORK_DIR=$(mktemp -d)
echo "Extracting backup to: ${WORK_DIR}"
tar -xzf ${BACKUP_FILE} -C ${WORK_DIR}
BACKUP_DIR=$(find ${WORK_DIR} -maxdepth 1 -type d -name "openstack-ctlplane-backup-*" | head -1)

if [ -z "${BACKUP_DIR}" ]; then
    echo "Error: Could not find backup directory in archive"
    rm -rf ${WORK_DIR}
    exit 1
fi

pushd ${BACKUP_DIR} > /dev/null
echo "Working directory: ${BACKUP_DIR}"
echo ""

# Helper function to apply resources with appropriate method
# Loops through each item in the backup file and checks if it has the
# kubectl.kubernetes.io/last-applied-configuration annotation.
# - Items WITH annotation: use client-side apply (backward compatibility)
# - Items WITHOUT annotation: use server-side apply (avoids size limits)
# Optional skip_pattern parameter: skip items whose names match the pattern
apply_resource() {
    local file=$1
    local namespace=$2
    local description=$3
    local skip_pattern=${4:-}

    # Get the count of items in the file
    local item_count=$(jq '.items | length' "${file}" 2>/dev/null || echo "0")

    if [ "${item_count}" -eq 0 ]; then
        echo "  Warning: No items found in ${file}"
        return 1
    fi

    echo "  Processing ${item_count} item(s) from ${file}..."
    echo ""

    local applied_count=0
    local skipped_count=0

    # Loop through each item
    for i in $(seq 0 $((item_count - 1))); do
        local item_name=$(jq -r ".items[${i}].metadata.name" "${file}")
        local item_kind=$(jq -r ".items[${i}].kind" "${file}")
        local has_annotation=$(jq -r ".items[${i}].metadata.annotations.\"kubectl.kubernetes.io/last-applied-configuration\" // empty" "${file}")

        # Skip items matching the skip pattern
        if [ -n "${skip_pattern}" ] && [[ "${item_name}" =~ ${skip_pattern} ]]; then
            echo "  ⊘ Skipping: ${item_kind}/${item_name} (matches skip pattern: ${skip_pattern})"
            echo ""
            skipped_count=$((skipped_count + 1))
            continue
        fi

        # Create temp file for this single item
        local temp_file=$(mktemp)
        jq ".items[${i}]" "${file}" > "${temp_file}"

        echo "  ┌─────────────────────────────────────────────────────────────"
        echo "  │ Applying: ${item_kind}/${item_name}"
        if [ -n "${has_annotation}" ]; then
            echo "  │ Method: client-side apply (has last-applied-configuration annotation)"
            echo "  └─────────────────────────────────────────────────────────────"
            oc apply -f "${temp_file}" -n "${namespace}" 2>&1 | sed 's/^/    /' | grep -v "Warning: resource" || true
        else
            echo "  │ Method: server-side apply (no annotation)"
            echo "  └─────────────────────────────────────────────────────────────"
            oc apply --server-side=true -f "${temp_file}" -n "${namespace}" 2>&1 | sed 's/^/    /' | grep -v "Warning: resource" || true
        fi
        echo ""

        rm -f "${temp_file}"
        applied_count=$((applied_count + 1))
    done

    if [ ${skipped_count} -gt 0 ]; then
        echo "  Summary: Applied ${applied_count}, Skipped ${skipped_count}"
        echo ""
    fi
}


echo "========================================"
echo "Step 1: Verify Operator Versions"
echo "========================================"
echo ""
echo "Required operator versions (from backup):"
cat operator-versions.txt | grep -A 5 "Installed Operators" || true
echo ""

echo "Current operator versions (on target cluster):"
echo "OpenStack Operator:"
oc get deployment openstack-operator-controller-manager -n openstack-operators -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || echo "Not found"
echo ""

echo "⚠️  CRITICAL: Operator versions MUST match!"
echo "Review the versions above and compare with operator-versions.txt"
echo ""
read -p "Do the operator versions match? (yes/no): " VERSIONS_MATCH

if [ "${VERSIONS_MATCH}" != "yes" ]; then
    echo "Error: Operator versions do not match. Aborting restore."
    echo "Install matching operator versions before proceeding."
    popd > /dev/null
    rm -rf ${WORK_DIR}
    exit 1
fi
echo ""

# Check if namespace exists
if ! oc get namespace ${NAMESPACE} >/dev/null 2>&1; then
    echo "Namespace ${NAMESPACE} does not exist. Creating..."
    oc create namespace ${NAMESPACE}
    oc label namespace ${NAMESPACE} openstack.org/name=${NAMESPACE}
    echo "✓ Namespace created"
else
    echo "✓ Namespace ${NAMESPACE} exists"
fi
echo ""

# Cleanup existing resources if requested
if [ "${SKIP_CLEANUP}" != "true" ]; then
    echo "========================================"
    echo "Step 2: Clean Up Existing Resources"
    echo "========================================"
    echo ""

    CTLPLANE_COUNT=$(oc get openstackcontrolplane -n ${NAMESPACE} --no-headers 2>/dev/null | wc -l)

    if [ "${CTLPLANE_COUNT}" -gt 0 ]; then
        echo "Found ${CTLPLANE_COUNT} OpenStackControlPlane CR(s) in namespace ${NAMESPACE}"
        read -p "Delete existing OpenStackControlPlane resources? (yes/no): " DELETE_CONFIRM

        if [ "${DELETE_CONFIRM}" = "yes" ]; then
            echo "Deleting OpenStackControlPlane CRs..."
            oc delete openstackcontrolplane --all -n ${NAMESPACE}

            echo "Waiting for cleanup to complete (checking every 10 seconds)..."
            while true; do
                POD_COUNT=$(oc get pods -n ${NAMESPACE} --no-headers 2>/dev/null | wc -l)
                echo "  Pods remaining: ${POD_COUNT}"
                if [ "${POD_COUNT}" -eq 0 ]; then
                    break
                fi
                sleep 10
            done
            echo "✓ Cleanup complete"
        else
            echo "Skipping cleanup. Note: This may cause conflicts during restore."
        fi
    else
        echo "No existing OpenStackControlPlane resources found"
    fi
    echo ""
else
    echo "Skipping cleanup (SKIP_CLEANUP=true)"
    echo ""
fi

echo "========================================"
echo "RESTORE ORDER IS CRITICAL!"
echo "========================================"
echo "Following the correct order to ensure:"
echo "  1. CA secrets exist before Issuers"
echo "  2. Issuers exist before operators trigger Certificate creation"
echo "  3. cert-manager issues fresh certificates from the same CA"
echo ""

echo "========================================"
echo "Step 3: Restore NetworkAttachmentDefinitions"
echo "========================================"
oc apply -f network-attachment-definitions-backup.json -n ${NAMESPACE}
echo "✓ NetworkAttachmentDefinitions restored"
echo ""
echo "Verifying NetworkAttachmentDefinitions..."
oc get network-attachment-definition -n ${NAMESPACE}
echo ""

echo "========================================"
echo "Step 4: Restore Secrets"
echo "========================================"
echo "CRITICAL: CA secrets must exist BEFORE Issuers"
echo "         - Issuers reference CA secrets (e.g., rootca-internal, rootca-public)"
echo "         - cert-manager will issue fresh certificates from these CAs"
echo ""
echo "Filtering secrets to restore only:"
echo "  1. User-provided secrets (no ownerReferences, no service-cert label)"
echo "  2. CA secrets (rootca-*) - preserves trust chain"
echo "  3. Database password secrets (*-db-password) - maintains DB access"
echo ""
echo "Excluding from restore:"
echo "  - RabbitMQ-related secrets (rabbitmq-*) - RBAC conflicts"
echo "  - Certificate secrets (service-cert label) - cert-manager recreates them"
echo "  - Other operator-managed secrets (with ownerReferences)"
echo ""

# Filter secrets: keep only user-provided, CAs, and DB passwords
# Exclude:
# - rabbitmq-* (RBAC conflicts)
# - Secrets with "service-cert" label (certificate secrets - cert-manager recreates them)
SECRETS_FILTERED=$(mktemp)
jq '.items |= map(
  select((.metadata.name | startswith("rabbitmq-")) | not) |
  select(.metadata.labels."service-cert" | not) |
  select(
    (.metadata.ownerReferences == null) or
    (.metadata.name | startswith("rootca-")) or
    (.metadata.name | contains("-db-password")) or
    (.metadata.name | contains("-dbpassword"))
  )
) | del(.metadata)' secrets-all-backup.json > ${SECRETS_FILTERED}

FILTERED_COUNT=$(jq '.items | length' ${SECRETS_FILTERED})
echo "Filtered secrets to restore: ${FILTERED_COUNT}"
echo ""

# Apply filtered secrets using the helper function
apply_resource ${SECRETS_FILTERED} ${NAMESPACE} "Secrets"
rm -f ${SECRETS_FILTERED}

echo "✓ Secrets restored (user-provided + CAs + database passwords)"
echo ""
SECRET_COUNT=$(oc get secrets -n ${NAMESPACE} --no-headers | wc -l)
echo "Total secrets in namespace: ${SECRET_COUNT}"
echo ""

echo "========================================"
echo "Step 5: Restore ConfigMaps"
echo "========================================"
echo "Filtering ConfigMaps to restore only:"
echo "  1. User-provided ConfigMaps (no ownerReferences)"
echo ""
echo "Operator-created ConfigMaps (including RabbitMQ) will be recreated by operators."
echo ""

# Filter ConfigMaps: keep only user-provided (no ownerReferences)
# Note: RabbitMQ ConfigMaps have ownerReferences, so they're automatically excluded
CONFIGMAPS_FILTERED=$(mktemp)
jq '.items |= map(
  select(.metadata.ownerReferences == null)
) | del(.metadata)' configmaps-all-backup.json > ${CONFIGMAPS_FILTERED}

FILTERED_COUNT=$(jq '.items | length' ${CONFIGMAPS_FILTERED})
echo "Filtered ConfigMaps to restore: ${FILTERED_COUNT}"
echo ""

# Apply filtered ConfigMaps using the helper function
apply_resource ${CONFIGMAPS_FILTERED} ${NAMESPACE} "ConfigMaps"
rm -f ${CONFIGMAPS_FILTERED}

echo "✓ ConfigMaps restored (user-provided only)"
echo ""

echo "========================================"
echo "Step 6: Restore TLS Issuers"
echo "========================================"
echo "CRITICAL: Issuers restored AFTER secrets (need CA secrets to exist)"
echo ""
echo "Restoring both operator-managed Issuers (rootca-*) and custom Issuers:"
echo "  - Operator-managed Issuers will be reconciled by the operator"
echo "  - Custom Issuers (if any) are preserved for custom CA configuration"
echo ""
echo "NOTE: Certificate CRs are NOT restored. Operators will create them during"
echo "      reconciliation. cert-manager will adopt CA secrets and issue fresh"
echo "      service certificates."
echo ""
apply_resource issuer-backup.json ${NAMESPACE} "Issuers"
echo "✓ Issuers restored"
echo ""
echo "Verifying Issuers are ready..."
oc get issuer -n ${NAMESPACE}
echo ""
echo "Waiting for Issuers to become ready..."
sleep 5
oc wait --for=condition=Ready issuer --all -n ${NAMESPACE} --timeout=60s || echo "Warning: Some issuers may not be ready yet"
echo ""

echo "========================================"
echo "Step 7: Restore MariaDBDatabase CRs"
echo "========================================"
echo "CRITICAL: MariaDBDatabase CRs restored AFTER secrets (database password secrets are needed)"
echo ""
apply_resource mariadbdatabase-backup.json ${NAMESPACE} "MariaDBDatabase CRs"
echo "✓ MariaDBDatabase CRs restored"
echo ""

echo "========================================"
echo "Step 8: Restore MariaDBAccount CRs"
echo "========================================"
echo "CRITICAL: MariaDBAccount CRs restored AFTER databases (accounts depend on databases)"
echo ""
apply_resource mariadbaccount-backup.json ${NAMESPACE} "MariaDBAccount CRs"
echo "✓ MariaDBAccount CRs restored"
echo ""

echo "========================================"
echo "Step 9: Restore Related CRs"
echo "========================================"

if [ -f openstackversion-backup.json ]; then
    OPENSTACKVERSION_COUNT=$(jq '.items | length' openstackversion-backup.json 2>/dev/null || echo "0")
    if [ "${OPENSTACKVERSION_COUNT}" -gt 0 ]; then
        echo "Restoring OpenStackVersion..."
        oc apply -f openstackversion-backup.json -n ${NAMESPACE}
        echo "✓ OpenStackVersion restored"
    else
        echo "No OpenStackVersion to restore (file is empty)"
    fi
else
    echo "No OpenStackVersion to restore"
fi

echo "Restoring NetConfig (CRITICAL)..."
oc apply -f netconfig-backup.json -n ${NAMESPACE}
echo "✓ NetConfig restored"

if [ -f topology-backup.json ]; then
    TOPOLOGY_COUNT=$(jq '.items | length' topology-backup.json 2>/dev/null || echo "0")
    if [ "${TOPOLOGY_COUNT}" -gt 0 ]; then
        echo "Restoring Topology..."
        oc apply -f topology-backup.json -n ${NAMESPACE}
        echo "✓ Topology restored"
    else
        echo "No Topology to restore (file is empty)"
    fi
else
    echo "No Topology to restore"
fi
echo ""

echo "========================================"
echo "Step 10: Restore OpenStackControlPlane CR"
echo "========================================"
echo ""
echo "When the OpenStackControlPlane CR is restored, operators will:"
echo "  1. Reconcile and create Certificate CRs for all services"
echo "  2. cert-manager will issue fresh certificates from the restored CAs"
echo "  3. Services will use new certificates with fresh expiry dates"
echo ""
read -p "Ready to restore OpenStackControlPlane CR? This will trigger operator reconciliation. (yes/no): " RESTORE_CONFIRM

if [ "${RESTORE_CONFIRM}" != "yes" ]; then
    echo "Aborting. You can manually restore later with:"
    echo "  cd ${BACKUP_DIR}"
    echo "  oc apply -f openstackcontrolplane-backup.json -n ${NAMESPACE}"
    popd > /dev/null
    rm -rf ${WORK_DIR}
    exit 1
fi

oc apply -f openstackcontrolplane-backup.json -n ${NAMESPACE}
echo "✓ OpenStackControlPlane CR restored"
echo ""

echo "Waiting for operator reconciliation to start..."
sleep 10
echo ""

echo "Checking RabbitMQ cluster status..."
oc get rabbitmq -n ${NAMESPACE} || echo "No RabbitMQ resources yet"
echo ""

echo "Waiting for RabbitMQ clusters to be created and ready..."
echo "This may take several minutes. Checking every 30 seconds..."
WAIT_COUNT=0
MAX_WAIT=20  # 10 minutes max

while [ ${WAIT_COUNT} -lt ${MAX_WAIT} ]; do
    RABBITMQ_COUNT=$(oc get rabbitmq -n ${NAMESPACE} --no-headers 2>/dev/null | wc -l)

    if [ "${RABBITMQ_COUNT}" -gt 0 ]; then
        echo ""
        echo "RabbitMQ clusters found:"
        oc get rabbitmq -n ${NAMESPACE}
        echo ""

        # Check if all are ready
        NOT_READY=$(oc get rabbitmq -n ${NAMESPACE} -o json | jq '[.items[] | select(.status.conditions[] | select(.type=="Ready" and .status!="True"))] | length')

        if [ "${NOT_READY}" -eq 0 ]; then
            echo "✓ All RabbitMQ clusters are ready!"
            break
        else
            echo "Waiting for ${NOT_READY} RabbitMQ cluster(s) to become ready..."
        fi
    else
        echo "  No RabbitMQ clusters yet... (attempt $((WAIT_COUNT+1))/${MAX_WAIT})"
    fi

    sleep 30
    WAIT_COUNT=$((WAIT_COUNT+1))
done

if [ ${WAIT_COUNT} -ge ${MAX_WAIT} ]; then
    echo "Warning: Timeout waiting for RabbitMQ clusters. Check manually:"
    echo "  oc get rabbitmq -n ${NAMESPACE}"
    echo "  oc get pods -n ${NAMESPACE} | grep rabbitmq"
fi
echo ""

echo "Checking RabbitMQ pods..."
oc get pods -n ${NAMESPACE} | grep rabbitmq || echo "No RabbitMQ pods found yet"
echo ""

# RabbitMQ User Restoration
if [ "${SKIP_RABBITMQ_RESTORE}" != "true" ]; then
    echo "========================================"
    echo "Step 11: Restore RabbitMQ User Credentials"
    echo "========================================"
    echo ""
    echo "⚠️  CRITICAL FOR EDPM/DATA PLANE DEPLOYMENTS ⚠️"
    echo ""
    echo "The new RabbitMQ clusters have random credentials."
    echo "You MUST restore the original user credentials so that:"
    echo "  - Control plane services can authenticate"
    echo "  - EDPM data plane nodes maintain connectivity"
    echo ""

    read -p "Restore RabbitMQ user credentials now? (yes/no): " RABBITMQ_CONFIRM

    if [ "${RABBITMQ_CONFIRM}" = "yes" ]; then
        echo ""
        echo "Extracting RabbitMQ credentials from backup..."

        # Main RabbitMQ
        RABBITMQ_USER=$(jq -r '.items[] | select(.metadata.name=="rabbitmq-default-user") | .data.username' secrets-all-backup.json 2>/dev/null | base64 -d)
        RABBITMQ_PASS=$(jq -r '.items[] | select(.metadata.name=="rabbitmq-default-user") | .data.password' secrets-all-backup.json 2>/dev/null | base64 -d)

        if [ -n "${RABBITMQ_USER}" ]; then
            echo "Main RabbitMQ user: ${RABBITMQ_USER}"
            echo "Restoring to rabbitmq cluster..."

            oc rsh -n ${NAMESPACE} rabbitmq-server-0 rabbitmqctl add_user "${RABBITMQ_USER}" "${RABBITMQ_PASS}" 2>/dev/null || echo "  (user may already exist)"
            oc rsh -n ${NAMESPACE} rabbitmq-server-0 rabbitmqctl set_user_tags "${RABBITMQ_USER}" administrator
            oc rsh -n ${NAMESPACE} rabbitmq-server-0 rabbitmqctl set_permissions -p / "${RABBITMQ_USER}" ".*" ".*" ".*"
            echo "✓ Main RabbitMQ user restored"
        else
            echo "Warning: Could not find rabbitmq-default-user in backup"
        fi
        echo ""

        # Cell RabbitMQ
        RABBITMQ_CELL1_USER=$(jq -r '.items[] | select(.metadata.name=="rabbitmq-cell1-default-user") | .data.username' secrets-all-backup.json 2>/dev/null | base64 -d)
        RABBITMQ_CELL1_PASS=$(jq -r '.items[] | select(.metadata.name=="rabbitmq-cell1-default-user") | .data.password' secrets-all-backup.json 2>/dev/null | base64 -d)

        if [ -n "${RABBITMQ_CELL1_USER}" ]; then
            echo "Cell1 RabbitMQ user: ${RABBITMQ_CELL1_USER}"
            echo "Restoring to rabbitmq-cell1 cluster..."

            oc rsh -n ${NAMESPACE} rabbitmq-cell1-server-0 rabbitmqctl add_user "${RABBITMQ_CELL1_USER}" "${RABBITMQ_CELL1_PASS}" 2>/dev/null || echo "  (user may already exist)"
            oc rsh -n ${NAMESPACE} rabbitmq-cell1-server-0 rabbitmqctl set_user_tags "${RABBITMQ_CELL1_USER}" administrator
            oc rsh -n ${NAMESPACE} rabbitmq-cell1-server-0 rabbitmqctl set_permissions -p / "${RABBITMQ_CELL1_USER}" ".*" ".*" ".*"
            echo "✓ Cell1 RabbitMQ user restored"
        else
            echo "Warning: Could not find rabbitmq-cell1-default-user in backup"
        fi
        echo ""

        # Notifications RabbitMQ (if exists)
        RABBITMQ_NOTIF_USER=$(jq -r '.items[] | select(.metadata.name=="rabbitmq-notifications-default-user") | .data.username' secrets-all-backup.json 2>/dev/null | base64 -d)

        if [ -n "${RABBITMQ_NOTIF_USER}" ]; then
            RABBITMQ_NOTIF_PASS=$(jq -r '.items[] | select(.metadata.name=="rabbitmq-notifications-default-user") | .data.password' secrets-all-backup.json 2>/dev/null | base64 -d)

            echo "Notifications RabbitMQ user: ${RABBITMQ_NOTIF_USER}"
            echo "Restoring to rabbitmq-notifications cluster..."

            oc rsh -n ${NAMESPACE} rabbitmq-notifications-server-0 rabbitmqctl add_user "${RABBITMQ_NOTIF_USER}" "${RABBITMQ_NOTIF_PASS}" 2>/dev/null || echo "  (user may already exist)"
            oc rsh -n ${NAMESPACE} rabbitmq-notifications-server-0 rabbitmqctl set_user_tags "${RABBITMQ_NOTIF_USER}" administrator
            oc rsh -n ${NAMESPACE} rabbitmq-notifications-server-0 rabbitmqctl set_permissions -p / "${RABBITMQ_NOTIF_USER}" ".*" ".*" ".*"
            echo "✓ Notifications RabbitMQ user restored"
        else
            echo "No rabbitmq-notifications cluster in backup"
        fi
        echo ""

        echo "Verifying RabbitMQ users..."
        if [ -n "${RABBITMQ_USER}" ]; then
            echo "Main RabbitMQ:"
            oc rsh -n ${NAMESPACE} rabbitmq-server-0 rabbitmqctl list_users | grep "${RABBITMQ_USER}" || echo "  User not found!"
        fi
        if [ -n "${RABBITMQ_CELL1_USER}" ]; then
            echo "Cell1 RabbitMQ:"
            oc rsh -n ${NAMESPACE} rabbitmq-cell1-server-0 rabbitmqctl list_users | grep "${RABBITMQ_CELL1_USER}" || echo "  User not found!"
        fi
        echo ""

        echo "✓ RabbitMQ user credentials restored successfully!"
        echo ""
        echo "IMPORTANT: Data plane nodes will get updated credentials on their next EDPM deployment run."
    else
        echo ""
        echo "⚠️  WARNING: Skipping RabbitMQ user restoration!"
        echo "You must manually restore RabbitMQ users or data plane nodes will lose connectivity."
        echo "See the documentation for manual restoration steps."
    fi
else
    echo "Skipping RabbitMQ user restoration (SKIP_RABBITMQ_RESTORE=true)"
fi
echo ""

# Return to original directory and cleanup temporary directory
popd > /dev/null
rm -rf ${WORK_DIR}

echo "========================================"
echo "Restore completed!"
echo "========================================"
echo ""
echo "Next steps:"
echo "1. Monitor control plane status:"
echo "   oc get openstackcontrolplane -n ${NAMESPACE} --watch"
echo ""
echo "2. Check service endpoints:"
echo "   oc get svc -n ${NAMESPACE}"
echo ""
echo "3. Verify all pods are running:"
echo "   oc get pods -n ${NAMESPACE}"
echo ""
echo "4. Check for any errors in operator logs:"
echo "   oc logs -n openstack-operators deployment/openstack-operator-controller-manager --tail=50"
echo ""
echo "5. Verify EDPM nodes can connect (if applicable):"
echo "   - Check nova-compute logs on compute nodes"
echo "   - Check neutron agent logs on network nodes"
echo ""
echo "For detailed verification steps, see docs/dev/backup-restore-stateless.md"
echo ""
