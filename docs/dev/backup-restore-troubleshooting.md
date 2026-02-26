# OpenStack Backup and Restore - Troubleshooting

This document contains troubleshooting guidance for OpenStack backup and restore procedures.

For the main backup/restore documentation, see:
- [Control Plane Backup/Restore](backup-restore-ctlplane.md)
- [Data Plane Backup/Restore](backup-restore-dataplane.md)
- [Storage Volumes Backup/Restore](backup-restore-storage-volumes.md)

---

## Troubleshooting

### Issue: Operator Version Mismatch

**Symptoms:**
- Control plane CR fails to reconcile
- Error messages about unknown fields or invalid schema
- CRDs rejected during restore
- Operator logs show validation errors

**Diagnosis:**

```bash
# Compare operator versions
cat operator-versions.txt  # From backup

# vs current versions
oc get deployment openstack-operator-controller-manager -n openstack-operators -o jsonpath='{.spec.template.spec.containers[0].image}'

# Check for CRD version differences
oc get crd openstackcontrolplanes.core.openstack.org -o jsonpath='{.spec.versions[*].name}'
```

**Solution:**

```bash
# Option 1: Install matching operator version on target cluster (RECOMMENDED)
# Follow your operator installation procedure to install the specific version
# shown in operator-versions.txt from the backup

# Option 2: If source cluster is still available, upgrade operators then re-backup
# (Only if target has newer operators and you want to move to newer version)

# DO NOT attempt to:
# - Manually edit CRs to match new schema (likely to fail)
# - Force apply with --force flag (will cause data corruption)
# - Mix operator versions (openstack-operator vs infra-operator)
```

**Prevention:**
- Always document operator versions during backup
- Test restores in non-production environment first
- Maintain operator version parity across clusters used for DR

### Issue: RabbitMQ Authentication Failures

**Symptoms:**
- Services fail to start or restart repeatedly
- Error logs show "ACCESS_REFUSED" or authentication failures
- TransportURL CRs show errors
- Service logs contain RabbitMQ connection errors

**Diagnosis:**

```bash
# Check service logs for RabbitMQ auth errors
oc logs -n openstack deployment/nova-api | grep -i rabbit
oc logs -n openstack deployment/neutron-api | grep -i rabbit

# Verify RabbitMQ user exists (should match backed-up credentials)
oc rsh -n openstack rabbitmq-server-0 rabbitmqctl list_users

# Check transport URL secret was automatically created
oc get secret rabbitmq-transport-url-nova-api-transport -n openstack

# Decode and verify transport URL (should reference the restored user)
oc get secret rabbitmq-transport-url-nova-api-transport -n openstack -o jsonpath='{.data.transport_url}' | base64 -d
```

**Solution:**

```bash
# Option 1: Re-run the user restoration from step 8
# Extract credentials from backup and add them again

# Get credentials from backup
RABBITMQ_USER=$(jq -r '.items[] | select(.metadata.name=="rabbitmq-default-user") | .data.username' secrets-all-backup.json | base64 -d)
RABBITMQ_PASS=$(jq -r '.items[] | select(.metadata.name=="rabbitmq-default-user") | .data.password' secrets-all-backup.json | base64 -d)

# Delete the user if it exists (to reset)
oc rsh -n openstack rabbitmq-server-0 rabbitmqctl delete_user "${RABBITMQ_USER}" || echo "User doesn't exist"

# Re-add the user
oc rsh -n openstack rabbitmq-server-0 rabbitmqctl add_user -- "${RABBITMQ_USER}" "${RABBITMQ_PASS}"
oc rsh -n openstack rabbitmq-server-0 rabbitmqctl set_user_tags "${RABBITMQ_USER}" administrator
oc rsh -n openstack rabbitmq-server-0 rabbitmqctl set_permissions -p / "${RABBITMQ_USER}" ".*" ".*" ".*"

# Verify user permissions
oc rsh -n openstack rabbitmq-server-0 rabbitmqctl list_user_permissions "${RABBITMQ_USER}"

# Restart affected services to pick up credentials
oc delete pod -n openstack -l service=nova
```

