# Backup and Restore with Webhook-Based Labeling

## Overview

This document describes a webhook-based approach to backup and restore for OpenStack on OpenShift. The design eliminates hardcoded resource lists by using CRD annotations to declare backup/restore behavior, and mutating webhooks to automatically label resources.

## Goals

1. **Full Namespace Backup**: Backup everything in the namespace to ensure complete snapshot
2. **Selective Restore**: Use webhook-added labels to restore only necessary resources
3. **Dynamic Resource Discovery**: No hardcoded lists - CRD annotations declare what needs restore
4. **Declarative Restore Order**: Restore order defined in CRD annotations, not in code
5. **Kubernetes-Native**: Leverage OADP label selectors for filtering
6. **No Controller Required (Initially)**: Can be used manually with OADP Restore CRs
7. **Optional Automation**: Golang controller for full automation (future enhancement)

## Key Concepts

### CRD Annotations

CRD definitions declare restore behavior using annotations (all prefixed with `openstack.org/backup-`):

```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: openstackcontrolplanes.core.openstack.org
  annotations:
    openstack.org/backup-restore: "true"
    openstack.org/backup-category: "controlplane"
    openstack.org/backup-restore-order: "6"
```

**Annotations:**
- `openstack.org/backup-restore`: Whether instances of this CRD should be restored (`"true"` or `"false"`)
- `openstack.org/backup-category`: Category for selective backup/restore
  - `"controlplane"`: Control plane resources
  - `"dataplane"`: Data plane resources
  - `"infrastructure"`: Infrastructure resources (RabbitMQ, MariaDB configs)
  - `"all"`: General resources needed by all deployments
- `openstack.org/backup-restore-order`: Numeric order for restore sequence (e.g., `"1"`, `"2"`, `"3"`)

### Mutating Webhooks

Reuse the existing webhook pattern where openstack-operator already calls `ValidateCreate` and `ValidateUpdate` functions from service operators.

**Architecture:**

1. **OpenStack Operator Webhooks** (existing pattern):
   - Existing webhooks in `api/core/v1beta1/openstackcontrolplane_webhook.go`
   - Already calls `ValidateCreate` from service operators (e.g., `keystone.ValidateCreate`)
   - Add backup labeling logic to these existing validation functions
   - Works on both Create and Update (handles existing environments)

2. **Infrastructure Operator Webhook** (independent):
   - Runs its own separate mutating webhook
   - Handles infrastructure resources (NetConfig, IPSet, RabbitMQUser, etc.)
   - Independent from openstack-operator webhook

**Example: Adding Backup Labeling to Existing Webhooks**

In `keystone-operator/api/v1beta1/keystoneapi_webhook.go`:

```go
func (r *KeystoneAPI) ValidateCreate() error {
    // Existing validation logic...

    // NEW: Add restore labels to user-provided resources
    if err := r.labelUserProvidedResourcesForRestore(); err != nil {
        return err
    }

    return nil
}

func (r *KeystoneAPI) labelUserProvidedResourcesForRestore() error {
    ctx := context.Background()

    // Label Secret if user-provided (no ownerReferences)
    if r.Spec.Secret != "" {
        if err := labelResourceForRestoreIfUserProvided(ctx, r.Namespace, "Secret", r.Spec.Secret); err != nil {
            return err
        }
    }

    // Label CustomConfigSecret if user-provided
    if r.Spec.HttpdCustomization != nil && r.Spec.HttpdCustomization.CustomConfigSecret != "" {
        if err := labelResourceForRestoreIfUserProvided(ctx, r.Namespace, "Secret",
            r.Spec.HttpdCustomization.CustomConfigSecret); err != nil {
            return err
        }
    }

    // Label referenced ConfigMaps in ExtraMounts if user-provided
    for _, mount := range r.Spec.ExtraMounts {
        for _, vol := range mount.Propagation {
            if vol.ConfigMap != nil {
                if err := labelResourceForRestoreIfUserProvided(ctx, r.Namespace, "ConfigMap",
                    vol.ConfigMap.Name); err != nil {
                    return err
                }
            }
        }
    }

    return nil
}
```

**Generic Helper Function (in lib-common):**

