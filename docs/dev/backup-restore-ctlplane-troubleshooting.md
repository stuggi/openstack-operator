# OpenStack Control Plane Backup and Restore - Troubleshooting

This document contains troubleshooting guidance for the OpenStack Control Plane backup and restore procedures.

For the main backup/restore documentation, see [backup-restore-ctlplane.md](backup-restore-ctlplane.md).

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
