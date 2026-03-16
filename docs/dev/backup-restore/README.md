# OpenStack Backup and Restore

This directory contains the OADP-based backup and restore implementation for
OpenStack on OpenShift. It uses CRD labels for dynamic resource discovery,
OADP/Velero for PVC snapshots and resource backup, and Ansible playbooks for
orchestration.

## Quick Start

```bash
# Backup
ansible-playbook docs/dev/backup-restore/backup/backup-openstack.yaml
# With Data Mover (upload snapshots to S3/MinIO):
ansible-playbook docs/dev/backup-restore/backup/backup-openstack.yaml -e snapshot_move_data=true

# Restore
ansible-playbook docs/dev/backup-restore/restore/restore-openstack.yaml \
  -e backup_timestamp=20260311-081234
```

## Directory Structure

| Path | Description |
|------|-------------|
| [`minio/`](minio/) | MinIO deployment (S3-compatible storage for OADP) |
| [`oadp/`](oadp/) | OADP operator installation and configuration |
| [`backup/`](backup/) | OADP Backup CRs and backup playbook |
| [`restore/`](restore/) | OADP Restore CRs, manual restore docs, and restore playbook |
| [`scripts/`](scripts/) | Helper scripts (`restore-galera.sh`, `list-backup-candidates.sh`) |
| [`backup-restore-controller-design.md`](backup-restore-controller-design.md) | Design: CRD labels, controller-based labeling, restore ordering |
| [`backup-restore-controller-implementation.md`](backup-restore-controller-implementation.md) | Implementation guide for the OpenStackBackupConfig controller |
| [`backup-restore-pvc-enhancement.md`](backup-restore-pvc-enhancement.md) | Enhancement proposal: serialize CRs to PVC |

## Prerequisites

### General Requirements

1. **OpenShift CLI (`oc`) installed** — version compatible with your cluster
2. **Cluster access** — cluster admin or namespace admin for the `openstack` namespace
3. **Ansible** — for running backup/restore playbooks
4. **OADP operator** installed and configured (see [`oadp/README.md`](oadp/README.md) and [`minio/README.md`](minio/README.md))
5. **CSI Volume Snapshot support** — required for PVC backup/restore

### CSI Volume Snapshot Support

Both source and target clusters must have a StorageClass that supports
CSI Volume Snapshots:

- The staged deployment restore approach requires PVCs to be restored
  **before** service pods start
- CSI snapshots restore data at the storage layer without needing pods

**Supported StorageClasses:**
- LVM storage (TopoLVM/LVMS)
- Ceph RBD
- Cloud provider storage (AWS EBS, Azure Disk, GCP PD)
- **NOT supported**: Local storage (node-local directories)

```bash
# Verify VolumeSnapshotClass exists
oc get volumesnapshotclass
```

### LVMS Storage: Binary Units Required

PVC sizes **must use binary units (Gi, Mi, Ti)** not decimal units (G, M, T).
Using decimal units causes LVM extent rounding issues that break CSI snapshots
with the error: `requested size is smaller than source logical volume`.

- Correct: `storage: 5Gi`
- Wrong: `storage: 5G`

