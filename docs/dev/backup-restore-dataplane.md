# OpenStack DataPlane Backup and Restore

This document describes the procedure for backing up and restoring the OpenStack DataPlane configuration (compute nodes and edge services).

## Overview

The OpenStack DataPlane consists of:
- **NetConfig**: Network configuration (defines network topology, subnets, IP allocation pools)
- **OpenStackDataPlaneNodeSet**: Defines compute nodes and their configuration
- **OpenStackDataPlaneService**: Defines services deployed on dataplane nodes
- **IPSet**: IP address allocations for dataplane nodes
- **Reservation**: IP address reservations for dataplane interfaces

**Important:** OpenStackDataPlaneDeployment resources **are backed up for reference** but are **NOT restored** as they are ephemeral. Re-creating them would trigger a new deployment, which is not desired during restore.

**Note:** This procedure is designed for DataPlaneNodeSets with `preProvisioned: true`. For EDPM nodes provisioned via OpenStackBaremetalSet and Metal3, there is an additional procedure required to restore those objects (OpenStackBaremetalSet, BareMetalHost, etc.).

## Prerequisites

- Ansible installed on the system running the backup/restore
- `oc` CLI configured and authenticated to the cluster
- `jq` installed for JSON processing

## What Gets Backed Up

### Network Configuration
1. **NetConfig** - Network topology definition (subnets, IP allocation pools, VLANs)

### DataPlane Custom Resources
2. **OpenStackDataPlaneNodeSet** - Compute node definitions with configuration
3. **OpenStackDataPlaneService** - Service definitions for dataplane
4. **OpenStackDataPlaneDeployment** - Deployment history (for reference only, NOT restored)

### IP Allocation Resources (without ownerReferences)
5. **Reservation** - IP reservations for node interfaces
6. **IPSet** - IP allocations for dataplane nodes

> **Note:** Reservations and IPSets are backed up **without ownerReferences** to allow restoration before recreating the DataPlaneNodeSets. The operator will re-establish ownership on reconciliation.

## Backup Procedure

Follow these steps to backup the DataPlane:

```bash
NAMESPACE=openstack
BACKUP_DIR=./openstack-dataplane-backup-$(date +%Y%m%d-%H%M%S)
mkdir -p $BACKUP_DIR

# 1. Backup NetConfig (CRITICAL: defines network topology)
oc get netconfig -n $NAMESPACE -o json | \
  jq 'del(.items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > $BACKUP_DIR/netconfig-backup.json

# 2. Backup DataPlaneNodeSets (remove ownerReferences)
oc get openstackdataplanenodeset -n $NAMESPACE -o json | \
  jq 'del(.items[].metadata.ownerReferences,
          .items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > $BACKUP_DIR/dataplanenodeset-backup.json

# 3. Backup DataPlaneServices (remove ownerReferences)
oc get openstackdataplaneservice -n $NAMESPACE -o json | \
  jq 'del(.items[].metadata.ownerReferences,
          .items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > $BACKUP_DIR/dataplaneservice-backup.json

# 4. Backup DataPlaneDeployments (for reference, NOT restored)
oc get openstackdataplanedeployment -n $NAMESPACE -o json | \
  jq 'del(.items[].metadata.ownerReferences,
          .items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > $BACKUP_DIR/dataplanedeployment-backup.json

# 5. Backup Reservations (CRITICAL: remove ownerReferences)
oc get reservation -n $NAMESPACE -o json | \
  jq 'del(.items[].metadata.ownerReferences,
          .items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > $BACKUP_DIR/reservation-backup.json

# 6. Backup IPSets (CRITICAL: remove ownerReferences)
oc get ipset -n $NAMESPACE -o json | \
  jq 'del(.items[].metadata.ownerReferences,
          .items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > $BACKUP_DIR/ipset-backup.json

# 7. Create archive
tar -czf openstack-dataplane-backup-$(date +%Y%m%d-%H%M%S).tar.gz $BACKUP_DIR
```

## Restore Procedure

### Prerequisites

1. **ControlPlane must be restored first** from the ControlPlane backup (see [backup-restore-stateless.md](backup-restore-stateless.md))

2. The target cluster should have the same OpenStack operator versions as the source cluster (check `operator-versions.txt` from ControlPlane backup)

3. The namespace should exist (any existing DataPlane resources with the same names will be replaced)

### Restore Steps

**Important Restore Order:**

