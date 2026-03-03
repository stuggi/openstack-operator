#!/bin/bash
# Helper Script: Create GaleraRestore CRs from GaleraBackup CRs
#
# This script automatically creates GaleraRestore CRs for all GaleraBackup CRs
# found in a backup archive. This eliminates the manual step of creating these CRs.
#
# Usage:
#   ./create-galerarestore-crs.sh <backup-dir> [namespace]
#   ./create-galerarestore-crs.sh openstack-ctlplane-backup-20260303-163159
#   ./create-galerarestore-crs.sh openstack-ctlplane-backup-20260303-163159 openstack

set -e

# Configuration
BACKUP_DIR="${1}"
OPENSTACK_NAMESPACE="${2:-openstack}"

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
if [ -z "$BACKUP_DIR" ]; then
    print_error "Backup directory is required"
    echo ""
    echo "Usage:"
    echo "  $0 <backup-dir> [namespace]"
    echo ""
    echo "Examples:"
    echo "  $0 openstack-ctlplane-backup-20260303-163159"
    echo "  $0 openstack-ctlplane-backup-20260303-163159 openstack"
    exit 1
fi

if [ ! -d "$BACKUP_DIR" ]; then
    print_error "Backup directory not found: $BACKUP_DIR"
    exit 1
fi

print_header "Create GaleraRestore CRs from GaleraBackup CRs"
echo "Backup Directory: $BACKUP_DIR"
echo "OpenStack Namespace: $OPENSTACK_NAMESPACE"

# Check if galerabackup-backup.json exists
GALERABACKUP_FILE="${BACKUP_DIR}/galerabackup-backup.json"

if [ ! -f "$GALERABACKUP_FILE" ]; then
    print_warning "No GaleraBackup backup found: $GALERABACKUP_FILE"
    echo ""
    print_info "This is expected if:"
    print_info "  - Your deployment doesn't use Galera/MariaDB backups"
    print_info "  - GaleraBackup CRs were not included in the backup"
    echo ""
    print_info "Skipping GaleraRestore CR creation."
    exit 0
fi

# Get list of GaleraBackup names
BACKUP_NAMES=$(jq -r '.items[].metadata.name' "$GALERABACKUP_FILE")

if [ -z "$BACKUP_NAMES" ]; then
    print_warning "No GaleraBackup CRs found in backup file"
    exit 0
fi

# Count backups
BACKUP_COUNT=$(echo "$BACKUP_NAMES" | wc -l)
print_info "Found $BACKUP_COUNT GaleraBackup CR(s) in backup"
echo ""

# Create GaleraRestore CRs
CREATED_COUNT=0
while IFS= read -r BACKUP_NAME; do
    # Generate restore name (e.g., "openstack" → "openstackrestore")
    RESTORE_NAME="${BACKUP_NAME}restore"

    echo "Processing: $BACKUP_NAME"
    print_info "Creating GaleraRestore CR: $RESTORE_NAME"

    # Create GaleraRestore CR
    cat <<EOF | oc apply -f - -n "$OPENSTACK_NAMESPACE"
apiVersion: mariadb.openstack.org/v1beta1
kind: GaleraRestore
metadata:
  name: ${RESTORE_NAME}
  namespace: ${OPENSTACK_NAMESPACE}
spec:
  backupSource: ${BACKUP_NAME}
EOF

    if [ $? -eq 0 ]; then
        print_success "Created GaleraRestore CR: $RESTORE_NAME (backupSource: $BACKUP_NAME)"
        CREATED_COUNT=$((CREATED_COUNT + 1))
    else
        print_error "Failed to create GaleraRestore CR: $RESTORE_NAME"
    fi

    echo ""
done <<< "$BACKUP_NAMES"

# Summary
print_header "Summary"
print_success "Created $CREATED_COUNT GaleraRestore CR(s)"

# List created GaleraRestore CRs
echo ""
echo "Checking created GaleraRestore CRs..."
oc get galerarestore -n "$OPENSTACK_NAMESPACE"

echo ""
print_header "Next Steps"
echo "For each GaleraRestore CR, you must now execute the database restore:"
echo ""
echo "1. List dump files to find the closest timestamp to your backup:"
while IFS= read -r BACKUP_NAME; do
    RESTORE_NAME="${BACKUP_NAME}restore"
    echo "   oc exec -n $OPENSTACK_NAMESPACE openstack-restore-${RESTORE_NAME} -- ls -la /backup/data/"
done <<< "$BACKUP_NAMES"

echo ""
echo "2. Execute restore using the matching timestamp file:"
while IFS= read -r BACKUP_NAME; do
    RESTORE_NAME="${BACKUP_NAME}restore"
    echo "   oc exec -n $OPENSTACK_NAMESPACE openstack-restore-${RESTORE_NAME} -- \\"
    echo "     /var/lib/backup-scripts/restore_galera --yes /backup/data/*_YYYY-MM-DD_HH-MM-SS.sql.gz"
    echo ""
done <<< "$BACKUP_NAMES"

echo "See docs/dev/backup-restore-ctlplane.md for detailed restore instructions."
