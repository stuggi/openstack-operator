# OpenStack Restore Process

This directory contains OADP Restore CRs for restoring OpenStack from backup.

## Restore Order

Restores must be executed in sequence. Wait for each restore to complete before starting the next.

| File | Order | Resources | Notes |
|------|-------|-----------|-------|
| `01-restore-order-00-pvcs.yaml` | 00 | PVCs (Glance images, GaleraBackup) | Storage foundation, restored from CSI snapshots |
| `02-restore-order-10-foundation.yaml` | 10 | Secrets, ConfigMaps, NADs | User-provided resources without ownerRefs |
| `03-restore-order-20-infrastructure.yaml` | 20 | OpenStackVersion, OpenStackBackupConfig, MariaDBAccount, MariaDBDatabase | Infrastructure base |
| `04-restore-order-30-controlplane.yaml` | 30 | OpenStackControlPlane | **Adds `deployment-stage: infrastructure-only` annotation** |
| `05-restore-order-40-backup-config.yaml` | 40 | GaleraBackup | Backup configuration CRs |
| `06-manual-database-restore.md` | 50 | **Manual** | Create GaleraRestore CRs, run restore script, remove deployment-stage annotation |
| `07-restore-order-60-dataplane.yaml` | 60 | OpenStackDataPlaneNodeSet | DataPlane resources (optional) |

## Prerequisites

- OADP operator installed and configured
- Velero storage location and volume snapshot location configured
- Backups exist (created with `docs/dev/webhook/backup/backup-*.yaml`)
- Target namespace exists (or will be created during restore)

## Quick Start

```bash
# 1. Storage foundation - PVCs
oc apply -f 01-restore-order-00-pvcs.yaml
oc wait --for=condition=complete restore/openstack-restore-00-pvcs -n openshift-adp --timeout=15m

# 2. Foundation resources
oc apply -f 02-restore-order-10-foundation.yaml
oc wait --for=condition=complete restore/openstack-restore-10-foundation -n openshift-adp --timeout=5m

# 3. Infrastructure CRs
oc apply -f 03-restore-order-20-infrastructure.yaml
oc wait --for=condition=complete restore/openstack-restore-20-infrastructure -n openshift-adp --timeout=5m

# 4. OpenStackControlPlane (with staging annotation)
oc apply -f 04-restore-order-30-controlplane.yaml
oc wait --for=condition=complete restore/openstack-restore-30-controlplane -n openshift-adp --timeout=5m

# 5. Backup configuration
oc apply -f 05-restore-order-40-backup-config.yaml
oc wait --for=condition=complete restore/openstack-restore-40-backup-config -n openshift-adp --timeout=5m

# 6. Manual database restore
# See 06-manual-database-restore.md for detailed steps

# 7. DataPlane (if applicable)
oc apply -f 07-restore-order-60-dataplane.yaml
oc wait --for=condition=complete restore/openstack-restore-60-dataplane -n openshift-adp --timeout=5m
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
- Implementation guide: `docs/dev/webhook/backup-restore-webhook-implementation.md`
- Restore scripts: `docs/dev/scripts/restore-galera-latest.sh`
