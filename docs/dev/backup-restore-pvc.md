# PVC/PV Backup and Restore Procedure

## Overview

This procedure covers backup and restore of **Persistent Volume Claims (PVCs)** containing stateful data:
- **MariaDB/Galera databases** - OpenStack service databases (Keystone, Nova, Neutron, Glance, etc.)
- **Glance image storage** - VM images and snapshots
- **Other persistent data** - Service-specific storage

**IMPORTANT**: PVC backup should be performed **in addition to** the stateless control plane backup and OVN database backup documented in `backup-restore-ctlplane.md` and `backup-restore-ovn.md`.

## Backup Strategy Overview

Different PVCs require different backup approaches:

| Component | PVC Name Pattern | Recommended Method | Reason |
|-----------|------------------|-------------------|---------|
| MariaDB | `mysql-db-*`, `mariadb-data` | Database dump | Application-consistent, portable |
| Glance | `glance-*` | CSI snapshot or tar | Large files, snapshot preferred |
| RabbitMQ | `rabbitmq-*` | Usually not needed | Recreated from config |
| Other services | Various | CSI snapshot or tar | Depends on size and importance |

## Prerequisites

- **Access to OpenStack namespace** - You need cluster-admin or sufficient RBAC permissions
- **Storage for backups** - Local disk, NFS, or S3-compatible storage
- **oc CLI** - OpenShift/Kubernetes command-line tool
- **CSI driver support** (optional) - For volume snapshots

## Method 1: Database Dumps (MariaDB/Galera)

**Best for:** MariaDB Galera cluster databases

### Backup Procedure

#### Quick Backup Script

```bash
#!/bin/bash
set -e

NAMESPACE="${NAMESPACE:-openstack}"
BACKUP_DIR="${BACKUP_DIR:-./backups/mariadb}"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

echo "=========================================="
echo "MariaDB Database Backup"
echo "=========================================="
echo "Namespace: ${NAMESPACE}"
echo "Backup directory: ${BACKUP_DIR}"
echo ""

# Create backup directory
mkdir -p ${BACKUP_DIR}

# Find MariaDB Galera pod
echo "Finding MariaDB Galera pod..."
MARIADB_POD=$(oc get pod -n ${NAMESPACE} -l component=galera -o jsonpath='{.items[0].metadata.name}')

if [ -z "${MARIADB_POD}" ]; then
    echo "Error: No MariaDB Galera pod found"
    exit 1
fi

echo "Using pod: ${MARIADB_POD}"
echo ""

# Get root password
echo "Retrieving database credentials..."
ROOT_PASSWORD=$(oc get secret openstack-secret -n ${NAMESPACE} -o jsonpath='{.data.DbRootPassword}' | base64 -d)

# Backup all databases
echo "Backing up all databases..."
BACKUP_FILE="${BACKUP_DIR}/mariadb-all-databases-${TIMESTAMP}.sql"

oc exec -n ${NAMESPACE} ${MARIADB_POD} -c galera -- bash -c \
  "mysqldump --all-databases --single-transaction --quick --lock-tables=false --routines --triggers --events -uroot -p${ROOT_PASSWORD}" \
  > ${BACKUP_FILE}

if [ ! -s ${BACKUP_FILE} ]; then
    echo "Error: Backup file is empty!"
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
cat > ${BACKUP_DIR}/mariadb-backup-${TIMESTAMP}-metadata.txt <<EOF
MariaDB Backup Metadata
========================
Backup Date: $(date)
Backup Created By: $(oc whoami)
Namespace: ${NAMESPACE}
Source Pod: ${MARIADB_POD}

OpenShift Cluster:
- Console URL: $(oc whoami --show-console)
- Server: $(oc whoami --show-server)

MariaDB Status:
$(oc exec -n ${NAMESPACE} ${MARIADB_POD} -c galera -- mysql -uroot -p${ROOT_PASSWORD} -e "SHOW DATABASES;" 2>/dev/null || echo "Unable to list databases")

Files in this backup:
- mariadb-all-databases-${TIMESTAMP}.sql.gz - Complete database dump
EOF

echo "✓ Metadata saved"
echo ""

echo "=========================================="
echo "MariaDB Backup Complete"
echo "=========================================="
echo "Backup file: ${BACKUP_FILE}.gz"
echo "Size: $(du -h ${BACKUP_FILE}.gz | cut -f1)"
echo ""
echo "Next steps:"
echo "1. Store backup securely off-cluster"
echo "2. Verify backup integrity"
echo "3. Test restore on a test cluster"
echo ""
```