```go
// labelResourceForRestoreIfUserProvided adds restore labels to resource if it has no ownerReferences
func labelResourceForRestoreIfUserProvided(ctx context.Context, namespace, kind, name string) error {
    // Get the resource
    var obj client.Object
    switch kind {
    case "Secret":
        obj = &corev1.Secret{}
    case "ConfigMap":
        obj = &corev1.ConfigMap{}
    default:
        return fmt.Errorf("unsupported resource kind: %s", kind)
    }

    key := client.ObjectKey{Namespace: namespace, Name: name}
    if err := k8sClient.Get(ctx, key, obj); err != nil {
        if errors.IsNotFound(err) {
            // Resource doesn't exist yet, skip labeling
            return nil
        }
        return err
    }

    // Check if resource has ownerReferences
    if len(obj.GetOwnerReferences()) > 0 {
        // Resource is managed by controller, skip labeling
        return nil
    }

    // Add restore labels (user-provided resource)
    labels := obj.GetLabels()
    if labels == nil {
        labels = make(map[string]string)
    }

    // Only add if not already labeled
    if labels["openstack.org/backup-restore"] == "true" {
        return nil
    }

    labels["openstack.org/backup-restore"] = "true"
    labels["openstack.org/backup-category"] = "all"
    labels["openstack.org/backup-restore-order"] = "1"  // Secrets/ConfigMaps always order 1
    obj.SetLabels(labels)

    // Update the resource
    return k8sClient.Update(ctx, obj)
}
```

**Key Points:**

1. **Reuse Existing Pattern**: No new webhook infrastructure needed
2. **Service Operator Knowledge**: Each service operator knows what resources it references
3. **Generic Helper**: Common logic to check ownerReferences and add labels
4. **Works on Create and Update**: Handles both new and existing deployments
5. **User-Provided Detection**: Only labels resources without ownerReferences

### OADP Integration

#### Full Namespace Backup

One OADP Backup CR captures **everything** in the namespace (complete snapshot):

```yaml
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: openstack-backup-20260303-120000
  namespace: openshift-adp
spec:
  includedNamespaces:
  - openstack
  # NO labelSelector - backup everything
  snapshotVolumes: true  # Backup PVCs with CSI snapshots
  defaultVolumesToFsBackup: false
  storageLocation: velero-1
  ttl: 720h
```

**Optional: Exclude large PVC data** (if storage is limited):

```yaml
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: openstack-backup-20260303-120000
  namespace: openshift-adp
spec:
  includedNamespaces:
  - openstack
  # Backup all CRs, Secrets, ConfigMaps, but exclude PVC data
  snapshotVolumes: false  # Skip PVC snapshots to reduce backup size
  defaultVolumesToFsBackup: false
  storageLocation: velero-1
  ttl: 720h
```

#### Selective Restore (By Order)

Multiple OADP Restore CRs, one per restore order, using labels added by webhooks:

```yaml
# Restore Order 1: Secrets, ConfigMaps, NADs
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-restore-order-1
  namespace: openshift-adp
spec:
  backupName: openstack-backup-20260303-120000
  labelSelector:
    matchLabels:
      openstack.org/backup-restore: "true"
      openstack.org/backup-restore-order: "1"
  restorePVs: false  # Don't restore PVCs in this order
---
# Restore Order 2: TLS Issuers
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-restore-order-2
  namespace: openshift-adp
spec:
  backupName: openstack-backup-20260303-120000
  labelSelector:
    matchLabels:
      openstack.org/backup-restore: "true"
      openstack.org/backup-restore-order: "2"
  restorePVs: false
---
# And so on for each restore order...
```

**Key Point**: Webhooks add `openstack.org/backup-restore: "true"` labels to resources that need restore. OADP restore uses these labels for selective restore, even though the backup contains everything.

## Restore Order

The restore sequence is critical for maintaining dependencies between resources.

