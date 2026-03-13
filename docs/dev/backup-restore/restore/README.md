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
# Pre-create dummy pods to pin PVCs to nodes (see Troubleshooting section),
# then run the playbook with pin_pvcs=true:
ansible-playbook docs/dev/backup-restore/restore/restore-openstack.yaml \
  -e pvc_backup_name=openstack-backup-pvcs-20260311-081234 \
  -e resources_backup_name=openstack-backup-resources-20260311-081234 \
  -e pin_pvcs=true
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

**Workaround (recommended): Pre-create dummy pods to pin PVCs to nodes.**
This approach solves the WaitForFirstConsumer deadlock AND preserves the original
node-to-PVC mapping from the backup. Dummy pods with `nodeSelector` act as the
"first consumer" — the scheduler annotates the PVC with the selected node, which
triggers WaitForFirstConsumer binding when the restore creates the PVCs.
Use `nodeSelector` (not `nodeName`) because `nodeName` bypasses the scheduler
and the PVC binding annotation never gets set.

```bash
# 1. Download the backup metadata (small — only resource manifests, no PV data)
velero backup download <pvc-backup-name> -o /tmp/backup.tar.gz
mkdir -p /tmp/backup && tar xzf /tmp/backup.tar.gz -C /tmp/backup

# 2. Extract PVC-to-node mapping from PVs (topolvm stores node in nodeAffinity)
for f in /tmp/backup/resources/persistentvolumes/cluster/*.json; do
  pvc=$(jq -r '.spec.claimRef.name // empty' "$f")
  ns=$(jq -r '.spec.claimRef.namespace // empty' "$f")
  node=$(jq -r '
    .spec.nodeAffinity.required.nodeSelectorTerms[0].matchExpressions[]
    | select(.key | contains("topolvm")) | .values[0]
  ' "$f" 2>/dev/null)
  [ -n "$pvc" ] && [ -n "$node" ] && echo "${pvc}:${node}"
done
# Example output:
#   glance-glance-default-single-0:master-0
#   glance-glance-default-single-1:master-1
#   glance-glance-default-single-2:master-2
#   mysql-db-openstack-galera-0:master-0
#   mysql-db-openstack-galera-1:master-1
#   mysql-db-openstack-galera-2:master-2

# 3. Create dummy Deployments BEFORE the PVC restore.
#    Pods referencing non-existent PVCs stay Pending until the restore creates
#    the PVCs. The scheduler then annotates the PVC with the selected node,
#    triggering WaitForFirstConsumer binding on the target node.
#    Using Deployments (not bare Pods) ensures automatic restart if a pod
#    gets evicted during longer manual procedures.
#    IMPORTANT: Use nodeSelector (not nodeName) so the pod goes through the
#    scheduler — nodeName bypasses it and the PVC annotation never gets set.
for pvc_node in \
  "glance-glance-default-single-0:master-0" \
  "glance-glance-default-single-1:master-1" \
  "glance-glance-default-single-2:master-2" \
  "mysql-db-openstack-galera-0:master-0" \
  "mysql-db-openstack-galera-1:master-1" \
  "mysql-db-openstack-galera-2:master-2"; do

  pvc="${pvc_node%%:*}"
  node="${pvc_node##*:}"

  cat <<EOF | oc apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: pvc-pin-${pvc}
  namespace: openstack
  labels:
    app: pvc-pin
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: pvc-pin
      pvc: ${pvc}
  template:
    metadata:
      labels:
        app: pvc-pin
        pvc: ${pvc}
    spec:
      nodeSelector:
        kubernetes.io/hostname: ${node}
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.9
        volumeMounts:
        - name: data
          mountPath: /data
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: ${pvc}
EOF
done

# 4. Apply the resource modifier ConfigMap (no StorageClass swap needed)
oc apply -f 00-resource-modifiers-configmap.yaml

# 5. Run the PVC restore
#    Velero creates PVCs → dummy pods trigger WaitForFirstConsumer binding
#    on target nodes → DataDownloads proceed
oc apply -f 01-restore-order-00-pvcs.yaml

# 6. Monitor progress
oc get datadownload -n openshift-adp -w

# 7. After all DataDownloads complete, delete dummy Deployments
oc delete deployment -n openstack -l app=pvc-pin
```

**Alternative: Immediate StorageClass.** If you don't need to control which node
each PVC lands on, you can create a StorageClass with `volumeBindingMode: Immediate`
and use a Velero resource modifier to swap the StorageClass during restore.
This is simpler but topolvm picks nodes by capacity, which may place multiple
PVCs for the same StatefulSet on one node (breaking pod anti-affinity scheduling).
See `00-resource-modifiers-configmap.yaml` rule 4 for the StorageClass swap rule.

**Note:** These workarounds are only needed when restoring from the
BackupStorageLocation (S3/MinIO) using the Data Mover (`snapshotMoveData: true`).
They are not needed when restoring from local CSI snapshots (without data mover).

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