**Prevention:**
- Always verify RabbitMQ user restoration completed successfully (step 8)
- Check `rabbitmqctl list_users` output includes the backed-up username
- Test one service (e.g., nova-api) before assuming all services will work
- **For EDPM deployments**: Verify compute and network node connectivity immediately after restore
- Monitor data plane node logs during restore to catch issues early

**EDPM-Specific Checks:**

If data plane nodes (compute/network nodes) cannot connect:

```bash
# On a compute node, check nova-compute configuration
ssh compute-node-1
sudo grep -i transport_url /var/lib/config-data/nova/etc/nova/nova.conf
# Verify the username matches what you restored in step 8

# On a network node, check neutron agent configuration
ssh network-node-1
sudo grep -i transport_url /var/lib/config-data/neutron/etc/neutron/neutron.conf
# Verify the username matches what you restored in step 8

# Test direct RabbitMQ connectivity from data plane node
# Get RabbitMQ service IP
oc get svc rabbitmq-cell1 -n openstack -o jsonpath='{.status.loadBalancer.ingress[0].ip}'

# From compute node, test connection (requires amqp-tools)
ssh compute-node-1
# Test if port is reachable
telnet <rabbitmq-cell1-ip> 5672
```

If credentials don't match, you may need to either:
1. Re-run step 8 to restore the correct user credentials in RabbitMQ
2. Or update data plane node configurations (not recommended - requires reconfiguring all nodes)

### Issue: RabbitMQ RBAC/Ownership Errors

**Symptoms:**
- RabbitMQ cluster CR shows `status: False` with RBAC errors
- RabbitMQ operator logs show "forbidden: cannot set an ownerRef" errors
- RabbitMQ pods fail to reconcile properly

**Diagnosis:**

This happens when RabbitMQ-related secrets or ConfigMaps are accidentally restored from backup instead of being filtered out. Check RabbitMQ cluster status:

```bash
# Check RabbitMQ cluster status
oc get rabbitmqcluster -n openstack
oc describe rabbitmqcluster rabbitmq -n openstack

# Look for errors like these in the status or operator logs:
# For Secrets:
# secrets "rabbitmq-default-user" is forbidden: cannot set an ownerRef on a resource you can't delete:
# RBAC: clusterrole.rbac.authorization.k8s.io "rabbitmq-cluster-operator-proxy-role" not found

# For ConfigMaps:
# configmaps "rabbitmq-plugins-conf" is forbidden: cannot set an ownerRef on a resource you can't delete:
# RBAC: clusterrole.rbac.authorization.k8s.io "rabbitmq-cluster-operator-proxy-role" not found

# In RabbitMQ cluster CR status:
#   - lastTransitionTime: "2026-01-20T16:32:08Z"
#   message: 'secrets "rabbitmq-default-user" is forbidden: cannot set an ownerRef on a resource you can't delete: RBAC: clusterrole.rbac.authorization.k8s.io "rabbitmq-cluster-operator-proxy-role" not found, <nil>'
#   reason: Error
#   status: "False"
```

**Root Cause:**

When operators create resources, they own them from the start with no permission issues. When pre-existing resources are restored first, operators try to adopt them by setting `ownerReferences`, but Kubernetes requires delete permissions to set ownerReferences. The operator doesn't have delete permissions on resources it didn't create, causing the RBAC error.

**Solution:**

Delete the pre-existing RabbitMQ secrets/ConfigMaps and let the operator recreate them:

```bash
# Delete RabbitMQ-related secrets
oc delete secret -n openstack -l app.kubernetes.io/part-of=rabbitmq

# Delete RabbitMQ-related ConfigMaps
oc delete configmap -n openstack -l app.kubernetes.io/part-of=rabbitmq

# Restart the RabbitMQ operator to trigger reconciliation
oc delete pod -n openstack-operators -l control-plane=rabbitmq-cluster-operator

# Wait for RabbitMQ clusters to reconcile and create fresh resources
oc get rabbitmqcluster -n openstack --watch

# After RabbitMQ clusters are ready, restore user credentials (step 11)
# See "RabbitMQ User Management" in the Scope section for details
```

