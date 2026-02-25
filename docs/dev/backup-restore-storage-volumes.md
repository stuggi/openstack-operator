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
openstack.org/backup-volume: "true"
```

PVCs already have an existing `service: <service-name>` label for service identification.

### Labeling Existing PVCs

Add the backup label to PVCs that need to be backed up:

```bash
# Label Glance PVCs
oc label pvc -n openstack -l service=glance \
  openstack.org/backup-volume=true

# Label Cinder PVCs
oc label pvc -n openstack -l service=cinder \
  openstack.org/backup-volume=true

# Label Swift PVCs (if using PVC backend)
oc label pvc -n openstack -l service=swift \
  openstack.org/backup-volume=true

# Label Manila PVCs (if applicable)
oc label pvc -n openstack -l service=manila \
  openstack.org/backup-volume=true
```

### Verify Labeled PVCs

```bash
# List all PVCs marked for backup
oc get pvc -n openstack -l openstack.org/backup-volume=true

# Show which services have PVCs marked for backup
oc get pvc -n openstack -l openstack.org/backup-volume=true \
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
      openstack.org/backup-volume: "true"
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
          openstack.org/backup-volume: "true"
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
        openstack.org/backup-volume: "true"
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
oc get pvc -n openstack -l openstack.org/backup-volume=true
```

## Integration with Overall Backup Strategy

Storage volume backups complement other backup procedures:

### Complete Backup Workflow

```bash
# 1. Backup Control Plane CRs and configuration
ansible-playbook backup-openstack-ctlplane.yaml -e openstack_namespace=openstack

# 2. Backup databases using dedicated procedures
# (MariaDB, OVN - handled by respective teams)

# 3. Backup storage volumes with OADP (ad-hoc)
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
      openstack.org/backup-volume: "true"
  defaultVolumesToRestic: true
  storageLocation: velero-1
EOF

# 4. Backup Data Plane
ansible-playbook backup-openstack-dataplane.yaml -e openstack_namespace=openstack
```

### Complete Restore Workflow

```bash
# 1. Restore Control Plane (with staged deployment)
ansible-playbook restore-openstack-ctlplane.yaml \
  -e openstack_namespace=openstack \
  -e backup_file=backups/openstack-ctlplane-backup-*.tar.gz

# 2. Restore databases during infrastructure-only stage
# (MariaDB, OVN - handled by respective teams, see restore-openstack-ctlplane.yaml Step 11)

# 3. Restore storage volumes with OADP
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

# 4. Resume OpenStackControlPlane deployment
oc annotate openstackcontrolplane openstack-galera-network-isolation \
  -n openstack core.openstack.org/deployment-stage-

# 5. Restore Data Plane
ansible-playbook restore-openstack-dataplane.yaml \
  -e openstack_namespace=openstack \
  -e backup_file=backups/openstack-dataplane-backup-*.tar.gz
```

## Alternative PVC Backup Methods

While OADP is recommended for automated backups, you can use alternative methods:

### Manual CSI Snapshots

If your storage class supports CSI snapshots:

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

### Storage Array Snapshots

Use your storage vendor's snapshot capabilities (NetApp, Dell EMC, Pure Storage, etc.).

### File-Level Backup Tools

Use tools like rsync, tar, or restic directly:

```bash
# Backup from pod
oc exec glance-api-0 -- tar czf /tmp/data.tar.gz /var/lib/glance
oc cp glance-api-0:/tmp/data.tar.gz ./glance-backup.tar.gz
```

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
