# OpenStack Storage Volumes Backup and Restore

This guide covers backing up and restoring persistent volumes for OpenStack services PVCs (like Glance, Cinder, Swift, Manila, ...) using OADP (OpenShift API for Data Protection).

## Overview

**OADP (OpenShift API for Data Protection)** is Red Hat's operator-based backup solution for OpenShift, built on the upstream Velero project. For OpenStack deployments, OADP provides automated backup and restore for persistent storage volumes.

### What OADP Provides

- ✅ **Automated PVC/PV backups** using Restic for file-level backups
- ✅ **Scheduled backups** (daily, weekly, etc.)
- ✅ **Label-based selection** for backup filtering
- ✅ **S3-compatible object storage** integration (MinIO, AWS S3, etc.)
- ✅ **Retention policies** and automatic cleanup

### What OADP Does NOT Handle

OADP complements, but does not replace, other backup methods:

- ❌ **Database backups** - MariaDB and OVN databases use dedicated procedures for transactional consistency
- ❌ **Staged deployment workflows** - Control plane restore requires manual staged deployment
- ❌ **Complex restoration logic** - RabbitMQ credential management requires manual procedures

### Backup Strategy Summary

| Component | Backup Method | Reason |
|-----------|---------------|--------|
| **MariaDB/Galera** | Dedicated procedure | Ensures transactional consistency |
| **OVN Databases** | Dedicated procedure | Ensures database consistency |
| **RabbitMQ** | No backup | Ephemeral, recreated on restore |
| **Glance** | **OADP/Restic** | Image storage needs file-level backup |
| **Cinder** | **OADP/Restic** | Volume backend storage (depends on backend) |
| **Swift** | **OADP/Restic** | Object storage (if using PVC backend) |
| **Manila** | **OADP/Restic** | Share storage (depends on backend) |

## Prerequisites

- OADP operator installed and configured (see [setup-oadp-minio.md](setup-oadp-minio.md))
- MinIO or other S3-compatible storage configured as backup location
- PVCs labeled for backup (see labeling section below)

## Label Convention

Services that need PVC/PV backup should label their PVCs with:

```yaml
openstack.org/backup: "true"
```

PVCs already have an existing `service: <service-name>` label for service identification.

### Labeling Existing PVCs

Add the backup label to PVCs that need to be backed up:

```bash
# Label Galera backup PVCs (database dumps) manually by name
# These contain database dumps from GaleraBackup CRs - NOT the production database PVCs
# List backup PVCs first
oc get pvc -n openstack | grep mysql-backup

# Label each backup PVC by name (until automatic labeling is implemented)
oc label pvc mysql-backup-openstack-backup-openstack -n openstack \
  openstack.org/backup=true

# For multiple Galera instances (e.g., cell1)
oc label pvc mysql-backup-openstack-cell1-backup-openstack-cell1 -n openstack \
  openstack.org/backup=true

# Label Glance PVCs
oc label pvc -n openstack -l service=glance \
  openstack.org/backup=true

# Label Cinder PVCs
oc label pvc -n openstack -l service=cinder \
  openstack.org/backup=true

# Label Swift PVCs (if using PVC backend)
oc label pvc -n openstack -l service=swift \
  openstack.org/backup=true

# Label Manila PVCs (if applicable)
oc label pvc -n openstack -l service=manila \
  openstack.org/backup=true
```

### Verify Labeled PVCs

```bash
# List all PVCs marked for backup
oc get pvc -n openstack -l openstack.org/backup=true

# Show which services have PVCs marked for backup
oc get pvc -n openstack -l openstack.org/backup=true \
  -o custom-columns=NAME:.metadata.name,SERVICE:.metadata.labels.service,SIZE:.spec.resources.requests.storage
```

## Ad-hoc Backup (Manual or from Playbook)

Create a one-time backup by creating a Backup CR directly:

```bash
cat <<EOF | oc apply -f -
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: openstack-volumes-$(date +%Y%m%d-%H%M%S)
  namespace: openshift-adp
spec:
  includedNamespaces:
  - openstack
  labelSelector:
    matchLabels:
      openstack.org/backup: "true"
  defaultVolumesToRestic: true
  storageLocation: velero-1
  ttl: 720h  # 30 days
EOF
```