**Prevention:**
- Always use the smart filtering approach in step 4 (Restore Secrets) which excludes RabbitMQ resources
- Never restore secrets/ConfigMaps with `app.kubernetes.io/part-of=rabbitmq` label
- Review the restore script output to ensure RabbitMQ resources were filtered

### Issue: Operator Not Reconciling

```bash
# Check operator is running
oc get pods -n openstack-operators

# Check operator logs
oc logs -n openstack-operators deployment/openstack-operator-controller-manager -f

# Verify CRDs are installed
oc get crd | grep openstack
```

### Issue: Secrets Not Found

```bash
# Verify secret exists
oc get secret <secret-name> -n openstack

# Check secret is referenced correctly in CR
oc get openstackcontrolplane -n openstack -o jsonpath='{.items[0].spec.secret}'
```

### Issue: Services Stuck in Pending

```bash
# Check pod events
oc describe pod <pod-name> -n openstack

# Common issues:
# - Storage class not available
# - Insufficient resources
# - Image pull errors
```

### Issue: Different StorageClass in Target

```bash
# List available storage classes in OpenShift
oc get storageclass

# Check which one is marked as default
oc get storageclass -o jsonpath='{.items[?(@.metadata.annotations.storageclass\.kubernetes\.io/is-default-class=="true")].metadata.name}'

# Update the control plane CR before applying
# You can edit the JSON file directly or use jq to update it
vi openstackcontrolplane-backup.json
# Change global storageClass to available class in target cluster
# Also check for service-specific storage class overrides in service templates:
#   - spec.galera.templates[*].storageClass
#   - spec.rabbitmq.templates[*].persistence.storageClassName

# Or use jq to update programmatically:
# jq '.items[0].spec.storageClass = "new-storage-class-name"' openstackcontrolplane-backup.json > openstackcontrolplane-backup.json.tmp
# mv openstackcontrolplane-backup.json.tmp openstackcontrolplane-backup.json
#   - spec.ovn.template.ovnDBCluster[*].storageClass

# Common OpenShift storage classes:
# - ocs-storagecluster-ceph-rbd (OpenShift Data Foundation)
# - local-storage
```

---

## OADP Storage Volume Backup/Restore Issues

For issues related to OADP-based storage volume backups (Glance, Cinder, Swift, Manila), see [backup-restore-storage-volumes.md](backup-restore-storage-volumes.md).

### OADP Backup Stuck in "InProgress"

**Symptoms:**
- Backup remains in "InProgress" status for extended time
- Restic pods showing high CPU/memory usage
- Backup never completes

**Diagnosis:**

```bash
# Find Restic pods
oc get pods -n openshift-adp -l app.kubernetes.io/name=node-agent

# Check Restic logs on specific node
oc logs -n openshift-adp node-agent-xxxxx

# Check backup status
oc get backup openstack-volumes-20260225-140530 -n openshift-adp -o yaml
```

**Common Causes:**
- Large volumes taking longer than expected
- Network issues to MinIO
- Resource constraints on Restic pods
- PVC mounted by running pod (some backup methods require pod to be stopped)

**Solutions:**

```bash
# Increase timeout and retry
oc wait --for=jsonpath='{.status.phase}'=Completed \
  backup/openstack-volumes-20260225-140530 \
  -n openshift-adp \
  --timeout=120m

# Increase Restic resources in DPA
oc edit dataprotectionapplication velero -n openshift-adp
# Add under spec.configuration.restic:
#   podConfig:
#     resourceAllocations:
#       limits:
#         cpu: "2"
#         memory: 4Gi
#       requests:
#         cpu: "1"
#         memory: 2Gi
```

### OADP Backup Failed

**Symptoms:**
- Backup shows "Failed" or "PartiallyFailed" status
- Error messages in backup description

**Diagnosis:**

