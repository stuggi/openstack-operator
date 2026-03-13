# OpenStack Restore Process

This directory contains OADP Restore CRs for restoring OpenStack from backup.

## Restore Order

Restores must be executed in sequence. Wait for each restore to complete before starting the next.

| File | Order | Resources | Notes |
|------|-------|-----------|-------|
| `00-resource-modifiers-configmap.yaml` | - | ConfigMap | **Prerequisite:** resource modifier rules for all restores |
| `01-restore-order-00-pvcs.yaml` | 00 | PVCs (Glance images, GaleraBackup) | Storage foundation, restored from CSI snapshots |
| `02-restore-order-10-foundation.yaml` | 10 | Secrets, ConfigMaps, NADs | User-provided resources without ownerRefs |
| `03-restore-order-20-infrastructure.yaml` | 20 | OpenStackVersion, OpenStackBackupConfig, MariaDBAccount, MariaDBDatabase, NetConfig, Topology, BGPConfiguration, DNSData, InstanceHa | Infrastructure base (**InstanceHa restored with `spec.disabled: True`**) |
| `04-restore-order-30-controlplane.yaml` | 30 | OpenStackControlPlane, Reservation | **Adds `deployment-stage: infrastructure-only` annotation** |
| `05-restore-order-40-backup-config.yaml` | 40 | GaleraBackup, IPSet, DataPlaneService | Backup config, IP sets, custom DataPlane services |
| `06-manual-database-restore.md` | 50 | **Manual** | Create GaleraRestore CRs, run restore script |
| `06b-restore-rabbitmq-secrets.yaml` | - | Secrets (to temp ns) | Restore secrets to `openstack-restore-tmp`, copy `*-default-user` as `*-restored-user`, create RabbitMQUser CRs |
| *(Step 9 in playbook)* | - | **Manual** | Remove deployment-stage annotation, wait for control plane ready |
| `07-restore-order-60-dataplane.yaml` | 60 | OpenStackDataPlaneNodeSet | DataPlane resources (optional) |
| *(Post-restore)* | - | **Manual** | Run EDPM deployment to resync credentials (required if credentials were rotated between backup and restore) |
| *(Final step)* | - | **Manual** | Re-enable InstanceHa (`spec.disabled: False`) after verifying the restored cloud is operational |

## Prerequisites

- OADP operator installed and configured
- Velero BackupStorageLocation (BSL) configured and pointing to the same
  object storage (S3/MinIO) used during backup
- VolumeSnapshotLocation configured (for CSI snapshot restores)
- Target namespace exists (or will be created during restore)

### Discovering Backups

When using the Data Mover (`snapshotMoveData: true`), backup metadata and PVC
data are stored in the BackupStorageLocation (S3/MinIO). This enables restore
even on a completely new cluster — Velero automatically syncs and discovers
existing backups from the object storage.

On a new cluster (or after deleting local Velero backup objects):

```bash
# 1. Verify BSL is available and syncing
oc get backupstoragelocation -n openshift-adp

# 2. Wait for Velero to sync (default: every 1 minute)
#    Backups will appear automatically
oc get backup -n openshift-adp

# 3. Find the backup names to use for restore
oc get backup -n openshift-adp -o custom-columns=NAME:.metadata.name,PHASE:.status.phase,CREATED:.metadata.creationTimestamp
```

Pass the discovered backup names to the restore playbook:
```bash
ansible-playbook docs/dev/backup-restore/restore/restore-openstack.yaml \
  -e pvc_backup_name=openstack-backup-pvcs-20260313-122645 \
  -e resources_backup_name=openstack-backup-resources-20260313-122645
```

## Quick Start

### Automated (Recommended)

Use the restore playbook to orchestrate the full restore flow:

```bash
ansible-playbook docs/dev/backup-restore/restore/restore-openstack.yaml
```