#### Manual Backup Steps

```bash
# 1. Find MariaDB pod
NAMESPACE="openstack"
MARIADB_POD=$(oc get pod -n ${NAMESPACE} -l component=galera -o jsonpath='{.items[0].metadata.name}')
echo "Using pod: ${MARIADB_POD}"

# 2. Get root password
ROOT_PASSWORD=$(oc get secret openstack-secret -n ${NAMESPACE} -o jsonpath='{.data.DbRootPassword}' | base64 -d)

# 3. Create backup
oc exec -n ${NAMESPACE} ${MARIADB_POD} -c galera -- \
  mysqldump --all-databases --single-transaction --quick --lock-tables=false \
  --routines --triggers --events -uroot -p${ROOT_PASSWORD} \
  > mariadb-backup-$(date +%Y%m%d-%H%M%S).sql

# 4. Compress
gzip mariadb-backup-*.sql

# 5. Verify
ls -lh mariadb-backup-*.sql.gz
```

### Restore Procedure

#### Quick Restore Script

```bash
#!/bin/bash
set -e

NAMESPACE="${NAMESPACE:-openstack}"
BACKUP_FILE="${1}"

if [ -z "${BACKUP_FILE}" ]; then
    echo "Usage: $0 <backup-file.sql.gz>"
    echo ""
    echo "Example:"
    echo "  $0 mariadb-backup-20260123-120000.sql.gz"
    exit 1
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

echo "✓ Backup file verified"
echo "  Size: $(du -h ${BACKUP_FILE} | cut -f1)"
echo ""

# Find MariaDB pod
echo "Finding MariaDB Galera pod..."
MARIADB_POD=$(oc get pod -n ${NAMESPACE} -l component=galera -o jsonpath='{.items[0].metadata.name}')

if [ -z "${MARIADB_POD}" ]; then
    echo "Error: No MariaDB Galera pod found"
    echo "Please ensure MariaDB is running before restoring"
    exit 1
fi

echo "✓ Using pod: ${MARIADB_POD}"
echo ""

# Get root password
echo "Retrieving database credentials..."
ROOT_PASSWORD=$(oc get secret openstack-secret -n ${NAMESPACE} -o jsonpath='{.data.DbRootPassword}' | base64 -d)
echo ""

# Warning
echo "⚠️  WARNING: This will replace all databases in the MariaDB cluster!"
echo "⚠️  All current data will be lost!"
echo ""
read -p "Continue with restore? (yes/no): " CONFIRM

if [ "${CONFIRM}" != "yes" ]; then
    echo "Restore cancelled."
    exit 0
fi
echo ""

# Decompress if needed
RESTORE_FILE="${BACKUP_FILE}"
if [[ "${BACKUP_FILE}" == *.gz ]]; then
    echo "Decompressing backup..."
    gunzip -c ${BACKUP_FILE} > /tmp/mariadb-restore-temp.sql
    RESTORE_FILE="/tmp/mariadb-restore-temp.sql"
    echo "✓ Backup decompressed"
    echo ""
fi

# Restore database
echo "Restoring database..."
echo "This may take several minutes depending on database size..."
echo ""

cat ${RESTORE_FILE} | oc exec -n ${NAMESPACE} -i ${MARIADB_POD} -c galera -- \
  mysql -uroot -p${ROOT_PASSWORD}

# Cleanup temp file
if [ "${RESTORE_FILE}" != "${BACKUP_FILE}" ]; then
    rm -f ${RESTORE_FILE}
fi

echo "✓ Database restored"
echo ""

# Verify databases
echo "Verifying databases..."
oc exec -n ${NAMESPACE} ${MARIADB_POD} -c galera -- \
  mysql -uroot -p${ROOT_PASSWORD} -e "SHOW DATABASES;" 2>/dev/null | grep -E "keystone|nova|neutron|glance|placement"
echo ""

echo "=========================================="
echo "MariaDB Restore Complete"
echo "=========================================="
echo ""
echo "Next steps:"
echo "1. Verify database contents:"
echo "   oc exec -n ${NAMESPACE} ${MARIADB_POD} -c galera -- mysql -uroot -p\${ROOT_PASSWORD} -e 'SHOW DATABASES;'"
echo ""
echo "2. Restart OpenStack services to reconnect to databases:"
echo "   oc delete pod -l service=keystone -n ${NAMESPACE}"
echo "   oc delete pod -l service=nova -n ${NAMESPACE}"
echo "   # etc."
echo ""
echo "3. Verify OpenStack services are functional"
echo ""
```

