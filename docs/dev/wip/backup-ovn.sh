#!/bin/bash
set -e

# OVN Database Backup Script
# This script backs up both OVN Northbound and Southbound databases

NAMESPACE="${NAMESPACE:-openstack}"
BACKUP_DIR="${BACKUP_DIR:-./backups}"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

echo "=========================================="
echo "OVN Database Backup"
echo "=========================================="
echo "Namespace: ${NAMESPACE}"
echo "Backup directory: ${BACKUP_DIR}"
echo "Timestamp: ${TIMESTAMP}"
echo ""

# Create backup directory
mkdir -p ${BACKUP_DIR}

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

# Backup OVN Northbound Database
echo "Backing up OVN Northbound database..."
NB_BACKUP="${BACKUP_DIR}/ovn-nb-backup-${TIMESTAMP}.db"
oc exec -n ${NAMESPACE} ovsdbserver-nb-0 -c nb-ovsdb -- \
  ovsdb-client backup tcp:127.0.0.1:6641 > ${NB_BACKUP}

if [ ! -s ${NB_BACKUP} ]; then
    echo "Error: NB backup file is empty!"
    exit 1
fi

echo "✓ OVN NB backup saved to: ${NB_BACKUP}"
echo "  Size: $(du -h ${NB_BACKUP} | cut -f1)"
echo ""

# Backup OVN Southbound Database
echo "Backing up OVN Southbound database..."
SB_BACKUP="${BACKUP_DIR}/ovn-sb-backup-${TIMESTAMP}.db"
oc exec -n ${NAMESPACE} ovsdbserver-sb-0 -c sb-ovsdb -- \
  ovsdb-client backup tcp:127.0.0.1:6642 > ${SB_BACKUP}

if [ ! -s ${SB_BACKUP} ]; then
    echo "Error: SB backup file is empty!"
    exit 1
fi

echo "✓ OVN SB backup saved to: ${SB_BACKUP}"
echo "  Size: $(du -h ${SB_BACKUP} | cut -f1)"
echo ""

# Create archive
echo "Creating backup archive..."
ARCHIVE="${BACKUP_DIR}/ovn-backup-${TIMESTAMP}.tar.gz"
tar -czf ${ARCHIVE} -C ${BACKUP_DIR} \
  ovn-nb-backup-${TIMESTAMP}.db \
  ovn-sb-backup-${TIMESTAMP}.db

echo "✓ Archive created: ${ARCHIVE}"
echo "  Size: $(du -h ${ARCHIVE} | cut -f1)"
echo ""

# Create metadata file
cat > ${BACKUP_DIR}/ovn-backup-${TIMESTAMP}-metadata.txt <<EOF
OVN Database Backup Metadata
============================
Backup Date: $(date)
Backup Created By: $(oc whoami)
Namespace: ${NAMESPACE}

OpenShift Cluster:
- Console URL: $(oc whoami --show-console)
- Server: $(oc whoami --show-server)

OVN Database Pods:
$(oc get pods -n ${NAMESPACE} | grep ovsdbserver)

OVN NB Database Info:
$(oc exec -n ${NAMESPACE} ovsdbserver-nb-0 -c nb-ovsdb -- ovn-nbctl --db=tcp:127.0.0.1:6641 show | head -20)

Files in this backup:
- ovn-nb-backup-${TIMESTAMP}.db - OVN Northbound database
- ovn-sb-backup-${TIMESTAMP}.db - OVN Southbound database
EOF

echo "✓ Metadata saved to: ${BACKUP_DIR}/ovn-backup-${TIMESTAMP}-metadata.txt"
echo ""

echo "=========================================="
echo "OVN Backup Complete"
echo "=========================================="
echo "Archive: ${ARCHIVE}"
echo "Size: $(du -h ${ARCHIVE} | cut -f1)"
echo ""
echo "Files in backup:"
echo "  - ovn-nb-backup-${TIMESTAMP}.db"
echo "  - ovn-sb-backup-${TIMESTAMP}.db"
echo ""
echo "Next steps:"
echo "1. Store backup archive securely:"
echo "   scp ${ARCHIVE} backup-server:/backups/"
echo ""
echo "2. Verify backup integrity:"
echo "   tar -tzf ${ARCHIVE}"
echo ""
echo "3. Test restore on a test cluster before relying on this backup"
echo ""
