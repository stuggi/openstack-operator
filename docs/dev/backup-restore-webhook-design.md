# Backup and Restore with Webhook-Based Labeling

## Overview

This document describes a webhook-based approach to backup and restore for OpenStack on OpenShift. The design eliminates hardcoded resource lists by using CRD annotations to declare backup/restore behavior, and mutating webhooks to automatically label resources.

## Goals

1. **Full Backup, Selective Restore**:
   - Backup: All user resources (Secrets, ConfigMaps, CRs) - complete snapshot
   - Restore: Only webhook-labeled resources - automatic filtering
2. **Dynamic Resource Discovery**: No hardcoded lists - CRD annotations declare what needs restore
3. **Declarative Restore Order**: Restore order defined in CRD annotations, not in code
4. **Operator-Managed Exclusion**: Operators recreate their own resources (not restored from backup)
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

    // Set category if not already set (allows user override)
    if labels["openstack.org/backup-category"] == "" {
        labels["openstack.org/backup-category"] = "all"
    }

    // Set default restore order if not already set (allows user override)
    if labels["openstack.org/backup-restore-order"] == "" {
        // Default restore order by resource type
        // Users can pre-label resources to customize order
        switch kind {
        case "Secret", "ConfigMap":
            labels["openstack.org/backup-restore-order"] = "1"
        default:
            labels["openstack.org/backup-restore-order"] = "1"
        }
    }

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

One OADP Backup CR captures **all user resources** in the namespace (complete snapshot):

```yaml
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: openstack-backup-20260303-120000
  namespace: openshift-adp
spec:
  includedNamespaces:
  - openstack
  # NO labelSelector - backup all CRs, Secrets, ConfigMaps
  # Exclude operator-managed resources that will be recreated
  excludedResources:
  - pods
  - replicasets
  - jobs
  - events
  - statefulsets
  snapshotVolumes: true  # Enable CSI snapshots for PVCs
  defaultVolumesToFsBackup: false
  storageLocation: velero-1
  ttl: 720h
```

**Backup Strategy:**
- ✅ **All Secrets** in namespace (user-provided AND operator-managed)
- ✅ **All ConfigMaps** in namespace (user-provided AND operator-managed)
- ✅ **All CRs** (OpenStackControlPlane, MariaDBDatabase, DataPlaneNodeSet, etc.)
- ✅ **All NetworkAttachmentDefinitions, Issuers, etc.**
- ✅ **PVCs with label** `openstack.org/backup: "true"` (CSI snapshots)
- ❌ **Excluded**: Pods, ReplicaSets, Jobs, Events, StatefulSets (operator-managed, will be recreated)

**Why backup ALL Secrets/ConfigMaps?**
- Ensures complete snapshot (nothing missed)
- Simple backup logic (no complex filtering)
- Restore is selective via webhook labels (see below)

**Note on PVC Backup:**
- **PVCs are selectively backed up** using the `openstack.org/backup: "true"` label
- OADP's CSI snapshot logic respects the backup label when creating volume snapshots
- Individual PVCs can be excluded using annotation: `backup.velero.io/backup-volumes: "false"`

