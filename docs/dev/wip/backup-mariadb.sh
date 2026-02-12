#!/bin/bash
set -e

# MariaDB Database Backup Script
# This script backs up all databases from the MariaDB Galera cluster

NAMESPACE="${NAMESPACE:-openstack}"
BACKUP_DIR="${BACKUP_DIR:-./backups/mariadb}"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

echo "=========================================="
echo "MariaDB Database Backup"
echo "=========================================="
echo "Namespace: ${NAMESPACE}"
echo "Backup directory: ${BACKUP_DIR}"
echo "Timestamp: ${TIMESTAMP}"
echo ""

# Create backup directory
mkdir -p ${BACKUP_DIR}

# Find MariaDB Galera pod
echo "Finding MariaDB Galera pod..."
MARIADB_POD=$(oc get pod -n ${NAMESPACE} -l component=galera -o jsonpath='{.items[0].metadata.name}')

if [ -z "${MARIADB_POD}" ]; then
    echo "Error: No MariaDB Galera pod found in namespace ${NAMESPACE}"
    echo "Please verify the MariaDB pods are running:"
    echo "  oc get pods -n ${NAMESPACE} -l component=galera"
    exit 1
fi

echo "✓ Using pod: ${MARIADB_POD}"
echo ""

# Get root password
echo "Retrieving database credentials..."
ROOT_PASSWORD=$(oc get secret openstack-secret -n ${NAMESPACE} -o jsonpath='{.data.DbRootPassword}' | base64 -d 2>/dev/null)

if [ -z "${ROOT_PASSWORD}" ]; then
    echo "Error: Could not retrieve DbRootPassword from openstack-secret"
    echo "Please verify the secret exists:"
    echo "  oc get secret openstack-secret -n ${NAMESPACE}"
    exit 1
fi

echo "✓ Credentials retrieved"
echo ""

# Test database connectivity
echo "Testing database connectivity..."
if ! oc exec -n ${NAMESPACE} ${MARIADB_POD} -c galera -- \
    mysql -uroot -p${ROOT_PASSWORD} -e "SELECT 1;" &>/dev/null; then
    echo "Error: Cannot connect to MariaDB"
    echo "Please verify the database is accessible"
    exit 1
fi
echo "✓ Database connection successful"
echo ""

# Backup all databases
echo "Backing up all databases..."
echo "This may take several minutes depending on database size..."
BACKUP_FILE="${BACKUP_DIR}/mariadb-all-databases-${TIMESTAMP}.sql"

oc exec -n ${NAMESPACE} ${MARIADB_POD} -c galera -- bash -c \
  "mysqldump --all-databases --single-transaction --quick --lock-tables=false --routines --triggers --events -uroot -p${ROOT_PASSWORD}" \
  > ${BACKUP_FILE}

if [ ! -s ${BACKUP_FILE} ]; then
    echo "Error: Backup file is empty!"
    rm -f ${BACKUP_FILE}
    exit 1
fi

echo "✓ Database backup saved to: ${BACKUP_FILE}"
echo "  Size: $(du -h ${BACKUP_FILE} | cut -f1)"
echo ""

# Compress backup
echo "Compressing backup..."
gzip ${BACKUP_FILE}
echo "✓ Compressed to: ${BACKUP_FILE}.gz"
echo "  Size: $(du -h ${BACKUP_FILE}.gz | cut -f1)"
echo ""

# Create metadata
echo "Creating metadata file..."
cat > ${BACKUP_DIR}/mariadb-backup-${TIMESTAMP}-metadata.txt <<EOF
MariaDB Backup Metadata
========================
Backup Date: $(date)
Backup Created By: $(oc whoami 2>/dev/null || echo "unknown")
Namespace: ${NAMESPACE}
Source Pod: ${MARIADB_POD}

OpenShift Cluster:
- Console URL: $(oc whoami --show-console 2>/dev/null || echo "unknown")
- Server: $(oc whoami --show-server 2>/dev/null || echo "unknown")

MariaDB Status:
$(oc exec -n ${NAMESPACE} ${MARIADB_POD} -c galera -- mysql -uroot -p${ROOT_PASSWORD} -e "SHOW DATABASES;" 2>/dev/null | head -20 || echo "Unable to list databases")

Files in this backup:
- mariadb-all-databases-${TIMESTAMP}.sql.gz - Complete database dump

Backup includes:
- All databases (--all-databases)
- Stored routines, triggers, events
- Consistent snapshot (--single-transaction)
EOF

echo "✓ Metadata saved to: ${BACKUP_DIR}/mariadb-backup-${TIMESTAMP}-metadata.txt"
echo ""

echo "=========================================="
echo "MariaDB Backup Complete"
echo "=========================================="
echo "Backup file: ${BACKUP_FILE}.gz"
echo "Size: $(du -h ${BACKUP_FILE}.gz | cut -f1)"
echo ""
echo "Next steps:"
echo "1. Store backup securely off-cluster:"
echo "   scp ${BACKUP_FILE}.gz backup-server:/backups/"
echo ""
echo "2. Verify backup integrity:"
echo "   gunzip -c ${BACKUP_FILE}.gz | head -50"
echo ""
echo "3. Test restore on a test cluster before relying on this backup"
echo ""
