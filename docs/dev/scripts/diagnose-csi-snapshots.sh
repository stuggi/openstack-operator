#!/bin/bash
# CSI Snapshot Configuration Diagnostic Script
#
# This script checks all prerequisites and configuration for OADP CSI snapshots
#
# Usage:
#   ./diagnose-csi-snapshots.sh

set -e

# Configuration
OADP_NAMESPACE="${OADP_NAMESPACE:-openshift-adp}"
OPENSTACK_NAMESPACE="${OPENSTACK_NAMESPACE:-openstack}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Helper functions
print_header() {
    echo ""
    echo -e "${BLUE}=========================================${NC}"
    echo -e "${BLUE}$1${NC}"
    echo -e "${BLUE}=========================================${NC}"
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

print_info() {
    echo -e "  $1"
}

print_header "CSI Snapshot Configuration Diagnostic"
echo "OADP Namespace: $OADP_NAMESPACE"
echo "OpenStack Namespace: $OPENSTACK_NAMESPACE"

EXIT_CODE=0

# Check 1: VolumeSnapshotClass with velero label
print_header "Check 1: VolumeSnapshotClass Configuration"

VSC_COUNT=$(oc get volumesnapshotclass -l velero.io/csi-volumesnapshot-class=true --no-headers 2>/dev/null | wc -l)
echo "VolumeSnapshotClasses with velero label: $VSC_COUNT"

if [ "$VSC_COUNT" -eq 0 ]; then
    print_error "No VolumeSnapshotClass with velero.io/csi-volumesnapshot-class=true label found"
    print_info "Create one with:"
    print_info "  oc apply -f - <<EOF"
    print_info "  apiVersion: snapshot.storage.k8s.io/v1"
    print_info "  kind: VolumeSnapshotClass"
    print_info "  metadata:"
    print_info "    name: lvms-velero"
    print_info "    labels:"
    print_info "      velero.io/csi-volumesnapshot-class: \"true\""
    print_info "  driver: topolvm.io"
    print_info "  deletionPolicy: Retain"
    print_info "  EOF"
    EXIT_CODE=1
elif [ "$VSC_COUNT" -eq 1 ]; then
    VSC_NAME=$(oc get volumesnapshotclass -l velero.io/csi-volumesnapshot-class=true -o jsonpath='{.items[0].metadata.name}')
    VSC_DRIVER=$(oc get volumesnapshotclass -l velero.io/csi-volumesnapshot-class=true -o jsonpath='{.items[0].driver}')
    VSC_POLICY=$(oc get volumesnapshotclass -l velero.io/csi-volumesnapshot-class=true -o jsonpath='{.items[0].deletionPolicy}')
    print_success "Found VolumeSnapshotClass: $VSC_NAME"
    print_info "Driver: $VSC_DRIVER"
    print_info "Deletion Policy: $VSC_POLICY"

    if [ "$VSC_POLICY" != "Retain" ]; then
        print_warning "Deletion policy is '$VSC_POLICY', Red Hat recommends 'Retain' for backups"
    fi
else
    print_warning "Multiple VolumeSnapshotClasses ($VSC_COUNT) with velero label found"
    print_info "Only ONE per driver should have the label. List:"
    oc get volumesnapshotclass -l velero.io/csi-volumesnapshot-class=true -o custom-columns=NAME:.metadata.name,DRIVER:.driver,DELETIONPOLICY:.deletionPolicy
    print_info ""
    print_info "Remove label from unwanted classes:"
    print_info "  oc label volumesnapshotclass <name> velero.io/csi-volumesnapshot-class-"
fi

# Check 2: DataProtectionApplication - NO snapshotLocations
print_header "Check 2: DataProtectionApplication (DPA) Configuration"

if ! oc get dpa velero -n "$OADP_NAMESPACE" &>/dev/null; then
    print_error "DataProtectionApplication 'velero' not found in namespace $OADP_NAMESPACE"
    EXIT_CODE=1
else
    print_success "DataProtectionApplication 'velero' exists"

    # Check for snapshotLocations (should NOT exist for CSI)
    SNAPSHOT_LOCATIONS=$(oc get dpa velero -n "$OADP_NAMESPACE" -o jsonpath='{.spec.snapshotLocations}' 2>/dev/null)

    if [ -z "$SNAPSHOT_LOCATIONS" ] || [ "$SNAPSHOT_LOCATIONS" == "null" ]; then
        print_success "snapshotLocations is NOT configured (correct for CSI snapshots)"
    else
        print_error "snapshotLocations IS configured in DPA (this prevents CSI snapshots!)"
        print_info "Current value: $SNAPSHOT_LOCATIONS"
        print_info ""
        print_info "The snapshotLocations field forces OADP to use cloud provider snapshots"
        print_info "(AWS EBS, Azure Disk, GCP PD) instead of CSI snapshots."
        print_info ""
        print_info "To fix, remove snapshotLocations from DPA:"
        print_info "  oc patch dpa velero -n $OADP_NAMESPACE --type=json -p='[{\"op\": \"remove\", \"path\": \"/spec/snapshotLocations\"}]'"
        print_info ""
        print_info "Then restart velero:"
        print_info "  oc delete pod -n $OADP_NAMESPACE -l app.kubernetes.io/name=velero"
        EXIT_CODE=1
    fi

    # Check for CSI plugin in defaultPlugins (CRITICAL!)
    DEFAULT_PLUGINS=$(oc get dpa velero -n "$OADP_NAMESPACE" -o jsonpath='{.spec.configuration.velero.defaultPlugins}' 2>/dev/null)

    if echo "$DEFAULT_PLUGINS" | grep -q "csi"; then
        print_success "CSI plugin is enabled in defaultPlugins"
        print_info "Plugins: $DEFAULT_PLUGINS"
    else
        print_error "CSI plugin is NOT enabled in defaultPlugins (CRITICAL!)"
        print_info "Current plugins: $DEFAULT_PLUGINS"
        print_info ""
        print_info "The CSI plugin is REQUIRED for CSI volume snapshots."
        print_info "Without it, OADP will not create VolumeSnapshots even if"
        print_info "everything else is configured correctly."
        print_info ""
        print_info "To fix, add CSI plugin to defaultPlugins:"
        print_info "  oc patch dpa velero -n $OADP_NAMESPACE --type=json -p='[{\"op\": \"add\", \"path\": \"/spec/configuration/velero/defaultPlugins/-\", \"value\": \"csi\"}]'"
        print_info ""
        print_info "Then restart velero:"
        print_info "  oc delete pod -n $OADP_NAMESPACE -l app.kubernetes.io/name=velero"
        print_info ""
        print_info "After restart, verify CSI plugin loaded:"
        print_info "  oc logs -n $OADP_NAMESPACE -l app.kubernetes.io/name=velero | grep -i csi"
        EXIT_CODE=1
    fi

    # Check backupLocations exists
    BACKUP_LOCATIONS=$(oc get dpa velero -n "$OADP_NAMESPACE" -o jsonpath='{.spec.backupLocations}' 2>/dev/null)
    if [ -n "$BACKUP_LOCATIONS" ] && [ "$BACKUP_LOCATIONS" != "null" ]; then
        print_success "backupLocations is configured (for S3 backup metadata storage)"
    else
        print_warning "backupLocations is not configured"
    fi
fi

# Check 3: Velero pod status
print_header "Check 3: Velero Pod Status"

VELERO_POD=$(oc get pod -n "$OADP_NAMESPACE" -l app.kubernetes.io/name=velero -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
if [ -z "$VELERO_POD" ]; then
    print_error "Velero pod not found"
    EXIT_CODE=1
else
    VELERO_STATUS=$(oc get pod "$VELERO_POD" -n "$OADP_NAMESPACE" -o jsonpath='{.status.phase}')
    VELERO_READY=$(oc get pod "$VELERO_POD" -n "$OADP_NAMESPACE" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')
    VELERO_AGE=$(oc get pod "$VELERO_POD" -n "$OADP_NAMESPACE" -o jsonpath='{.metadata.creationTimestamp}')

    if [ "$VELERO_STATUS" == "Running" ] && [ "$VELERO_READY" == "True" ]; then
        print_success "Velero pod is running and ready"
        print_info "Pod: $VELERO_POD"
        print_info "Created: $VELERO_AGE"
    else
        print_error "Velero pod is not ready (Status: $VELERO_STATUS, Ready: $VELERO_READY)"
        EXIT_CODE=1
    fi

    # Check for CSI plugin in logs
    print_info ""
    print_info "Checking Velero logs for CSI plugin..."
    if oc logs "$VELERO_POD" -n "$OADP_NAMESPACE" 2>/dev/null | grep -q "csi.*plugin"; then
        print_success "CSI plugin references found in Velero logs"
    else
        print_warning "No CSI plugin references found in Velero logs"
    fi
fi

# Check 4: BackupStorageLocation
print_header "Check 4: BackupStorageLocation Status"

BSL_COUNT=$(oc get backupstoragelocation -n "$OADP_NAMESPACE" --no-headers 2>/dev/null | wc -l)
if [ "$BSL_COUNT" -eq 0 ]; then
    print_error "No BackupStorageLocation found"
    EXIT_CODE=1
else
    print_info "Found $BSL_COUNT BackupStorageLocation(s)"
    oc get backupstoragelocation -n "$OADP_NAMESPACE" -o custom-columns=NAME:.metadata.name,PHASE:.status.phase,DEFAULT:.spec.default

    # Check if any is Available
    AVAILABLE_COUNT=$(oc get backupstoragelocation -n "$OADP_NAMESPACE" -o jsonpath='{.items[?(@.status.phase=="Available")].metadata.name}' 2>/dev/null | wc -w)
    if [ "$AVAILABLE_COUNT" -gt 0 ]; then
        print_success "At least one BackupStorageLocation is Available"
    else
        print_error "No BackupStorageLocation is in Available phase"
        EXIT_CODE=1
    fi
fi

# Check 5: PVCs labeled for backup
print_header "Check 5: PVCs Labeled for Backup"

PVC_COUNT=$(oc get pvc -n "$OPENSTACK_NAMESPACE" -l openstack.org/backup=true --no-headers 2>/dev/null | wc -l)
echo "PVCs with openstack.org/backup=true label: $PVC_COUNT"

if [ "$PVC_COUNT" -eq 0 ]; then
    print_warning "No PVCs labeled for backup"
    print_info "To label PVCs, run:"
    print_info "  oc label pvc <pvc-name> -n $OPENSTACK_NAMESPACE openstack.org/backup=true"
else
    print_success "Found $PVC_COUNT PVC(s) labeled for backup"
    echo ""
    oc get pvc -n "$OPENSTACK_NAMESPACE" -l openstack.org/backup=true -o custom-columns=NAME:.metadata.name,SIZE:.spec.resources.requests.storage,STORAGECLASS:.spec.storageClassName

    # Check if PVC StorageClass has matching CSI driver
    if [ "$VSC_COUNT" -gt 0 ]; then
        print_info ""
        print_info "Checking if PVC StorageClasses match VolumeSnapshotClass driver..."

        PVC_SC=$(oc get pvc -n "$OPENSTACK_NAMESPACE" -l openstack.org/backup=true -o jsonpath='{.items[0].spec.storageClassName}' 2>/dev/null)
        if [ -n "$PVC_SC" ]; then
            PVC_PROVISIONER=$(oc get storageclass "$PVC_SC" -o jsonpath='{.provisioner}' 2>/dev/null)
            print_info "PVC StorageClass provisioner: $PVC_PROVISIONER"
            print_info "VolumeSnapshotClass driver: $VSC_DRIVER"

            if [ "$PVC_PROVISIONER" == "$VSC_DRIVER" ]; then
                print_success "Drivers match!"
            else
                print_error "Driver mismatch! PVCs use '$PVC_PROVISIONER' but VolumeSnapshotClass uses '$VSC_DRIVER'"
                EXIT_CODE=1
            fi
        fi
    fi
fi

# Check 6: Recent backup status
print_header "Check 6: Recent Backup Status"

LATEST_BACKUP=$(oc get backup -n "$OADP_NAMESPACE" --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1].metadata.name}' 2>/dev/null)
if [ -z "$LATEST_BACKUP" ]; then
    print_info "No backups found yet"
else
    print_info "Latest backup: $LATEST_BACKUP"

    BACKUP_PHASE=$(oc get backup "$LATEST_BACKUP" -n "$OADP_NAMESPACE" -o jsonpath='{.status.phase}')
    CSI_ATTEMPTED=$(oc get backup "$LATEST_BACKUP" -n "$OADP_NAMESPACE" -o jsonpath='{.status.csiVolumeSnapshotsAttempted}')
    CSI_COMPLETED=$(oc get backup "$LATEST_BACKUP" -n "$OADP_NAMESPACE" -o jsonpath='{.status.csiVolumeSnapshotsCompleted}')
    BACKUP_VSL=$(oc get backup "$LATEST_BACKUP" -n "$OADP_NAMESPACE" -o jsonpath='{.spec.volumeSnapshotLocations}')

    print_info "Phase: ${BACKUP_PHASE:-unknown}"
    print_info "CSI Snapshots Attempted: ${CSI_ATTEMPTED:-0}"
    print_info "CSI Snapshots Completed: ${CSI_COMPLETED:-0}"
    print_info "Backup spec volumeSnapshotLocations: ${BACKUP_VSL:-empty}"

    if [ -n "$BACKUP_VSL" ] && [ "$BACKUP_VSL" != "[]" ] && [ "$BACKUP_VSL" != "null" ]; then
        print_error "Latest backup has volumeSnapshotLocations set in spec!"
        print_info "This means the DPA is injecting it. Check DPA configuration above."
        EXIT_CODE=1
    fi

    if [ "${CSI_ATTEMPTED:-0}" -eq 0 ]; then
        print_error "No CSI snapshots were attempted in latest backup"
        EXIT_CODE=1
    elif [ "${CSI_ATTEMPTED}" -eq "${CSI_COMPLETED}" ]; then
        print_success "All CSI snapshots completed successfully"
    else
        print_warning "Not all CSI snapshots completed (${CSI_COMPLETED}/${CSI_ATTEMPTED})"
    fi
fi

# Summary
print_header "Summary"

if [ "$EXIT_CODE" -eq 0 ]; then
    print_success "All checks passed! CSI snapshots should work."
    echo ""
    print_info "To create a test backup, run:"
    print_info "  cat <<EOF | oc apply -f -"
    print_info "  apiVersion: velero.io/v1"
    print_info "  kind: Backup"
    print_info "  metadata:"
    print_info "    name: test-csi-\$(date +%Y%m%d-%H%M%S)"
    print_info "    namespace: $OADP_NAMESPACE"
    print_info "  spec:"
    print_info "    includedNamespaces:"
    print_info "    - $OPENSTACK_NAMESPACE"
    print_info "    labelSelector:"
    print_info "      matchLabels:"
    print_info "        openstack.org/backup: \"true\""
    print_info "    snapshotVolumes: true"
    print_info "    defaultVolumesToFsBackup: false"
    print_info "    storageLocation: velero-1"
    print_info "    ttl: 720h"
    print_info "  EOF"
else
    print_error "Issues detected - review the errors above and fix them"
    echo ""
    print_info "Common fixes:"
    print_info "1. Add CSI plugin to DPA defaultPlugins (MOST COMMON!):"
    print_info "   oc patch dpa velero -n $OADP_NAMESPACE --type=json -p='[{\"op\": \"add\", \"path\": \"/spec/configuration/velero/defaultPlugins/-\", \"value\": \"csi\"}]'"
    print_info ""
    print_info "2. Remove snapshotLocations from DPA:"
    print_info "   oc patch dpa velero -n $OADP_NAMESPACE --type=json -p='[{\"op\": \"remove\", \"path\": \"/spec/snapshotLocations\"}]'"
    print_info ""
    print_info "3. Restart Velero after DPA changes:"
    print_info "   oc delete pod -n $OADP_NAMESPACE -l app.kubernetes.io/name=velero"
    print_info ""
    print_info "4. Create VolumeSnapshotClass with velero label (see Check 1 above)"
fi

exit $EXIT_CODE