1. **NetConfig FIRST** (defines network topology, required by Reservations/IPSets)
2. **Reservations SECOND** (before IPSets and NodeSets, no ownerReferences)
3. **IPSets THIRD** (before NodeSets, no ownerReferences)
4. **DataPlaneServices FOURTH** (before NodeSets, as NodeSets may reference them)
5. **DataPlaneNodeSets LAST**
6. **DataPlaneDeployments** are **NOT restored** (backed up for reference only)

```bash
NAMESPACE=openstack
BACKUP_DIR=./openstack-dataplane-backup-YYYYMMDD-HHMMSS

# Extract backup
tar -xzf openstack-dataplane-backup-*.tar.gz

# Step 1: Restore NetConfig (FIRST - defines network topology)
oc apply -f $BACKUP_DIR/netconfig-backup.json

# Step 2: Restore Reservations (SECOND)
oc apply -f $BACKUP_DIR/reservation-backup.json

# Step 3: Restore IPSets (THIRD)
oc apply -f $BACKUP_DIR/ipset-backup.json

# Step 4: Restore DataPlane Services (FOURTH)
oc apply -f $BACKUP_DIR/dataplaneservice-backup.json

# Step 5: Restore DataPlane NodeSets (LAST)
oc apply -f $BACKUP_DIR/dataplanenodeset-backup.json

# Step 6: Verify restoration
oc get netconfig -n $NAMESPACE
oc get reservation -n $NAMESPACE
oc get ipset -n $NAMESPACE
oc get openstackdataplaneservice -n $NAMESPACE
oc get openstackdataplanenodeset -n $NAMESPACE
```

## Why Remove ownerReferences?

**Problem:** IPSets and Reservations typically have `ownerReferences` with UIDs pointing to the original DataPlaneNodeSet. Kubernetes will reject resources with ownerReferences to non-existent UIDs.

**Solution:** Remove `ownerReferences` from the backup. On restore:
1. IPSets and Reservations are created first (without owners)
2. DataPlaneNodeSets are created second
3. The operator reconciles and re-establishes ownership automatically

## Relationship to ControlPlane Backup

The DataPlane backup is **separate** from the ControlPlane backup but has certain prerequisites that are typically satisfied by restoring the ControlPlane first.

**Prerequisites for DataPlane Restore:**
- **Secrets/ConfigMaps**: SSH keys, certificates, and configurations referenced by NodeSets must exist. While these are logically DataPlane resources, they are backed up with ControlPlane because there is no reliable way to distinguish which secrets/configmaps belong to ControlPlane vs DataPlane (some may be shared between both).
- **Operators**: OpenStack operators must be running to reconcile DataPlane resources

These prerequisites can be satisfied by restoring the ControlPlane (typical case) or by ensuring operators are installed and required secrets/configmaps exist in the target cluster.

**NetConfig** is included in the **DataPlane** backup (not ControlPlane) because:
- NetConfig defines network topology consumed by DataPlane resources (IPSets, Reservations)
- ControlPlane components use NetworkAttachmentDefinitions and MetalLB (not NetConfig)

**Why this separation?**
- **ControlPlane**: Stateless services, MariaDB/RabbitMQ definitions, Secrets/ConfigMaps (all)
- **DataPlane**: Network topology (NetConfig), compute nodes (NodeSets), IP allocations (IPSets/Reservations)

See [backup-restore-stateless.md](backup-restore-stateless.md) for ControlPlane backup/restore procedures.

## Complete Backup/Restore Workflow

For a full OpenStack backup and restore:

### Backup Order
1. **Backup ControlPlane** - see [backup-restore-stateless.md](backup-restore-stateless.md)
2. **Backup DataPlane** (includes NetConfig) - follow the backup procedure above

### Restore Order
1. **Cleanup target cluster** (if needed) - see [cleanup-openstack-ctlplane.yaml](cleanup-openstack-ctlplane.yaml)
2. **Restore ControlPlane** - see [backup-restore-stateless.md](backup-restore-stateless.md)
3. **Restore DataPlane** (includes NetConfig) - follow the restore procedure above

**Note:** Typically the ControlPlane is restored before the DataPlane to ensure operators are running and required secrets/configmaps exist. However, DataPlane can be restored independently if these prerequisites are already in place.

## Limitations

### Scope
- This procedure is designed for **DataPlaneNodeSets with `preProvisioned: true`**
- For EDPM nodes provisioned via **OpenStackBaremetalSet and Metal3**, there is an additional procedure required to restore those objects (OpenStackBaremetalSet, BareMetalHost, etc.)

