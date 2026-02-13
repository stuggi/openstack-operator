# OADP Integration with OpenStack Backup/Restore

## Overview

This document describes how **OADP (OpenShift API for Data Protection)** can complement the OpenStack backup/restore procedures documented in [backup-restore-ctlplane.md](backup-restore-ctlplane.md) and [backup-restore-dataplane.md](backup-restore-dataplane.md).

**OADP** is Red Hat's operator-based backup solution for OpenShift, built on the upstream Velero project. It provides declarative, automated backup and restore capabilities for Kubernetes resources and persistent volumes.

## Key Understanding

OADP is **NOT a replacement** for the existing OpenStack backup/restore procedures. It is a **complementary tool** that is particularly valuable for **automating persistent volume backups**.

### What OADP Does Well

- ✅ **Automated PVC/PV backups** using CSI volume snapshots or Restic file-level backups
- ✅ **Scheduled backups** (CronJob-style)
- ✅ **Centralized backup management** via Custom Resources
- ✅ **S3-compatible object storage** integration
- ✅ **Volume snapshot lifecycle management**
- ✅ **User flexibility** - Can be replaced with any PVC backup method

### What OADP Does NOT Solve

- ❌ **Cannot restore Kubernetes resource status subresources** (same limitation as manual restore)
- ❌ **Cannot support staged deployment workflows** (requires manual control)
- ❌ **Less flexible resource filtering** compared to manual jq-based filtering
- ❌ **Cannot handle complex restoration logic** (e.g., RabbitMQ credential restoration)

## OADP Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  OpenShift Cluster                                          │
│                                                              │
│  ┌──────────────────────┐      ┌──────────────────────┐    │
│  │  OADP Operator       │      │  Velero Pod          │    │
│  │  (watches Backup CRs)│─────▶│  (executes backups)  │    │
│  └──────────────────────┘      └──────────────────────┘    │
│                                          │                   │
│                                          │                   │
│                                          ▼                   │
│  ┌──────────────────────────────────────────────────────┐  │
│  │  Namespace: openstack                                 │  │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐           │  │
│  │  │ PVCs     │  │ PVs      │  │ Other    │           │  │
│  │  │ (Glance, │  │          │  │ volumes  │           │  │
│  │  │  etc.)   │  │          │  │          │           │  │
│  │  └──────────┘  └──────────┘  └──────────┘           │  │
│  └──────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
                          │
                          │ Volume snapshots
                          ▼
            ┌─────────────────────────────┐
            │  CSI Storage Provider       │
            │  (Ceph, NetApp, etc.)       │
            │                              │
            │  VolumeSnapshot objects     │
            │  └── glance-data-snap-123   │
            └─────────────────────────────┘
                          │
                          │ Snapshot metadata
                          ▼
            ┌─────────────────────────────┐
            │  S3-Compatible Storage      │
            │  (MinIO, AWS S3, etc.)      │
            │                              │
            │  backup-xyz/                 │
            │  ├── metadata.json           │
            │  └── volumesnapshots/        │
            └─────────────────────────────┘
```

## Recommended Use Case: PVC/PV Backup

The **primary value** of OADP for OpenStack backup/restore is **automating persistent volume backups**.

### Current Manual Approach (Without OADP)

For backing up persistent volumes, you would use manual processes like:

```bash
# Manual tar from PVC
oc exec pod-name -- tar czf /tmp/data-backup.tar.gz /var/lib/data
oc cp pod-name:/tmp/data-backup.tar.gz ./backup.tar.gz

# Or use CSI snapshots manually
oc create -f - <<EOF
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: data-snapshot-$(date +%Y%m%d)
  namespace: openstack
spec:
  source:
    persistentVolumeClaimName: glance-data
  volumeSnapshotClassName: csi-snapshot-class
EOF
```

**Issues:**
- Manual process (requires scripting)
- No automated scheduling
- Snapshot lifecycle management (retention, cleanup) is manual
- No centralized backup tracking

### OADP Approach

OADP automates PVC backups using CSI volume snapshots:

```yaml
apiVersion: velero.io/v1
kind: Schedule
metadata:
  name: openstack-volumes-daily
  namespace: openshift-adp
spec:
  schedule: "0 2 * * *"  # Daily at 2 AM
  template:
    includedNamespaces:
    - openstack

    # Only backup PVCs, not CRs
    includedResources:
    - persistentvolumeclaims
    - persistentvolumes

    # Optionally filter specific PVCs by label
    labelSelector:
      matchLabels:
        app.kubernetes.io/name: glance

    snapshotVolumes: true
    storageLocation: s3-backup-location
    volumeSnapshotLocations:
    - csi-snapshot-location

    # Retention policy
    ttl: 720h  # 30 days
