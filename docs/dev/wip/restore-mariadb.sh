#!/bin/bash
set -e

# MariaDB Database Restore Script
# This script restores all databases to the MariaDB Galera cluster

NAMESPACE="${NAMESPACE:-openstack}"
BACKUP_FILE="${1}"

# Print usage
usage() {
    cat <<EOF
Usage: $0 <backup-file.sql.gz>

Restores all MariaDB databases from a backup file.

Arguments:
  backup-file.sql.gz    Path to MariaDB backup file (compressed or uncompressed)

Environment Variables:
  NAMESPACE             Target namespace (default: openstack)

Example:
  NAMESPACE=openstack ./restore-mariadb.sh mariadb-all-databases-20260123-120000.sql.gz

  # Or with uncompressed file:
  gunzip mariadb-all-databases-20260123-120000.sql.gz
  ./restore-mariadb.sh mariadb-all-databases-20260123-120000.sql

EOF
    exit 1
}

# Check arguments
if [ -z "${BACKUP_FILE}" ]; then
    usage
fi

echo "=========================================="
echo "MariaDB Database Restore"
echo "=========================================="
echo "Namespace: ${NAMESPACE}"
echo "Backup file: ${BACKUP_FILE}"
echo ""

# Verify backup file exists
if [ ! -f "${BACKUP_FILE}" ]; then
    echo "Error: Backup file not found: ${BACKUP_FILE}"
    exit 1
fi

# Verify backup file is not empty
if [ ! -s "${BACKUP_FILE}" ]; then
    echo "Error: Backup file is empty: ${BACKUP_FILE}"
    exit 1
fi

echo "✓ Backup file verified"
echo "  Size: $(du -h ${BACKUP_FILE} | cut -f1)"
echo ""

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

# Show current databases
echo "Current databases in MariaDB:"
oc exec -n ${NAMESPACE} ${MARIADB_POD} -c galera -- \
    mysql -uroot -p${ROOT_PASSWORD} -e "SHOW DATABASES;" 2>/dev/null | grep -E "keystone|nova|neutron|glance|placement|cinder|heat|manila" || echo "  (No OpenStack databases found)"
echo ""

# Warning and confirmation
echo "⚠️  WARNING: This will replace all databases in the MariaDB cluster!"
echo "⚠️  All current database data will be lost!"
echo "⚠️  OpenStack services will be affected during restore!"
echo ""
echo "It is recommended to:"
echo "1. Scale down OpenStack services before restoring"
echo "2. Have a current backup of the existing databases"
echo "3. Test this procedure on a non-production environment first"
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

oc exec -n ${NAMESPACE} ${MARIADB_POD} -c galera -- \
    mysqldump --all-databases --single-transaction --quick --lock-tables=false -uroot -p${ROOT_PASSWORD} \
    > ${EMERGENCY_BACKUP_DIR}/mariadb-emergency.sql 2>/dev/null || true

if [ -s ${EMERGENCY_BACKUP_DIR}/mariadb-emergency.sql ]; then
    gzip ${EMERGENCY_BACKUP_DIR}/mariadb-emergency.sql
    echo "✓ Emergency backup saved to: ${EMERGENCY_BACKUP_DIR}/mariadb-emergency.sql.gz"
else
    echo "⚠️  Warning: Could not create emergency backup"
fi
echo ""

# Decompress if needed
RESTORE_FILE="${BACKUP_FILE}"
CLEANUP_TEMP=false

if [[ "${BACKUP_FILE}" == *.gz ]]; then
    echo "Decompressing backup..."
    RESTORE_FILE="/tmp/mariadb-restore-temp-$$.sql"
    gunzip -c ${BACKUP_FILE} > ${RESTORE_FILE}
    CLEANUP_TEMP=true
    echo "✓ Backup decompressed"
    echo ""
fi

# Restore database
echo "=========================================="
echo "Restoring Database"
echo "=========================================="
echo ""
echo "This may take several minutes depending on database size..."
echo "Progress indicators may not be shown during restore..."
echo ""

START_TIME=$(date +%s)

cat ${RESTORE_FILE} | oc exec -n ${NAMESPACE} -i ${MARIADB_POD} -c galera -- \
  mysql -uroot -p${ROOT_PASSWORD}

END_TIME=$(date +%s)
DURATION=$((END_TIME - START_TIME))

# Cleanup temp file
if [ "${CLEANUP_TEMP}" = "true" ]; then
    rm -f ${RESTORE_FILE}
fi

echo ""
echo "✓ Database restored in ${DURATION} seconds"
echo ""

# Verify databases
echo "Verifying restored databases..."
RESTORED_DBS=$(oc exec -n ${NAMESPACE} ${MARIADB_POD} -c galera -- \
    mysql -uroot -p${ROOT_PASSWORD} -e "SHOW DATABASES;" 2>/dev/null | grep -E "keystone|nova|neutron|glance|placement|cinder|heat|manila" || true)

if [ -z "${RESTORED_DBS}" ]; then
    echo "⚠️  Warning: No OpenStack databases found after restore"
    echo "This may indicate an issue with the backup file"
else
    echo "✓ Restored databases:"
    echo "${RESTORED_DBS}"
fi
echo ""

# Completion
echo "=========================================="
echo "MariaDB Restore Complete"
echo "=========================================="
echo ""
echo "Emergency backup saved to: ${EMERGENCY_BACKUP_DIR}"
echo ""
echo "Next steps:"
echo ""
echo "1. Verify database contents:"
echo "   oc exec -n ${NAMESPACE} ${MARIADB_POD} -c galera -- mysql -uroot -p\${ROOT_PASSWORD} -e 'SHOW DATABASES;'"
echo ""
echo "2. Check specific database contents (example):"
echo "   oc exec -n ${NAMESPACE} ${MARIADB_POD} -c galera -- mysql -uroot -p\${ROOT_PASSWORD} keystone -e 'SHOW TABLES;'"
echo ""
echo "3. Restart OpenStack services to reconnect to databases:"
echo "   oc rollout restart deployment -l service=keystone -n ${NAMESPACE}"
echo "   oc rollout restart deployment -l service=nova -n ${NAMESPACE}"
echo "   oc rollout restart deployment -l service=neutron -n ${NAMESPACE}"
echo "   oc rollout restart deployment -l service=glance -n ${NAMESPACE}"
echo ""
echo "4. Verify OpenStack services are functional:"
echo "   openstack endpoint list"
echo "   openstack service list"
echo ""
echo "5. Monitor MariaDB cluster status:"
echo "   oc exec -n ${NAMESPACE} ${MARIADB_POD} -c galera -- mysql -uroot -p\${ROOT_PASSWORD} -e 'SHOW STATUS LIKE \"wsrep_%\";'"
echo ""
