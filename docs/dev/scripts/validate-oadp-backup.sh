#!/bin/bash
# OADP Backup Validation Script
#
# This script validates that an OADP backup completed successfully with CSI snapshots.
#
# Usage:
#   ./validate-oadp-backup.sh [backup-name]
#   ./validate-oadp-backup.sh                    # Uses latest backup
#   ./validate-oadp-backup.sh openstack-volumes-20260303-093007

set -e

# Configuration
OADP_NAMESPACE="${OADP_NAMESPACE:-openshift-adp}"
OPENSTACK_NAMESPACE="${OPENSTACK_NAMESPACE:-openstack}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Helper functions
print_header() {
    echo ""
    echo "========================================="
    echo "$1"
    echo "========================================="
}

print_success() {
    echo -e "${GREEN}✓${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}⚠${NC}  $1"
}

print_error() {
    echo -e "${RED}✗${NC} $1"
}

# Get backup name
if [ -z "$1" ]; then
    echo "No backup name provided, using latest backup..."
    BACKUP_NAME=$(oc get backup -n "$OADP_NAMESPACE" --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1].metadata.name}' 2>/dev/null)
    if [ -z "$BACKUP_NAME" ]; then
        print_error "No backups found in namespace $OADP_NAMESPACE"
        exit 1
    fi
    echo "Using backup: $BACKUP_NAME"
else
    BACKUP_NAME="$1"
fi

# Verify backup exists
if ! oc get backup "$BACKUP_NAME" -n "$OADP_NAMESPACE" &>/dev/null; then
    print_error "Backup $BACKUP_NAME not found in namespace $OADP_NAMESPACE"
    exit 1
fi

print_header "OADP Backup Validation"
echo "Backup Name: $BACKUP_NAME"
echo "OADP Namespace: $OADP_NAMESPACE"
echo "OpenStack Namespace: $OPENSTACK_NAMESPACE"

# Get backup status
print_header "Backup Status"

PHASE=$(oc get backup "$BACKUP_NAME" -n "$OADP_NAMESPACE" -o jsonpath='{.status.phase}')
WARNINGS=$(oc get backup "$BACKUP_NAME" -n "$OADP_NAMESPACE" -o jsonpath='{.status.warnings}')
ERRORS=$(oc get backup "$BACKUP_NAME" -n "$OADP_NAMESPACE" -o jsonpath='{.status.errors}')
ITEMS_BACKED_UP=$(oc get backup "$BACKUP_NAME" -n "$OADP_NAMESPACE" -o jsonpath='{.status.progress.itemsBackedUp}')
TOTAL_ITEMS=$(oc get backup "$BACKUP_NAME" -n "$OADP_NAMESPACE" -o jsonpath='{.status.progress.totalItems}')

echo "Phase: $PHASE"
echo "Warnings: ${WARNINGS:-0}"
echo "Errors: ${ERRORS:-0}"
echo "Items Backed Up: ${ITEMS_BACKED_UP:-0} / ${TOTAL_ITEMS:-0}"

# Check phase
if [ "$PHASE" == "Completed" ]; then
    print_success "Backup completed successfully"
elif [ "$PHASE" == "InProgress" ]; then
    print_warning "Backup is still in progress"
elif [ "$PHASE" == "PartiallyFailed" ]; then
    print_warning "Backup partially failed"
else
    print_error "Backup failed with phase: $PHASE"
fi

# Check for warnings and errors
if [ "${WARNINGS:-0}" -gt 0 ]; then
    print_warning "$WARNINGS warning(s) detected"
fi

if [ "${ERRORS:-0}" -gt 0 ]; then
    print_error "$ERRORS error(s) detected"
fi

# Get CSI snapshot statistics
print_header "CSI Snapshot Statistics"

CSI_ATTEMPTED=$(oc get backup "$BACKUP_NAME" -n "$OADP_NAMESPACE" -o jsonpath='{.status.csiVolumeSnapshotsAttempted}')
CSI_COMPLETED=$(oc get backup "$BACKUP_NAME" -n "$OADP_NAMESPACE" -o jsonpath='{.status.csiVolumeSnapshotsCompleted}')

echo "CSI Snapshots Attempted: ${CSI_ATTEMPTED:-0}"
echo "CSI Snapshots Completed: ${CSI_COMPLETED:-0}"

if [ "${CSI_ATTEMPTED:-0}" -eq 0 ]; then
    print_error "No CSI snapshots attempted - check VolumeSnapshotClass label and Backup CR spec"
elif [ "${CSI_ATTEMPTED}" -eq "${CSI_COMPLETED}" ]; then
    print_success "All CSI snapshots completed successfully"
else
    print_warning "Not all CSI snapshots completed (${CSI_COMPLETED}/${CSI_ATTEMPTED})"
fi

# Check VolumeSnapshots
print_header "VolumeSnapshots Created"

VOLUMESNAPSHOT_COUNT=$(oc get volumesnapshot -n "$OPENSTACK_NAMESPACE" -l velero.io/backup-name="$BACKUP_NAME" --no-headers 2>/dev/null | wc -l)
echo "VolumeSnapshots found: $VOLUMESNAPSHOT_COUNT"

