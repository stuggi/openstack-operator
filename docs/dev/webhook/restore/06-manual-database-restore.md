# Order 50: Manual Database Restore

This step is NOT automated via OADP Restore CR. It requires manual intervention to restore database data from the backup PVCs using GaleraRestore CRs.

## Prerequisites

- Order 00-40 restore completed
- OpenStackControlPlane is deployed with `deployment-stage: infrastructure-only` annotation
- Galera database pods are running but contain empty databases
- Backup PVCs are restored and mounted
- GaleraBackup CRs are restored (from order 40)

## Steps

### 1. Identify GaleraBackup CRs

List the restored GaleraBackup CRs to find the backup sources:

```bash
oc get galerabackup -n openstack
```

Expected output shows backup CR names (e.g., `openstack`, `openstack-cell1`)

### 2. Create GaleraRestore CRs

For each GaleraBackup, create a corresponding GaleraRestore CR:

```bash
# Main cell restore
cat <<EOF | oc apply -f -
apiVersion: mariadb.openstack.org/v1beta1
kind: GaleraRestore
metadata:
  name: openstackrestore
  namespace: openstack
spec:
  backupSource: openstack
EOF

# Cell1 restore (if multi-cell)
cat <<EOF | oc apply -f -
apiVersion: mariadb.openstack.org/v1beta1
kind: GaleraRestore
metadata:
  name: openstackrestorecell1
  namespace: openstack
spec:
  backupSource: openstack-cell1
EOF
```

### 3. Wait for restore pods to be ready

The mariadb-operator will create restore pods:

```bash
oc get pods -n openstack | grep restore
```

Wait for pods to be in `Running` state.

### 4. Execute the database restore

Use the helper script to restore from the latest backup:

```bash
# Main cell
docs/dev/scripts/restore-galera-latest.sh openstackrestore openstack

# Cell1 (if multi-cell)
docs/dev/scripts/restore-galera-latest.sh openstackrestorecell1 openstack
```

The script will:
- Find the latest backup file in the restore pod
- Execute the restore_galera script
- Delete the GaleraRestore CR after successful restore

### 5. Verify database restore

```bash
# Check databases are restored
oc exec -it galera-0 -n openstack -- mysql -e "SHOW DATABASES;"
```

### 6. Remove deployment-stage annotation

Resume full OpenStack deployment by removing the annotation:

```bash
oc annotate openstackcontrolplane <name> -n openstack openstack.org/deployment-stage-
```

Replace `<name>` with your OpenStackControlPlane CR name.

### 7. Wait for OpenStack services to start

```bash
oc get pods -n openstack
oc get openstackcontrolplane -n openstack
```

## Next Step

After manual database restore and annotation removal, proceed to:
- **Order 60**: Restore DataPlane resources (if applicable)