**Tracking:** [OSPRH-27441](https://issues.redhat.com/browse/OSPRH-27441)

### Operator Version Matching

The target cluster (where you restore) must have the **same versions** of
the OpenStack operators as the source cluster (where you backed up). CRD
schema changes between operator versions can break restore.

### Velero Version

```bash
VELERO_POD=$(oc get pods -n openshift-adp -l deploy=velero \
  --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}')
oc exec -n openshift-adp ${VELERO_POD} -- /velero version
```

[Velero v1.16+](https://github.com/vmware-tanzu/velero/releases/tag/v1.16.0)
(expected in OADP for OCP 4.19+) adds the `ignoreDelayBinding` flag for
node-agent, improving Data Mover handling of `WaitForFirstConsumer` PVCs.
See [`restore/README.md`](restore/README.md) for details and workarounds
for earlier versions.

## Backup Scope

| Component | Backed Up | NOT Backed Up |
|-----------|-----------|---------------|
| ControlPlane | OpenStackControlPlane, OpenStackVersion, NADs, Secrets, ConfigMaps, MariaDBDatabase/Account, GaleraBackup, Topology, BGPConfiguration, DNSData, InstanceHa, Database contents (Galera dumps), PVCs (Glance, GaleraBackup), RabbitMQ user credentials | Individual service CRs (recreated by controller), Certificate CRs (recreated by operators), Running pods, OVN database contents, RabbitMQ queue data (fresh cluster) |
| DataPlane | NetConfig, OpenStackDataPlaneNodeSet, OpenStackDataPlaneService, Reservation, IPSet | OpenStackDataPlaneDeployment (not restored to avoid triggering new deployments) |

## Restore Order

Restores must be executed in sequence. See [`restore/README.md`](restore/README.md)
for the full table.

| Order | Resources | Notes |
|-------|-----------|-------|
| 00 | PVCs | Storage foundation (CSI snapshots) |
| 10 | Secrets, ConfigMaps, NADs | User-provided resources |
| 20 | OpenStackVersion, MariaDB CRs, NetConfig, Topology, etc. | Infrastructure base |
| 30 | OpenStackControlPlane | With `deployment-stage: infrastructure-only` |
| 40 | GaleraBackup, IPSet, DataPlaneService | Backup config and IP sets |
| 50 | **Manual**: Database restore | Create GaleraRestore CRs, run restore |
| 55 | **Manual**: RabbitMQ credentials | Restore old secrets, create RabbitMQUser CRs |
| 60 | OpenStackDataPlaneNodeSet | DataPlane resources (optional) |

## Features Enabling Backup/Restore

| Feature | PR | Description |
|---------|-----|-------------|
| **Staged Deployment** | [#1785](https://github.com/openstack-k8s-operators/openstack-operator/pull/1785) | Pauses deployment after infrastructure is ready but before services start. Enables database restore before services initialize schemas. |
| **Service Name Caching** | [#1796](https://github.com/openstack-k8s-operators/openstack-operator/pull/1796) | Ensures service names remain consistent across backup/restore when `UniquePodNames` is enabled. |
| **CRD-Based Resource Discovery** | — | CRD labels (`backup.openstack.org/restore`, `restore-order`) declare restore behavior. No hardcoded resource lists. |
| **OpenStackBackupConfig Controller** | — | Automatically labels CR instances based on CRD labels for OADP restore filtering. |

## Post-Restore: Credential Rotation and EDPM Nodes

If credentials or certificates were rotated between the backup and the
restore, EDPM nodes may still have newer credentials/certs that don't match
the restored control plane state. This applies to:

- **ApplicationCredentials**: The restored Keystone DB contains old ACs. The
  openstack-operator will create new AC CRs on reconciliation, which
  generates new AC secrets. EDPM nodes still have the credentials from the
  last deployment run, which may not match. Additionally, if the backup is
  old, restored ACs may already be expired in the DB, requiring immediate
  rotation.
- **RabbitMQ**: The restored credentials (via `*-restored-user` secrets)
  match the backup, but EDPM nodes may have been updated with newer
  credentials since.
- **TLS/CA certificates**: If CAs were rotated between backup and restore,
  the restored control plane uses the old CA. EDPM nodes may have
  certificates signed by a newer CA, causing TLS trust failures in both
  directions.

**An EDPM deployment is required after restore** to resync all credentials
and certificates on the dataplane nodes with the restored control plane
state.

## Post-Restore: Re-enable InstanceHa

InstanceHa is restored with `spec.disabled: True` to prevent fencing during
the restore process. After verifying the restored cloud is fully operational,
re-enable it:

```bash
oc patch instanceha <name> -n openstack --type merge -p '{"spec":{"disabled":"False"}}'
```

## Limitations and Known Issues

### Fully Updated Environments Only

Backup/restore is only supported for environments that are **fully updated**.
It is **NOT supported** for partial update states (e.g., operators updated
but ControlPlane/DataPlane CRs not yet updated). The OpenStackVersion CR
does not correctly restore in-flight update state.

**Before backup**, ensure all operator updates and service updates are
complete.

**Related:** [OSPRH-26244](https://issues.redhat.com/browse/OSPRH-26244),
[OSPRH-26246](https://issues.redhat.com/browse/OSPRH-26246)

### DataPlane Deployment History

When restoring DataPlane, the OpenStackDataPlaneDeployment history is lost.
NodeSets will show `waiting for OpenStackDataPlaneDeployment...` — this is
a **safe state**; actual dataplane nodes are running correctly.

### Pre-Provisioned Nodes Only

The DataPlane backup/restore procedure is designed for NodeSets with
`preProvisioned: true`. For nodes provisioned via OpenStackBaremetalSet and
Metal3, additional procedures are required.

## See Also

- MinIO setup: [`minio/README.md`](minio/README.md)
- OADP setup: [`oadp/README.md`](oadp/README.md)
- Restore scripts: [`scripts/restore-galera.sh`](scripts/restore-galera.sh)
