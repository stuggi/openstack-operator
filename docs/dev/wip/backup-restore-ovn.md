# OVN Database Backup and Restore Procedure

## Overview

This procedure covers backup and restore of **OVN (Open Virtual Network) databases** which contain critical network topology and state:
- Virtual networks, subnets, routers
- Security groups and rules
- Load balancers
- Port bindings
- DHCP options
- And other network configurations

**IMPORTANT**: OVN backup should be performed **in addition to** the stateless control plane backup documented in `../backup-restore-ctlplane.md`.

## OVN Architecture

OVN uses two clustered databases:

1. **OVN Northbound Database (NB)** - Logical network topology
   - Logical switches, routers, load balancers
   - ACLs and security groups
   - High-level network configuration
   - Written by Neutron, read by ovn-northd

2. **OVN Southbound Database (SB)** - Physical network bindings
   - Chassis (hypervisor) information
   - Port bindings to physical locations
   - Datapath flows
   - Written by ovn-northd, read by ovn-controller

Both databases are replicated across multiple pods for high availability using RAFT consensus.

## Prerequisites

- **Access to OVN pods** - You need to be able to exec into OVN NB/SB pods
- **Sufficient storage** - Local or remote location for backup files
- **oc CLI** - OpenShift/Kubernetes command-line tool

## Backup Procedure

### Quick Backup Script

```bash
#!/bin/bash
set -e

NAMESPACE="${NAMESPACE:-openstack}"
BACKUP_DIR="${BACKUP_DIR:-./backups}"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

echo "=========================================="
echo "OVN Database Backup"
echo "=========================================="
echo "Namespace: ${NAMESPACE}"
echo "Backup directory: ${BACKUP_DIR}"
echo ""

# Create backup directory
mkdir -p ${BACKUP_DIR}

# Backup OVN Northbound Database
echo "Backing up OVN Northbound database..."
NB_BACKUP="${BACKUP_DIR}/ovn-nb-backup-${TIMESTAMP}.db"
oc exec -n ${NAMESPACE} ovsdbserver-nb-0 -c nb-ovsdb -- \
  ovsdb-client backup tcp:127.0.0.1:6641 > ${NB_BACKUP}
echo "✓ OVN NB backup saved to: ${NB_BACKUP}"
echo "  Size: $(du -h ${NB_BACKUP} | cut -f1)"
echo ""

# Backup OVN Southbound Database
echo "Backing up OVN Southbound database..."
SB_BACKUP="${BACKUP_DIR}/ovn-sb-backup-${TIMESTAMP}.db"
oc exec -n ${NAMESPACE} ovsdbserver-sb-0 -c sb-ovsdb -- \
  ovsdb-client backup tcp:127.0.0.1:6642 > ${SB_BACKUP}
echo "✓ OVN SB backup saved to: ${SB_BACKUP}"
echo "  Size: $(du -h ${SB_BACKUP} | cut -f1)"
echo ""

# Create archive
ARCHIVE="${BACKUP_DIR}/ovn-backup-${TIMESTAMP}.tar.gz"
tar -czf ${ARCHIVE} -C ${BACKUP_DIR} \
  ovn-nb-backup-${TIMESTAMP}.db \
  ovn-sb-backup-${TIMESTAMP}.db

echo "=========================================="
echo "OVN Backup Complete"
echo "=========================================="
echo "Archive: ${ARCHIVE}"
echo "Size: $(du -h ${ARCHIVE} | cut -f1)"
echo ""
echo "Next steps:"
echo "1. Store backup archive securely"
echo "2. Test restore on a test cluster"
echo "3. Integrate with your backup automation"
echo ""
```

### Manual Backup Steps

#### 1. Identify OVN Database Pods

```bash
# List OVN database pods
oc get pods -n openstack | grep ovsdbserver

# You should see pods like: ovsdbserver-nb-0, ovsdbserver-sb-0 (and their replicas)
# NB and SB databases are in separate pods
```