## Method 2: CSI Volume Snapshots

**Best for:** Glance image storage, large PVCs with CSI snapshot support

### Prerequisites

```bash
# Check if CSI snapshots are available
oc get volumesnapshotclass

# If no snapshot class exists, this method is not available
# Use Method 3 (Tar/Rsync) instead
```

### Backup Procedure

#### Create snapshots for all PVCs

```bash
#!/bin/bash
set -e

NAMESPACE="${NAMESPACE:-openstack}"
SNAPSHOT_CLASS="${SNAPSHOT_CLASS:-}"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

echo "=========================================="
echo "PVC Snapshot Backup"
echo "=========================================="
echo "Namespace: ${NAMESPACE}"
echo "Timestamp: ${TIMESTAMP}"
echo ""

# Auto-detect snapshot class if not specified
if [ -z "${SNAPSHOT_CLASS}" ]; then
    SNAPSHOT_CLASS=$(oc get volumesnapshotclass -o jsonpath='{.items[0].metadata.name}')
    if [ -z "${SNAPSHOT_CLASS}" ]; then
        echo "Error: No VolumeSnapshotClass found"
        echo "CSI snapshots are not available on this cluster"
        exit 1
    fi
fi

echo "Using VolumeSnapshotClass: ${SNAPSHOT_CLASS}"
echo ""

# List PVCs to backup
echo "Finding PVCs in namespace ${NAMESPACE}..."
PVCS=$(oc get pvc -n ${NAMESPACE} -o jsonpath='{.items[*].metadata.name}')

if [ -z "${PVCS}" ]; then
    echo "No PVCs found in namespace ${NAMESPACE}"
    exit 0
fi

echo "Found PVCs:"
for pvc in ${PVCS}; do
    echo "  - ${pvc}"
done
echo ""

# Create snapshots
for pvc in ${PVCS}; do
    SNAPSHOT_NAME="${pvc}-snapshot-${TIMESTAMP}"
    echo "Creating snapshot for ${pvc}..."

    cat <<EOF | oc apply -f -
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

    echo "✓ Snapshot created: ${SNAPSHOT_NAME}"
done

echo ""
echo "=========================================="
echo "Snapshot Backup Complete"
echo "=========================================="
echo ""
echo "Created snapshots:"
oc get volumesnapshot -n ${NAMESPACE} -l backup-timestamp=${TIMESTAMP}
echo ""
echo "To list all snapshots:"
echo "  oc get volumesnapshot -n ${NAMESPACE}"
echo ""
```

### Restore Procedure

