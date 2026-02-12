#!/bin/bash
set -e

# PVC Snapshot Backup Script
# This script creates CSI volume snapshots for all PVCs in a namespace

NAMESPACE="${NAMESPACE:-openstack}"
SNAPSHOT_CLASS="${SNAPSHOT_CLASS:-}"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
PVC_FILTER="${PVC_FILTER:-}"  # Optional: filter PVCs by name pattern (e.g., "glance")

echo "=========================================="
echo "PVC Snapshot Backup"
echo "=========================================="
echo "Namespace: ${NAMESPACE}"
echo "Timestamp: ${TIMESTAMP}"
if [ -n "${PVC_FILTER}" ]; then
    echo "PVC filter: ${PVC_FILTER}"
fi
echo ""

# Auto-detect snapshot class if not specified
if [ -z "${SNAPSHOT_CLASS}" ]; then
    echo "Detecting VolumeSnapshotClass..."
    SNAPSHOT_CLASS=$(oc get volumesnapshotclass -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)

    if [ -z "${SNAPSHOT_CLASS}" ]; then
        echo "Error: No VolumeSnapshotClass found in the cluster"
        echo ""
        echo "CSI volume snapshots are not available on this cluster."
        echo "Please check:"
        echo "1. Your storage class supports CSI snapshots"
        echo "2. VolumeSnapshotClass is configured"
        echo ""
        echo "To check:"
        echo "  oc get volumesnapshotclass"
        echo "  oc get csidriver"
        echo ""
        echo "Alternatively, use the tar-based backup method:"
        echo "  ./backup-pvc-tar.sh <pvc-name>"
        exit 1
    fi
fi

echo "✓ Using VolumeSnapshotClass: ${SNAPSHOT_CLASS}"
echo ""

# List PVCs to backup
echo "Finding PVCs in namespace ${NAMESPACE}..."

if [ -n "${PVC_FILTER}" ]; then
    PVCS=$(oc get pvc -n ${NAMESPACE} -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | grep "${PVC_FILTER}")
else
    PVCS=$(oc get pvc -n ${NAMESPACE} -o jsonpath='{.items[*].metadata.name}')
fi

if [ -z "${PVCS}" ]; then
    echo "No PVCs found in namespace ${NAMESPACE}"
    if [ -n "${PVC_FILTER}" ]; then
        echo "with filter: ${PVC_FILTER}"
    fi
    exit 0
fi

echo "✓ Found PVCs to backup:"
for pvc in ${PVCS}; do
    # Get PVC size for information
    SIZE=$(oc get pvc ${pvc} -n ${NAMESPACE} -o jsonpath='{.spec.resources.requests.storage}')
    echo "  - ${pvc} (${SIZE})"
done
echo ""

# Confirm backup
echo "This will create CSI volume snapshots for the above PVCs."
echo ""
read -p "Continue with snapshot creation? (yes/no): " CONFIRM

if [ "${CONFIRM}" != "yes" ]; then
    echo "Snapshot creation cancelled."
    exit 0
fi
echo ""

# Create snapshots
SNAPSHOT_COUNT=0
FAILED_COUNT=0

for pvc in ${PVCS}; do
    SNAPSHOT_NAME="${pvc}-snapshot-${TIMESTAMP}"
    echo "Creating snapshot for ${pvc}..."

    if cat <<EOF | oc apply -f -
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: ${SNAPSHOT_NAME}
  namespace: ${NAMESPACE}
  labels:
    backup-timestamp: "${TIMESTAMP}"
    source-pvc: "${pvc}"
spec:
  volumeSnapshotClassName: ${SNAPSHOT_CLASS}
  source:
    persistentVolumeClaimName: ${pvc}
EOF
    then
        echo "✓ Snapshot created: ${SNAPSHOT_NAME}"
        SNAPSHOT_COUNT=$((SNAPSHOT_COUNT + 1))
    else
        echo "✗ Failed to create snapshot for ${pvc}"
        FAILED_COUNT=$((FAILED_COUNT + 1))
    fi
    echo ""
done

# Wait for snapshots to be ready
echo "Waiting for snapshots to be ready..."
echo "This may take a few moments..."
sleep 5

# Check snapshot status
echo ""
echo "Snapshot Status:"
echo "----------------------------------------"
oc get volumesnapshot -n ${NAMESPACE} -l backup-timestamp=${TIMESTAMP}
echo ""

# Verify snapshots are ready
echo "Verifying snapshots are ready..."
for pvc in ${PVCS}; do
    SNAPSHOT_NAME="${pvc}-snapshot-${TIMESTAMP}"

    READY=$(oc get volumesnapshot ${SNAPSHOT_NAME} -n ${NAMESPACE} -o jsonpath='{.status.readyToUse}' 2>/dev/null || echo "false")

    if [ "${READY}" = "true" ]; then
        SIZE=$(oc get volumesnapshot ${SNAPSHOT_NAME} -n ${NAMESPACE} -o jsonpath='{.status.restoreSize}' 2>/dev/null || echo "unknown")
        echo "✓ ${SNAPSHOT_NAME} - Ready (${SIZE})"
    else
        echo "⚠️  ${SNAPSHOT_NAME} - Not ready yet (check status)"
    fi
done
echo ""

# Create metadata
METADATA_FILE="./backups/pvc-snapshots-${TIMESTAMP}-metadata.txt"
mkdir -p ./backups

cat > ${METADATA_FILE} <<EOF
PVC Snapshot Backup Metadata
=============================
Backup Date: $(date)
Backup Created By: $(oc whoami 2>/dev/null || echo "unknown")
Namespace: ${NAMESPACE}
Snapshot Class: ${SNAPSHOT_CLASS}

OpenShift Cluster:
- Console URL: $(oc whoami --show-console 2>/dev/null || echo "unknown")
- Server: $(oc whoami --show-server 2>/dev/null || echo "unknown")

Snapshots Created:
$(oc get volumesnapshot -n ${NAMESPACE} -l backup-timestamp=${TIMESTAMP} -o custom-columns=NAME:.metadata.name,PVC:.spec.source.persistentVolumeClaimName,READY:.status.readyToUse,SIZE:.status.restoreSize)

To restore a snapshot:
  oc apply -f - <<RESTORE
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: <new-pvc-name>
  namespace: ${NAMESPACE}
spec:
  dataSource:
    name: <snapshot-name>
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: <size>
RESTORE
EOF

echo "✓ Metadata saved to: ${METADATA_FILE}"
echo ""

echo "=========================================="
echo "PVC Snapshot Backup Complete"
echo "=========================================="
echo "Snapshots created: ${SNAPSHOT_COUNT}"
if [ ${FAILED_COUNT} -gt 0 ]; then
    echo "Failed snapshots: ${FAILED_COUNT}"
fi
echo ""
echo "To list all snapshots:"
echo "  oc get volumesnapshot -n ${NAMESPACE} -l backup-timestamp=${TIMESTAMP}"
echo ""
echo "To describe a specific snapshot:"
echo "  oc describe volumesnapshot <snapshot-name> -n ${NAMESPACE}"
echo ""
echo "To restore a snapshot, see: ${METADATA_FILE}"
echo ""
echo "⚠️  Note: Snapshots are stored in the same storage backend as the source PVCs."
echo "For disaster recovery, also create database dumps or tar backups stored off-cluster."
echo ""
