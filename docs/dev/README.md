# OpenStack Operator Backup and Restore Documentation

This directory contains documentation for backing up and restoring OpenStack deployments on Kubernetes/OpenShift.

## Quick Start

For a complete OpenStack backup and restore:

1. **[Backup ControlPlane](backup-restore-ctlplane.md)** - OpenStackControlPlane CR, secrets, configmaps
2. **[Backup DataPlane](backup-restore-dataplane.md)** - Compute nodes and network configuration

## Core Backup/Restore Procedures

| Document | Description |
|----------|-------------|
| [backup-restore-ctlplane.md](backup-restore-ctlplane.md) | **ControlPlane** backup/restore - OpenStackControlPlane CR, secrets, configmaps |
| [backup-restore-dataplane.md](backup-restore-dataplane.md) | **DataPlane** backup/restore - Compute nodes (NodeSets), network configuration (NetConfig), IP allocations |

## Ansible Playbooks

| Playbook | Description |
|----------|-------------|
| [backup-openstack-ctlplane.yaml](backup-openstack-ctlplane.yaml) | Ansible playbook to backup ControlPlane resources |
| [backup-openstack-dataplane.yaml](backup-openstack-dataplane.yaml) | Ansible playbook to backup DataPlane resources |
| [restore-openstack-ctlplane.yaml](restore-openstack-ctlplane.yaml) | Ansible playbook to restore ControlPlane resources |
| [restore-openstack-dataplane.yaml](restore-openstack-dataplane.yaml) | Ansible playbook to restore DataPlane resources |

## Backup/Restore Workflow

### Full Backup Procedure

```bash
# 1. Backup ControlPlane (OpenStackControlPlane CR, secrets, configmaps)
ansible-playbook backup-openstack-ctlplane.yaml

# 2. Backup DataPlane (NodeSets, NetConfig, IP allocations)
ansible-playbook backup-openstack-dataplane.yaml
```

### Full Restore Procedure

```bash
# 1. Ensure operators are installed in target cluster

# 2. Restore ControlPlane (OpenStackControlPlane CR, secrets, configmaps)
ansible-playbook restore-openstack-ctlplane.yaml

# 3. Restore DataPlane (NodeSets, NetConfig, IP allocations)
ansible-playbook restore-openstack-dataplane.yaml
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
| ControlPlane | OpenStackControlPlane CR, Secrets, ConfigMaps | Individual service CRs (recreated by controller), Running pods |
| DataPlane | NodeSets, Services, NetConfig, IP allocations | Deployment history (ephemeral) |

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