```bash
#!/bin/bash
set -e

NAMESPACE="${NAMESPACE:-openstack}"
SNAPSHOT_NAME="${1}"
NEW_PVC_NAME="${2}"

if [ -z "${SNAPSHOT_NAME}" ] || [ -z "${NEW_PVC_NAME}" ]; then
    echo "Usage: $0 <snapshot-name> <new-pvc-name>"
    echo ""
    echo "Example:"
    echo "  $0 glance-data-snapshot-20260123-120000 glance-data-restored"
    exit 1
fi

echo "=========================================="
echo "PVC Restore from Snapshot"
echo "=========================================="
echo "Namespace: ${NAMESPACE}"
echo "Snapshot: ${SNAPSHOT_NAME}"
echo "New PVC: ${NEW_PVC_NAME}"
echo ""

# Verify snapshot exists
if ! oc get volumesnapshot ${SNAPSHOT_NAME} -n ${NAMESPACE} &>/dev/null; then
    echo "Error: Snapshot ${SNAPSHOT_NAME} not found"
    exit 1
fi

# Get storage class from original PVC
STORAGE_CLASS=$(oc get volumesnapshot ${SNAPSHOT_NAME} -n ${NAMESPACE} -o jsonpath='{.spec.source.persistentVolumeClaimName}' | xargs -I {} oc get pvc {} -n ${NAMESPACE} -o jsonpath='{.spec.storageClassName}')

# Create PVC from snapshot
cat <<EOF | oc apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${NEW_PVC_NAME}
  namespace: ${NAMESPACE}
spec:
  storageClassName: ${STORAGE_CLASS}
  dataSource:
    name: ${SNAPSHOT_NAME}
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi  # Adjust as needed
EOF

echo "✓ PVC created from snapshot"
echo ""
echo "Waiting for PVC to be bound..."
oc wait --for=jsonpath='{.status.phase}'=Bound pvc/${NEW_PVC_NAME} -n ${NAMESPACE} --timeout=300s

echo ""
echo "=========================================="
echo "Restore Complete"
echo "=========================================="
echo ""
echo "PVC ${NEW_PVC_NAME} created from snapshot ${SNAPSHOT_NAME}"
echo ""
echo "Next steps:"
echo "1. Update your deployment to use the restored PVC"
echo "2. Restart pods to mount the restored volume"
echo ""
```

## Method 3: Tar/Rsync Copy

**Best for:** When CSI snapshots are not available, smaller PVCs

### Backup Procedure

```bash
#!/bin/bash
set -e

NAMESPACE="${NAMESPACE:-openstack}"
PVC_NAME="${1}"
BACKUP_DIR="${BACKUP_DIR:-./backups/pvc}"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

if [ -z "${PVC_NAME}" ]; then
    echo "Usage: $0 <pvc-name>"
    echo ""
    echo "Available PVCs:"
    oc get pvc -n ${NAMESPACE}
    exit 1
fi

echo "=========================================="
echo "PVC Tar Backup"
echo "=========================================="
echo "Namespace: ${NAMESPACE}"
echo "PVC: ${PVC_NAME}"
echo "Backup directory: ${BACKUP_DIR}"
echo ""

mkdir -p ${BACKUP_DIR}

# Verify PVC exists
if ! oc get pvc ${PVC_NAME} -n ${NAMESPACE} &>/dev/null; then
    echo "Error: PVC ${PVC_NAME} not found"
    exit 1
fi

# Create helper pod
echo "Creating backup helper pod..."
cat <<EOF | oc apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: pvc-backup-helper
  namespace: ${NAMESPACE}
spec:
  containers:
  - name: backup
    image: registry.access.redhat.com/ubi9/ubi:latest
    command: ["sleep", "infinity"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: ${PVC_NAME}
  restartPolicy: Never
EOF

# Wait for pod to be ready
echo "Waiting for helper pod to be ready..."
oc wait --for=condition=Ready pod/pvc-backup-helper -n ${NAMESPACE} --timeout=60s
echo "✓ Helper pod ready"
echo ""

# Create backup
echo "Creating tar backup..."
echo "This may take several minutes..."
BACKUP_FILE="${BACKUP_DIR}/${PVC_NAME}-${TIMESTAMP}.tar.gz"

oc exec -n ${NAMESPACE} pvc-backup-helper -- tar czf - -C /data . > ${BACKUP_FILE}

echo "✓ Backup created: ${BACKUP_FILE}"
echo "  Size: $(du -h ${BACKUP_FILE} | cut -f1)"
echo ""

# Cleanup helper pod
echo "Cleaning up helper pod..."
oc delete pod pvc-backup-helper -n ${NAMESPACE}
echo ""

echo "=========================================="
echo "PVC Backup Complete"
echo "=========================================="
echo "Backup file: ${BACKUP_FILE}"
echo ""
```