```

**Benefits:**
- ✅ Automated scheduling
- ✅ CSI snapshot integration
- ✅ Automatic retention/cleanup
- ✅ Centralized management
- ✅ S3 storage for metadata

## Integration with OpenStack Backup Procedures

### Hybrid Approach (Recommended)

Use OADP **only for PVC/PV backups**, keep existing procedures for everything else:

| Component | Backup Method | Tool | Why |
|-----------|---------------|------|-----|
| **ControlPlane CRs** | Ansible playbook | [backup-openstack-ctlplane.yaml](backup-openstack-ctlplane.yaml) | Staged deployment requires manual control |
| **DataPlane CRs** | Ansible playbook | [backup-openstack-dataplane.yaml](backup-openstack-dataplane.yaml) | Simple, git-friendly |
| **Secrets/ConfigMaps** | Ansible playbook | [backup-openstack-ctlplane.yaml](backup-openstack-ctlplane.yaml) | Complex filtering logic |
| **Persistent Volumes** | OADP | CSI snapshots | Automated, scheduled, managed lifecycle |

**Note:** Database backups (MariaDB/Galera) and OVN database backups have dedicated procedures and are not covered by OADP volume snapshots.

### Full Backup Workflow

```bash
# 1. Schedule OADP to backup PVCs automatically (one-time setup)
oc apply -f oadp-volume-schedule.yaml

# 2. Backup ControlPlane CRs (manual/scheduled via Ansible)
ansible-playbook backup-openstack-ctlplane.yaml -e openstack_namespace=openstack

# 3. Backup DataPlane CRs (manual/scheduled via Ansible)
ansible-playbook backup-openstack-dataplane.yaml -e openstack_namespace=openstack

# 4. Database backups use dedicated procedures (not OADP)
# See wip/ directory for database-specific backup scripts

# OADP handles PVC backups automatically in the background
```

### Full Restore Workflow

```bash
# 1. Restore ControlPlane CRs with staged deployment
ansible-playbook restore-openstack-ctlplane.yaml \
  -e openstack_namespace=openstack \
  -e backup_file=backups/openstack-ctlplane-backup-20260212.tar.gz

# 2. Wait for infrastructure to be ready
oc wait --for=condition=OpenStackControlPlaneInfrastructureReady \
  openstackcontrolplane/openstack -n openstack --timeout=20m

# 3. Restore PVCs from OADP backup (if using OADP)
velero restore create --from-backup openstack-volumes-20260212

# 4. Wait for PVC restore to complete
velero restore describe openstack-restore --details

# 5. Database restores use dedicated procedures (not OADP)
# See wip/ directory for database-specific restore scripts

# 6. Resume ControlPlane deployment
oc annotate openstackcontrolplane openstack -n openstack \
  core.openstack.org/deployment-stage-

# 7. Restore DataPlane CRs
ansible-playbook restore-openstack-dataplane.yaml \
  -e openstack_namespace=openstack \
  -e backup_file=backups/openstack-dataplane-backup-20260212.tar.gz
```

## OADP Configuration Examples

### Install OADP Operator

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: openshift-adp
---
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: openshift-adp
  namespace: openshift-adp
spec:
  targetNamespaces:
  - openshift-adp
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: oadp-operator
  namespace: openshift-adp
spec:
  channel: stable-1.4
  name: oadp-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
```

### Configure S3 Storage for Backup Metadata

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: cloud-credentials
  namespace: openshift-adp
stringData:
  cloud: |
    [default]
    aws_access_key_id=<ACCESS_KEY>
    aws_secret_access_key=<SECRET_KEY>
---
apiVersion: oadp.openshift.io/v1alpha1
kind: DataProtectionApplication
metadata:
  name: openstack-backup
  namespace: openshift-adp
spec:
  configuration:
    velero:
      defaultPlugins:
      - openshift
      - aws
      - csi
    restic:
      enable: false  # Use CSI snapshots, not Restic

  backupLocations:
  - name: s3-backup-location
    velero:
      provider: aws
      default: true
      objectStorage:
        bucket: openstack-backups
        prefix: ocp-cluster-1
      config:
        region: us-east-1
        s3Url: https://s3.example.com  # MinIO or S3-compatible
        s3ForcePathStyle: "true"
      credential:
        name: cloud-credentials
        key: cloud

  snapshotLocations:
  - name: csi-snapshot-location
    velero:
      provider: csi
```

### Example: Glance Images PVC Backup

```yaml
apiVersion: velero.io/v1
kind: Schedule
metadata:
  name: glance-weekly-backup
  namespace: openshift-adp
