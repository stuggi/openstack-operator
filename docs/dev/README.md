# OpenStack Operator Backup and Restore Documentation

This directory contains documentation for backing up and restoring OpenStack deployments on Kubernetes/OpenShift.

## Quick Start

For a complete OpenStack backup and restore:

1. **[Backup ControlPlane](backup-restore-ctlplane.md)** - OpenStackControlPlane CR, secrets, configmaps
2. **[Backup DataPlane](backup-restore-dataplane.md)** - Compute nodes and network configuration

## Prerequisites and Setup

### General Requirements

Before starting any backup or restore procedure, ensure you have:

1. **OpenShift CLI (`oc`) installed** - Version compatible with your cluster
2. **Cluster access** - Valid credentials with appropriate permissions
3. **Logged into the cluster**:

```bash
# Login to your OpenShift cluster
oc login https://api.your-cluster.example.com:6443 --username <username> --password <password>

# Or use token-based authentication
oc login --token=<token> --server=https://api.your-cluster.example.com:6443

# Verify you're connected
oc whoami
oc project openstack
```

4. **Sufficient permissions** - Cluster admin or namespace admin rights for the `openstack` namespace
5. **Storage for backups** - Local or remote location to store backup archives

### OADP Setup

| Document | Description |
|----------|-------------|
| [setup-oadp-minio.md](setup-oadp-minio.md) | **OADP Setup** - Set up OADP (OpenShift API for Data Protection) for storage volume backups. Uses MinIO as S3-compatible storage (ODF or other S3-compatible storage can also be used) |

