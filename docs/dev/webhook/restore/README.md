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
| `05-restore-order-40-backup-config.yaml` | 40 | GaleraBackup, IPSet | Backup configuration CRs and IP sets |
| `06-manual-database-restore.md` | 50 | **Manual** | Create GaleraRestore CRs, run restore script |
| `06b-restore-rabbitmq-secrets.yaml` | - | Secrets (to temp ns) | Restore secrets to `openstack-restore-tmp`, copy `*-default-user` as `*-restored-user`, create RabbitMQUser CRs |
| *(Step 9 in playbook)* | - | **Manual** | Remove deployment-stage annotation, wait for control plane ready |
| `07-restore-order-60-dataplane.yaml` | 60 | OpenStackDataPlaneNodeSet | DataPlane resources (optional) |
| *(Final step)* | - | **Manual** | Re-enable InstanceHa (`spec.disabled: False`) after verifying the restored cloud is operational |

## Prerequisites

- OADP operator installed and configured
- Velero storage location and volume snapshot location configured
- Backups exist (created with `docs/dev/webhook/backup/backup-*.yaml`)
- Target namespace exists (or will be created during restore)

## Quick Start

### Automated (Recommended)

Use the restore playbook to orchestrate the full restore flow:

```bash
ansible-playbook docs/dev/webhook/restore/restore-openstack.yaml
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
ansible-playbook docs/dev/webhook/restore/restore-openstack.yaml \
  -e pvc_backup_name=openstack-backup-pvcs-20260311-081234 \
  -e resources_backup_name=openstack-backup-resources-20260311-081234 \
  -e restore_dataplane=false

# With additional RabbitMQ clusters:
ansible-playbook docs/dev/webhook/restore/restore-openstack.yaml \
  -e pvc_backup_name=openstack-backup-pvcs-20260311-081234 \
  -e resources_backup_name=openstack-backup-resources-20260311-081234 \
  -e '{"rabbitmq_clusters": ["rabbitmq", "rabbitmq-cell1", "rabbitmq-cell2"]}'
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

### Database restore issues

```bash
# Check GaleraRestore CR
oc get galerarestore -n openstack
oc describe galerarestore <name> -n openstack

# Check restore pod logs
oc logs <backup-source>-restore-<restore-name> -n openstack
```

## See Also

- Backup CRs: `docs/dev/webhook/backup/`
- Implementation guide: `docs/dev/webhook/backup-restore-controller-implementation.md`
- Restore scripts: `docs/dev/scripts/restore-galera-latest.sh`