```bash
# Get backup status and errors
oc get backup openstack-volumes-20260225-140530 -n openshift-adp -o yaml

# Check Velero logs
oc logs -n openshift-adp deployment/velero

# Describe backup for events
oc describe backup openstack-volumes-20260225-140530 -n openshift-adp
```

**Common Causes:**
- PVCs not labeled correctly
- MinIO connectivity issues
- Insufficient permissions
- Storage full in MinIO

**Solutions:**

```bash
# Verify PVC labels
oc get pvc -n openstack --show-labels | grep backup-volume

# Test MinIO connectivity
oc run -it --rm debug --image=curlimages/curl --restart=Never -- \
  curl -v https://$(oc get route minio-api -n minio -o jsonpath='{.spec.host}')

# Check MinIO storage
oc get pvc -n minio
```

### PVCs Not Being Backed Up

**Symptoms:**
- Backup completes but some PVCs are missing
- Backup shows fewer volumes than expected

**Diagnosis:**

```bash
# Check if PVCs have the backup label
oc get pvc -n openstack --show-labels | grep backup-volume

# Check backup spec
oc get backup openstack-volumes-20260225-140530 -n openshift-adp -o yaml | grep -A 5 labelSelector

# List PVCs that should be backed up
oc get pvc -n openstack -l service=glance
oc get pvc -n openstack -l service=cinder
oc get pvc -n openstack -l service=swift
oc get pvc -n openstack -l service=manila
```

**Solutions:**

```bash
# Add missing labels
oc label pvc <pvc-name> -n openstack openstack.org/backup=true

# Verify label was added
oc get pvc <pvc-name> -n openstack --show-labels
```

### OADP Restore Creates New PVCs

**Symptoms:**
- Restore creates new PVCs instead of restoring data to existing ones
- Old PVCs still exist after restore
- Pods not using restored data

**Diagnosis:**
This is expected behavior for OADP. The restore process creates new PVCs with the same names and restores data into them.

**Solutions:**

```bash
# Proper restore workflow:
# 1. Scale down pods using the PVCs
oc scale deployment glance-api -n openstack --replicas=0

# 2. Delete old PVCs
oc delete pvc glance-api-0 -n openstack

# 3. Run OADP restore (creates new PVCs with restored data)
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

# 4. Wait for restore to complete
oc wait --for=jsonpath='{.status.phase}'=Completed \
  restore/openstack-volumes-restore -n openshift-adp --timeout=60m

# 5. Scale up pods (will use restored PVCs)
oc scale deployment glance-api -n openstack --replicas=1
```

### BackupStorageLocation Unavailable

**Symptoms:**
- BackupStorageLocation shows "Unavailable" status
- Backups cannot be created
- Error about storage location

**Diagnosis:**

```bash
# Check BSL status
oc get backupstoragelocation -n openshift-adp

# Get detailed status
oc get backupstoragelocation -n openshift-adp -o yaml

# Check Velero logs
oc logs -n openshift-adp deployment/velero
```

**Common Causes:**
- MinIO not running or not accessible
- Incorrect credentials in cloud-credentials secret
- MinIO bucket doesn't exist
- Network connectivity issues

**Solutions:**

```bash
# Verify MinIO is running
oc get deployment minio -n minio

# Verify MinIO bucket exists
oc get route minio-console -n minio -o jsonpath='{.spec.host}'
# Open in browser and check for 'velero' bucket

# Verify cloud credentials
oc get secret cloud-credentials -n openshift-adp -o yaml

# Recreate cloud credentials if needed
cat <<EOF > /tmp/credentials-velero
[default]
aws_access_key_id=<ACCESS_KEY_ID>
aws_secret_access_key=<SECRET_ACCESS_KEY>
EOF

oc create secret generic cloud-credentials \
  --from-file cloud=/tmp/credentials-velero \
  -n openshift-adp \
  --dry-run=client -o yaml | oc apply -f -

rm /tmp/credentials-velero

# Restart Velero pod to pick up new credentials
oc delete pod -l app.kubernetes.io/name=velero -n openshift-adp
```

---
