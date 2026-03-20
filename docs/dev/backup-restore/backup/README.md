# OpenStack Backup CRs

This directory contains OADP Backup CR templates and playbook for backing up
OpenStack environments.

The `templates/` directory contains Jinja2 templates for Velero Backup CRs.
The backup playbook (`backup-openstack.yaml`) renders these templates with the
appropriate variables and applies them in order, ensuring that what gets tested
is exactly what gets documented.

## Backup Approach

We use a two-backup strategy:

1. **PVC backup** (`templates/backup-pvcs.yaml.j2`) — PVCs with CSI snapshots
   - Filters at backup time using `labelSelector`
   - Only includes PVCs labeled with `backup.openstack.org/backup: "true"`
   - Supports Data Mover (`snapshotMoveData: true`, default) to upload snapshot
     data to the BackupStorageLocation (S3/MinIO) via Kopia, enabling restore
     even after total cluster loss

2. **Resources backup** (`templates/backup-resources.yaml.j2`) — everything except PVCs
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

# Without Data Mover (local snapshots only):
ansible-playbook docs/dev/backup-restore/backup/backup-openstack.yaml \
  -e snapshot_move_data=false

# Skip all confirmation prompts (non-interactive):
ansible-playbook docs/dev/backup-restore/backup/backup-openstack.yaml \
  -e auto_ack=true
```

### Manual

The templates in `templates/` use Jinja2 variables. For manual backups,
replace the variables with your values or render them with the playbook.

```bash
# Set a common timestamp for all backup artifacts
BACKUP_TS=$(date +%Y%m%d-%H%M%S)

# 1. Trigger Galera database dumps (required before PVC backup)
#    Creates jobs from GaleraBackup cronjobs and passes the timestamp
#    so DB dump filenames match the backup name.
for CRONJOB in $(oc get cronjob -n openstack -l app=galera -o jsonpath='{.items[*].metadata.name}'); do
  JOB_NAME="${CRONJOB}-${BACKUP_TS}"
  oc -n openstack create job --from=cronjob/${CRONJOB} ${JOB_NAME} \
    --dry-run=client -o json | \
    jq '.spec.template.spec.containers[0].env += [{"name":"BACKUP_TIMESTAMP","value":"'${BACKUP_TS}'"}]' | \
    oc -n openstack create -f -
done

# Wait for all backup jobs to complete
oc -n openstack wait --for=condition=complete job -l app=galera --timeout=10m

# 2. OADP PVC backup (CSI snapshots of labeled PVCs)
#    See templates/backup-pvcs.yaml.j2 for the full template.
#    Render with: ansible -m template -a "src=templates/backup-pvcs.yaml.j2 dest=/tmp/backup-pvcs.yaml" localhost -e backup_name_suffix=${BACKUP_TS} ...
oc apply -f /tmp/backup-pvcs.yaml
oc wait --for=jsonpath='{.status.phase}'=Completed backup/openstack-backup-pvcs-${BACKUP_TS} -n openshift-adp --timeout=30m

# 3. OADP resources backup
#    See templates/backup-resources.yaml.j2 for the full template.
oc apply -f /tmp/backup-resources.yaml
oc wait --for=jsonpath='{.status.phase}'=Completed backup/openstack-backup-resources-${BACKUP_TS} -n openshift-adp --timeout=30m

# Check backup status
oc get backup -n openshift-adp -o custom-columns=NAME:.metadata.name,PHASE:.status.phase
```

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

All templates accept variables for customization. Key variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `openstack_namespace` | `openstack` | Target namespace |
| `oadp_namespace` | `openshift-adp` | OADP namespace |
| `storage_location` | `velero-1` | Velero storage location |
| `backup_ttl` | `720h` | Backup retention (30 days) |
| `snapshot_move_data` | `true` | Upload snapshots to BSL via Data Mover |
| `auto_ack` | `false` | Skip confirmation prompts |

## Verification

```bash
# List all backups
oc get backup -n openshift-adp

# Check backup details and status
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

## See Also

- Restore CRs: `docs/dev/backup-restore/restore/`
- Implementation guide: `docs/dev/backup-restore/backup-restore-controller-implementation.md`
- Design document: `docs/dev/backup-restore/backup-restore-controller-design.md`
