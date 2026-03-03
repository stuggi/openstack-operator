# OpenStack Storage Volumes Backup and Restore

This guide covers backing up and restoring persistent volumes for OpenStack services PVCs (like Glance, Cinder, Swift, Manila, ...) using OADP (OpenShift API for Data Protection).

## Overview

**OADP (OpenShift API for Data Protection)** is Red Hat's operator-based backup solution for OpenShift, built on the upstream Velero project. For OpenStack deployments, OADP provides automated backup and restore for persistent storage volumes.

### What OADP Provides

- ✅ **Automated PVC/PV backups** using CSI Volume Snapshots (requires CSI-capable StorageClass)
- ✅ **Storage-level snapshots** that restore data before pods start (critical for staged deployment)
- ✅ **Scheduled backups** (daily, weekly, etc.)
- ✅ **Label-based selection** for backup filtering
- ✅ **S3-compatible object storage** integration (MinIO, AWS S3, etc.)
- ✅ **Retention policies** and automatic cleanup

**Note:** Filesystem backup (Restic/Kopia) is not compatible with the staged deployment restore approach because it requires pods to mount PVCs during restore.

### What OADP Does NOT Handle

OADP complements, but does not replace, other backup methods:

- ❌ **Database backups** - MariaDB and OVN databases use dedicated procedures for transactional consistency
- ❌ **Staged deployment workflows** - Control plane restore requires manual staged deployment
- ❌ **Complex restoration logic** - RabbitMQ credential management requires manual procedures

### Backup Strategy Summary

| Component | Backup Method | Reason |
|-----------|---------------|--------|
| **MariaDB/Galera** | Dedicated procedure + OADP (backup PVCs only) | Ensures transactional consistency via dumps; OADP backs up dump files |
| **OVN Databases** | Dedicated procedure | Ensures database consistency |
| **RabbitMQ** | No backup | Ephemeral, recreated on restore |
| **Glance** | **OADP/CSI Snapshots** | Image storage PVCs backed up at storage layer |
| **Cinder** | **OADP/CSI Snapshots** | Volume backend storage PVCs (depends on backend) |
| **Swift** | **OADP/CSI Snapshots** | Object storage PVCs (if using PVC backend) |
| **Manila** | **OADP/CSI Snapshots** | Share storage PVCs (depends on backend) |

## Prerequisites

**CRITICAL REQUIREMENT: CSI Volume Snapshot Support**

The backup/restore procedure with staged deployment requires a StorageClass that supports **CSI Volume Snapshots**. This is because:
- The restore procedure uses infrastructure-only staging (no service pods running)
- PVCs must be restored with data **before** pods start
- Filesystem backup (Restic/Kopia) requires pods to mount PVCs during restore
- CSI snapshots restore data at the storage layer without needing pods

**Supported StorageClasses:**
- ✅ LVM storage (TopoLVM/LVMS) - supports CSI snapshots
- ✅ Ceph RBD - supports CSI snapshots
- ✅ Most cloud provider storage (AWS EBS, Azure Disk, GCP PD) - support CSI snapshots
- ❌ Local storage (node-local directories) - **does NOT support CSI snapshots**

**Required Setup:**
- OADP operator installed and configured (see [setup-oadp-minio.md](setup-oadp-minio.md))
- MinIO or other S3-compatible storage configured as backup location
- **VolumeSnapshotClass configured** for your storage provider (see below)
- PVCs labeled for backup (see labeling section below)
- **CRITICAL for LVMS:** PVC sizes must use binary units (Gi, Mi, Ti) not decimal units (G, M, T). Using decimal units causes LVM extent rounding issues that break CSI snapshots. Example: use `5Gi` not `5G`

**Verify CSI Snapshot Support:**

```bash
# Check if VolumeSnapshotClass exists
oc get volumesnapshotclass

# Check if any VolumeSnapshotClass has the required velero label
oc get volumesnapshotclass -l velero.io/csi-volumesnapshot-class=true
```

**Create VolumeSnapshotClass for OADP:**

OADP requires a VolumeSnapshotClass with the label `velero.io/csi-volumesnapshot-class: "true"`. If your storage operator (like LVMS) manages the VolumeSnapshotClass and prevents manual labeling, you must create a separate VolumeSnapshotClass for OADP:

```bash
# For LVM storage (TopoLVM/LVMS), create a dedicated VolumeSnapshotClass for OADP:
cat <<EOF | oc apply -f -
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: lvms-velero
  labels:
    velero.io/csi-volumesnapshot-class: "true"
  annotations:
    snapshot.storage.kubernetes.io/is-default-class: "false"
driver: topolvm.io
deletionPolicy: Retain
EOF

# Verify it was created
oc get volumesnapshotclass lvms-velero --show-labels
```

**Important Notes:**

- **deletionPolicy: Retain** - Red Hat recommendation to preserve snapshots even if VolumeSnapshot objects are deleted
- **velero.io/csi-volumesnapshot-class label** - Required for OADP to identify which VolumeSnapshotClass to use
- **Only one VolumeSnapshotClass per driver** should have the velero label. If your operator-managed class has the label, remove it: `oc label volumesnapshotclass <name> velero.io/csi-volumesnapshot-class-`
- **Operator-managed classes** - Some operators (like LVMS) manage VolumeSnapshotClasses and may revert manual label changes. In this case, create a separate class as shown above.

**Note:** If your storage does not support CSI snapshots, you cannot use the staged deployment restore approach. See [backup-restore-ctlplane.md](backup-restore-ctlplane.md) for alternative restore strategies.