| Order | Resources | Notes |
|-------|-----------|-------|
| 1 | NetworkAttachmentDefinitions<br>Secrets (CA + user-provided)<br>ConfigMaps (user-provided) | Foundation resources |
| 2 | TLS Issuers | Requires CA secrets to exist |
| 3 | MariaDBDatabase | Requires database password secrets |
| 4 | MariaDBAccount | Requires MariaDBDatabase CRs |
| 5 | OpenStackVersion<br>Topology<br>BGPConfiguration<br>DNSData<br>InstanceHa | Related CRs without critical dependencies |
| 6 | OpenStackControlPlane | **Add staged deployment annotation**<br>Creates infrastructure only |
| 7 | RabbitMQUser (user-created)<br>RabbitMQVhost (user-created) | User-created RabbitMQ resources |
| 8 | PVCs | Restored from CSI snapshots via OADP |
| 9 | GaleraBackup | Database backup configuration |
| 10 | *Database Restore* | **Manual/Controller**: Create GaleraRestore CRs<br>Execute restore from latest backup |
| 11 | *RabbitMQ Credentials* | **Manual/Controller**: Create RabbitMQUser CRs<br>Restore original credentials |
| 12 | *Resume Deployment* | **Manual/Controller**: Remove staged annotation<br>Resume full deployment |

**Notes:**
- Orders 1-9: Pure OADP restore (no special handling)
- Orders 10-12: Require additional logic (manual steps or controller automation)

## CRD Annotation Mapping

### Core Operator CRDs

| CRD | Restore | Category | Order | Notes |
|-----|---------|----------|-------|-------|
| OpenStackControlPlane | true | controlplane | 6 | Main control plane CR |
| OpenStackVersion | true | controlplane | 5 | Version tracking |

### Infrastructure Operator CRDs

| CRD | Backup | Category | Order | Notes |
|-----|--------|----------|-------|-------|
| NetConfig | true | infrastructure | 5 | Network configuration |
| IPSet | true | infrastructure | 5 | IP address sets |
| Reservation | true | infrastructure | 5 | IP reservations |
| BGPConfiguration | true | infrastructure | 5 | BGP config |
| DNSData | true | infrastructure | 5 | DNS records |
| Topology | true | infrastructure | 5 | Network topology |
| RabbitMQUser* | true | infrastructure | 7 | User-created only |
| RabbitMQVhost* | true | infrastructure | 7 | User-created only |
| InstanceHa | true | infrastructure | 5 | Instance HA config |

*Only for user-created resources (no ownerReferences)

### MariaDB Operator CRDs

| CRD | Backup | Category | Order | Notes |
|-----|--------|----------|-------|-------|
| MariaDBDatabase | true | controlplane | 3 | Database definitions |
| MariaDBAccount | true | controlplane | 4 | Database accounts |
| GaleraBackup | true | controlplane | 9 | Backup configuration |

### Data Plane CRDs

| CRD | Backup | Category | Order | Notes |
|-----|--------|----------|-------|-------|
| OpenStackDataPlaneNodeSet | true | dataplane | 5 | Node set definitions |
| OpenStackDataPlaneService* | true | dataplane | 5 | Custom services only |

*Only for user-created services (no ownerReferences)

### Kubernetes Core Resources

| Resource | Backup | Category | Order | Notes |
|----------|--------|----------|-------|-------|
| Secret* | true | all | 1 | User-provided only (no ownerReferences) |
| ConfigMap* | true | all | 1 | User-provided only (no ownerReferences) |
| NetworkAttachmentDefinition | true | all | 1 | Network attachments |
| Issuer (cert-manager) | true | all | 2 | TLS certificate issuers |
| PersistentVolumeClaim** | true | all | 8 | Service storage volumes |

*Only resources without ownerReferences
**Only PVCs with label `openstack.org/backup: "true"`

## Backup Categories

Categories enable selective backup/restore scenarios:

### Control Plane Only
```yaml
labelSelector:
  matchLabels:
    openstack.org/backup-restore: "true"
    openstack.org/backup-category: "controlplane"
```
Use case: Control plane disaster recovery (restore only control plane resources)

### Data Plane Only
```yaml
labelSelector:
  matchLabels:
    openstack.org/backup-restore: "true"
    openstack.org/backup-category: "dataplane"
```
Use case: Data plane node replacement (restore only data plane resources)

### Infrastructure Only
```yaml
labelSelector:
  matchLabels:
    openstack.org/backup-restore: "true"
    openstack.org/backup-category: "infrastructure"
```
Use case: Network/messaging configuration recovery