### Restore Procedure

```bash
#!/bin/bash
set -e

NAMESPACE="${NAMESPACE:-openstack}"
BACKUP_FILE="${1}"
PVC_NAME="${2}"

if [ -z "${BACKUP_FILE}" ] || [ -z "${PVC_NAME}" ]; then
    echo "Usage: $0 <backup-file.tar.gz> <pvc-name>"
    echo ""
    echo "Example:"
    echo "  $0 glance-data-20260123-120000.tar.gz glance-data"
    exit 1
fi

echo "=========================================="
echo "PVC Tar Restore"
echo "=========================================="
echo "Namespace: ${NAMESPACE}"
echo "Backup file: ${BACKUP_FILE}"
echo "Target PVC: ${PVC_NAME}"
echo ""

# Verify backup file
if [ ! -f "${BACKUP_FILE}" ]; then
    echo "Error: Backup file not found: ${BACKUP_FILE}"
    exit 1
fi

echo "✓ Backup file verified"
echo "  Size: $(du -h ${BACKUP_FILE} | cut -f1)"
echo ""

# Verify PVC exists
if ! oc get pvc ${PVC_NAME} -n ${NAMESPACE} &>/dev/null; then
    echo "Error: PVC ${PVC_NAME} not found"
    echo "Please create the PVC before restoring"
    exit 1
fi

echo "⚠️  WARNING: This will replace all data in PVC ${PVC_NAME}!"
echo ""
read -p "Continue with restore? (yes/no): " CONFIRM

if [ "${CONFIRM}" != "yes" ]; then
    echo "Restore cancelled."
    exit 0
fi
echo ""

# Create helper pod
echo "Creating restore helper pod..."
cat <<EOF | oc apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: pvc-restore-helper
  namespace: ${NAMESPACE}
spec:
  containers:
  - name: restore
    image: registry.access.redhat.com/ubi9/ubi:latest
    command: ["sleep", "infinity"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: ${PVC_NAME}
  restartPolicy: Never
EOF

# Wait for pod to be ready
echo "Waiting for helper pod to be ready..."
oc wait --for=condition=Ready pod/pvc-restore-helper -n ${NAMESPACE} --timeout=60s
echo "✓ Helper pod ready"
echo ""

# Clear existing data
echo "Clearing existing data..."
oc exec -n ${NAMESPACE} pvc-restore-helper -- rm -rf /data/*
echo "✓ Existing data cleared"
echo ""

# Restore backup
echo "Restoring backup..."
echo "This may take several minutes..."
cat ${BACKUP_FILE} | oc exec -n ${NAMESPACE} -i pvc-restore-helper -- tar xzf - -C /data

echo "✓ Backup restored"
echo ""

# Cleanup helper pod
echo "Cleaning up helper pod..."
oc delete pod pvc-restore-helper -n ${NAMESPACE}
echo ""

echo "=========================================="
echo "PVC Restore Complete"
echo "=========================================="
echo ""
echo "Next steps:"
echo "1. Restart pods using this PVC"
echo "2. Verify data integrity"
echo ""
```