spec:
  schedule: "0 3 * * 0"  # Weekly on Sunday at 3 AM
  template:
    includedNamespaces:
    - openstack

    # Only backup Glance PVCs
    labelSelector:
      matchLabels:
        app.kubernetes.io/name: glance

    includedResources:
    - persistentvolumeclaims
    - persistentvolumes

    snapshotVolumes: true
    storageLocation: s3-backup-location
    volumeSnapshotLocations:
    - csi-snapshot-location

    # Keep backups for 90 days (images can be large)
    ttl: 2160h
```

### Example: All OpenStack PVCs Backup

```yaml
apiVersion: velero.io/v1
kind: Schedule
metadata:
  name: openstack-volumes-daily
  namespace: openshift-adp
spec:
  schedule: "0 2 * * *"  # Daily at 2 AM
  template:
    includedNamespaces:
    - openstack

    # Backup all PVCs in the namespace
    includedResources:
    - persistentvolumeclaims
    - persistentvolumes

    snapshotVolumes: true
    storageLocation: s3-backup-location
    volumeSnapshotLocations:
    - csi-snapshot-location

    # Keep backups for 30 days
    ttl: 720h
```

## User Flexibility: Alternative PVC Backup Methods

**OADP is completely optional.** Users may have their own preferred methods for backing up PVC content and can replace OADP with any approach that works for their infrastructure.

### Option 1: Manual CSI Snapshots

```bash
# Create snapshot manually
oc create -f - <<EOF
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: glance-snapshot-$(date +%Y%m%d)
  namespace: openstack
spec:
  source:
    persistentVolumeClaimName: glance-data
  volumeSnapshotClassName: csi-snapshot-class
EOF
```

### Option 2: Storage Array Snapshots

If using enterprise storage (NetApp, Pure Storage, Dell EMC, etc.), use array-native snapshots:

```bash
# Example: NetApp ONTAP
netapp-cli snapshot create -volume glance_vol -snapshot backup_20260212

# Example: Pure Storage
purecli volume create-snap --volume glance_vol --snap backup_20260212
```

### Option 3: Backup to Object Storage

```bash
# Stream PVC data to S3
oc exec glance-api-0 -- \
  tar czf - /var/lib/glance/images | \
  aws s3 cp - s3://backups/glance-backup-$(date +%Y%m%d).tar.gz
```

### Option 4: File-Level Backup Tools

```bash
# Restic backup (without OADP)
oc exec -it pod-name -- \
  restic backup /var/lib/data -r s3:s3.amazonaws.com/my-backup-bucket

# Duplicity
oc exec -it pod-name -- \
  duplicity /var/lib/data s3://backup-bucket/glance
```

### Option 5: rsync to External Storage

```bash
# Sync PVC data to external NFS
oc exec pod-name -- \
  rsync -av /var/lib/data/ /mnt/external-backup/data-$(date +%Y%m%d)/
