#!/bin/bash
set -e

# OVN Database Restore Script
# This script restores both OVN Northbound and Southbound databases

NAMESPACE="${NAMESPACE:-openstack}"
NB_BACKUP="${1}"
SB_BACKUP="${2}"

# Print usage
usage() {
    cat <<EOF
Usage: $0 <nb-backup-file> <sb-backup-file>

Restores OVN Northbound and Southbound databases from backup files.

Arguments:
  nb-backup-file    Path to OVN NB database backup file
  sb-backup-file    Path to OVN SB database backup file

Environment Variables:
  NAMESPACE         Target namespace (default: openstack)

Example:
  NAMESPACE=openstack ./restore-ovn.sh ovn-nb-backup-20260122-120000.db ovn-sb-backup-20260122-120000.db

  # Or extract from archive first:
  tar -xzf ovn-backup-20260122-120000.tar.gz
  ./restore-ovn.sh ovn-nb-backup-20260122-120000.db ovn-sb-backup-20260122-120000.db

EOF
    exit 1
}

# Check arguments
if [ -z "${NB_BACKUP}" ] || [ -z "${SB_BACKUP}" ]; then
    usage
fi

echo "=========================================="
echo "OVN Database Restore"
echo "=========================================="
echo "Namespace: ${NAMESPACE}"
echo "NB Backup: ${NB_BACKUP}"
echo "SB Backup: ${SB_BACKUP}"
echo ""

# Verify backup files exist
if [ ! -f "${NB_BACKUP}" ]; then
    echo "Error: NB backup file not found: ${NB_BACKUP}"
    exit 1
fi

if [ ! -f "${SB_BACKUP}" ]; then
    echo "Error: SB backup file not found: ${SB_BACKUP}"
    exit 1
fi

# Verify backup files are not empty
if [ ! -s "${NB_BACKUP}" ]; then
    echo "Error: NB backup file is empty: ${NB_BACKUP}"
    exit 1
fi

if [ ! -s "${SB_BACKUP}" ]; then
    echo "Error: SB backup file is empty: ${SB_BACKUP}"
    exit 1
fi

echo "✓ Backup files verified"
echo "  NB size: $(du -h ${NB_BACKUP} | cut -f1)"
echo "  SB size: $(du -h ${SB_BACKUP} | cut -f1)"
echo ""

# Check if OVN database pods are running
echo "Checking OVN database pods..."
if ! oc get pod ovsdbserver-nb-0 -n ${NAMESPACE} &>/dev/null; then
    echo "Error: OVN NB database pod 'ovsdbserver-nb-0' not found in namespace ${NAMESPACE}"
    echo "Please verify the OVN database pods are running:"
    echo "  oc get pods -n ${NAMESPACE} | grep ovsdbserver"
    exit 1
fi

if ! oc get pod ovsdbserver-sb-0 -n ${NAMESPACE} &>/dev/null; then
    echo "Error: OVN SB database pod 'ovsdbserver-sb-0' not found in namespace ${NAMESPACE}"
    echo "Please verify the OVN database pods are running:"
    echo "  oc get pods -n ${NAMESPACE} | grep ovsdbserver"
    exit 1
fi
echo "✓ OVN database pods are running"
echo ""

# Warning and confirmation
echo "⚠️  WARNING: This will replace the current OVN databases!"
echo "⚠️  Network connectivity will be briefly disrupted!"
echo "⚠️  All current network state will be lost!"
echo ""
echo "Current OVN state will be backed up before restore."
echo ""
read -p "Continue with restore? (yes/no): " CONFIRM

if [ "${CONFIRM}" != "yes" ]; then
    echo "Restore cancelled."
    exit 0
fi
echo ""

# Create emergency backup of current state
echo "Creating emergency backup of current state..."
EMERGENCY_BACKUP_DIR="./emergency-backup-$(date +%Y%m%d-%H%M%S)"
mkdir -p ${EMERGENCY_BACKUP_DIR}

oc exec -n ${NAMESPACE} ovsdbserver-nb-0 -c nb-ovsdb -- \
  ovsdb-client backup tcp:127.0.0.1:6641 > ${EMERGENCY_BACKUP_DIR}/ovn-nb-emergency.db || true

oc exec -n ${NAMESPACE} ovsdbserver-sb-0 -c sb-ovsdb -- \
  ovsdb-client backup tcp:127.0.0.1:6642 > ${EMERGENCY_BACKUP_DIR}/ovn-sb-emergency.db || true

echo "✓ Emergency backup saved to: ${EMERGENCY_BACKUP_DIR}"
echo ""

# Step 1: Scale down ovn-northd
echo "=========================================="
echo "Step 1: Scaling down ovn-northd"
echo "=========================================="
echo ""

# Check if ovn-northd deployment exists
if ! oc get deployment ovn-northd -n ${NAMESPACE} &>/dev/null; then
    echo "Warning: ovn-northd deployment not found. Skipping scale-down."
    echo ""
    NORTHD_REPLICAS=0
    SKIP_NORTHD=true