## Complete Backup Strategy

### Recommended Backup Order

```bash
# 1. Backup stateless control plane
./backup-openstack-ctlplane.sh

# 2. Backup OVN databases
./backup-ovn.sh

# 3. Backup MariaDB databases
./backup-mariadb.sh

# 4. Backup Glance PVCs (if using CSI snapshots)
./backup-pvc-snapshots.sh

# Or use tar method for Glance (if no CSI support)
# ./backup-pvc-tar.sh glance-data
```

### Recommended Restore Order

```bash
# 1. Restore stateless control plane (creates PVCs)
./restore-openstack-ctlplane.sh openstack-ctlplane-backup-*.tar.gz

# 2. Wait for MariaDB pods to be ready
oc wait --for=condition=Ready pod -l component=galera -n openstack --timeout=300s

# 3. Restore MariaDB databases
./restore-mariadb.sh mariadb-backup-*.sql.gz

# 4. Restore Glance PVCs (if using snapshots)
# Note: This may require recreating the PVC with a different name first
# ./restore-pvc-snapshot.sh glance-data-snapshot-* glance-data

# 5. Wait for OVN pods to be ready
oc wait --for=condition=Ready pod/ovsdbserver-nb-0 pod/ovsdbserver-sb-0 -n openstack --timeout=300s

# 6. Restore OVN databases
./restore-ovn.sh ovn-nb-backup-*.db ovn-sb-backup-*.db

# 7. Restart all OpenStack services
oc delete pod -l app=openstackcontrolplane -n openstack
```

## Automation Example

### Automated Daily Backups (CronJob)

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: openstack-mariadb-backup
  namespace: openstack-backups
spec:
  schedule: "0 1 * * *"  # 1 AM daily
  jobTemplate:
    spec:
      template:
        spec:
          serviceAccountName: backup-sa
          containers:
          - name: backup
            image: registry.redhat.io/openshift4/ose-cli:latest
            command:
            - /bin/bash
            - -c
            - |
              #!/bin/bash
              set -e

              NAMESPACE=openstack
              TIMESTAMP=$(date +%Y%m%d-%H%M%S)
              BACKUP_DIR=/backups/mariadb

              mkdir -p ${BACKUP_DIR}

              # Find MariaDB pod
              MARIADB_POD=$(oc get pod -n ${NAMESPACE} -l component=galera -o jsonpath='{.items[0].metadata.name}')

              # Get root password
              ROOT_PASSWORD=$(oc get secret openstack-secret -n ${NAMESPACE} -o jsonpath='{.data.DbRootPassword}' | base64 -d)

              # Backup database
              oc exec -n ${NAMESPACE} ${MARIADB_POD} -c galera -- \
                mysqldump --all-databases --single-transaction --quick --lock-tables=false \
                --routines --triggers --events -uroot -p${ROOT_PASSWORD} \
                > ${BACKUP_DIR}/mariadb-${TIMESTAMP}.sql

              # Compress
              gzip ${BACKUP_DIR}/mariadb-${TIMESTAMP}.sql

              # Upload to S3 (example)
              # aws s3 cp ${BACKUP_DIR}/mariadb-${TIMESTAMP}.sql.gz s3://my-backups/mariadb/

              # Cleanup old backups (keep last 7 days)
              find ${BACKUP_DIR} -name "mariadb-*.sql.gz" -mtime +7 -delete

              echo "MariaDB backup completed: mariadb-${TIMESTAMP}.sql.gz"
            volumeMounts:
            - name: backup-storage
              mountPath: /backups
          volumes:
          - name: backup-storage
            persistentVolumeClaim:
              claimName: backup-pvc
          restartPolicy: OnFailure