See [PVC Labeling Strategy](#pvc-labeling-strategy) for details on how PVCs are labeled for backup.

**Alternative: Split Backup into CRs and PVCs** (if you want explicit separation):

```yaml
# Backup 1: All CRs and core resources (no PVCs)
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: openstack-crs-20260303-120000
  namespace: openshift-adp
spec:
  includedNamespaces:
  - openstack
  excludedResources:
  - persistentvolumeclaims
  - persistentvolumes
  snapshotVolumes: false
  storageLocation: velero-1
  ttl: 720h
---
# Backup 2: Only labeled PVCs with CSI snapshots
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: openstack-pvcs-20260303-120000
  namespace: openshift-adp
spec:
  includedNamespaces:
  - openstack
  includedResources:
  - persistentvolumeclaims
  labelSelector:
    matchLabels:
      openstack.org/backup: "true"
  snapshotVolumes: true
  defaultVolumesToFsBackup: false
  storageLocation: velero-1
  ttl: 720h
```

#### Selective Restore (By Order)

Multiple OADP Restore CRs, one per restore order, using labels added by webhooks.

**Restore Strategy:**
- ✅ **Only resources with** `openstack.org/backup-restore: "true"` **label**
- ✅ **Webhooks add labels to user-provided resources** (no ownerReferences)
- ❌ **Operator-managed resources excluded** (no labels, will be recreated by operators)

This means:
- User-provided Secrets → Labeled by webhook → Restored ✅
- Operator-created Secrets → Not labeled → Not restored, recreated by operator ✅
- User-provided ConfigMaps → Labeled by webhook → Restored ✅
- Operator-created ConfigMaps → Not labeled → Not restored, recreated by operator ✅
- All CRs with annotations → Labeled by webhook → Restored ✅

**Example Restore CRs:**

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

**Key Points:**
- **Backup**: All user resources in namespace (all Secrets, ConfigMaps, CRs) - complete snapshot
- **Restore**: Only resources with `openstack.org/backup-restore: "true"` label - selective filtering
- **Webhooks**: Add restore labels to user-provided resources (no ownerReferences)
- **Operators**: Recreate their own Secrets/ConfigMaps on reconciliation (not restored from backup)

## Customizing Restore Order for Core Resources

### Manual Labeling (Available Immediately)

Users can pre-label Secrets, ConfigMaps, PVCs, and cert-manager resources to customize their restore order. The webhook respects existing labels and won't overwrite them.

**Example: CA secret restored in order 1, service secret in order 5**

```bash
# CA certificate secret (restored early)
oc label secret openstack-ca-cert \
  openstack.org/backup-restore=true \
  openstack.org/backup-category=all \
  openstack.org/backup-restore-order=1 \
  -n openstack

# Service-specific secret (restored after infrastructure)
oc label secret nova-cell1-config \
  openstack.org/backup-restore=true \
  openstack.org/backup-category=controlplane \
  openstack.org/backup-restore-order=5 \
  -n openstack
```

**How it works:**
1. User creates and labels resource with desired restore order
2. Webhook checks if resource already has `openstack.org/backup-restore: "true"`
3. If yes, webhook skips labeling (preserves user's custom order)
4. If no, webhook applies default labels

### Configuration via CRD (Future - Phase 4)

When the Golang controller is implemented, restore order defaults can be configured via CRD:

```yaml
apiVersion: core.openstack.org/v1beta1
kind: OpenStackBackupConfig
metadata:
  name: backup-config
  namespace: openstack
spec:
  # Default restore orders for core Kubernetes resources
  restoreDefaults:
    secrets:
      category: "all"
      order: "1"
    configmaps:
      category: "all"
      order: "1"
    persistentvolumeclaims:
      category: "all"
      order: "8"
    issuers:  # cert-manager Issuer
      category: "all"
      order: "2"
    networkattachmentdefinitions:
      category: "all"
      order: "1"

  # Custom overrides for specific resources
  customOrders:
  - resource:
      kind: Secret
      name: openstack-ca-cert
    category: "all"
    order: "1"
  - resource:
      kind: Secret
      name: nova-cell1-config
    category: "controlplane"
    order: "5"
```

**Benefits of CRD-based configuration:**
- Centralized configuration for all restore order defaults
- Easy to customize per deployment
- No need to manually label every resource
- Can be backed up and restored along with other CRs

**Implementation approach:**
1. Webhook reads OpenStackBackupConfig CR to get default orders
2. Applies configured defaults instead of hardcoded values
3. Still respects existing labels (manual overrides take precedence)
4. Fallback to hardcoded defaults if no config CR exists

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

| CRD | Restore | Category | Order | Notes |
|-----|---------|----------|-------|-------|
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

| CRD | Restore | Category | Order | Notes |
|-----|---------|----------|-------|-------|
| MariaDBDatabase | true | controlplane | 3 | Database definitions |
| MariaDBAccount | true | controlplane | 4 | Database accounts |
| GaleraBackup | true | controlplane | 9 | Backup configuration |

### Data Plane CRDs

| CRD | Restore | Category | Order | Notes |
|-----|---------|----------|-------|-------|
| OpenStackDataPlaneNodeSet | true | dataplane | 5 | Node set definitions |
| OpenStackDataPlaneService* | true | dataplane | 5 | Custom services only |

*Only for user-created services (no ownerReferences)

### Kubernetes Core Resources

| Resource | Restore | Category | Order | Notes |
|----------|---------|----------|-------|-------|
| Secret* | true | all | 1 | User-provided only (no ownerReferences) |
| ConfigMap* | true | all | 1 | User-provided only (no ownerReferences) |
| NetworkAttachmentDefinition | true | all | 1 | Network attachments |
| Issuer (cert-manager) | true | all | 2 | TLS certificate issuers |
| PersistentVolumeClaim** | true | all | 8 | Service storage volumes |

*Only resources without ownerReferences
**PVCs use dual-label approach (see PVC Labeling Strategy below)

### PVC Labeling Strategy

PVCs use a **dual-label approach** to separate backup inclusion from restore inclusion:

**Label Purposes:**
- **`openstack.org/backup: "true"`** - Include PVC in backup snapshot (required for CSI snapshot)
- **`openstack.org/backup-restore: "true"`** - Include PVC in restore operation (optional)

**Common Scenarios:**

1. **Production data (backup AND restore)** - Most PVCs:
   ```yaml
   metadata:
     labels:
       openstack.org/backup: "true"              # Snapshot during backup
       openstack.org/backup-restore: "true"      # Restore during restore
       openstack.org/backup-restore-order: "8"
     annotations:
       service: glance
   ```
   Examples: Glance images, Cinder volumes, Manila shares, Galera backup dumps

2. **Backup-only data (backup but NOT restore)** - Logs, temporary data:
   ```yaml
   metadata:
     labels:
       openstack.org/backup: "true"              # Snapshot during backup
       # NO backup-restore label → excluded from restore
     annotations:
       service: logging
   ```
   Examples: Log aggregation PVCs, audit logs, test data

   Use case: Backup for audit/compliance, but don't restore old logs to new environment

3. **Skip backup entirely** - Caches, ephemeral data:
   ```yaml
   metadata:
     labels:
       # NO backup label
     annotations:
       backup.velero.io/backup-volumes: "false"  # Explicitly skip even if labeled
       service: memcached
   ```
   Examples: Cache storage, temporary workspaces

**How Labels Are Used:**

**Backup CR** - Uses `openstack.org/backup` label selector:
```yaml
apiVersion: velero.io/v1
kind: Backup
spec:
  includedNamespaces:
  - openstack
  labelSelector:
    matchLabels:
      openstack.org/backup: "true"  # Only PVCs with backup label
  snapshotVolumes: true
```

**Restore CR** - Uses `openstack.org/backup-restore` label selector:
```yaml
apiVersion: velero.io/v1
kind: Restore
spec:
  labelSelector:
    matchLabels:
      openstack.org/backup-restore: "true"
      openstack.org/backup-restore-order: "8"
  restorePVs: true
```

**Excluding Individual PVCs:**

If a PVC has `openstack.org/backup: "true"` but should be skipped, add Velero's annotation:
```yaml
metadata:
  labels:
    openstack.org/backup: "true"
  annotations:
    backup.velero.io/backup-volumes: "false"  # Override: skip this PVC
```

**Who Sets These Labels:**

- Service operators add `openstack.org/backup: "true"` when creating PVCs that need backup
- Webhook (or operator) adds `openstack.org/backup-restore: "true"` + order to PVCs that should restore
- Manual override via `backup.velero.io/backup-volumes: "false"` annotation when needed

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

**Goal**: Automatic labeling of resources for restore

**Changes:**
1. Add CRD annotations to all operator CRDs
2. Implement mutating webhook in openstack-operator (reuse ValidateCreate pattern)
3. Implement generic helper function in lib-common (respects existing labels)
4. Deploy webhook configuration
5. Test that resources get labeled on creation

**Backward Compatibility**: Existing Ansible backup/restore continues to work

**Features:**
- Automatic labeling of user-provided resources (no ownerReferences)
- Respects existing labels (allows manual customization)
- Default restore order based on resource type
- Works on both Create and Update (handles existing environments)

**Testing:**
```bash
# Test 1: Automatic labeling with defaults
oc create secret generic test-secret --from-literal=foo=bar -n openstack

# Verify default labels were added
oc get secret test-secret -n openstack -o jsonpath='{.metadata.labels}'
# Should show: openstack.org/backup-restore: "true", openstack.org/backup-restore-order: "1"

# Test 2: Manual override (pre-label before webhook runs)
oc create secret generic custom-secret \
  --from-literal=foo=bar \
  -n openstack \
  --dry-run=client -o yaml | \
  oc label -f - --local \
    openstack.org/backup-restore=true \
    openstack.org/backup-category=controlplane \
    openstack.org/backup-restore-order=5 \
    --dry-run=client -o yaml | \
  oc apply -f -

# Verify custom labels were preserved
oc get secret custom-secret -n openstack -o jsonpath='{.metadata.labels}'
# Should show: openstack.org/backup-restore: "true", openstack.org/backup-restore-order: "5"
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
  # NO labelSelector - backup everything
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
      openstack.org/backup-restore: "true"
      openstack.org/backup-restore-order: "1"
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
      openstack.org/backup-restore: "true"
      openstack.org/backup-restore-order: "2"
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
      openstack.org/backup-restore: "true"
      openstack.org/backup-restore-order: "6"
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

**Goal**: Full automation with controller and CRDs

**New CRDs:**

**OpenStackBackupConfig** - Configure restore order defaults
```yaml
apiVersion: core.openstack.org/v1beta1
kind: OpenStackBackupConfig
metadata:
  name: backup-config
  namespace: openstack
spec:
  restoreDefaults:
    secrets:
      category: "all"
      order: "1"
    configmaps:
      category: "all"
      order: "1"
    persistentvolumeclaims:
      category: "all"
      order: "8"
    issuers:
      category: "all"
      order: "2"
```

**OpenStackBackupRestore** - Execute backup/restore operations
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

3. **Database Restore Automation**: Should controller exec into pods or require manual intervention?
   - Automated mode: Controller execs into pods
   - Manual mode: User runs commands

4. **Webhook Scope**: Should webhook run in openstack-operator or separate deployment?
   - openstack-operator: Simpler deployment (reuse existing webhooks)
   - Separate: Cleaner separation of concerns

5. **OpenStackBackupConfig Scope**: Should the config CR be namespace-scoped or cluster-scoped?
   - Namespace-scoped: Different configs per OpenStack deployment
   - Cluster-scoped: Single config for all OpenStack deployments

6. **Default vs Custom Order Precedence**: How should the order precedence work?
   - Current proposal: Manual labels > OpenStackBackupConfig > Hardcoded defaults
   - Alternative: OpenStackBackupConfig > Manual labels > Hardcoded defaults

7. **Webhook Update Logic**: Should webhook update resources on every reconcile?
   - Only on Create: Simpler, but doesn't handle label removal
   - On Create and Update: Handles label changes, but more update operations
   - Current proposal: On Create and Update (ValidateCreate + ValidateUpdate)

## Next Steps

1. Sketch detailed implementation for Phase 1 (webhook)
2. Define exact CRD annotation schema
3. Implement webhook in openstack-operator
4. Test with sample resources
5. Document migration path from current Ansible approach