See also: [OADP Setup Playbooks](#oadp-setup-playbooks) for automated installation using Ansible.

### Restore Prerequisites

#### Operator Version Matching

**CRITICAL**: The target cluster (where you restore) must have the **same versions** of the OpenStack operators as the source cluster (where you backed up).

**Why this is critical:**
- CRD schema changes between operator versions can break restore
- Different operator versions may expect different CR field structures
- Container image versions tracked in OpenStackVersion CR must match operator versions

See [Operator Version Requirements](backup-restore-ctlplane.md#operator-version-requirements) for detailed procedures on checking, documenting, and verifying operator versions.

#### Storage Class Availability

The target cluster must have the same storage classes available as the source cluster, or you must update the backup files before restore.

**Note**: The OpenStackControlPlane CR defines a global `storageClass`, but individual services (Galera, RabbitMQ, OVN, etc.) can override this with service-specific storage class parameters.

See [Storage Class Requirements](backup-restore-ctlplane.md#storage-class-requirements) for detailed procedures.

## Core Backup/Restore Procedures

| Document | Description |
|----------|-------------|
| [backup-restore-ctlplane.md](backup-restore-ctlplane.md) | **ControlPlane** backup/restore - OpenStackControlPlane CR, secrets, configmaps |
| [backup-restore-dataplane.md](backup-restore-dataplane.md) | **DataPlane** backup/restore - Compute nodes (NodeSets), network configuration (NetConfig), IP allocations |
| [backup-restore-storage-volumes.md](backup-restore-storage-volumes.md) | **Storage Volumes** - Backup/restore persistent volumes (Glance, Cinder, Swift, Manila, ...) using OADP |
| [backup-restore-troubleshooting.md](backup-restore-troubleshooting.md) | **Troubleshooting** - Common issues and solutions for backup/restore |
| [backup-restore-ctlplane-alternatives.md](backup-restore-ctlplane-alternatives.md) | **Alternative Approaches** - Other backup methods (e.g., must-gather) |

## Ansible Playbooks

### Backup and Restore Playbooks

| Playbook | Description |
|----------|-------------|
| [playbooks/backup-openstack-ctlplane.yaml](playbooks/backup-openstack-ctlplane.yaml) | Ansible playbook to backup ControlPlane resources |
| [playbooks/backup-openstack-dataplane.yaml](playbooks/backup-openstack-dataplane.yaml) | Ansible playbook to backup DataPlane resources |
| [playbooks/restore-openstack-ctlplane.yaml](playbooks/restore-openstack-ctlplane.yaml) | Ansible playbook to restore ControlPlane resources |
| [playbooks/restore-openstack-dataplane.yaml](playbooks/restore-openstack-dataplane.yaml) | Ansible playbook to restore DataPlane resources |
| [playbooks/cleanup-openstack-ctlplane.yaml](playbooks/cleanup-openstack-ctlplane.yaml) | Ansible playbook to clean up ControlPlane resources (use before restore) |
| [playbooks/cleanup-openstack-dataplane.yaml](playbooks/cleanup-openstack-dataplane.yaml) | Ansible playbook to clean up DataPlane resources (use before restore) |

### OADP Setup Playbooks

| Playbook | Description |
|----------|-------------|
| [playbooks/setup-minio.yaml](playbooks/setup-minio.yaml) | Deploy MinIO as S3-compatible storage for OADP (does NOT use ODF) |
| [playbooks/setup-oadp.yaml](playbooks/setup-oadp.yaml) | Install and configure OADP operator with MinIO backend |

## Backup/Restore Workflow

### Full Backup Procedure

```bash
# 1. Backup ControlPlane (OpenStackControlPlane CR, secrets, configmaps)
ansible-playbook playbooks/backup-openstack-ctlplane.yaml -e openstack_namespace=openstack

# 2. Backup DataPlane (NodeSets, NetConfig, IP allocations)
ansible-playbook playbooks/backup-openstack-dataplane.yaml -e openstack_namespace=openstack
```

### Full Restore Procedure

```bash
# 1. Ensure operators are installed in target cluster

# 2. Restore ControlPlane (OpenStackControlPlane CR, secrets, configmaps)
ansible-playbook playbooks/restore-openstack-ctlplane.yaml \
  -e openstack_namespace=openstack \
  -e backup_file=backups/openstack-ctlplane-backup-20260209-162223.tar.gz

# 3. Restore DataPlane (NodeSets, NetConfig, IP allocations)
ansible-playbook playbooks/restore-openstack-dataplane.yaml \
  -e openstack_namespace=openstack \
  -e backup_file=backups/openstack-dataplane-backup-20260209-162223.tar.gz
```

## Features Added for Backup/Restore

The following enhancements were implemented to enable reliable backup and restore:

| Feature | PR | Description |
|---------|-----|-------------|
| **Staged Deployment** | [#1785](https://github.com/openstack-k8s-operators/openstack-operator/pull/1785) | Allows pausing deployment after infrastructure (MariaDB, OVN, RabbitMQ) is ready but before OpenStack services start. Enables database restore before services initialize schemas, avoiding conflicts and service restarts. See [enhancement-staged-deployment-restore.md](enhancement-staged-deployment-restore.md) for details. |
| **Service Name Caching** | [#1796](https://github.com/openstack-k8s-operators/openstack-operator/pull/1796) | Ensures service names remain consistent across backup/restore when `UniquePodNames` is enabled. Service names are cached via webhook during initial creation and preserved when CR is recreated from backup, preventing service name changes that would break references. |

## Key Concepts

### ControlPlane vs DataPlane

- **ControlPlane**: Stateless OpenStack services running in containers (Keystone, Nova API, Neutron API, etc.), along with infrastructure services (MariaDB, RabbitMQ, OVN)
- **DataPlane**: Compute nodes and edge services (Nova Compute, OVN agents, etc.) running on baremetal or VMs

### Backup Scope

| Component | What's Backed Up | What's NOT Backed Up |
|-----------|------------------|---------------------|
| ControlPlane | OpenStackControlPlane CR<br>OpenStackVersion CR<br>NetworkAttachmentDefinitions<br>Secrets (all application secrets)<br>ConfigMaps (user-provided only)<br>MariaDBDatabase/MariaDBAccount CRs<br>GaleraBackup CRs<br>Issuer CRs (TLS)<br>Topology<br>BGPConfiguration<br>DNSData<br>InstanceHa<br>**Database contents** (Galera/MariaDB dumps)<br>**PVCs/PVs** (Glance, Cinder, Swift, Manila - via OADP)<br>**RabbitMQ user credentials** (for fresh cluster recreation) | Individual service CRs (Keystone, Nova, etc. - recreated by controller)<br>Certificate CRs (recreated by operators)<br>Running pods<br>OVN database contents<br>RabbitMQ queue data (fresh cluster created) |
| DataPlane | NetConfig (network topology)<br>OpenStackDataPlaneNodeSet<br>OpenStackDataPlaneService<br>Reservation (IP reservations)<br>IPSet (IP allocations)<br>OpenStackDataPlaneDeployment (reference only) | OpenStackDataPlaneDeployment status (not restored to avoid triggering new deployments) |

### Restore Order

The restore order is important due to dependencies:

1. **ControlPlane** - Provides operators, secrets, configmaps
2. **DataPlane** - Requires ControlPlane prerequisites (operators running, secrets/configmaps existing)

## Limitations and Known Issues

### DataPlane Deployment History

When restoring DataPlane, the OpenStackDataPlaneDeployment history is lost. NodeSets will show:

```
STATUS: False
MESSAGE: NodeSet setup ready, waiting for OpenStackDataPlaneDeployment...
```

This is a **safe state** - the actual dataplane nodes are running correctly; only the Kubernetes CR status shows waiting. See [backup-restore-dataplane.md](backup-restore-dataplane.md#deployment-history-lost-after-restore) for details and potential solutions.

### Pre-Provisioned Nodes Only

The current DataPlane backup/restore procedure is designed for **NodeSets with `preProvisioned: true`**. For nodes provisioned via OpenStackBaremetalSet and Metal3, additional procedures are required.

## Future Enhancements

The following features are under consideration for future implementation:

### Experimental Restore Scenarios (Not Tested)

Additional restore scenarios have been documented but **NOT tested**:

| Document | Description |
|----------|-------------|
| [backup-restore-ctlplane-experimental.md](backup-restore-ctlplane-experimental.md) | **Experimental** - Scenario 2 (Different Namespace) and Scenario 3 (Different Cluster) restore procedures. ⚠️ Use at your own risk. |

These scenarios are theoretically possible but require additional testing and validation before production use.

### Backup/Restore During Partial Updates

**Current Limitation:**
Backup/restore does **not work** when the environment is in a partially updated state. If a minor update is in progress (e.g., operators updated but ControlPlane or DataPlane CRs not yet updated), the OpenStackVersion CR does not correctly restore the in-flight update state. This results in loss of service container images from older releases.

**Impact:**
- **Must complete updates fully** before backup (operators + ControlPlane + DataPlane all at the same version)
- Backup during partial update will lose version tracking information
- Restore will fail or result in incorrect service image versions

**Proposed Enhancement:**
- Properly handle OpenStackVersion CR in-flight state during backup/restore
- Preserve service image information across all release versions involved in the update
- Detect and warn when attempting backup during partial update

**Related Issues:**
- [OSPRH-26244](https://issues.redhat.com/browse/OSPRH-26244) - Backup/restore support
- [OSPRH-26246](https://issues.redhat.com/browse/OSPRH-26246) - Backup only possible for fully updated environments

### Resource Labeling for ControlPlane vs DataPlane Separation

**Current Limitation:**
All Secrets and ConfigMaps are backed up together in the ControlPlane backup because there is today no reliable way to distinguish which resources belong to ControlPlane vs DataPlane. Some resources may be shared between both.

**Proposed Enhancement:**
Operators could label all resources they create or reference (including user-provided Secrets/ConfigMaps) with component labels such as:
- `openstack.org/component: controlplane`
- `openstack.org/component: dataplane`

**Benefits:**
- Enable complete separation of ControlPlane and DataPlane backup/restore procedures
- Allow restoring only DataPlane resources without ControlPlane
- Clearer resource ownership and dependency tracking
- Smaller, more focused backups

**Related Issues:**
- [OSPRH-26643](https://issues.redhat.com/browse/OSPRH-26643) - Resource Labeling for ControlPlane vs DataPlane Separation

### Automatic CR Discovery for Backup

**Current Limitation:**
The backup procedures use a hardcoded list of Custom Resources (OpenStackVersion, Topology, BGPConfiguration, DNSData, InstanceHa, etc.). When new CRDs are introduced in operator upgrades, the backup procedure must be manually updated to include them.

**Proposed Enhancement:**
Add labels or annotations to CRD definitions to enable automatic discovery of resources that should be backed up:

```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: topologies.topology.openstack.org
  labels:
    openstack.org/backup: "true"
    openstack.org/backup-category: "controlplane"  # or "dataplane"
```

Backup procedures could then automatically discover all CRs to backup:

```bash
# Get all CRDs marked for backup
BACKUP_CRDS=$(oc get crd -l openstack.org/backup=true,openstack.org/backup-category=controlplane \
  -o jsonpath='{.items[*].spec.names.plural}')

# Backup each CRD's resources
for crd in $BACKUP_CRDS; do
  oc get $crd -n openstack -o json | jq '...' > backup/${crd}-backup.json
done
```

**Benefits:**
- Automatic discovery of new CRs when operators are upgraded
- No manual backup procedure updates needed
- Clear declaration of which CRs are user-facing vs operator-managed
- Separation of ControlPlane vs DataPlane resources at the CRD level

**Considerations:**
- Requires changes to all operator CRD definitions
- Need to define backup categories and filtering strategies
- May need additional labels (e.g., `openstack.org/backup-filter: "no-owner"` for user-created resources only)

**Related Issues:**
- [OSPRH-26645](https://issues.redhat.com/browse/OSPRH-26645) - Automatic CR Discovery for Backup

### Storage Volume Backup Labels

**Current Limitation:**
Services that create PVCs requiring backup must be manually labeled with `openstack.org/backup: "true"` to enable OADP/Restic backups (see [backup-restore-storage-volumes.md](backup-restore-storage-volumes.md)).

**Proposed Enhancement:**
Service operators (glance-operator, cinder-operator, swift-operator, manila-operator) should automatically add the `openstack.org/backup: "true"` label to PVCs they create for persistent storage.

**Implementation:**
Operators would add the label in their PVC creation logic:

```go
// Example: Add backup label to PVC template
pvc.Labels = map[string]string{
    "service":                        "glance",
    "openstack.org/backup":   "true",
    "app.kubernetes.io/name":         "glance",
}
```

**Benefits:**
- Automatic opt-in for volume backups without manual labeling
- Clear declaration of which PVCs contain persistent data
- Consistent backup coverage across all storage services
- No manual intervention needed after operator deployment

**Services Requiring This:**
- Glance (image storage)
- Cinder (volume backend storage, depending on backend)
- Swift (object storage, if using PVC backend)
- Manila (share storage, depending on backend)

**Related Issues:**
- [OSPRH-27012](https://issues.redhat.com/browse/OSPRH-27012) - Storage Volume Backup Labels

### Galera Backup Timestamp Tracking

**Current Limitation:**
When performing Galera database backups during the control plane backup procedure, the timestamp on the database dump files does not exactly match the control plane backup timestamp. This creates a manual step during restore where users must exec into the restore pod and find the dump file with the closest timestamp to the backup.

**Example:**
- Control plane backup triggered: `2026-02-26_10-12-00` (global BACKUP_DATE)
- Galera backup job created: `backup-openstack-2026-02-26_10-12-00`
- Actual dump file created: `openstack_backup_2026-02-26_10-12-59.sql.gz` (59 seconds later)

During restore, users must:
1. List dump files: `oc exec openstack-restore-openstackrestore -- ls -la /backup/data/`
2. Manually find the closest timestamp to the control plane backup
3. Execute restore with the correct timestamp

**Proposed Enhancement:**
Pass the global `BACKUP_DATE` timestamp as an environment variable when creating Galera backup jobs:

```bash
# Updated job creation command
oc create job backup-openstack-${BACKUP_DATE} \
  --from=cronjob/openstack-backup-openstack \
  --env="BACKUP_TIMESTAMP=${BACKUP_DATE}" \
  -n openstack
```

The Galera backup script would check for the `BACKUP_TIMESTAMP` environment variable:

```bash
# In the Galera backup script
TIMESTAMP=${BACKUP_TIMESTAMP:-$(date +%Y-%m-%d_%H-%M-%S)}
DUMP_FILE="openstack_backup_${TIMESTAMP}.sql.gz"
GRANTS_FILE="openstack_backup-grants_${TIMESTAMP}.sql.gz"
```

**Benefits:**
- Eliminates manual timestamp matching during restore
- Dump filenames exactly match control plane backup timestamp
- Simple implementation (just pass environment variable)
- Backward compatible (falls back to auto-generated timestamp if env var not set)
- No metadata tracking or additional CRs needed
- Fully automated restore without user intervention

**Result:**
- Backup archive: `openstack-ctlplane-backup-2026-02-26_10-12-00.tar.gz`
- Dump files: `openstack_backup_2026-02-26_10-12-00.sql.gz` (exact match!)
- Restore: Can automatically construct dump file path from archive name

**Implementation:**
- Update backup playbook to pass `BACKUP_TIMESTAMP` env var when creating jobs
- Update Galera backup script to use env var if present
- Update restore docs to use exact timestamp from backup archive name

**Related Issues:**
- [OSPRH-27069](https://issues.redhat.com/browse/OSPRH-27069) - Allow passing backup timestamp to backup_galera