The playbook runs all restore steps in order, including:
- Ordered OADP restores (PVCs → Foundation → Infrastructure → ControlPlane → GaleraBackup)
- Waits for infrastructure to be ready (Galera, OVN, RabbitMQ)
- Automated database restore (creates GaleraRestore CRs, runs restore script)
- RabbitMQ credential restore (restores old secrets from backup, creates RabbitMQUser CRs)
- Removes deployment-stage annotation to resume full deployment
- Waits for control plane to be ready
- Optional DataPlane restore

Override defaults with extra vars:
```bash
ansible-playbook docs/dev/backup-restore/restore/restore-openstack.yaml \
  -e pvc_backup_name=openstack-backup-pvcs-20260311-081234 \
  -e resources_backup_name=openstack-backup-resources-20260311-081234 \
  -e restore_dataplane=false

# With additional RabbitMQ clusters:
ansible-playbook docs/dev/backup-restore/restore/restore-openstack.yaml \
  -e pvc_backup_name=openstack-backup-pvcs-20260311-081234 \
  -e resources_backup_name=openstack-backup-resources-20260311-081234 \
  -e '{"rabbitmq_clusters": ["rabbitmq", "rabbitmq-cell1", "rabbitmq-cell2"]}'

# Data Mover restore with WaitForFirstConsumer storage (LVM):
# Create the Immediate StorageClass first (see Troubleshooting section), then:
ansible-playbook docs/dev/backup-restore/restore/restore-openstack.yaml \
  -e pvc_backup_name=openstack-backup-pvcs-20260311-081234 \
  -e resources_backup_name=openstack-backup-resources-20260311-081234 \
  -e immediate_binding_class=lvms-local-storage-immediate
```

### Manual

Create the resource modifier ConfigMap first, then apply restore CRs in order:

```bash
# 0. Create resource modifier ConfigMap (required for all restores)
oc apply -f 00-resource-modifiers-configmap.yaml

# 1. Storage foundation - PVCs
oc apply -f 01-restore-order-00-pvcs.yaml
oc wait --for=jsonpath='{.status.phase}'=Completed restore/openstack-restore-00-pvcs -n openshift-adp --timeout=15m

# 2. Foundation resources
oc apply -f 02-restore-order-10-foundation.yaml
oc wait --for=jsonpath='{.status.phase}'=Completed restore/openstack-restore-10-foundation -n openshift-adp --timeout=5m

# 3. Infrastructure CRs
oc apply -f 03-restore-order-20-infrastructure.yaml
oc wait --for=jsonpath='{.status.phase}'=Completed restore/openstack-restore-20-infrastructure -n openshift-adp --timeout=5m

# 4. OpenStackControlPlane (with staging annotation)
oc apply -f 04-restore-order-30-controlplane.yaml
oc wait --for=jsonpath='{.status.phase}'=Completed restore/openstack-restore-30-controlplane -n openshift-adp --timeout=5m

# 5. Backup configuration
oc apply -f 05-restore-order-40-backup-config.yaml
oc wait --for=jsonpath='{.status.phase}'=Completed restore/openstack-restore-40-backup-config -n openshift-adp --timeout=5m

# 6. Manual database restore
# See 06-manual-database-restore.md for detailed steps

# 7. DataPlane (if applicable)
oc apply -f 07-restore-order-60-dataplane.yaml
oc wait --for=jsonpath='{.status.phase}'=Completed restore/openstack-restore-60-dataplane -n openshift-adp --timeout=5m
```

## Verification

After each restore step:

```bash
# Check restore status
oc get restore -n openshift-adp

# Check restored resources
oc get all,pvc,secret,configmap,nad -n openstack

# Check OpenStack CRs
oc get openstackcontrolplane,openstackversion,mariadbaccount,mariadbdatabase -n openstack
```

## Troubleshooting

### Restore stuck or failed

```bash
# Check restore details
oc describe restore <restore-name> -n openshift-adp

# Check Velero logs
oc logs -n openshift-adp deployment/velero
```