#### 2. Backup OVN Northbound Database

```bash
# Backup NB database
oc exec -n openstack ovsdbserver-nb-0 -c nb-ovsdb -- \
  ovsdb-client backup tcp:127.0.0.1:6641 > ovn-nb-backup-$(date +%Y%m%d-%H%M%S).db

# Verify backup
ls -lh ovn-nb-backup-*.db
```

**What this does:**
- Connects to the OVN NB database on port 6641
- Creates a consistent snapshot of the database
- Saves it as a standalone database file

#### 3. Backup OVN Southbound Database

```bash
# Backup SB database
oc exec -n openstack ovsdbserver-sb-0 -c sb-ovsdb -- \
  ovsdb-client backup tcp:127.0.0.1:6642 > ovn-sb-backup-$(date +%Y%m%d-%H%M%S).db

# Verify backup
ls -lh ovn-sb-backup-*.db
```

#### 4. Create Backup Archive

```bash
# Create timestamped archive
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
tar -czf ovn-backup-${TIMESTAMP}.tar.gz ovn-nb-backup-*.db ovn-sb-backup-*.db

# Store securely
# Examples:
# scp ovn-backup-${TIMESTAMP}.tar.gz backup-server:/backups/
```

## Restore Procedure

### Prerequisites

- **OVN pods are running** - The OVN database cluster must be operational
- **Backup files available** - Extract the backup archive
- **Network downtime acceptable** - Restoring OVN databases will cause brief network disruption

### Quick Restore Script

```bash
#!/bin/bash
set -e

NAMESPACE="${NAMESPACE:-openstack}"
NB_BACKUP="${1}"
SB_BACKUP="${2}"

if [ -z "${NB_BACKUP}" ] || [ -z "${SB_BACKUP}" ]; then
    echo "Usage: $0 <nb-backup-file> <sb-backup-file>"
    echo ""
    echo "Example:"
    echo "  $0 ovn-nb-backup-20260122-120000.db ovn-sb-backup-20260122-120000.db"
    exit 1
fi

echo "=========================================="
echo "OVN Database Restore"
echo "=========================================="
echo "Namespace: ${NAMESPACE}"
echo "NB Backup: ${NB_BACKUP}"
echo "SB Backup: ${SB_BACKUP}"
echo ""

# Verify backup files exist
if [ ! -f "${NB_BACKUP}" ]; then
    echo "Error: NB backup file not found: ${NB_BACKUP}"
    exit 1
fi

if [ ! -f "${SB_BACKUP}" ]; then
    echo "Error: SB backup file not found: ${SB_BACKUP}"
    exit 1
fi

echo "⚠️  WARNING: This will replace the current OVN databases!"
echo "⚠️  Network connectivity will be briefly disrupted!"
echo ""
read -p "Continue with restore? (yes/no): " CONFIRM

if [ "${CONFIRM}" != "yes" ]; then
    echo "Restore cancelled."
    exit 0
fi

# Scale down OVN northd to prevent conflicts
echo ""
echo "Step 1: Scaling down ovn-northd..."
NORTHD_REPLICAS=$(oc get deployment ovn-northd -n ${NAMESPACE} -o jsonpath='{.spec.replicas}')
oc scale deployment ovn-northd -n ${NAMESPACE} --replicas=0
echo "✓ ovn-northd scaled to 0"
echo ""

# Wait for ovn-northd to stop
echo "Waiting for ovn-northd pods to terminate..."
oc wait --for=delete pod -l app=ovn-northd -n ${NAMESPACE} --timeout=60s || true
echo ""

# Restore NB database
echo "Step 2: Restoring OVN Northbound database..."
cat ${NB_BACKUP} | oc exec -n ${NAMESPACE} -i ovsdbserver-nb-0 -c nb-ovsdb -- \
  ovsdb-client restore tcp:127.0.0.1:6641
echo "✓ OVN NB database restored"
echo ""

# Restore SB database
echo "Step 3: Restoring OVN Southbound database..."
cat ${SB_BACKUP} | oc exec -n ${NAMESPACE} -i ovsdbserver-sb-0 -c sb-ovsdb -- \
  ovsdb-client restore tcp:127.0.0.1:6642
echo "✓ OVN SB database restored"
echo ""

# Scale up OVN northd
echo "Step 4: Scaling up ovn-northd..."
oc scale deployment ovn-northd -n ${NAMESPACE} --replicas=${NORTHD_REPLICAS}
echo "✓ ovn-northd scaled to ${NORTHD_REPLICAS}"
echo ""

# Wait for ovn-northd to be ready
echo "Waiting for ovn-northd to be ready..."
oc wait --for=condition=Available deployment/ovn-northd -n ${NAMESPACE} --timeout=120s
echo ""

echo "=========================================="
echo "OVN Restore Complete"
echo "=========================================="
echo ""
echo "Next steps:"
echo "1. Verify OVN database contents:"
echo "   oc exec -n ${NAMESPACE} ovsdbserver-nb-0 -c nb-ovsdb -- ovn-nbctl show"
echo "   oc exec -n ${NAMESPACE} ovsdbserver-sb-0 -c sb-ovsdb -- ovn-sbctl show"
echo ""
echo "2. Check ovn-northd logs for any errors:"
echo "   oc logs -n ${NAMESPACE} deployment/ovn-northd --tail=50"
echo ""
echo "3. Verify network connectivity for VMs/pods"
echo ""
```