### Check Backup Progress

```bash
# Watch backup progress
oc get backup -n openshift-adp -w

# Get detailed backup status
oc describe backup openstack-volumes-20260225-140530 -n openshift-adp

# Check for errors
oc get backup openstack-volumes-20260225-140530 -n openshift-adp -o jsonpath='{.status.phase}'
```

### Trigger from Ansible Playbook

Example task to trigger ad-hoc backup:

```yaml
- name: Create OpenStack storage volumes backup
  ansible.builtin.shell: |
    BACKUP_NAME="openstack-volumes-$(date +%Y%m%d-%H%M%S)"
    cat <<EOF | oc apply -f -
    apiVersion: velero.io/v1
    kind: Backup
    metadata:
      name: ${BACKUP_NAME}
      namespace: openshift-adp
    spec:
      includedNamespaces:
      - openstack
      labelSelector:
        matchLabels:
          openstack.org/backup: "true"
      defaultVolumesToRestic: true
      storageLocation: velero-1
      ttl: 720h
    EOF
    echo ${BACKUP_NAME}
  register: backup_result
  changed_when: true

- name: Wait for backup to complete
  ansible.builtin.shell: |
    oc wait --for=jsonpath='{.status.phase}'=Completed \
      backup/{{ backup_result.stdout_lines[-1] }} \
      -n openshift-adp \
      --timeout=60m
  changed_when: false
```

## Scheduled Backup (Automated)

For automated daily backups, create a Schedule CR:

```bash
cat <<EOF | oc apply -f -
apiVersion: velero.io/v1
kind: Schedule
metadata:
  name: openstack-storage-volumes-daily
  namespace: openshift-adp
spec:
  schedule: "0 2 * * *"  # Daily at 2 AM
  template:
    includedNamespaces:
    - openstack
    labelSelector:
      matchLabels:
        openstack.org/backup: "true"
    defaultVolumesToRestic: true
    storageLocation: velero-1
    ttl: 168h  # 7 days retention
EOF
```

### Verify Schedule

```bash
# Check schedule status
oc get schedule -n openshift-adp

# View recent backups from schedule
oc get backup -n openshift-adp -l velero.io/schedule-name=openstack-storage-volumes-daily
```

## Restore Storage Volumes

### Restore All Storage Volumes

```bash
cat <<EOF | oc apply -f -
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-volumes-restore
  namespace: openshift-adp
spec:
  backupName: openstack-volumes-20260225-140530
  includedNamespaces:
  - openstack
  restorePVs: true
EOF
```

### Check Restore Progress

```bash
# Watch restore progress
oc get restore -n openshift-adp -w

# Get detailed restore status
oc describe restore openstack-volumes-restore -n openshift-adp

# Verify PVCs were restored
oc get pvc -n openstack -l openstack.org/backup=true
```

## Integration with Overall Backup Strategy

Storage volume backups are integrated into the complete OpenStack backup/restore workflow:

- **Backup**: Storage volumes are backed up as **Step 9** in the control plane backup procedure
- **Restore**: Storage volumes are restored as **Step 12** in the control plane restore procedure (after database restore, before resuming deployment)

For the complete integrated backup/restore workflow, see:
- [Control Plane Backup/Restore](backup-restore-ctlplane.md) - Includes storage volumes (Step 9 backup, Step 12 restore)
- [Data Plane Backup/Restore](backup-restore-dataplane.md) - NodeSets, NetConfig, IP allocations

## Troubleshooting