### Deployment History Lost After Restore

**Current Behavior:**
When restoring only the NodeSets (without Deployments), the deployment history is lost and NodeSets enter a "Requested" state:

```bash
$ oc get openstackdataplanenodeset
NAME             STATUS   MESSAGE
openstack-edpm   False    NodeSet setup ready, waiting for OpenStackDataPlaneDeployment...
```

```yaml
conditions:
  - lastTransitionTime: "2026-02-11T11:13:22Z"
    message: NodeSet setup ready, waiting for OpenStackDataPlaneDeployment...
    reason: Requested
    severity: Info
    status: "False"
    type: Ready
```

This is a **safe state** - the NodeSet is ready but waiting for a deployment to be created. The actual dataplane nodes are running correctly; this only affects the Kubernetes CR status.

**Root Cause:**
We cannot restore the OpenStackDataPlaneDeployment with its previous state. The default behavior is to not restore the status subresource. If we wanted to restore the status to reflect the completed deployment state (`status.deployed = true`), it would require a second API call. The problem is that the first API call to restore the deployment spec would trigger a new deployment run instead of restoring the old status.

The deployment controller starts reconciling when it sees a deployment with `status.deployed == false` (the default for newly created resources). The controller checks this at openstackdataplanedeployment_controller.go:106-110:

```go
if instance.Status.Deployed {
    return ctrl.Result{}, nil  // Skip processing if already deployed
}
```

Between the first API call (creating the deployment) and the second call (patching the status), the controller would already start processing, potentially triggering unwanted ansible job execution.

**Potential Solution (Under Discussion):**
Add an annotation-based mechanism to prevent controller reconciliation during restore:

1. Modify the deployment controller to check for a restore annotation before processing:
   ```go
   if restoring, ok := instance.Annotations["dataplane.openstack.org/restore-in-progress"]; ok && restoring == "true" {
       Log.Info("Deployment restore in progress, skipping reconciliation")
       return ctrl.Result{}, nil
   }
   ```

2. Restore workflow would be:
   - Restore NodeSets first (controller dependency: deployments check that nodesets exist)
   - Create deployment with spec + annotation `dataplane.openstack.org/restore-in-progress: "true"`
   - Update status subresource with `status.deployed = true`
   - Remove the annotation to resume normal reconciliation

This approach follows the existing pattern used in ControlPlane's `deployment-stage` annotation mechanism.

**Status:** This will be addressed in a future update after team discussion.

### Not Backed Up
- **OpenStackDataPlaneDeployment status**: Currently not restored to avoid triggering new deployments (see "Deployment History Lost After Restore" above).

### Dependencies from ControlPlane Backup
The following are restored as part of the ControlPlane backup/restore procedure:
- **Secrets referenced by NodeSets**: SSH keys, certificates, etc.
- **ConfigMaps referenced by NodeSets**: Custom configuration

### Post-Restore Verification
After restore, you may need to:
- Verify SSH keys are accessible
- Test connectivity to compute nodes
- Verify EDPM ansible can reach the nodes

## Troubleshooting

### IPSets/Reservations show errors about networks not found
**Cause:** NetConfig not restored
**Solution:** Restore NetConfig first (Step 1 of DataPlane restore procedure)

### DataPlaneNodeSet stuck in "Not Ready"
**Cause:** Missing secrets or configmaps
**Solution:** Check NodeSet spec for referenced secrets/configmaps and ensure they exist

### Duplicate IP allocations
**Cause:** Reservations or IPSets restored after NodeSet tried to allocate IPs
**Solution:** Always restore Reservations and IPSets **before** NodeSets (Reservations → IPSets → NodeSets)

## Files in Backup

- `netconfig-backup.json` - Network topology definition
- `dataplanenodeset-backup.json` - NodeSet definitions
- `dataplaneservice-backup.json` - Service definitions
- `dataplanedeployment-backup.json` - Deployment history (reference only, NOT restored)
- `reservation-backup.json` - IP reservations (no ownerReferences)
- `ipset-backup.json` - IP allocations (no ownerReferences)
- `README.md` - Backup metadata

## References

- [ControlPlane Backup/Restore](backup-restore-stateless.md) - Must be done first
- [Cleanup Playbook](cleanup-openstack-ctlplane.yaml) - Clean before restore