if [ "$VOLUMESNAPSHOT_COUNT" -gt 0 ]; then
    print_success "VolumeSnapshots created"
    echo ""
    oc get volumesnapshot -n "$OPENSTACK_NAMESPACE" -l velero.io/backup-name="$BACKUP_NAME" \
        -o custom-columns=NAME:.metadata.name,PVC:.spec.source.persistentVolumeClaimName,READY:.status.readyToUse,CLASS:.spec.volumeSnapshotClassName 2>/dev/null || true
else
    print_error "No VolumeSnapshots found for this backup"
fi

# Check expected PVCs
print_header "Expected PVCs (labeled for backup)"

PVC_COUNT=$(oc get pvc -n "$OPENSTACK_NAMESPACE" -l openstack.org/backup=true --no-headers 2>/dev/null | wc -l)
echo "PVCs with backup label: $PVC_COUNT"

if [ "$PVC_COUNT" -gt 0 ]; then
    echo ""
    oc get pvc -n "$OPENSTACK_NAMESPACE" -l openstack.org/backup=true \
        -o custom-columns=NAME:.metadata.name,SIZE:.spec.resources.requests.storage,STORAGECLASS:.spec.storageClassName 2>/dev/null || true
else
    print_warning "No PVCs labeled with openstack.org/backup=true"
fi

# Check VolumeSnapshotClass
print_header "VolumeSnapshotClass Configuration"

VSC_COUNT=$(oc get volumesnapshotclass -l velero.io/csi-volumesnapshot-class=true --no-headers 2>/dev/null | wc -l)
echo "VolumeSnapshotClasses with velero label: $VSC_COUNT"

if [ "$VSC_COUNT" -gt 0 ]; then
    echo ""
    oc get volumesnapshotclass -l velero.io/csi-volumesnapshot-class=true \
        -o custom-columns=NAME:.metadata.name,DRIVER:.driver,DELETIONPOLICY:.deletionPolicy 2>/dev/null || true

    if [ "$VSC_COUNT" -gt 1 ]; then
        print_warning "Multiple VolumeSnapshotClasses with velero label - only one per driver should have it"
    else
        print_success "VolumeSnapshotClass configured correctly"
    fi
else
    print_error "No VolumeSnapshotClass with velero.io/csi-volumesnapshot-class=true label found"
fi

# Check backup spec for CSI configuration
print_header "Backup Spec Validation"

SNAPSHOT_VOLUMES=$(oc get backup "$BACKUP_NAME" -n "$OADP_NAMESPACE" -o jsonpath='{.spec.snapshotVolumes}')
DEFAULT_FS_BACKUP=$(oc get backup "$BACKUP_NAME" -n "$OADP_NAMESPACE" -o jsonpath='{.spec.defaultVolumesToFsBackup}')
VSL_LOCATIONS=$(oc get backup "$BACKUP_NAME" -n "$OADP_NAMESPACE" -o jsonpath='{.spec.volumeSnapshotLocations}')

echo "snapshotVolumes: ${SNAPSHOT_VOLUMES:-not set}"
echo "defaultVolumesToFsBackup: ${DEFAULT_FS_BACKUP:-not set}"
echo "volumeSnapshotLocations: ${VSL_LOCATIONS:-not set}"

CSI_SPEC_OK=true
if [ "$SNAPSHOT_VOLUMES" != "true" ]; then
    print_error "snapshotVolumes should be 'true' for CSI snapshots"
    CSI_SPEC_OK=false
fi

if [ "$DEFAULT_FS_BACKUP" != "false" ]; then
    print_warning "defaultVolumesToFsBackup should be 'false' for CSI snapshots"
    CSI_SPEC_OK=false
fi

if [ -n "$VSL_LOCATIONS" ] && [ "$VSL_LOCATIONS" != "[]" ]; then
    print_error "volumeSnapshotLocations should be '[]' to force CSI snapshots (got: $VSL_LOCATIONS)"
    CSI_SPEC_OK=false
fi

if [ "$CSI_SPEC_OK" = true ]; then
    print_success "Backup spec configured correctly for CSI snapshots"
fi

# Show backup warnings if any
if [ "${WARNINGS:-0}" -gt 0 ]; then
    print_header "Backup Warnings Details"
    oc describe backup "$BACKUP_NAME" -n "$OADP_NAMESPACE" | grep -A 20 "Events:" || echo "No event details available"
fi

# Overall assessment
print_header "Overall Assessment"

EXIT_CODE=0

if [ "$PHASE" == "Completed" ] && \
   [ "${ERRORS:-0}" -eq 0 ] && \
   [ "${CSI_ATTEMPTED:-0}" -gt 0 ] && \
   [ "${CSI_ATTEMPTED}" -eq "${CSI_COMPLETED}" ] && \
   [ "$VOLUMESNAPSHOT_COUNT" -gt 0 ]; then
    print_success "Backup completed successfully with CSI snapshots!"
    if [ "${WARNINGS:-0}" -gt 0 ]; then
        print_warning "However, there were $WARNINGS warning(s) - review above for details"
    fi
elif [ "$PHASE" == "InProgress" ]; then
    print_warning "Backup is still in progress - run this script again when it completes"
    EXIT_CODE=2
else
    print_error "Issues detected with backup - review details above"
    EXIT_CODE=1
fi

echo ""
echo "For more details, run:"
echo "  oc describe backup $BACKUP_NAME -n $OADP_NAMESPACE"
echo "  oc logs -n $OADP_NAMESPACE deployment/velero | grep $BACKUP_NAME"

exit $EXIT_CODE