**CSI Snapshots vs Provider Snapshots:**

OADP supports two snapshot mechanisms:
1. **CSI Volume Snapshots** - Uses Kubernetes CSI VolumeSnapshot API (what we use for LVMS/TopoLVM, Ceph RBD, etc.)
2. **Provider Snapshots** - Uses cloud provider APIs (AWS EBS, Azure Disk, GCP PD)

**CRITICAL: DataProtectionApplication Configuration**

For CSI snapshots to work, the DataProtectionApplication (DPA) must **NOT** include `snapshotLocations`. The `snapshotLocations` field is only for cloud provider snapshots. If present in the DPA, OADP will try to use provider snapshots instead of CSI, which fails for local storage.

**Correct DPA configuration (no snapshotLocations):**
```yaml
apiVersion: oadp.openshift.io/v1alpha1
kind: DataProtectionApplication
spec:
  backupLocations:
  - velero:
      provider: aws
      objectStorage:
        bucket: velero
  # snapshotLocations: NOT INCLUDED for CSI snapshots
```

**Required Backup CR fields for CSI snapshots:**
- `snapshotVolumes: true` - Enable volume snapshots
- `defaultVolumesToFsBackup: false` - Disable filesystem backup (Restic/Kopia)
- `volumeSnapshotLocations: []` - Optional but recommended to explicitly disable provider snapshots

**Note:** Even if you set `volumeSnapshotLocations: []` in the Backup CR, if the DPA has `snapshotLocations` configured, OADP may override your setting. The safest approach is to not configure `snapshotLocations` in the DPA at all.

**Troubleshooting CSI Snapshot Configuration:**

If CSI snapshots are not being created, run the diagnostic script to check all prerequisites:

```bash
docs/dev/scripts/diagnose-csi-snapshots.sh
```

This checks:
- VolumeSnapshotClass with velero label
- DataProtectionApplication configuration:
  - **CSI plugin in defaultPlugins (CRITICAL!)** - Most common issue
  - No snapshotLocations configured (critical!)
  - BackupLocations configured
- Velero pod status and CSI plugin loading in logs
- BackupStorageLocation availability
- PVCs labeled for backup
- StorageClass and VolumeSnapshotClass driver matching
- Recent backup CSI snapshot statistics

The script will provide specific fix commands for any issues found.

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
  snapshotVolumes: true
  defaultVolumesToFsBackup: false
  volumeSnapshotLocations: []
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

### Validate Backup with Automated Script

Use the validation script to check if the backup completed successfully with CSI snapshots:

```bash
# Validate the latest backup
docs/dev/scripts/validate-oadp-backup.sh

# Validate a specific backup
docs/dev/scripts/validate-oadp-backup.sh openstack-volumes-20260303-093007

# With custom namespaces
OADP_NAMESPACE=openshift-adp OPENSTACK_NAMESPACE=openstack docs/dev/scripts/validate-oadp-backup.sh
```

The script checks:
- Backup phase (Completed/InProgress/Failed)
- Warnings and errors
- CSI snapshot statistics (attempted vs completed)
- VolumeSnapshot status (normally cleaned up after backup - expected)
- VolumeSnapshotContent status (actual snapshot data - should persist)
- VolumeSnapshotClass configuration
- Backup CR spec validation
- Expected PVCs with backup label

**Note:** VolumeSnapshots are transient resources that OADP creates during backup and cleans up afterward. The actual snapshot data persists in VolumeSnapshotContents (if `deletionPolicy: Retain`) or is moved to S3 (if `snapshotMoveData: true`). Don't be alarmed if VolumeSnapshots are gone after backup completes.

Exit codes:
- `0` - Backup successful with CSI snapshots
- `1` - Backup failed or has issues
- `2` - Backup still in progress

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
      snapshotVolumes: true
      defaultVolumesToFsBackup: false
      volumeSnapshotLocations: []
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
    snapshotVolumes: true
    defaultVolumesToFsBackup: false
    volumeSnapshotLocations: []
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
- **LVMS snapshot fails with "requested size is smaller than source logical volume"** - See detailed fix below

### Fix: LVMS Snapshot Size Mismatch Error

If you see the error `requested size is smaller than source logical volume`, this means your PVC is using decimal units (G, M, T) instead of binary units (Gi, Mi, Ti). The LVM extent rounding causes the actual logical volume to be slightly larger than the requested size, breaking snapshots.

**To fix existing PVCs:**

```bash
# 1. Edit the PVC to change storage units from decimal to binary
# Example: Change "5G" to "5Gi"
oc edit pvc <pvc-name> -n openstack

# In the editor, change:
#   spec:
#     resources:
#       requests:
#         storage: 5G
# To:
#   spec:
#     resources:
#       requests:
#         storage: 5Gi

# 2. Wait for resize to complete
# For PVCs with pods currently running (e.g., Glance, Cinder):
#   - Resize happens automatically, no pod restart needed
#   - Kubernetes and CSI driver handle online resize

# For PVCs with NO pods running (e.g., Galera backup PVCs):
#   - Resize is applied when the PVC is next mounted
#   - Either wait for next backup job or trigger one manually:
oc create job manual-backup --from=cronjob/<backup-cronjob-name> -n openstack

# 3. Verify the PVC capacity was updated
oc get pvc <pvc-name> -n openstack -o jsonpath='{.status.capacity.storage}'
echo ""
```

**Prevention:** Always use binary units (Gi, Mi, Ti) when creating PVCs for LVMS storage, especially if you plan to use CSI snapshots.

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