### PVC restore issues

```bash
# Check volume snapshots
oc get volumesnapshot -n openstack
oc get volumesnapshotcontent

# Check PVC events
oc describe pvc <pvc-name> -n openstack
```

### Data Mover restore stuck with WaitForFirstConsumer storage (LVM)

**Known issue:** When using the OADP Data Mover (`snapshotMoveData: true`) with a
StorageClass that has `volumeBindingMode: WaitForFirstConsumer` (e.g., LVM/topolvm),
the PVC restore at order 00 will deadlock. The data mover waits for the PVC to be
consumed by a pod before downloading data, but with WaitForFirstConsumer the PVC
won't bind until a pod references it. Since PVCs are restored before any workload
pods exist, this creates a deadlock.

**Symptoms:**
- Restore stuck in `WaitingForPluginOperations` phase
- DataDownload CRs in `Accepted` or `<none>` phase, never progressing
- PVCs in `Pending` state with event: "waiting for first consumer to be created"
- Node-agent logs: `error to wait target PVC consumed ... context deadline exceeded`

**Upstream issues:**
- [velero#7561](https://github.com/vmware-tanzu/velero/issues/7561) — WaitForFirstConsumer incompatibility
- [velero#8044](https://github.com/vmware-tanzu/velero/issues/8044) — Enhancement proposal
- [velero#9343](https://github.com/vmware-tanzu/velero/issues/9343) — Topology-aware storage

**Workaround:** Create a temporary StorageClass with `volumeBindingMode: Immediate`
and use a Velero resource modifier to swap the StorageClass on restored PVCs.
This allows PVCs to bind immediately without waiting for a consumer pod.

```bash
# 1. Get the parameters from your existing StorageClass
oc get storageclass lvms-local-storage -o yaml

# 2. Check which topology key your nodes use
oc get nodes -o json | jq '.items[].metadata.labels | with_entries(select(.key | contains("topolvm")))'

# 3. Create a temporary StorageClass with Immediate binding
#    (adjust provisioner, parameters, and reclaimPolicy to match your environment)
#    Use allowedTopologies to control which nodes get the volumes.
cat <<EOF | oc apply -f -
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: lvms-local-storage-immediate
provisioner: topolvm.io
reclaimPolicy: Delete
volumeBindingMode: Immediate
allowVolumeExpansion: true
allowedTopologies:
- matchLabelExpressions:
  - key: topology.topolvm.io/node
    values:
    - master-0
    - master-1
    - master-2
EOF

# 3. Uncomment the PVC StorageClass modifier rule in
#    00-resource-modifiers-configmap.yaml (rule 4) and apply it,
#    or add it to the playbook's inline ConfigMap.
#    The rule changes storageClassName on restored PVCs to the Immediate class.

# 4. Run the PVC restore — PVCs will bind immediately and DataDownloads proceed

# 5. After restore is complete, delete the temporary StorageClass
oc delete storageclass lvms-local-storage-immediate
```

The restored PVCs will use the Immediate StorageClass. With topolvm, this means
volumes are provisioned on a node chosen by the provisioner (capacity-based).
Workload pods (StatefulSets) will then be scheduled to the nodes where their
PVCs were provisioned.

**Note:** This workaround is not needed when restoring from local CSI snapshots
(without data mover). It only affects cross-cluster or disaster recovery restores
where PVC data is downloaded from the BackupStorageLocation (S3/MinIO).

### Database restore issues

```bash
# Check GaleraRestore CR
oc get galerarestore -n openstack
oc describe galerarestore <name> -n openstack

# Check restore pod logs
oc logs <backup-source>-restore-<restore-name> -n openstack
```

## See Also

- Backup CRs: `docs/dev/backup-restore/backup/`
- Implementation guide: `docs/dev/backup-restore/backup-restore-controller-implementation.md`
- Restore scripts: `docs/dev/scripts/restore-galera-latest.sh`