### Manual Restore Steps

#### 1. Scale Down ovn-northd

```bash
# Save current replica count
NORTHD_REPLICAS=$(oc get deployment ovn-northd -n openstack -o jsonpath='{.spec.replicas}')

# Scale down ovn-northd (prevents it from processing during restore)
oc scale deployment ovn-northd -n openstack --replicas=0

# Wait for pods to terminate
oc wait --for=delete pod -l app=ovn-northd -n openstack --timeout=60s
```

**Why:** ovn-northd synchronizes NB to SB. We need to stop it during restore to prevent conflicts.

#### 2. Restore OVN Northbound Database

```bash
# Restore NB database
cat ovn-nb-backup-20260122-120000.db | \
  oc exec -n openstack -i ovsdbserver-nb-0 -c nb-ovsdb -- \
  ovsdb-client restore tcp:127.0.0.1:6641

# Verify NB contents
oc exec -n openstack ovsdbserver-nb-0 -c nb-ovsdb -- ovn-nbctl show
```

#### 3. Restore OVN Southbound Database

```bash
# Restore SB database
cat ovn-sb-backup-20260122-120000.db | \
  oc exec -n openstack -i ovsdbserver-sb-0 -c sb-ovsdb -- \
  ovsdb-client restore tcp:127.0.0.1:6642

# Verify SB contents
oc exec -n openstack ovsdbserver-sb-0 -c sb-ovsdb -- ovn-sbctl show
```

#### 4. Scale Up ovn-northd

```bash
# Restore original replica count
oc scale deployment ovn-northd -n openstack --replicas=${NORTHD_REPLICAS}

# Wait for deployment to be ready
oc wait --for=condition=Available deployment/ovn-northd -n openstack --timeout=120s

# Check logs
oc logs -n openstack deployment/ovn-northd --tail=50
```

#### 5. Verify Network State

```bash
# Check OVN NB logical topology
oc exec -n openstack ovsdbserver-nb-0 -c nb-ovsdb -- ovn-nbctl show

# Check OVN SB physical bindings
oc exec -n openstack ovsdbserver-sb-0 -c sb-ovsdb -- ovn-sbctl show

# List logical switches
oc exec -n openstack ovsdbserver-nb-0 -c nb-ovsdb -- ovn-nbctl ls-list

# List logical routers
oc exec -n openstack ovsdbserver-nb-0 -c nb-ovsdb -- ovn-nbctl lr-list

# Check chassis (compute nodes)
oc exec -n openstack ovsdbserver-sb-0 -c sb-ovsdb -- ovn-sbctl chassis-list
```