### All Labeled Resources
```yaml
labelSelector:
  matchLabels:
    openstack.org/backup-restore: "true"
```
Use case: Full restore of all labeled resources (default)

## Implementation Phases

### Phase 1: Webhook & CRD Annotations (No Controller)

**Goal**: Automatic labeling of resources for backup

**Changes:**
1. Add CRD annotations to all operator CRDs
2. Implement mutating webhook in openstack-operator
3. Deploy webhook configuration
4. Test that resources get labeled on creation

**Backward Compatibility**: Existing Ansible backup/restore continues to work

**Testing:**
```bash
# Create a test secret
oc create secret generic test-secret --from-literal=foo=bar -n openstack

# Verify label was added
oc get secret test-secret -n openstack -o jsonpath='{.metadata.labels}'
# Should show: openstack.org/backup: "true", openstack.org/restore-order: "1"
```

### Phase 2: OADP Backup (No Controller)

**Goal**: Single OADP backup instead of manual `oc get` + jq

**Changes:**
1. Create single OADP Backup CR with label selector
2. Verify all labeled resources are backed up
3. Test CSI snapshot integration

**Backward Compatibility**: Ansible restore still works (reads from OADP backup)

**Example:**
```bash
# Create backup
oc apply -f - <<EOF
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: openstack-backup-$(date +%Y%m%d-%H%M%S)
  namespace: openshift-adp
spec:
  includedNamespaces:
  - openstack
  labelSelector:
    matchLabels:
      openstack.org/backup: "true"
  snapshotVolumes: true
  defaultVolumesToFsBackup: false
  storageLocation: velero-1
  ttl: 720h
EOF

# Monitor backup
oc get backup -n openshift-adp -w
```

### Phase 3: OADP Restore (Manual, No Controller)

**Goal**: Use OADP Restore CRs with label selectors instead of Ansible

**Changes:**
1. Create OADP Restore CRs for each restore order
2. Manually create each restore in sequence
3. Wait for completion between orders
4. Handle special cases manually (staged deployment, database restore)

**No Controller Required**: User creates OADP Restore CRs manually

**Example Manual Restore:**
```bash
# Order 1: Secrets, ConfigMaps, NADs
cat <<EOF | oc apply -f -
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-restore-order-1
  namespace: openshift-adp
spec:
  backupName: openstack-backup-20260303-120000
  labelSelector:
    matchLabels:
      openstack.org/backup: "true"
      openstack.org/restore-order: "1"
  restorePVs: false
EOF

# Wait for completion
oc wait --for=jsonpath='{.status.phase}'=Completed \
  restore/openstack-restore-order-1 -n openshift-adp --timeout=10m

# Order 2: TLS Issuers
cat <<EOF | oc apply -f -
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-restore-order-2
  namespace: openshift-adp
spec:
  backupName: openstack-backup-20260303-120000
  labelSelector:
    matchLabels:
      openstack.org/backup: "true"
      openstack.org/restore-order: "2"
  restorePVs: false
EOF

# Wait for completion
oc wait --for=jsonpath='{.status.phase}'=Completed \
  restore/openstack-restore-order-2 -n openshift-adp --timeout=10m

# Continue for each order...

# Order 6: OpenStackControlPlane
cat <<EOF | oc apply -f -
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-restore-order-6
  namespace: openshift-adp
spec:
  backupName: openstack-backup-20260303-120000
  labelSelector:
    matchLabels:
      openstack.org/backup: "true"
      openstack.org/restore-order: "6"
  restorePVs: false
EOF

oc wait --for=jsonpath='{.status.phase}'=Completed \
  restore/openstack-restore-order-6 -n openshift-adp --timeout=10m

# MANUAL: Add staged deployment annotation
oc annotate openstackcontrolplane openstack-galera-network-isolation \
  -n openstack core.openstack.org/deployment-stage=infrastructure-only

# MANUAL: Wait for infrastructure ready
oc wait --for=condition=OpenStackControlPlaneInfrastructureReady \
  openstackcontrolplane/openstack-galera-network-isolation \
  -n openstack --timeout=20m

# Orders 7-9: Continue with OADP restores...

# Order 10: Database restore (MANUAL)
# Create GaleraRestore CRs
# Execute database restore
# Clean up GaleraRestore CRs

# Order 11: RabbitMQ credentials (MANUAL)
# Create RabbitMQUser CRs with restored secrets

# Order 12: Resume deployment (MANUAL)
# Remove staged deployment annotation
oc annotate openstackcontrolplane openstack-galera-network-isolation \
  -n openstack core.openstack.org/deployment-stage-
```