```

**The key point**: The OpenStack backup/restore procedures document **what needs to be backed up** (which PVCs contain important data), but users can choose **how** to back up PVC content based on their infrastructure, compliance requirements, and operational preferences.

## Pros and Cons of OADP

### Pros

| Benefit | Description |
|---------|-------------|
| **Automated Scheduling** | Set-it-and-forget-it daily/weekly backups via Schedule CR |
| **CSI Integration** | Native support for CSI volume snapshots (storage-agnostic) |
| **Centralized Management** | Single pane of glass for backup operations |
| **Declarative** | GitOps-friendly (Backup/Restore/Schedule are CRs) |
| **S3 Storage** | Industry-standard object storage for metadata |
| **Retention Policies** | Automatic cleanup of old backups via TTL |
| **Disaster Recovery** | Built-in support for cluster migration |
| **Multi-Namespace** | Can backup PVCs across multiple namespaces |
| **No Application Downtime** | CSI snapshots are instant (crash-consistent) |
| **Vendor Neutral** | Based on open-source Velero, works with any CSI driver |

### Cons

| Limitation | Description |
|------------|-------------|
| **Requires CSI Driver** | Storage backend must support CSI snapshots |
| **Complex Setup** | Requires OADP operator, S3 storage configuration |
| **Storage Overhead** | Snapshots consume storage space on the backend |
| **Not Application-Consistent** | CSI snapshots are crash-consistent, not app-consistent (use hooks for consistency) |
| **Restore Complexity** | Velero CLI required for restore operations |
| **S3 Dependency** | Requires S3-compatible object storage for metadata |
| **Snapshot Limitations** | Some storage backends have snapshot limits (e.g., max 32 per volume) |
| **Learning Curve** | New concepts (BackupStorageLocation, VolumeSnapshotLocation, etc.) |

## OADP Limitations

### Cannot Restore Kubernetes Resource Status

As discussed in [backup-restore-dataplane.md](backup-restore-dataplane.md#deployment-history-lost-after-restore), OADP **cannot restore status subresources** for Kubernetes CRs.

This is a **Kubernetes API limitation**, not an OADP limitation:
- Kubernetes separates spec (desired state) from status (observed state)
- Status can only be updated via dedicated status subresource endpoints
- Standard create/update API calls ignore status fields
- Only controllers can update status

**Impact:** Even if you backup OpenStack CRs with OADP, the status will not be restored. Use the Ansible playbooks for CR backup/restore instead.

### Cannot Support Staged Deployment

OADP cannot support the ControlPlane staged deployment workflow where:
1. Restore with `core.openstack.org/deployment-stage: "infrastructure-only"` annotation
2. Wait for infrastructure ready
3. Restore databases
4. Remove annotation to resume deployment

**Workaround:** Do not use OADP for CR backup/restore. Use Ansible playbooks which provide full control over the restore process.

## Decision Matrix: When to Use OADP

| Scenario | Use OADP? | Reason |
|----------|-----------|--------|
| Glance image storage PVC | ✅ Yes | Large data, CSI snapshots efficient, automated scheduling |
| Other service PVCs | ✅ Yes | If CSI snapshots supported and automation desired |
| ControlPlane CR backup | ❌ No | Staged deployment requires manual control via playbooks |
| DataPlane CR backup | ❌ No | Ansible playbook sufficient, git-friendly |
| Secrets/ConfigMaps | ❌ No | Complex filtering logic handled by playbooks |
| Database backups | ❌ No | Use dedicated database backup procedures |
| Full disaster recovery | ⚠️ Partial | Good for PVC restore, but CRs need playbooks |
| Compliance/Audit | ✅ Yes | S3 storage, retention policies, centralized logging |

## Restore Process with OADP

When restoring PVCs from OADP backups, the process integrates with the staged deployment workflow:

```bash
# 1. Restore ControlPlane with staging (creates PVC definitions)
ansible-playbook restore-openstack-ctlplane.yaml \
  -e openstack_namespace=openstack \
  -e backup_file=backups/openstack-ctlplane-backup.tar.gz

# 2. Wait for infrastructure PVCs to be created (but empty)
oc wait --for=condition=OpenStackControlPlaneInfrastructureReady \
  openstackcontrolplane/openstack -n openstack --timeout=20m

# 3. Restore PVC data from OADP backup
#    Note: OADP will restore PVCs from CSI snapshots
velero restore create openstack-pvc-restore \
  --from-backup openstack-volumes-20260212 \
  --include-resources persistentvolumeclaims,persistentvolumes

# 4. Wait for restore to complete
velero restore describe openstack-pvc-restore --details

# 5. Verify PVCs are restored and bound
oc get pvc -n openstack

# 6. Continue with database-specific restore procedures
# (See wip/ directory for database backup/restore scripts)

# 7. Resume ControlPlane deployment
oc annotate openstackcontrolplane openstack -n openstack \
  core.openstack.org/deployment-stage-

# 8. Restore DataPlane
ansible-playbook restore-openstack-dataplane.yaml \
  -e openstack_namespace=openstack \
  -e backup_file=backups/openstack-dataplane-backup.tar.gz
```

## Conclusion

**OADP is valuable for OpenStack backup/restore, but in a limited scope:**

**Recommended Usage:**
- ✅ Use OADP for **PVC/PV backups** (especially large volumes like Glance images)
- ✅ Use OADP for **automated scheduling and retention**
- ❌ Do NOT use OADP for CR backup/restore (use Ansible playbooks)

**This provides:**
- Automated, scheduled volume backups
- CSI snapshot integration
- S3 storage and retention management
- User flexibility (can replace with any PVC backup method)

**While maintaining:**
- Full control over CR restore (staged deployment)
- Complex filtering logic (jq-based)
- Git-friendly CR version control
- Application-specific backup procedures (databases)

The hybrid approach leverages OADP's strengths (volume management) while avoiding its limitations (CR restore complexity).

**Key Takeaway:** OADP is **optional and replaceable**. Users can choose any method to backup PVC content. The OpenStack backup/restore procedures focus on **what** to backup, not **how** to backup it.

## See Also

- [ControlPlane Backup/Restore](backup-restore-ctlplane.md) - Main ControlPlane procedure
- [DataPlane Backup/Restore](backup-restore-dataplane.md) - Main DataPlane procedure
- [README](README.md) - Overview and quick start
- [OADP Documentation](https://docs.openshift.com/container-platform/latest/backup_and_restore/application_backup_and_restore/installing/about-installing-oadp.html)
- [Velero Documentation](https://velero.io/docs/)