```

## Best Practices

1. **Backup Frequency**
   - MariaDB: Daily at minimum, more frequent for production
   - Glance: Daily or after significant image uploads
   - Coordinate all backups to run at the same time for consistency

2. **Backup Retention**
   - Keep at least 7 days of daily backups
   - Keep weekly backups for 4 weeks
   - Keep monthly backups for 12 months

3. **Backup Storage**
   - Store backups off-cluster (different storage system)
   - Use object storage (S3, etc.) for long-term retention
   - Encrypt backup files at rest
   - Test backup retrieval regularly

4. **Database Backups**
   - Use `--single-transaction` for consistent backups without locking
   - Include `--routines --triggers --events` for complete backup
   - Compress backups to save space
   - Verify backup files are not empty

5. **Testing**
   - Test restore procedures quarterly on non-production
   - Document restore time objectives (RTO)
   - Verify data integrity after restore
   - Practice disaster recovery scenarios

6. **Monitoring**
   - Monitor backup job success/failure
   - Alert on backup size anomalies
   - Track backup duration trends
   - Verify backup files are created

## Troubleshooting

### MariaDB Backup Issues

**Problem**: mysqldump fails with "Access denied"

```bash
# Verify root password
ROOT_PASSWORD=$(oc get secret openstack-secret -n openstack -o jsonpath='{.data.DbRootPassword}' | base64 -d)
echo "Root password: ${ROOT_PASSWORD}"

# Test MySQL access
oc exec -n openstack ${MARIADB_POD} -c galera -- \
  mysql -uroot -p${ROOT_PASSWORD} -e "SHOW DATABASES;"
```

**Problem**: Backup file is empty or incomplete

```bash
# Check pod logs
oc logs -n openstack ${MARIADB_POD} -c galera --tail=100

# Check disk space in pod
oc exec -n openstack ${MARIADB_POD} -c galera -- df -h

# Try backup with verbose output
oc exec -n openstack ${MARIADB_POD} -c galera -- \
  mysqldump -v --all-databases --single-transaction -uroot -p${ROOT_PASSWORD}
```

### MariaDB Restore Issues

**Problem**: Restore fails with "Unknown database"

```bash
# This is normal for some system databases
# Restore will recreate them
# Check for actual errors in the output
```

**Problem**: Tables already exist error

```bash
# You may need to drop databases first
oc exec -n openstack ${MARIADB_POD} -c galera -- \
  mysql -uroot -p${ROOT_PASSWORD} -e "DROP DATABASE keystone;"

# Or restore to a fresh MariaDB instance
```

### CSI Snapshot Issues

**Problem**: No VolumeSnapshotClass found

```bash
# Check if CSI driver supports snapshots
oc get csidriver

# Check storage class capabilities
oc get storageclass -o yaml

# CSI snapshots may not be available - use tar method instead
```

**Problem**: Snapshot stuck in "Pending"

```bash
# Check snapshot status
oc describe volumesnapshot <snapshot-name> -n openstack

# Check CSI driver logs
oc logs -n openshift-cluster-csi-drivers -l app=<csi-driver>

# Verify storage backend has capacity
```

### Tar Backup Issues

**Problem**: Helper pod won't start

```bash
# Check pod events
oc describe pod pvc-backup-helper -n openstack

# Check if PVC is already mounted by another pod
oc get pod -n openstack -o yaml | grep -A 5 "claimName: <pvc-name>"

# Some storage classes don't support ReadWriteMany
# You may need to scale down the application first
```

**Problem**: Backup/restore very slow

```bash
# Use rsync for incremental backups instead
oc exec -n openstack pvc-backup-helper -- \
  rsync -av --info=progress2 /data/ /destination/

# Or use multiple tar streams for large directories
```

## See Also

- [Stateless Control Plane Backup/Restore](backup-restore-ctlplane.md)
- [OVN Database Backup/Restore](backup-restore-ovn.md)
- [CSI Volume Snapshots Documentation](https://kubernetes.io/docs/concepts/storage/volume-snapshots/)
- [MariaDB Backup Documentation](https://mariadb.com/kb/en/backup-and-restore-overview/)
