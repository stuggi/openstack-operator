# OpenStack Backup CRs

This directory contains OADP Backup CRs for backing up OpenStack environments.

## Backup Approach

We use a two-backup strategy:

1. **backup-openstack-pvcs.yaml** - PVCs only (with CSI snapshots, local only)
   - Filters at backup time using `labelSelector`
   - Only includes PVCs labeled with `backup.openstack.org/backup: "true"`

2. **backup-openstack-pvcs-datamover.yaml** - PVCs with Data Mover (uploads to S3/MinIO)
   - Same as above but with `snapshotMoveData: true`
   - Uploads CSI snapshot data to BackupStorageLocation via Kopia
   - Enables restore even after total cluster loss
   - Requires OADP DPA with `nodeAgent` enabled

3. **backup-openstack-resources.yaml** - Everything except PVCs
   - Backs up all resources in the namespace
   - Excludes PVCs (backed up separately)
   - Includes: CRs, Secrets, ConfigMaps, NADs, etc.

## Why Two Backups?

- **Flexibility**: Allows selective restore (e.g., restore only PVCs or only CRs)
- **PVC Filtering**: Only backup PVCs we explicitly labeled (saves storage costs)
- **Restore Control**: Can restore in stages using `backup-restore-order` labels

## Prerequisites

- OADP operator installed in `openshift-adp` namespace
- Velero storage location configured (named `velero-1`)
- CSI snapshot capability for PVC backups
- OpenStack deployed in `openstack` namespace with backup labels applied
- GaleraBackup CRs configured for each Galera database instance (main cell and
  any additional cells). The CRs define cronjobs that dump databases to PVCs.
  **Note:** When using LVM storage, specify PVC sizes with binary suffixes (e.g.,
  `5Gi` not `5G`) to avoid size rounding issues.

## Quick Start

### Automated (Recommended)

Use the backup playbook to orchestrate the full backup flow:

```bash
ansible-playbook docs/dev/backup-restore/backup/backup-openstack.yaml
```

The playbook runs three steps:
1. **Trigger Galera DB dumps** — creates jobs from GaleraBackup cronjobs
2. **OADP PVC backup** — CSI snapshots of PVCs labeled with `backup.openstack.org/backup=true`
3. **OADP resources backup** — all resources in the namespace except PVCs

PVC backup labels are set automatically by service operators (glance-operator, mariadb-operator).

Override defaults with extra vars:
```bash
ansible-playbook docs/dev/backup-restore/backup/backup-openstack.yaml \
  -e openstack_namespace=openstack \
  -e storage_location=velero-1 \
  -e backup_ttl=168h

# With Data Mover (upload snapshots to S3/MinIO):
ansible-playbook docs/dev/backup-restore/backup/backup-openstack.yaml \
  -e snapshot_move_data=true
```

### Manual

Apply the backup CRs directly:

```bash
# Create both backups
oc apply -f backup-openstack-resources.yaml
oc apply -f backup-openstack-pvcs.yaml

# Monitor backup progress
oc get backup -n openshift-adp -w

# Check backup status
oc describe backup openstack-backup-resources -n openshift-adp
oc describe backup openstack-backup-pvcs -n openshift-adp
```

**Note:** When running manually, trigger Galera DB dumps first to ensure fresh database backups on the PVCs before the CSI snapshot.

## Backup Contents

### backup-openstack-pvcs

All PVCs labeled with `backup.openstack.org/backup: "true"` are backed up via
CSI volume snapshots. Service operators set this label automatically on PVCs
they manage (e.g., Glance image storage, GaleraBackup database dumps).

### backup-openstack-resources

All resources in the namespace except PVCs. This includes CRs, Secrets,
ConfigMaps, NetworkAttachmentDefinitions, and any other namespace-scoped
resources. Restore ordering is controlled by `backup.openstack.org/restore-order`
labels on the backed-up resources (see `docs/dev/backup-restore/restore/README.md`).

## Customization

### Change Target Namespace

Edit the `includedNamespaces` field in both files:

```yaml
spec:
  includedNamespaces:
  - your-namespace-here
```

### Change Storage Location

Edit the `storageLocation` field:

```yaml
spec:
  storageLocation: your-storage-location
```

### Change Retention

Edit the `ttl` field (default: 720h = 30 days):

```yaml
spec:
  ttl: 168h  # 7 days
```

## Verification

```bash
# List all backups
oc get backup -n openshift-adp

# Check backup details and status
oc get backup openstack-backup-resources -n openshift-adp -o yaml
oc get backup openstack-backup-pvcs -n openshift-adp -o yaml

# Verify backup phase is "Completed"
oc get backup -n openshift-adp -o custom-columns=NAME:.metadata.name,PHASE:.status.phase,ERRORS:.status.errors,WARNINGS:.status.warnings

# List volume snapshots created by backup
oc get volumesnapshot -n openstack
oc get volumesnapshotcontent
```

### Data Mover Verification

When using `snapshotMoveData: true`, verify that all PVC data was uploaded
to the BackupStorageLocation:

```bash
# Check that DataUpload CRs completed for each PVC
oc get datauploads -n openshift-adp -l velero.io/backup-name=openstack-backup-pvcs
oc get datauploads -n openshift-adp -o custom-columns=NAME:.metadata.name,PHASE:.status.phase,BYTES:.status.progress.totalBytes

# All DataUploads should show Phase=Completed
# If any show Phase=Failed, check node-agent logs:
oc logs -n openshift-adp -l name=node-agent --tail=50
```

## Scheduling Backups

To schedule regular backups, create a Velero Schedule CR:

```yaml
apiVersion: velero.io/v1
kind: Schedule
metadata:
  name: openstack-daily-backup
  namespace: openshift-adp
spec:
  schedule: "0 2 * * *"  # Daily at 2 AM
  template:
    # Copy spec from backup-openstack-resources.yaml
    includedNamespaces:
    - openstack
    # ... rest of backup spec
```

## See Also

- Restore CRs: `docs/dev/backup-restore/restore/`
- Implementation guide: `docs/dev/backup-restore/backup-restore-controller-implementation.md`
- Design document: `docs/dev/backup-restore/backup-restore-controller-design.md`
