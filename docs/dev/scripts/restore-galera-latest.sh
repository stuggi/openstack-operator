#!/bin/bash
# Helper Script: Restore Galera Database from Latest Backup
#
# This script automatically finds the latest backup dump file in a GaleraRestore
# pod and executes the restore. Useful for automated testing and CI/CD pipelines.
#
# Usage:
#   ./restore-galera-latest.sh <restore-name> [namespace]
#   ./restore-galera-latest.sh openstackrestore
#   ./restore-galera-latest.sh openstackrestorecell1 openstack

set -e

# Configuration
RESTORE_NAME="${1}"
OPENSTACK_NAMESPACE="${2:-openstack}"
DELETE_CR="${3:-yes}"  # Delete GaleraRestore CR after successful restore (yes/no)

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
if [ -z "$RESTORE_NAME" ]; then
    print_error "Restore name is required"
    echo ""
    echo "Usage:"
    echo "  $0 <restore-name> [namespace] [delete-cr]"
    echo ""
    echo "Arguments:"
    echo "  restore-name  - Name of the GaleraRestore CR (e.g., openstackrestore)"
    echo "  namespace     - OpenStack namespace (default: openstack)"
    echo "  delete-cr     - Delete GaleraRestore CR after successful restore (default: yes)"
    echo ""
    echo "Examples:"
    echo "  $0 openstackrestore"
    echo "  $0 openstackrestorecell1 openstack"
    echo "  $0 openstackrestore openstack no  # Keep GaleraRestore CR"
    exit 1
fi

POD_NAME="openstack-restore-${RESTORE_NAME}"

print_header "Restore Galera Database from Latest Backup"
echo "Restore Name: $RESTORE_NAME"
echo "Pod Name: $POD_NAME"
echo "Namespace: $OPENSTACK_NAMESPACE"

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

# List backup files in the pod
print_header "Finding Latest Backup"
print_info "Listing backup files in /backup/data/..."

BACKUP_FILES=$(oc exec -n "$OPENSTACK_NAMESPACE" "$POD_NAME" -- ls -1 /backup/data/*_backup_*.sql.gz 2>/dev/null | grep -v grants || true)

if [ -z "$BACKUP_FILES" ]; then
    print_error "No backup files found in /backup/data/"
    print_info "Expected files matching pattern: *_backup_YYYY-MM-DD_HH-MM-SS.sql.gz"
    exit 1
fi

# Count backup files
BACKUP_COUNT=$(echo "$BACKUP_FILES" | wc -l)
print_success "Found $BACKUP_COUNT backup file(s)"

# Get latest backup (last in sorted list)
LATEST_BACKUP=$(echo "$BACKUP_FILES" | sort | tail -1)
print_info "Latest backup: $LATEST_BACKUP"

# Extract timestamp from filename
# Pattern: openstack-cell1_backup_2026-03-03_15-32-13.sql.gz
# Extract: 2026-03-03_15-32-13
TIMESTAMP=$(basename "$LATEST_BACKUP" | sed -E 's/.*_backup_(.*)\.sql\.gz/\1/')

if [ -z "$TIMESTAMP" ]; then
    print_error "Could not extract timestamp from filename: $LATEST_BACKUP"
    exit 1
fi

print_success "Extracted timestamp: $TIMESTAMP"

# Construct restore pattern
# The restore script expects: /backup/data/*_YYYY-MM-DD_HH-MM-SS.sql.gz
# This pattern matches both the backup file and the grants file
RESTORE_PATTERN="/backup/data/*_${TIMESTAMP}.sql.gz"

print_info "Restore pattern: $RESTORE_PATTERN"

# Verify both backup and grants files exist
echo ""
print_info "Verifying backup files exist..."
MATCHED_FILES=$(oc exec -n "$OPENSTACK_NAMESPACE" "$POD_NAME" -- ls -1 "$RESTORE_PATTERN" 2>/dev/null || true)

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
print_header "Executing Database Restore"
echo "This will restore the database from the latest backup."
echo "Pattern: $RESTORE_PATTERN"
echo ""

# Run restore command
print_info "Running restore_galera script..."
echo ""

oc exec -n "$OPENSTACK_NAMESPACE" "$POD_NAME" -- \
  /var/lib/backup-scripts/restore_galera --yes "$RESTORE_PATTERN"

RESTORE_EXIT_CODE=$?

echo ""

if [ $RESTORE_EXIT_CODE -eq 0 ]; then
    print_header "Restore Completed Successfully"
    print_success "Database restored from: $LATEST_BACKUP"
    print_success "Timestamp: $TIMESTAMP"

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
