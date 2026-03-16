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

The mariadb-operator will create restore pods. The pod name follows the
pattern `<backupSource>-restore-<restoreName>`:

```bash
oc get pods -n openstack | grep restore
# Expected pods:
#   openstack-restore-openstackrestore        (main cell)
#   openstack-cell1-restore-openstackrestorecell1  (cell1, if multi-cell)
```

Wait for pods to be in `Running` state:

```bash
oc wait --for=jsonpath='{.status.phase}'=Running \
  pod/openstack-restore-openstackrestore -n openstack --timeout=5m

# Cell1 (if multi-cell):
oc wait --for=jsonpath='{.status.phase}'=Running \
  pod/openstack-cell1-restore-openstackrestorecell1 -n openstack --timeout=5m
```

### 4. Verify backup files exist

List the backup dump files matching your backup timestamp on the restore pod.
Replace `<BACKUP_TIMESTAMP>` with the timestamp used during backup
(e.g., `20260311-081234`):

```bash
BACKUP_TIMESTAMP=<BACKUP_TIMESTAMP>

# Main cell
oc exec -n openstack openstack-restore-openstackrestore -- \
  ls -1 /backup/data/*_${BACKUP_TIMESTAMP}.sql.gz

# Cell1 (if multi-cell)
oc exec -n openstack openstack-cell1-restore-openstackrestorecell1 -- \
  ls -1 /backup/data/*_${BACKUP_TIMESTAMP}.sql.gz
```

You should see two files per cell: a database dump and a grants file.

### 5. Execute the database restore

Run the `restore_galera` script inside each restore pod. The `--yes` flag
skips the confirmation prompt:

```bash
BACKUP_TIMESTAMP=<BACKUP_TIMESTAMP>

# Main cell
oc exec -n openstack openstack-restore-openstackrestore -- \
  /var/lib/backup-scripts/restore_galera --yes "/backup/data/*_${BACKUP_TIMESTAMP}.sql.gz"

# Cell1 (if multi-cell)
oc exec -n openstack openstack-cell1-restore-openstackrestorecell1 -- \
  /var/lib/backup-scripts/restore_galera --yes "/backup/data/*_${BACKUP_TIMESTAMP}.sql.gz"
```

### 6. Verify database restore

```bash
# Check databases are restored
oc exec -it galera-0 -n openstack -- mysql -e "SHOW DATABASES;"
```

### 7. Clean up GaleraRestore CRs

Delete the GaleraRestore CRs to terminate the restore pods:

```bash
oc delete galerarestore openstackrestore -n openstack
# Cell1 (if multi-cell):
oc delete galerarestore openstackrestorecell1 -n openstack
```

### 8. Restore RabbitMQ user credentials

The new RabbitMQ clusters have random credentials. Restore the original
`*-default-user` secrets from the backup to recover the old credentials,
then create RabbitMQUser CRs to re-establish them.

#### 8a. Restore secrets to a temporary namespace

```bash
# Create temp namespace
oc create namespace openstack-restore-tmp

# Restore all secrets from backup to temp namespace
# Edit backupName in 06b-restore-rabbitmq-secrets.yaml first, then:
oc apply -f 06b-restore-rabbitmq-secrets.yaml
oc wait --for=jsonpath='{.status.phase}'=Completed \
  restore/openstack-restore-rabbitmq-secrets -n openshift-adp --timeout=5m
```

#### 8b. Copy old credentials to target namespace

```bash
# For each RabbitMQ cluster (adjust cluster names for your deployment)
for CLUSTER in rabbitmq rabbitmq-cell1; do
  if ! oc get secret "${CLUSTER}-default-user" -n openstack-restore-tmp &>/dev/null; then
    echo "Secret ${CLUSTER}-default-user not found in temp namespace - skipping"
    continue
  fi

  TMPDIR=$(mktemp -d)
  oc extract secret/${CLUSTER}-default-user -n openstack-restore-tmp --to=${TMPDIR} --confirm
  oc create secret generic ${CLUSTER}-restored-user -n openstack --from-file=${TMPDIR}
  rm -rf ${TMPDIR}
  echo "Created ${CLUSTER}-restored-user in openstack"
done
```

#### 8c. Create RabbitMQUser CRs

```bash
for CLUSTER in rabbitmq rabbitmq-cell1; do
  RESTORED_SECRET="${CLUSTER}-restored-user"

  if ! oc get secret "${RESTORED_SECRET}" -n openstack &>/dev/null; then
    echo "Secret ${RESTORED_SECRET} not found - skipping"
    continue
  fi

  cat <<EOF | oc apply -f -
  apiVersion: rabbitmq.openstack.org/v1beta1
  kind: RabbitMQUser
  metadata:
    name: ${CLUSTER}-restored-user
    namespace: openstack
  spec:
    rabbitmqClusterName: ${CLUSTER}
    secret: ${RESTORED_SECRET}
    tags:
      - administrator
    permissions:
      configure: ".*"
      read: ".*"
      write: ".*"
EOF
  echo "Created RabbitMQUser CR for ${CLUSTER}"
done
```

#### 8d. Clean up temp namespace

```bash
oc delete namespace openstack-restore-tmp
```

### 9. Remove deployment-stage annotation

Resume full OpenStack deployment by removing the annotation:

```bash
oc annotate openstackcontrolplane <name> -n openstack core.openstack.org/deployment-stage-
```

Replace `<name>` with your OpenStackControlPlane CR name.

### 10. Wait for OpenStack services to start

```bash
oc get pods -n openstack
oc get openstackcontrolplane -n openstack
```

## Important: Credential Rotation and EDPM Nodes

If credentials or certificates were rotated between the backup and the restore, EDPM nodes
may still have newer credentials/certs that don't match the restored control plane state.
This applies to:

- **ApplicationCredentials**: The restored Keystone DB contains old ACs. The openstack-operator
  will create new AC CRs on reconciliation, which generates new AC secrets. EDPM nodes still
  have the credentials from the last deployment run, which may not match.
  Additionally, if the backup is old, restored ACs may already be expired in the DB,
  requiring immediate rotation.
- **RabbitMQ**: The restored credentials (via `*-restored-user` secrets) match the backup,
  but EDPM nodes may have been updated with newer credentials since.
- **TLS/CA certificates**: If CAs were rotated between backup and restore, the restored
  control plane uses the old CA. EDPM nodes may have certificates signed by a newer CA,
  causing TLS trust failures in both directions.

**An EDPM deployment is required after restore** to resync all credentials and certificates
on the dataplane nodes with the restored control plane state.

## Next Steps

After database restore, RabbitMQ credential restore, and annotation removal, proceed to:
1. **Order 60**: Restore DataPlane resources (if applicable)
2. **Run an EDPM deployment**: Required to resync credentials on dataplane nodes with
   the restored control plane, especially if credentials were rotated between backup and restore.
3. **Re-enable InstanceHa**: After verifying the restored cloud is fully operational,
   re-enable InstanceHa (it was restored with `spec.disabled: True` to prevent fencing):
   ```bash
   oc patch instanceha <name> -n openstack --type merge -p '{"spec":{"disabled":"False"}}'
   ```