For OADP storage volume backup/restore troubleshooting, see the [Backup/Restore Troubleshooting Guide](backup-restore-troubleshooting.md#oadp-storage-volume-backuprestore-issues).

Common issues:
- Backup stuck in "InProgress"
- Backup failed
- PVCs not being backed up
- Restore creates new PVCs
- BackupStorageLocation unavailable

## Performance Considerations

### Backup Duration

Restic backup time depends on:
- PVC size
- Amount of data
- Data change rate
- Network bandwidth to MinIO

Example estimates:
- 10GB Glance images: ~5-10 minutes
- 100GB Cinder volumes: ~30-60 minutes
- 1TB Swift storage: ~3-6 hours

### Optimize Backup Performance

1. **Schedule during low-traffic periods** (e.g., 2-4 AM)
2. **Use incremental backups** (Restic only backs up changed data)
3. **Adjust Restic resource limits**:

```yaml
spec:
  configuration:
    restic:
      enable: true
      podConfig:
        resourceAllocations:
          limits:
            cpu: "2"
            memory: 4Gi
          requests:
            cpu: "1"
            memory: 2Gi
```

4. **Split large volumes** into separate backups if needed

## References

- [OADP Setup with MinIO](setup-oadp-minio.md)
- [Control Plane Backup/Restore](backup-restore-ctlplane.md)
- [Data Plane Backup/Restore](backup-restore-dataplane.md)
- [Backup/Restore Troubleshooting](backup-restore-troubleshooting.md)
- [OADP Documentation](https://docs.openshift.com/container-platform/latest/backup_and_restore/application_backup_and_restore/oadp-intro.html)
- [Velero Backup API](https://velero.io/docs/main/api-types/backup/)

---

## Appendix: Alternative PVC Backup Methods

⚠️ **WARNING**: The methods described in this appendix are **NOT drop-in replacements** for OADP. They require **different restore workflows** and are **highly dependent on your storage backend and vendor tooling**. The integrated OADP procedure in [backup-restore-ctlplane.md](backup-restore-ctlplane.md) will NOT work with these alternative methods.

### CSI Volume Snapshots

**Backup Method**: Use Kubernetes CSI VolumeSnapshot resources if your storage class supports CSI snapshots.

**Requirements**:
- Storage class with CSI snapshot support
- VolumeSnapshotClass configured
- CSI driver installed and configured

**Backup Example**:
```bash
cat <<EOF | oc apply -f -
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: glance-data-snapshot-$(date +%Y%m%d)
  namespace: openstack
spec:
  source:
    persistentVolumeClaimName: glance-data
  volumeSnapshotClassName: csi-snapshot-class
EOF
```

**Restore Workflow - Option 1 (Pre-create from snapshot)**:
```bash
# At Step 12 in restore procedure, create PVCs from snapshots
cat <<EOF | oc apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: glance-data
  namespace: openstack
  labels:
    service: glance
spec:
  dataSource:
    name: glance-data-snapshot-20260225
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
  storageClassName: csi-storage-class
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 100Gi
EOF

# Then resume deployment (Step 14)
# Services find PVCs already populated
```

**Restore Workflow - Option 2 (Restore after service creation)**:
```bash
# 1. Resume deployment - services create empty PVCs and start

# 2. Scale down services via OpenStackControlPlane CR
oc patch openstackcontrolplane openstack -n openstack --type=merge -p '
spec:
  glance:
    template:
      glanceAPIs:
        default:
          replicas: 0
'

# Wait for pods to terminate
oc wait --for=delete pod -l service=glance -n openstack --timeout=60s

# 3. Delete empty PVCs
oc delete pvc glance-data -n openstack

# 4. Create new PVCs from snapshots
cat <<EOF | oc apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: glance-data
  namespace: openstack
  labels:
    service: glance
spec:
  dataSource:
    name: glance-data-snapshot-20260225
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
  storageClassName: csi-storage-class
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 100Gi
EOF

# 5. Scale up services via OpenStackControlPlane CR
oc patch openstackcontrolplane openstack -n openstack --type=merge -p '
spec:
  glance:
    template:
      glanceAPIs:
        default:
          replicas: 1
'
```

**Limitations**:
- Snapshots are typically local to the cluster/storage array
- Cross-cluster restore may not be possible
- Retention policies depend on storage backend
- Snapshot performance varies by storage vendor

---

### Storage Array Snapshots

**Backup Method**: Use your storage vendor's native snapshot capabilities.

**Examples**:
- **NetApp**: ONTAP snapshots, SnapMirror
- **Dell EMC**: PowerStore, Unity, VMAX snapshots
- **Pure Storage**: FlashArray, FlashBlade snapshots
- **Red Hat Ceph**: RBD snapshots
- **VMware vSAN**: vSAN snapshots

**Restore Workflow**:

The restore procedure is **highly vendor-specific** and typically requires:

1. **Stop services** before restoring (to ensure data consistency):
   ```bash
   # Example: Scale down Glance service via OpenStackControlPlane CR
   oc patch openstackcontrolplane openstack -n openstack --type=merge -p '
   spec:
     glance:
       template:
         glanceAPIs:
           default:
             replicas: 0
   '

   # Wait for pods to terminate
   oc wait --for=delete pod -l service=glance -n openstack --timeout=60s
   ```

2. **Use vendor tools** to restore storage at the array level:
   - NetApp: `volume clone create`, `snapmirror restore`
   - Dell EMC: EMC Unisphere, PowerStore Manager
   - Pure Storage: Purity GUI, REST API
   - Ceph: `rbd snap rollback`, `rbd clone`

3. **Verify PVs/PVCs** are bound after storage restore:
   ```bash
   oc get pvc -n openstack
   oc get pv
   ```

4. **Restart services**:
   ```bash
   # Example: Scale up Glance service via OpenStackControlPlane CR
   oc patch openstackcontrolplane openstack -n openstack --type=merge -p '
   spec:
     glance:
       template:
         glanceAPIs:
           default:
             replicas: 1
   '
   ```

**Important Notes**:
- Requires direct access to storage management tools
- May require storage administrator involvement
- Snapshot location (array-local vs replicated) affects DR capabilities
- Cross-cluster restore depends on array replication features
- Consult vendor documentation for specific procedures

---

### File-Level Backup Tools

**Backup Method**: Use traditional backup tools (rsync, tar, restic) by exec'ing into pods.

**Backup Example**:
```bash
# Backup from running pod
oc exec -n openstack glance-api-0 -- tar czf /tmp/glance-backup.tar.gz /var/lib/glance

# Copy backup out of pod
oc cp -n openstack glance-api-0:/tmp/glance-backup.tar.gz ./glance-backup-$(date +%Y%m%d).tar.gz

# Clean up temp file
oc exec -n openstack glance-api-0 -- rm /tmp/glance-backup.tar.gz
```

**Restore Workflow**:
```bash
# 1. Resume deployment - services create empty PVCs and start

# 2. Copy backup into pod
oc cp ./glance-backup-20260225.tar.gz -n openstack glance-api-0:/tmp/glance-backup.tar.gz

# 3. Extract data in pod
oc exec -n openstack glance-api-0 -- tar xzf /tmp/glance-backup.tar.gz -C /

# 4. Clean up temp file
oc exec -n openstack glance-api-0 -- rm /tmp/glance-backup.tar.gz

# 5. Restart pod to pick up restored data
oc delete pod -n openstack glance-api-0
```

**Limitations**:
- Requires pods to be running (cannot restore before services start)
- Data consistency depends on application state during backup
- Manual process for each service
- No automation or scheduling
- Error-prone for large datasets
- Slow for large volumes

---

### Comparison Summary

| Method | Pre-create PVCs | Integrated Workflow | Cross-Cluster | Automation | Vendor Dependency |
|--------|----------------|---------------------|---------------|------------|-------------------|
| **OADP** | ✅ Yes | ✅ Yes | ✅ Yes (via S3) | ✅ High | ❌ Minimal |
| **CSI Snapshots** | ⚠️ Optional | ⚠️ Partial | ❌ Usually No | ⚠️ Medium | ⚠️ CSI driver |
| **Storage Array** | ❌ No | ❌ No | ⚠️ Depends | ❌ Low | ✅ High |
| **File-Level** | ❌ No | ❌ No | ✅ Yes | ❌ Minimal | ❌ Minimal |

**Recommendation**: Use OADP for production environments unless you have specific requirements that necessitate alternative methods.