## Integration with Full Backup/Restore

### Complete Backup Strategy

For a complete OpenStack control plane backup:

1. **Stateless Services Backup** (see `../backup-restore-ctlplane.md`)
   - Custom Resources (OpenStackControlPlane, etc.)
   - Secrets (user-provided, CA certs, DB passwords)
   - ConfigMaps
   - Other custom resources

2. **OVN Databases Backup** (this document)
   - OVN Northbound database
   - OVN Southbound database

3. **MariaDB Galera Backup** (separate procedure)
   - Database dumps or volume snapshots
   - Keystone, Nova, Neutron, Glance databases

4. **Persistent Volumes** (if using PVCs)
   - Database PVCs (if not using dumps)
   - Glance image storage PVCs
   - Other service PVCs

### Recommended Backup Order

```bash
# 1. Backup stateless control plane
./backup-openstack-ctlplane.sh

# 2. Backup OVN databases
./backup-ovn.sh

# 3. Backup MariaDB (if not using persistent volumes)
# (See MariaDB backup procedure)

# 4. Create volume snapshots (if using PVCs)
# (See volume snapshot procedure)
```

### Recommended Restore Order

```bash
# 1. Restore MariaDB databases (if not using persistent volumes)
# (See MariaDB restore procedure)

# 2. Restore stateless control plane
./restore-openstack-ctlplane.sh openstack-ctlplane-backup-*.tar.gz

# 3. Wait for OVN pods to be ready
oc wait --for=condition=Ready pod/ovsdbserver-nb-0 pod/ovsdbserver-sb-0 -n openstack --timeout=300s

# 4. Restore OVN databases
./restore-ovn.sh ovn-nb-backup-*.db ovn-sb-backup-*.db

# 5. Verify all services are operational
```

## Automation Example

### Automated Daily Backups (CronJob)

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: ovn-backup
  namespace: openstack-backups
spec:
  schedule: "0 2 * * *"  # 2 AM daily
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

              TIMESTAMP=$(date +%Y%m%d-%H%M%S)
              BACKUP_DIR=/backups/ovn

              mkdir -p ${BACKUP_DIR}

              # Backup NB
              oc exec -n openstack ovsdbserver-nb-0 -c nb-ovsdb -- \
                ovsdb-client backup tcp:127.0.0.1:6641 > ${BACKUP_DIR}/ovn-nb-${TIMESTAMP}.db

              # Backup SB
              oc exec -n openstack ovsdbserver-sb-0 -c sb-ovsdb -- \
                ovsdb-client backup tcp:127.0.0.1:6642 > ${BACKUP_DIR}/ovn-sb-${TIMESTAMP}.db

              # Create archive
              tar -czf ${BACKUP_DIR}/ovn-backup-${TIMESTAMP}.tar.gz \
                -C ${BACKUP_DIR} \
                ovn-nb-${TIMESTAMP}.db \
                ovn-sb-${TIMESTAMP}.db

              # Cleanup old individual files
              rm ${BACKUP_DIR}/ovn-nb-${TIMESTAMP}.db
              rm ${BACKUP_DIR}/ovn-sb-${TIMESTAMP}.db

              # Upload to S3 (example)
              # aws s3 cp ${BACKUP_DIR}/ovn-backup-${TIMESTAMP}.tar.gz s3://my-backups/ovn/

              # Cleanup old backups (keep last 7 days)
              find ${BACKUP_DIR} -name "ovn-backup-*.tar.gz" -mtime +7 -delete

              echo "OVN backup completed: ovn-backup-${TIMESTAMP}.tar.gz"
            volumeMounts:
            - name: backup-storage
              mountPath: /backups
          volumes:
          - name: backup-storage
            persistentVolumeClaim:
              claimName: backup-pvc
          restartPolicy: OnFailure
