# OpenStack Backup CRs

This directory contains OADP Backup CRs for backing up OpenStack environments.

## Backup Approach

We use a two-backup strategy:

1. **backup-openstack-pvcs.yaml** - PVCs only (with CSI snapshots)
   - Filters at backup time using `labelSelector`
   - Only includes PVCs labeled with `openstack.org/backup: "true"`
   - Includes: Glance image storage PVCs, GaleraBackup PVCs

2. **backup-openstack-resources.yaml** - Everything except PVCs
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

## Quick Start

### Automated (Recommended)

Use the backup playbook to orchestrate the full backup flow:

```bash
ansible-playbook docs/dev/webhook/backup/backup-openstack.yaml
```

The playbook runs three steps:
1. **Trigger Galera DB dumps** — creates jobs from GaleraBackup cronjobs
2. **OADP PVC backup** — CSI snapshots of PVCs labeled with `openstack.org/backup=true`
3. **OADP resources backup** — all resources in the namespace except PVCs

PVC backup labels are set automatically by service operators (glance-operator, mariadb-operator).

Override defaults with extra vars:
```bash
ansible-playbook docs/dev/webhook/backup/backup-openstack.yaml \
  -e openstack_namespace=openstack \
  -e storage_location=velero-1 \
  -e backup_ttl=168h
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

- Glance local PVCs (image storage)
- GaleraBackup PVCs (database backups)
- Any other PVCs labeled with `openstack.org/backup: "true"`

### backup-openstack-resources

**Order 10 - Foundation:**
- Secrets without ownerRefs (user-provided)
- ConfigMaps without ownerRefs (user-provided)
- NetworkAttachmentDefinitions without ownerRefs

**Order 20 - Infrastructure:**
- OpenStackVersion
- OpenStackBackupConfig
- MariaDBAccount
- MariaDBDatabase

**Order 30 - ControlPlane:**
- OpenStackControlPlane

**Order 40 - Backup Config:**
- GaleraBackup

**Order 60 - DataPlane:**
- OpenStackDataPlaneNodeSet

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

# Check backup details
oc get backup openstack-backup-resources -n openshift-adp -o yaml
oc get backup openstack-backup-pvcs -n openshift-adp -o yaml

# Check what was backed up
oc get backup openstack-backup-resources -n openshift-adp \
  -o jsonpath='{.status.progress}' | jq

# List volume snapshots
oc get volumesnapshot -n openstack
oc get volumesnapshotcontent
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

- Restore CRs: `docs/dev/webhook/restore/`
- Implementation guide: `docs/dev/webhook/backup-restore-webhook-implementation.md`
- Design document: `docs/dev/webhook/backup-restore-webhook-design.md`
