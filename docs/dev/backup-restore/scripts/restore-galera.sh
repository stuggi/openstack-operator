#!/bin/bash
# Helper Script: Restore Galera Database from Backup
#
# This script restores a Galera database from a backup dump file in a
# GaleraRestore pod using the specified backup timestamp.
#
# Usage:
#   ./restore-galera.sh <restore-name> <backup-timestamp> [namespace]
#   ./restore-galera.sh openstackrestore 20260311-081234
#   ./restore-galera.sh openstackrestorecell1 20260311-081234 openstack
#
# Environment variables:
#   RESTORE_CONTENT - What to restore: "all" (default), "data", or "grants"
#     all    - Restore both database data and user grants
#     data   - Restore database data only (operators recreate users/grants)
#     grants - Restore user grants only

set -e

# Configuration
RESTORE_NAME="${1}"
BACKUP_TIMESTAMP="${2}"
OPENSTACK_NAMESPACE="${3:-openstack}"
DELETE_CR="${4:-yes}"  # Delete GaleraRestore CR after successful restore (yes/no)
RESTORE_CONTENT="${RESTORE_CONTENT:-all}"

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
    echo "  $1"
}

# Validate arguments
if [ -z "$RESTORE_NAME" ] || [ -z "$BACKUP_TIMESTAMP" ]; then
    print_error "Restore name and backup timestamp are required"
    echo ""
    echo "Usage:"
    echo "  $0 <restore-name> <backup-timestamp> [namespace] [delete-cr]"
    echo ""
    echo "Arguments:"
    echo "  restore-name      - Name of the GaleraRestore CR (e.g., openstackrestore)"
    echo "  backup-timestamp  - Timestamp of the backup (e.g., 20260311-081234)"
    echo "  namespace         - OpenStack namespace (default: openstack)"
    echo "  delete-cr         - Delete GaleraRestore CR after successful restore (default: yes)"
    echo ""
    echo "Environment variables:"
    echo "  RESTORE_CONTENT   - What to restore: all (default), data, or grants"
    echo ""
    echo "Examples:"
    echo "  $0 openstackrestore 20260311-081234"
    echo "  $0 openstackrestorecell1 20260311-081234 openstack"
    echo "  RESTORE_CONTENT=data $0 openstackrestore 20260311-081234"
    exit 1
fi

print_header "Restore Galera Database from Latest Backup"
echo "GaleraRestore CR: $RESTORE_NAME"
echo "Namespace: $OPENSTACK_NAMESPACE"

# Get backupSource from GaleraRestore CR spec
print_info "Reading GaleraRestore CR to get backupSource..."
BACKUP_SOURCE=$(oc get galerarestore "$RESTORE_NAME" -n "$OPENSTACK_NAMESPACE" \
  -o jsonpath='{.spec.backupSource}' 2>/dev/null || echo "")

if [ -z "$BACKUP_SOURCE" ]; then
    print_error "Could not get backupSource from GaleraRestore: $RESTORE_NAME"
    print_info "Make sure the GaleraRestore CR exists"
    exit 1
fi

print_info "Backup source: $BACKUP_SOURCE"

# Construct pod name: <backupSource>-restore-<galerarestore-name>
POD_NAME="${BACKUP_SOURCE}-restore-${RESTORE_NAME}"
print_info "Expected pod name: $POD_NAME"

# Check if pod exists
print_info "Checking if restore pod exists..."
if ! oc get pod "$POD_NAME" -n "$OPENSTACK_NAMESPACE" &>/dev/null; then
    print_error "Restore pod not found: $POD_NAME"
    print_info "Make sure the GaleraRestore CR has been created and the pod is running"
    exit 1
fi

print_success "Restore pod found: $POD_NAME"

# Check pod status
POD_PHASE=$(oc get pod "$POD_NAME" -n "$OPENSTACK_NAMESPACE" -o jsonpath='{.status.phase}')
if [ "$POD_PHASE" != "Running" ]; then
    print_error "Restore pod is not running (phase: $POD_PHASE)"
    print_info "Wait for the pod to be in Running state"
    exit 1
fi

print_success "Restore pod is running"

# Build file pattern from timestamp
print_header "Using Backup Timestamp"
TIMESTAMP="$BACKUP_TIMESTAMP"
print_success "Timestamp: $TIMESTAMP"
print_info "Restore content: $RESTORE_CONTENT"

RESTORE_PATTERN="/backup/data/*_${TIMESTAMP}.sql.gz"
print_info "Restore pattern: $RESTORE_PATTERN"

# Verify backup files exist
echo ""
print_info "Verifying backup files exist..."
MATCHED_FILES=$(oc exec -n "$OPENSTACK_NAMESPACE" "$POD_NAME" -- sh -c "ls -1 $RESTORE_PATTERN 2>/dev/null" || true)

if [ -z "$MATCHED_FILES" ]; then
    print_error "No files match pattern: $RESTORE_PATTERN"
    exit 1
fi

FILE_COUNT=$(echo "$MATCHED_FILES" | wc -l)
print_success "Found $FILE_COUNT file(s) matching pattern:"
echo "$MATCHED_FILES" | while read -r file; do
    print_info "  - $(basename "$file")"
done

# Execute restore
print_header "Executing Database Restore ($RESTORE_CONTENT)"
print_info "Running restore_galera script..."
echo ""

oc exec -n "$OPENSTACK_NAMESPACE" "$POD_NAME" -- \
  /var/lib/backup-scripts/restore_galera --yes --content "$RESTORE_CONTENT" $RESTORE_PATTERN

RESTORE_EXIT_CODE=$?

echo ""

if [ $RESTORE_EXIT_CODE -eq 0 ]; then
    print_header "Restore Completed Successfully"
    print_success "Database restored with timestamp: $TIMESTAMP"

    # Clean up GaleraRestore CR if requested
    if [ "$DELETE_CR" == "yes" ]; then
        echo ""
        print_info "Cleaning up GaleraRestore CR..."
        if oc delete galerarestore "$RESTORE_NAME" -n "$OPENSTACK_NAMESPACE" --ignore-not-found=true; then
            print_success "GaleraRestore CR deleted: $RESTORE_NAME"
            print_info "Restore pod will terminate"
        else
            print_warning "Failed to delete GaleraRestore CR: $RESTORE_NAME"
        fi
    else
        echo ""
        print_info "GaleraRestore CR preserved (delete-cr=no)"
        print_info "To delete manually:"
        print_info "  oc delete galerarestore $RESTORE_NAME -n $OPENSTACK_NAMESPACE"
    fi
else
    print_header "Restore Failed"
    print_error "restore_galera exited with code: $RESTORE_EXIT_CODE"
    print_info "Check the pod logs for details:"
    print_info "  oc logs -n $OPENSTACK_NAMESPACE $POD_NAME"
    exit 1
fi