else
    NORTHD_REPLICAS=$(oc get deployment ovn-northd -n ${NAMESPACE} -o jsonpath='{.spec.replicas}')
    echo "Current ovn-northd replicas: ${NORTHD_REPLICAS}"

    oc scale deployment ovn-northd -n ${NAMESPACE} --replicas=0
    echo "✓ ovn-northd scaled to 0"
    echo ""

    echo "Waiting for ovn-northd pods to terminate..."
    oc wait --for=delete pod -l app=ovn-northd -n ${NAMESPACE} --timeout=60s 2>/dev/null || true
    echo "✓ ovn-northd pods terminated"
    echo ""
    SKIP_NORTHD=false
fi

# Step 2: Restore NB database
echo "=========================================="
echo "Step 2: Restoring OVN Northbound database"
echo "=========================================="
echo ""

cat ${NB_BACKUP} | oc exec -n ${NAMESPACE} -i ovsdbserver-nb-0 -c nb-ovsdb -- \
  ovsdb-client restore tcp:127.0.0.1:6641

echo "✓ OVN NB database restored"
echo ""

# Verify NB restore
echo "Verifying NB database..."
NB_STATUS=$(oc exec -n ${NAMESPACE} ovsdbserver-nb-0 -c nb-ovsdb -- \
  ovsdb-client list-dbs tcp:127.0.0.1:6641 2>/dev/null | grep -c "OVN_Northbound" || true)

if [ "${NB_STATUS}" -eq 0 ]; then
    echo "⚠️  Warning: Could not verify NB database"
else
    echo "✓ NB database verified"
fi
echo ""

# Step 3: Restore SB database
echo "=========================================="
echo "Step 3: Restoring OVN Southbound database"
echo "=========================================="
echo ""

cat ${SB_BACKUP} | oc exec -n ${NAMESPACE} -i ovsdbserver-sb-0 -c sb-ovsdb -- \
  ovsdb-client restore tcp:127.0.0.1:6642

echo "✓ OVN SB database restored"
echo ""

# Verify SB restore
echo "Verifying SB database..."
SB_STATUS=$(oc exec -n ${NAMESPACE} ovsdbserver-sb-0 -c sb-ovsdb -- \
  ovsdb-client list-dbs tcp:127.0.0.1:6642 2>/dev/null | grep -c "OVN_Southbound" || true)

if [ "${SB_STATUS}" -eq 0 ]; then
    echo "⚠️  Warning: Could not verify SB database"
else
    echo "✓ SB database verified"
fi
echo ""

# Step 4: Scale up ovn-northd
if [ "${SKIP_NORTHD}" != "true" ]; then
    echo "=========================================="
    echo "Step 4: Scaling up ovn-northd"
    echo "=========================================="
    echo ""

    oc scale deployment ovn-northd -n ${NAMESPACE} --replicas=${NORTHD_REPLICAS}
    echo "✓ ovn-northd scaled to ${NORTHD_REPLICAS}"
    echo ""

    echo "Waiting for ovn-northd to be ready..."
    oc wait --for=condition=Available deployment/ovn-northd -n ${NAMESPACE} --timeout=120s 2>/dev/null || echo "⚠️  Timeout waiting for ovn-northd"
    echo "✓ ovn-northd is ready"
    echo ""
fi

# Completion
echo "=========================================="
echo "OVN Restore Complete"
echo "=========================================="
echo ""
echo "Emergency backup saved to: ${EMERGENCY_BACKUP_DIR}"
echo ""
echo "Next steps:"
echo ""
echo "1. Verify OVN database contents:"
echo "   oc exec -n ${NAMESPACE} ovsdbserver-nb-0 -c nb-ovsdb -- ovn-nbctl show"
echo "   oc exec -n ${NAMESPACE} ovsdbserver-sb-0 -c sb-ovsdb -- ovn-sbctl show"
echo ""
echo "2. Check ovn-northd logs for any errors:"
if [ "${SKIP_NORTHD}" != "true" ]; then
    echo "   oc logs -n ${NAMESPACE} deployment/ovn-northd --tail=50"
else
    echo "   (ovn-northd deployment not found - skipped)"
fi
echo ""
echo "3. Verify OVN cluster status:"
echo "   oc exec -n ${NAMESPACE} ovsdbserver-nb-0 -c nb-ovsdb -- ovs-appctl -t /var/run/ovn/ovnnb_db.ctl cluster/status OVN_Northbound"
echo "   oc exec -n ${NAMESPACE} ovsdbserver-sb-0 -c sb-ovsdb -- ovs-appctl -t /var/run/ovn/ovnsb_db.ctl cluster/status OVN_Southbound"
echo ""
echo "4. Verify network connectivity for VMs/pods"
echo ""
echo "5. Check chassis connections (compute nodes):"
echo "   oc exec -n ${NAMESPACE} ovsdbserver-sb-0 -c sb-ovsdb -- ovn-sbctl chassis-list"
echo ""