### Phase 4: Golang Controller (Full Automation)

**Goal**: Full automation with OpenStackBackupRestore CRD and controller

**New CRD:**
```yaml
apiVersion: core.openstack.org/v1beta1
kind: OpenStackBackupRestore
metadata:
  name: restore-20260303
  namespace: openstack
spec:
  backupName: openstack-backup-20260303-120000
  automated: true  # false for manual mode (prompts/pauses)
  automatedDatabaseRestore: true
  automatedRabbitMQRestore: true
```

**Controller Logic:**
1. Watch OpenStackBackupRestore CRs
2. Create OADP Restore CRs in sequence by restore order
3. Wait for each OADP Restore completion
4. Handle special cases:
   - Order 6: Add staged deployment annotation, wait for infrastructure ready
   - Order 10: Create GaleraRestore CRs, execute database restore, clean up
   - Order 11: Create RabbitMQUser CRs with restored credentials
   - Order 12: Remove staged deployment annotation
5. Update OpenStackBackupRestore status with progress

**Status Fields:**
```yaml
status:
  phase: InProgress  # Pending, InProgress, Completed, Failed
  currentRestoreOrder: 3
  conditions:
  - type: Order1Complete
    status: "True"
  - type: Order2Complete
    status: "True"
  - type: Order3InProgress
    status: "True"
```

## Benefits

### Compared to Current Ansible Approach

| Aspect | Current (Ansible) | Proposed (Webhook + OADP) |
|--------|------------------|---------------------------|
| Resource Discovery | Hardcoded `jq` filters | Dynamic via CRD annotations |
| Backup Mechanism | `oc get` + jq + OADP (for PVCs) | Single OADP Backup CR |
| Restore Order | Hardcoded in playbook | Declared in CRD annotations |
| Adding New CRD | Update Ansible playbook | Add CRD annotation only |
| Manual Restore | Run full playbook | Create OADP Restore CRs |
| Automation | Ansible playbook | Optional Golang controller |
| Kubernetes-Native | Partial (mixes oc + OADP) | Full (pure OADP) |

### Key Improvements

1. **Self-Describing System**: CRD annotations declare backup/restore behavior
2. **No Code Changes for New CRDs**: Just add annotations to CRD definition
3. **Flexible Automation**: Can be used manually or with controller
4. **Category-Based Restore**: Selective restore by category
5. **Better Testing**: Can test individual restore orders
6. **Kubernetes-Native**: Leverages OADP fully, standard Kubernetes patterns

## Open Questions

1. **Restore Order Conflicts**: What if two CRDs have the same restore order?
   - Run in parallel?
   - Deterministic sub-ordering (e.g., alphabetically by CRD name)?

2. **Webhook Performance**: Mutating webhook adds latency to CREATE operations
   - Is this acceptable?
   - Can we optimize for specific resource types?

3. **Label vs Annotation for Restore Order**: Should `restore-order` be a label or annotation?
   - Label: Can be used in OADP selector
   - Annotation: Cleaner, but need controller to copy to label

4. **Database Restore Automation**: Should controller exec into pods or require manual intervention?
   - Automated mode: Controller execs into pods
   - Manual mode: User runs commands

5. **PVC Labeling**: How do PVCs get the backup label?
   - Service operators add label when creating PVCs?
   - Separate webhook for PVCs?
   - Manual labeling required?

6. **Webhook Scope**: Should webhook run in openstack-operator or separate deployment?
   - openstack-operator: Simpler deployment
   - Separate: Cleaner separation of concerns

## Next Steps

1. Sketch detailed implementation for Phase 1 (webhook)
2. Define exact CRD annotation schema
3. Implement webhook in openstack-operator
4. Test with sample resources
5. Document migration path from current Ansible approach