```

## Verification and Testing

### Verify Backup Integrity

```bash
# Check backup file size (should not be zero)
ls -lh ovn-nb-backup-*.db ovn-sb-backup-*.db

# Verify backup is a valid OVSDB file
file ovn-nb-backup-*.db
# Should show: ovn-nb-backup-*.db: data

# Count records in backup (rough validation)
strings ovn-nb-backup-*.db | grep -c "uuid"
```

### Test Restore on Non-Production

**IMPORTANT**: Always test restore procedures on a non-production cluster first!

1. Deploy a test OpenStack environment
2. Create some test networks and VMs
3. Take an OVN backup
4. Delete some networks
5. Restore the backup
6. Verify the deleted networks are back

## Troubleshooting

### Backup Issues

**Problem**: `ovsdb-client backup` fails with connection error

```bash
# Check if OVN database is running
oc get pods -n openstack | grep ovsdbserver

# Check database logs
oc logs -n openstack ovsdbserver-nb-0 -c nb-ovsdb --tail=50
oc logs -n openstack ovsdbserver-sb-0 -c sb-ovsdb --tail=50

# Test connectivity
oc exec -n openstack ovsdbserver-nb-0 -c nb-ovsdb -- \
  ovsdb-client list-dbs tcp:127.0.0.1:6641
```

**Problem**: Backup file is empty or very small

```bash
# This might indicate database corruption or empty database
# Verify database contents before backup
oc exec -n openstack ovsdbserver-nb-0 -c nb-ovsdb -- ovn-nbctl show
```

### Restore Issues

**Problem**: `ovsdb-client restore` fails

```bash
# Check if database is accepting connections
oc exec -n openstack ovsdbserver-nb-0 -c nb-ovsdb -- \
  ovsdb-client list-dbs tcp:127.0.0.1:6641

# Check for database locks
oc exec -n openstack ovsdbserver-nb-0 -c nb-ovsdb -- \
  ovs-appctl -t /var/run/ovn/ovnnb_db.ctl cluster/status OVN_Northbound
```

**Problem**: Network not working after restore

```bash
# Restart ovn-controller on compute nodes
# (This will reconnect to the restored SB database)
oc delete pod -l app=ovn-controller -n openstack

# Check ovn-northd sync
oc logs -n openstack deployment/ovn-northd --tail=100

# Verify chassis are connected
oc exec -n openstack ovsdbserver-sb-0 -c sb-ovsdb -- ovn-sbctl chassis-list
```

## Best Practices

1. **Regular Backups**
   - Backup OVN databases daily at minimum
   - More frequent backups during maintenance windows
   - Coordinate with stateless control plane backups

2. **Backup Retention**
   - Keep at least 7 days of daily backups
   - Keep weekly backups for 4 weeks
   - Keep monthly backups for 12 months

3. **Backup Storage**
   - Store backups on separate storage from the cluster
   - Encrypt backup files at rest
   - Use immutable storage for compliance

4. **Testing**
   - Test restore procedures quarterly
   - Document restore time (RTO)
   - Verify data integrity after restore

5. **Monitoring**
   - Monitor backup success/failure
   - Alert on backup size changes (might indicate issues)
   - Track backup duration trends

## Limitations

1. **Cluster Size**: Backups are taken from a single replica. For large clusters, this might take significant time.

2. **Consistency**: While OVSDB provides consistent snapshots, there might be in-flight changes during backup.

3. **Downtime**: Restore requires brief network disruption while databases are replaced.

4. **Version Compatibility**: Backups from significantly different OVN versions might not be compatible.

## See Also

- [Stateless Control Plane Backup/Restore](../backup-restore-ctlplane.md) - Main backup/restore procedure
- [OVN Documentation](https://docs.ovn.org/) - Upstream OVN documentation
- [OVSDB Tools](http://www.openvswitch.org/support/dist-docs/ovsdb-client.1.html) - ovsdb-client reference
