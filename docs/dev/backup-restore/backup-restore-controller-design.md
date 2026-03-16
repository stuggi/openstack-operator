# Backup and Restore Design

## Overview

This document describes the design for backup and restore of OpenStack on OpenShift. The approach eliminates hardcoded resource lists by using CRD labels to declare backup/restore behavior, and a controller (OpenStackBackupConfig) to automatically label resource instances.

> **Note:** An earlier version of this design proposed using mutating admission webhooks to label resources at creation time. After evaluation, the controller-based approach was chosen instead. See [Controller vs Webhook Approach](#controller-vs-webhook-approach) for the rationale.

## Goals

1. **Full Backup, Selective Restore** (for CRs, Secrets, ConfigMaps):
   - Backup: All user resources (Secrets, ConfigMaps, CRs) - complete snapshot
   - Restore: Only labeled resources - automatic filtering via label selectors
   - **Exception - PVCs**: Selective backup AND selective restore (only labeled PVCs backed up/restored due to storage/performance concerns)
2. **Dynamic Resource Discovery**: No hardcoded lists - CRD labels declare what needs restore
3. **Declarative Restore Order**: Restore order defined in CRD labels, not in code
4. **Operator-Managed Exclusion**: Operators recreate their own resources (not restored from backup)
5. **Kubernetes-Native**: Leverage label selectors for filtering
6. **Controller-Based Labeling**: OpenStackBackupConfig controller labels CR instances based on CRD labels

## Key Concepts

### CRD Labels

CRD definitions use **restore labels** to control which instances should be restored (all prefixed with `backup.openstack.org/`):

```yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: openstackcontrolplanes.core.openstack.org
  labels:
    # Restore labels (explicit opt-in - must be present to restore)
    backup.openstack.org/restore: "true"
    backup.openstack.org/category: "controlplane"
    backup.openstack.org/restore-order: "30"
```

**Labels:**

- `backup.openstack.org/restore`: Whether instances of this CRD should be restored
  - **Default if missing**: `"false"` (explicit opt-in required - only restore what's needed)
  - `"true"`: Include in restore (controller adds restore labels to instances)
  - `"false"`: Exclude from restore (controller does NOT add restore labels)

- `backup.openstack.org/category`: Category for selective backup/restore
  - `"controlplane"`: Control plane resources (OpenStackControlPlane, MariaDB, services, user-provided Secrets/ConfigMaps, PVCs)
  - `"dataplane"`: Data plane resources (NetConfig, Topology, IPSet, Reservation, DataPlaneNodeSet)

- `backup.openstack.org/restore-order`: Numeric order for restore sequence
  - Uses gaps of 10 (e.g., `"00"`, `"10"`, `"20"`, `"30"`) to allow easy insertion of new resources
  - Common orders:
    - 00 (storage foundation - PVCs)
    - 10 (foundation - NADs, Secrets, ConfigMaps)
    - 20 (TLS & infrastructure - Issuers, MariaDB, NetConfig)
    - 30 (CtlPlane + networking)
    - 40 (backup config & user resources)
    - 50 (manual steps - database/RabbitMQ restore, resume deployment)
    - 60 (DataPlane)

**Restore Strategy:**

- ✅ **Full backup**: All CRs backed up by OADP (no label selector on Backup CR)
- ✅ **Selective restore**: Only CRs with `backup.openstack.org/restore: "true"` label on CRD are restored
- ✅ **Clear intent**: CRD labels declare what needs restoration
- ✅ **Dynamic discovery**: Query CRDs with `oc get crd -l backup.openstack.org/restore=true`
- ✅ **Controller-friendly**: Controllers can watch/list CRDs by label selector

**Examples:**

```yaml
# OpenStackDataPlaneDeployment - backup but DON'T restore
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: openstackdataplanedeployments.dataplane.openstack.org
  labels:
    # NO backup-restore label        # Don't restore (default: false)
    # All instances still backed up (full namespace backup)

# OpenStackControlPlane - backup AND restore
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: openstackcontrolplanes.core.openstack.org
  labels:
    backup.openstack.org/restore: "true"              # Include in restore
    backup.openstack.org/category: "controlplane"
    backup.openstack.org/restore-order: "30"
```

### Controller-Based Labeling (OpenStackBackupConfig)

The OpenStackBackupConfig controller handles resource labeling centrally from the openstack-operator. It discovers CRDs with backup-restore labels and patches matching labels onto CR instances.

**How it works:**

1. **CRD Label Cache**: Built once at operator startup, maps CRD names to their backup labels
2. **CR Instance Labeling**: Controller lists CR instances for each labeled CRD and patches backup labels onto them
3. **User Resource Labeling**: Secrets, ConfigMaps, and NADs without ownerReferences are labeled for restore
4. **PVC Labeling**: Service operators (glance-operator, mariadb-operator) label their PVCs directly

**Key Points:**

1. **Centralized**: Single controller in openstack-operator handles all API groups
2. **Retroactive**: Labels resources that existed before the controller was deployed
3. **Safe**: Controller failure doesn't block resource creation
4. **Dynamic Discovery**: Uses CRD label cache — adding labels to a new CRD type automatically includes its instances
5. **User-Provided Detection**: Only labels Secrets/ConfigMaps/NADs without ownerReferences (user-provided)

### Controller vs Webhook Approach

An earlier version of this design proposed using mutating admission webhooks to label resources at creation time. The controller-based approach was chosen instead for these reasons:

| Aspect | Controller (chosen) | Webhook (considered) |
|--------|--------------------|--------------------|
| **Complexity** | Single controller, centralized | Webhook per operator, distributed |
| **Infrastructure** | No extra TLS/service/webhook config | Requires webhook TLS certs, services, configs per operator |
| **Retroactive labeling** | Yes — labels existing resources | No — only fires on create/update; needs controller fallback anyway |
| **Failure impact** | Labels delayed, resource creation not blocked | Webhook failure blocks all resource creation |
| **Testing** | Simple envtests | Requires webhook server in tests |
| **Race condition** | Brief window before labeling | Atomic at creation time |

The theoretical race condition (resource created but not yet labeled when a backup runs) is not a practical concern: OpenStack deployments take minutes to hours to complete, and backups are scheduled operations. The controller labels resources within seconds of creation — long before any backup would run.

### Two Labeling Mechanisms

Resources get labeled for restore through two complementary mechanisms:

#### 1. Controller Labels CR Instances and User-Provided Resources

The OpenStackBackupConfig controller labels:
- **CR instances**: Based on CRD labels (e.g., OpenStackControlPlane, NetConfig, MariaDBDatabase)
- **User-provided Secrets**: Without ownerReferences (SSH keys, CA certs, passwords)
- **User-provided ConfigMaps**: Without ownerReferences (custom configurations)
- **NetworkAttachmentDefinitions**: Without ownerReferences

#### 2. Operators Directly Label Resources They Create

Operators add restore labels when creating resources, even if those resources have ownerReferences:
- Issuers (cert-manager) - both operator-managed and custom
- PVCs (glance-operator, mariadb-operator)
- Database password secrets (mariadb-operator)

**Why:** Some resources need restore even though they have ownerReferences:
- **Issuers**: Custom Issuers (external CAs) must be preserved; operator-managed Issuers (rootca-*) are harmlessly reconciled
- **PVCs**: Need snapshot restore before pods start (staged deployment)

**Example: openstack-operator creates Issuers with labels**

```go
// In openstack-operator when creating Issuer CRs
func (r *OpenStackControlPlaneReconciler) createIssuer(
    ctx context.Context,
    name string,
    spec certmanagerv1.IssuerSpec,
    instance *corev1beta1.OpenStackControlPlane,
) error {
    issuer := &certmanagerv1.Issuer{
        ObjectMeta: metav1.ObjectMeta{
            Name:      name,
            Namespace: instance.Namespace,
            Labels: map[string]string{
                // Add restore labels
                "backup.openstack.org/restore":       "true",
                "backup.openstack.org/category":      "all",
                "backup.openstack.org/restore-order": "20",
            },
            OwnerReferences: []metav1.OwnerReference{
                // Set OpenStackControlPlane as owner
                {
                    APIVersion: instance.APIVersion,
                    Kind:       instance.Kind,
                    Name:       instance.Name,
                    UID:        instance.UID,
                    Controller: ptr.To(true),
                },
            },
        },
        Spec: spec,
    }

    return r.Client.Create(ctx, issuer)
}
```

**What gets labeled:**
- ✅ Operator-managed Issuers (selfsigned-issuer, rootca-internal, rootca-public, rootca-ovn, rootca-libvirt)
- ✅ Custom Issuers referenced in OpenStackControlPlane spec (ACME, Vault, external CAs)
- ⚠️ User-created Issuers (outside OpenStackControlPlane) - User manually labels if backup/restore needed

**Example: PVC creation with annotation override support**

```go
// In glance-operator when creating PVC
func (r *GlanceReconciler) createPVC(
    ctx context.Context,
    name string,
    instance *glancev1.GlanceAPI,
) error {
    pvc := &corev1.PersistentVolumeClaim{
        ObjectMeta: metav1.ObjectMeta{
            Name:      name,
            Namespace: instance.Namespace,
            Labels:    getBackupLabels(instance.Annotations), // Helper reads annotations
            OwnerReferences: []metav1.OwnerReference{
                {
                    APIVersion: instance.APIVersion,
                    Kind:       instance.Kind,
                    Name:       instance.Name,
                    UID:        instance.UID,
                    Controller: ptr.To(true),
                },
            },
        },
        Spec: corev1.PersistentVolumeClaimSpec{
            // ... PVC spec
        },
    }

    return r.Client.Create(ctx, pvc)
}

// Helper function to determine backup labels (with annotation override support)
func getBackupLabels(annotations map[string]string) map[string]string {
    labels := make(map[string]string)

    // Always add backup labels for PVCs
    labels["backup.openstack.org/backup"] = "true"
    labels["backup.openstack.org/restore"] = "true"

    // Check for user override via annotation
    if order, ok := annotations["backup.openstack.org/restore-order"]; ok {
        labels["backup.openstack.org/restore-order"] = order
    } else {
        labels["backup.openstack.org/restore-order"] = "00"  // Default for PVCs (storage foundation)
    }

    if category, ok := annotations["backup.openstack.org/category"]; ok {
        labels["backup.openstack.org/category"] = category
    } else {
        labels["backup.openstack.org/category"] = "controlplane"
    }

    return labels
}
```

**Summary:**
- **Controller**: Labels CR instances and user-provided resources (no ownerReferences)
- **Operators**: Label resources they create (can have ownerReferences)
- **Result**: All necessary resources get labeled for restore, regardless of ownership

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
- ✅ **All CRs** (OpenStackControlPlane, MariaDBDatabase, DataPlaneNodeSet, DataPlaneDeployment, etc.)
- ✅ **All NetworkAttachmentDefinitions, Issuers, etc.**
- ✅ **PVCs with label** `backup.openstack.org/backup: "true"` (CSI snapshots)
- ❌ **Excluded**: Pods, ReplicaSets, Jobs, Events, StatefulSets (operator-managed, will be recreated)

**Why backup ALL CRs/Secrets/ConfigMaps?**
- Ensures complete snapshot (nothing missed)
- Simple backup logic (no label selector on OADP Backup CR)
- Restore is selective via labels (see below - only resources with `backup-restore: "true"` labels are restored)
- Examples of backed up but not restored: DataPlaneDeployment, operator-managed Secrets/ConfigMaps

**Note on PVC Backup:**
- **PVCs are selectively backed up** using the `backup.openstack.org/backup: "true"` label
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
      backup.openstack.org/backup: "true"
  snapshotVolumes: true
  defaultVolumesToFsBackup: false
  storageLocation: velero-1
  ttl: 720h
```

#### Selective Restore (By Order)

Multiple Restore CRs, one per restore order, using labels added by the controller.

**Restore Strategy:**
- ✅ **Only resources with** `backup.openstack.org/restore: "true"` **label**
- ✅ **Controller labels user-provided resources** (no ownerReferences) and CR instances
- ❌ **Operator-managed resources excluded** (no labels, will be recreated by operators)

This means:
- User-provided Secrets → Labeled by controller → Restored ✅
- Operator-created Secrets → Not labeled → Not restored, recreated by operator ✅
- User-provided ConfigMaps → Labeled by controller → Restored ✅
- Operator-created ConfigMaps → Not labeled → Not restored, recreated by operator ✅
- CR instances (with CRD labels) → Labeled by controller → Restored ✅

#### OwnerReference and Annotation Handling

**The Problems:**

When OADP restores resources from backup, several metadata fields can cause issues:

**1. OwnerReferences with Stale UIDs:**

Each resource gets a NEW UID on restore (UIDs are cluster-unique identifiers). However, backed-up ownerReferences contain OLD UIDs from the original cluster. This causes:

- **Orphaned resources**: Restored resource has ownerReference with old UID that doesn't match the new owner's UID
- **Broken ownership chain**: Kubernetes doesn't recognize the ownership relationship
- **Potential data loss**: Operators might try to delete/recreate PVCs when they don't recognize them as owned resources

**Example:**
```yaml
# Backup: PVC owned by GlanceAPI
metadata:
  name: glance-pvc
  ownerReferences:
  - uid: old-glance-uid-123  # Old UID from backup

# After restore:
# - PVC has ownerReference: old-glance-uid-123
# - GlanceAPI NOT restored (operator-managed)
# - Operator creates NEW GlanceAPI with NEW UID: new-glance-uid-456
# - PVC is orphaned (UID mismatch)
# - Operator might delete/recreate PVC → DATA LOSS!
```

**2. last-applied-configuration Annotation Too Large:**

The `kubectl.kubernetes.io/last-applied-configuration` annotation stores the entire resource specification from the last `kubectl apply`. This can:

- **Exceed size limits**: Very large resources fail to restore due to annotation size
- **Cause API server errors**: etcd has size limits on annotations
- **Be unnecessary**: Resource will get new annotation on next apply

**The Solution:**

Use Velero `resourceModifier` (via a ConfigMap) to **strip large annotations and add staging annotations** during restore. Velero requires resource modifier rules to be defined in a ConfigMap and referenced by the Restore CR.

**Step 1: Create the resource modifier ConfigMap** in the OADP namespace:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: openstack-restore-resource-modifiers
  namespace: openshift-adp
data:
  resource-modifiers.yaml: |
    version: v1
    resourceModifierRules:
    # Strip last-applied-configuration from all resources
    - conditions:
        groupResource: "*"
        namespaces:
        - openstack
      mergePatches:
      - patchData: |
          metadata:
            annotations:
              kubectl.kubernetes.io/last-applied-configuration: null
    # Add deployment-stage annotation to OpenStackControlPlane
    - conditions:
        groupResource: openstackcontrolplanes.core.openstack.org
        namespaces:
        - openstack
      mergePatches:
      - patchData: |
          metadata:
            annotations:
              core.openstack.org/deployment-stage: "infrastructure-only"
```

**Step 2: Reference the ConfigMap** in each Restore CR via `spec.resourceModifier.ref`:

```yaml
# Restore Order 10: Secrets, ConfigMaps, NADs
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-restore-order-10
  namespace: openshift-adp
spec:
  backupName: openstack-backup-20260303-120000
  labelSelector:
    matchLabels:
      backup.openstack.org/restore: "true"
      backup.openstack.org/restore-order: "10"
  resourceModifier:
    kind: ConfigMap
    name: openstack-restore-resource-modifiers
---
# Restore Order 20: Infrastructure CRs
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-restore-order-20
  namespace: openshift-adp
spec:
  backupName: openstack-backup-20260303-120000
  labelSelector:
    matchLabels:
      backup.openstack.org/restore: "true"
      backup.openstack.org/restore-order: "20"
  resourceModifier:
    kind: ConfigMap
    name: openstack-restore-resource-modifiers
---
# All restore orders reference the same ConfigMap.
# The deployment-stage rule only matches OpenStackControlPlane resources,
# so it is harmless in other restore steps.
```

**Key Points:**
- **Backup**: All user resources in namespace (all Secrets, ConfigMaps, CRs) - complete snapshot
- **Restore**: Only resources with `backup.openstack.org/restore: "true"` label - selective filtering
- **Metadata Cleanup**: All restore orders use `resourceModifiers` to remove:
  - `ownerReferences` - Prevents orphaned resources (operators adopt during reconciliation)
  - `kubectl.kubernetes.io/last-applied-configuration` - Can be too large and cause restore failures
- **Controller**: Adds restore labels to CR instances and user-provided resources (no ownerReferences)
- **Operators**: Recreate their own Secrets/ConfigMaps on reconciliation (not restored from backup)

## Customizing Restore Order for Core Resources

### Manual Labeling (Available Immediately)

Users can pre-label Secrets, ConfigMaps, PVCs, and cert-manager resources to customize their restore order. The controller respects existing labels and won't overwrite them.

**Example: CA secret restored in order 10, service secret in order 50**

```bash
# CA certificate secret (restored early)
oc label secret openstack-ca-cert \
  backup.openstack.org/restore=true \
  backup.openstack.org/category=all \
  backup.openstack.org/restore-order=10 \
  -n openstack

# Service-specific secret (restored after infrastructure)
oc label secret nova-cell1-config \
  backup.openstack.org/restore=true \
  backup.openstack.org/category=controlplane \
  backup.openstack.org/restore-order=50 \
  -n openstack
```

**How it works:**
1. User creates and labels resource with desired restore order
2. Controller checks if resource already has `backup.openstack.org/restore: "true"`
3. If yes, controller skips labeling (preserves user's custom order)
4. If no, controller applies default labels

### Annotation-Based Overrides (Alternative to Manual Labels)

Instead of pre-labeling resources, users can override restore order using **annotations**. The controller reads annotations and applies corresponding labels.

**Advantages:**
- Annotations show what's been customized (visible override)
- Labels always have the effective values (for OADP selectors)
- Operators can reconcile: annotation → label
- Clear distinction between defaults and user customization

**Example: Override restore order via annotation**

```bash
# Create resource with custom restore order annotation
cat <<EOF | oc apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: custom-ca-cert
  namespace: openstack
  annotations:
    backup.openstack.org/restore-order: "10"  # User customization via annotation
    backup.openstack.org/category: "all"      # Optional category override
stringData:
  ca.crt: |
    -----BEGIN CERTIFICATE-----
    ...
EOF

# Controller/operator reads annotation and applies label
# Result:
# labels:
#   backup.openstack.org/restore: "true"
#   backup.openstack.org/restore-order: "10"    # From annotation
#   backup.openstack.org/category: "all"        # From annotation
```

**How operators implement annotation overrides:**

```go
func getBackupLabels(obj client.Object) map[string]string {
    labels := make(map[string]string)

    // Check if user provided override annotation
    if order, ok := obj.GetAnnotations()["backup.openstack.org/restore-order"]; ok {
        // User customized - use annotation value
        labels["backup.openstack.org/restore-order"] = order
    } else {
        // Default - use CRD-defined order
        labels["backup.openstack.org/restore-order"] = getDefaultOrder(obj)
    }

    if category, ok := obj.GetAnnotations()["backup.openstack.org/category"]; ok {
        labels["backup.openstack.org/category"] = category
    } else {
        labels["backup.openstack.org/category"] = getDefaultCategory(obj)
    }

    // Always add restore label
    labels["backup.openstack.org/restore"] = "true"

    return labels
}
```

**Checking for customization:**

```bash
# List resources with custom restore order (annotation present)
oc get secrets -n openstack -o json | \
  jq '.items[] | select(.metadata.annotations["backup.openstack.org/restore-order"]) |
      {name: .metadata.name, order: .metadata.annotations["backup.openstack.org/restore-order"]}'
```

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
      order: "10"
    configmaps:
      category: "all"
      order: "10"
    persistentvolumeclaims:
      category: "all"
      order: "00"  # Storage foundation - restored first
    issuers:  # cert-manager Issuer
      category: "all"
      order: "20"
    networkattachmentdefinitions:
      category: "all"
      order: "10"

  # Custom overrides for specific resources
  customOrders:
  - resource:
      kind: Secret
      name: openstack-ca-cert
    category: "all"
    order: "10"
  - resource:
      kind: Secret
      name: nova-cell1-config
    category: "controlplane"
    order: "20"
```

**Benefits of CRD-based configuration:**
- Centralized configuration for all restore order defaults
- Easy to customize per deployment
- No need to manually label every resource
- Can be backed up and restored along with other CRs

**Implementation approach:**
1. Controller reads OpenStackBackupConfig CR to get default orders
2. Applies configured defaults instead of hardcoded values
3. Still respects existing labels (manual overrides take precedence)
4. Fallback to hardcoded defaults if no config CR exists

## Restore Order

The restore sequence is critical for maintaining dependencies between resources.

| Order | Resources | Notes |
|-------|-----------|-------|
| 00 | **PVCs** | **Storage Foundation**: CSI snapshots for all storage volumes (Galera backups, Glance images, Cinder volumes, Manila shares)<br>Restored first so backup data is available for database restore in order 50 |
| 10 | NetworkAttachmentDefinitions<br>Secrets (user-provided)<br>ConfigMaps (user-provided) | **Foundation Resources**: Core resources with no dependencies<br>Includes CA certs, DB passwords, SSH keys |
| 20 | OpenStackVersion<br>TLS Issuers<br>MariaDBDatabase<br>MariaDBAccount<br>Infrastructure CRs<br>NetConfig<br>InstanceHa | **Version & Infrastructure**: OpenStackVersion restored first (required by ControlPlane)<br>TLS Issuers need CA secrets, MariaDBAccount needs password secrets<br>Infrastructure: Topology, BGPConfiguration, DNSData<br>NetConfig: Network topology (required by Reservation/IPSet)<br>InstanceHa: Restored with `spec.disabled: True` (resource modifier) to prevent fencing; re-enable after verifying EDPM connectivity |
| 30 | OpenStackControlPlane<br>Reservation | **Control Plane + Networking**: CtlPlane restored with staged deployment annotation (`deployment-stage: infrastructure-only`)<br>ControlPlane controller will use the already-restored OpenStackVersion from order 20<br>Reservation needs NetConfig from order 20<br>Wait for infrastructure ready before proceeding |
| 40 | IPSet<br>GaleraBackup<br>RabbitMQUser (user-created)<br>RabbitMQVhost<br>DataPlaneService (user-created) | **IP Sets, Backup Config & User Resources** (while in infra-only mode)<br>IPSet: Requires Reservation from order 30<br>GaleraBackup: Backup configuration CR (needs CtlPlane)<br>RabbitMQUser/Vhost: User-created resources only (no ownerReferences)<br>DataPlaneService: Custom services before NodeSets |
| 50 | *Database Restore*<br>*RabbitMQ Credentials*<br>*Resume Deployment* | **Manual/Controller** (while in infra-only mode):<br>1. Create GaleraRestore CRs, execute restore from PVCs (order 00), clean up<br>2. Create RabbitMQUser CRs with old credentials (extract from backed-up secrets, create new `-restored-user` secrets/CRs)<br>3. Remove `deployment-stage` annotation → CtlPlane reconciles and starts all services |
| 60 | DataPlaneNodeSet | **Data Plane**: Node set definitions (needs IPSets/Reservations from order 30) |

**Notes:**
- **Orders 00-40**: Pure OADP restore (automated via label selectors)
- **Order 50**: Requires manual steps or controller automation (database restore, RabbitMQ credentials, resume deployment)
- **Gaps of 10**: Allows easy insertion of new resources (e.g., order 25 between 20 and 30) without renumbering
- **Staged Deployment**: CtlPlane restored with `deployment-stage: infrastructure-only` annotation in order 30, annotation removed in order 50 after database/RabbitMQ restore
- **RabbitMQ Restore Process**:
  - Order 10: Backed-up secrets restored (including `*-default-user` with old passwords)
  - Order 40: User-created RabbitMQUser CRs restored (no ownerReferences)
  - Order 50 (manual): Create NEW `-restored-user` secrets with old passwords + NEW RabbitMQUser CRs (operator-managed clusters get original credentials)
- **Customization**: All restore orders can be overridden via annotations on individual resources (see [Customizing Restore Order](#customizing-restore-order-for-core-resources))

## CRD Label Mapping

This section shows the labels that should be added to each CRD definition.

**Column definitions:**
- **Restore**: `backup.openstack.org/restore` label value (true = controller labels instances for restore, defaults to false if missing)
- **Category**: `backup.openstack.org/category` label value
- **Order**: `backup.openstack.org/restore-order` label value

**Note:** All CRs are backed up via full namespace backup (OADP Backup CR has no label selector). Only CRs with `backup-restore: true` label on their CRD are restored.

**Dynamic Discovery:** Controllers can discover all CRDs that participate in backup/restore:
```bash
oc get crd -l backup.openstack.org/restore=true
```

### Core Operator CRDs

| CRD | Restore | Category | Order | Notes |
|-----|---------|----------|-------|-------|
| OpenStackControlPlane | true | controlplane | 30 | Main control plane CR |
| OpenStackVersion | true | controlplane | 20 | Version tracking |

### Infrastructure Operator CRDs

| CRD | Restore | Category | Order | Notes |
|-----|---------|----------|-------|-------|
| NetConfig | true | dataplane | 20 | Network topology (required before Reservation/IPSet) |
| Topology | true | dataplane | 20 | Network topology |
| BGPConfiguration | true | dataplane | 20 | BGP config |
| DNSData | true | dataplane | 20 | DNS records |
| Reservation | true | dataplane | 30 | IP reservations (requires NetConfig) |
| IPSet | true | dataplane | 40 | IP address sets (requires Reservation) |
| InstanceHa | true | controlplane | 20 | **Restored with `spec.disabled: True`** via resource modifier to prevent fencing. Operator re-enables after verifying EDPM connectivity. |
| RabbitMQUser* | true | controlplane | 40 | User-created only (no ownerReferences) |
| RabbitMQVhost* | true | controlplane | 40 | User-created only (no ownerReferences) |

*User-created resources only. Operator-managed RabbitMQUser CRs are recreated in order 50 (manual/controller) with original credentials.

### MariaDB Operator CRDs

| CRD | Restore | Category | Order | Notes |
|-----|---------|----------|-------|-------|
| MariaDBDatabase | true | controlplane | 20 | Database definitions |
| MariaDBAccount | true | controlplane | 20 | Database accounts (references password secret) |
| GaleraBackup | true | controlplane | 40 | Backup configuration (needs CtlPlane from order 30) |

**Important:** mariadb-operator must label password secrets when creating them:
```go
// When mariadb-operator creates database password secret
secret := &corev1.Secret{
    ObjectMeta: metav1.ObjectMeta{
        Name:      "nova-api-db-secret",
        Namespace: namespace,
        Labels: map[string]string{
            // CRITICAL: Add restore labels so secret is restored before MariaDBAccount
            "backup.openstack.org/restore":       "true",
            "backup.openstack.org/category":      "all",
            "backup.openstack.org/restore-order": "10",  // Order 10 (before MariaDBAccount)
        },
        OwnerReferences: []metav1.OwnerReference{
            // MariaDBAccount owner
        },
    },
    Data: map[string][]byte{
        "password": []byte(generatedPassword),
    },
}
```

**Why:** MariaDBAccount CR references password secret (e.g., `spec.secret: nova-api-db-secret`). The secret must be restored in order 10 (before MariaDBAccount in order 40) so the operator can read the original password during reconciliation.

### Data Plane CRDs

| CRD | Restore | Category | Order | Notes |
|-----|---------|----------|-------|-------|
| OpenStackDataPlaneService* | true | dataplane | 40 | Custom services (before NodeSets) |
| OpenStackDataPlaneNodeSet | true | dataplane | 60 | Node set definitions (requires IPSets/Reservations) |
| OpenStackDataPlaneDeployment | false** | - | - | Backed up for reference, never restored (ephemeral) |

*Only for user-created services (no ownerReferences)
**No `backup-restore` label on CRD = defaults to false (not restored)

**DataPlane Integration:**

DataPlane resources are **integrated into the unified backup/restore** approach:

- **Backup**: Single OADP backup includes ControlPlane AND DataPlane (entire namespace)
- **Restore**: Flexible restore options using category labels:

```yaml
# Full restore (ControlPlane + DataPlane)
labelSelector:
  matchLabels:
    backup.openstack.org/restore: "true"

# ControlPlane only restore
labelSelector:
  matchLabels:
    backup.openstack.org/restore: "true"
    backup.openstack.org/category: "controlplane"

# DataPlane only restore
labelSelector:
  matchLabels:
    backup.openstack.org/restore: "true"
    backup.openstack.org/category: "dataplane"
```

**Benefits:**
- Single backup artifact (no separate DataPlane backup needed)
- Selective restore by category (restore ControlPlane first, verify, then DataPlane)
- Same restore order guarantees as current procedure (NetConfig → Reservation → IPSet → DataPlaneService → DataPlaneNodeSet)
- Replaces separate `backup-restore-dataplane.md` procedure

**DataPlane restore order dependencies** (already included in unified restore order table):
1. NetConfig (order 20) - Network topology
2. Reservation (order 30) - Requires NetConfig
3. IPSet (order 40) - Requires Reservation
4. DataPlaneService (order 40) - Before NodeSets
5. DataPlaneNodeSet (order 60) - Requires IPSets/Reservations

### Kubernetes Core Resources

**Note:** These are not OpenStack CRDs, so they don't have CRD annotations. Instead, the controller/operators add labels directly to resource instances.

| Resource | Restore | Category | Order | Notes |
|----------|---------|----------|-------|-------|
| Secret* | user-only | controlplane | 10 | All backed up; only user-provided restored (no ownerReferences) |
| ConfigMap* | user-only | controlplane | 10 | All backed up; only user-provided restored (no ownerReferences) |
| NetworkAttachmentDefinition | all | controlplane | 10 | All backed up and restored |
| Issuer (cert-manager) | all | controlplane | 20 | All backed up; operator adds labels to all for restore |
| PersistentVolumeClaim** | labeled-only | controlplane | 00 | **Storage Foundation**: Only labeled PVCs backed up and restored (exception to full backup)<br>Restored FIRST so backup data is available for database restore |

*All Secrets/ConfigMaps included in full namespace backup; controller only labels user-provided ones (no ownerReferences) for restore
**PVCs use dual-label approach and selective backup (see PVC Labeling Strategy below)

### PVC Labeling Strategy

PVCs use a **dual-label approach** to separate backup inclusion from restore inclusion:

**Label Purposes:**
- **`backup.openstack.org/backup: "true"`** - Include PVC in backup snapshot (required for CSI snapshot)
- **`backup.openstack.org/restore: "true"`** - Include PVC in restore operation (optional)

**Common Scenarios:**

1. **Production data (backup AND restore)** - Most PVCs:
   ```yaml
   metadata:
     labels:
       backup.openstack.org/backup: "true"              # Snapshot during backup
       backup.openstack.org/restore: "true"      # Restore during restore
       backup.openstack.org/restore-order: "00"
     annotations:
       service: glance
   ```
   Examples: Glance images, Cinder volumes, Manila shares, Galera backup dumps

2. **Backup-only data (backup but NOT restore)** - Logs, temporary data:
   ```yaml
   metadata:
     labels:
       backup.openstack.org/backup: "true"              # Snapshot during backup
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

**Backup CR** - Uses `backup.openstack.org/backup` label selector:
```yaml
apiVersion: velero.io/v1
kind: Backup
spec:
  includedNamespaces:
  - openstack
  labelSelector:
    matchLabels:
      backup.openstack.org/backup: "true"  # Only PVCs with backup label
  snapshotVolumes: true
```

**Restore CR** - Uses `backup.openstack.org/restore` label selector:
```yaml
apiVersion: velero.io/v1
kind: Restore
spec:
  labelSelector:
    matchLabels:
      backup.openstack.org/restore: "true"
      backup.openstack.org/restore-order: "00"
  restorePVs: true
```

**Excluding Individual PVCs:**

If a PVC has `backup.openstack.org/backup: "true"` but should be skipped, add Velero's annotation:
```yaml
metadata:
  labels:
    backup.openstack.org/backup: "true"
  annotations:
    backup.velero.io/backup-volumes: "false"  # Override: skip this PVC
```

**Who Sets These Labels:**

- Service operators add `backup.openstack.org/backup: "true"` when creating PVCs that need backup
- Controller (or operator) adds `backup.openstack.org/restore: "true"` + order to PVCs that should restore
- Manual override via `backup.velero.io/backup-volumes: "false"` annotation when needed

## Backup Categories

Categories enable selective backup/restore scenarios. The design uses **three categories**: `controlplane`, `dataplane`, and `all`.

### Category Assignment

**controlplane:**
- OpenStackControlPlane CR
- MariaDBDatabase, MariaDBAccount, GaleraBackup
- RabbitMQUser, RabbitMQVhost
- Issuers (cert-manager), InstanceHa
- **All user-provided Secrets and ConfigMaps** (CA certs, passwords, SSH keys, EDPM configs)
- PVCs for services

**dataplane:**
- NetConfig, Topology, BGPConfiguration, DNSData
- Reservation, IPSet
- OpenStackDataPlaneService, OpenStackDataPlaneNodeSet

**Rationale:**
- User-provided secrets (including SSH keys for DataPlane) get `controlplane` category
- Ensures ControlPlane restore includes all necessary credentials
- DataPlane restore can be done separately, but requires ControlPlane secrets to exist first
- Additional categories can be added later if needed

### Selective Restore Examples

#### Full Restore (ControlPlane + DataPlane)
```yaml
labelSelector:
  matchLabels:
    backup.openstack.org/restore: "true"
```
Use case: Complete disaster recovery

#### Control Plane Only
```yaml
labelSelector:
  matchLabels:
    backup.openstack.org/restore: "true"
    backup.openstack.org/category: "controlplane"
```
Use cases:
- Control plane disaster recovery
- Restore control plane first, verify, then restore data plane
- Includes all Secrets/ConfigMaps (needed by both ControlPlane and DataPlane)

#### Data Plane Only
```yaml
labelSelector:
  matchLabels:
    backup.openstack.org/restore: "true"
    backup.openstack.org/category: "dataplane"
```
Use cases:
- Data plane node replacement
- Isolated data plane restore (requires ControlPlane secrets already restored)
- Network topology reconfiguration

## Implementation Phases

### Phase 1: Controller & CRD Annotations

**Goal**: Automatic labeling of resources for restore

**Changes:**
1. Add CRD annotations to all operator CRDs
2. Implement OpenStackBackupConfig controller in openstack-operator
3. Implement generic helper function in lib-common (respects existing labels)
4. Test that resources get labeled on reconciliation

**Backward Compatibility**: Existing Ansible backup/restore continues to work

**Features:**
- Automatic labeling of user-provided resources (no ownerReferences)
- Respects existing labels (allows manual customization)
- Default restore order based on resource type
- Periodic reconciliation handles existing environments and new resources

**Testing:**
```bash
# Test 1: Automatic labeling with defaults
oc create secret generic test-secret --from-literal=foo=bar -n openstack

# Verify labels were added after controller reconciliation
oc get secret test-secret -n openstack -o jsonpath='{.metadata.labels}'
# Should show: backup.openstack.org/restore: "true", backup.openstack.org/restore-order: "10"

# Test 2: Manual override (pre-label before controller runs)
oc create secret generic custom-secret \
  --from-literal=foo=bar \
  -n openstack \
  --dry-run=client -o yaml | \
  oc label -f - --local \
    backup.openstack.org/restore=true \
    backup.openstack.org/category=controlplane \
    backup.openstack.org/restore-order=50 \
    --dry-run=client -o yaml | \
  oc apply -f -

# Verify custom labels were preserved
oc get secret custom-secret -n openstack -o jsonpath='{.metadata.labels}'
# Should show: backup.openstack.org/restore: "true", backup.openstack.org/restore-order: "50"
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
# Step 0: Create the resource modifier ConfigMap (required for all restores)
oc apply -f docs/dev/backup-restore/restore/00-resource-modifiers-configmap.yaml

# Order 00: PVCs (Storage Foundation)
cat <<EOF | oc apply -f -
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-restore-order-00
  namespace: openshift-adp
spec:
  backupName: openstack-backup-20260303-120000
  labelSelector:
    matchLabels:
      backup.openstack.org/restore: "true"
      backup.openstack.org/restore-order: "00"
  restorePVs: true  # CSI snapshots
  resourceModifier:
    kind: ConfigMap
    name: openstack-restore-resource-modifiers
EOF

# Wait for completion (CSI snapshot restore may take time)
oc wait --for=jsonpath='{.status.phase}'=Completed \
  restore/openstack-restore-order-00 -n openshift-adp --timeout=20m

# Order 10: Foundation Resources (NADs, Secrets, ConfigMaps)
cat <<EOF | oc apply -f -
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-restore-order-10
  namespace: openshift-adp
spec:
  backupName: openstack-backup-20260303-120000
  labelSelector:
    matchLabels:
      backup.openstack.org/restore: "true"
      backup.openstack.org/restore-order: "10"
  resourceModifier:
    kind: ConfigMap
    name: openstack-restore-resource-modifiers
EOF

# Wait for completion
oc wait --for=jsonpath='{.status.phase}'=Completed \
  restore/openstack-restore-order-10 -n openshift-adp --timeout=10m

# Order 20: Infrastructure CRs (MariaDB CRs, OpenStackVersion, etc.)
cat <<EOF | oc apply -f -
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-restore-order-20
  namespace: openshift-adp
spec:
  backupName: openstack-backup-20260303-120000
  labelSelector:
    matchLabels:
      backup.openstack.org/restore: "true"
      backup.openstack.org/restore-order: "20"
  resourceModifier:
    kind: ConfigMap
    name: openstack-restore-resource-modifiers
EOF

# Wait for completion
oc wait --for=jsonpath='{.status.phase}'=Completed \
  restore/openstack-restore-order-20 -n openshift-adp --timeout=10m

# Order 30: ControlPlane (with staged deployment annotation added by ConfigMap)
cat <<EOF | oc apply -f -
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-restore-order-30
  namespace: openshift-adp
spec:
  backupName: openstack-backup-20260303-120000
  labelSelector:
    matchLabels:
      backup.openstack.org/restore: "true"
      backup.openstack.org/restore-order: "30"
  resourceModifier:
    kind: ConfigMap
    name: openstack-restore-resource-modifiers
EOF

oc wait --for=jsonpath='{.status.phase}'=Completed \
  restore/openstack-restore-order-30 -n openshift-adp --timeout=10m

# Wait for infrastructure ready (annotation already applied by resource modifier)
oc wait --for=condition=OpenStackControlPlaneInfrastructureReady \
  openstackcontrolplane/openstack-galera-network-isolation \
  -n openstack --timeout=20m

# Order 40: Backup Config & User Resources (while in infra-only mode)
cat <<EOF | oc apply -f -
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-restore-order-40
  namespace: openshift-adp
spec:
  backupName: openstack-backup-20260303-120000
  labelSelector:
    matchLabels:
      backup.openstack.org/restore: "true"
      backup.openstack.org/restore-order: "40"
  resourceModifier:
    kind: ConfigMap
    name: openstack-restore-resource-modifiers
EOF

oc wait --for=jsonpath='{.status.phase}'=Completed \
  restore/openstack-restore-order-40 -n openshift-adp --timeout=10m

# Order 50: Manual Steps (while in infra-only mode)
echo "Order 50: Database & RabbitMQ Restore + Resume Deployment"

# 50.1: Database Restore
echo "Step 1: Database Restore"
# Create GaleraRestore CRs (reference backup PVCs from order 00)
# Execute restore_galera script
# Clean up GaleraRestore CRs
# See: docs/dev/backup-restore/scripts/restore-galera.sh

# 50.2: RabbitMQ Credentials Restore
echo "Step 2: RabbitMQ Credentials"
# Extract old passwords from backed-up secrets
# Create new secrets named "*-restored-user" with old passwords
# Create new RabbitMQUser CRs referencing "-restored-user" secrets
# See: docs/dev/playbooks/restore-openstack-ctlplane.yaml (lines 1332-1427)

# 50.3: Resume Deployment
echo "Step 3: Resume Deployment"
oc annotate openstackcontrolplane openstack-galera-network-isolation \
  -n openstack core.openstack.org/deployment-stage-

# Operator will now reconcile and start all services
# Wait for full deployment ready
oc wait --for=condition=Ready \
  openstackcontrolplane/openstack-galera-network-isolation \
  -n openstack --timeout=30m

# Order 60: Data Plane
cat <<EOF | oc apply -f -
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-restore-order-60
  namespace: openshift-adp
spec:
  backupName: openstack-backup-20260303-120000
  labelSelector:
    matchLabels:
      backup.openstack.org/restore: "true"
      backup.openstack.org/restore-order: "60"
  resourceModifier:
    kind: ConfigMap
    name: openstack-restore-resource-modifiers
EOF

oc wait --for=jsonpath='{.status.phase}'=Completed \
  restore/openstack-restore-order-60 -n openshift-adp --timeout=10m

echo "Full restore completed!"
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
      order: "10"
    configmaps:
      category: "all"
      order: "10"
    persistentvolumeclaims:
      category: "all"
      order: "00"  # Storage foundation - restored first
    issuers:
      category: "all"
      order: "20"
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
2. Create OADP Restore CRs in sequence by restore order (00, 10, 20, 30, 40)
3. Wait for each OADP Restore completion
4. Handle special cases:
   - Order 00: Wait for PVC CSI snapshot restore (may take time)
   - Order 30: OADP adds staged deployment annotation, wait for infrastructure ready
   - Order 50 (manual steps - controller executes in sequence):
     - Create GaleraRestore CRs, execute database restore, clean up
     - Create RabbitMQUser CRs with old credentials (extract from backed-up secrets, create `-restored-user` secrets/CRs)
     - Remove staged deployment annotation, wait for CtlPlane ready
5. Restore order 60 (DataPlane)
6. Update OpenStackBackupRestore status with progress

**Status Fields:**
```yaml
status:
  phase: InProgress  # Pending, InProgress, Completed, Failed
  currentRestoreOrder: 20
  conditions:
  - type: Order00Complete
    status: "True"
  - type: Order10Complete
    status: "True"
  - type: Order20InProgress
    status: "True"
```

## Benefits

### Compared to Current Ansible Approach

| Aspect | Current (Ansible) | Proposed (Controller + OADP) |
|--------|------------------|-------------------------------|
| Resource Discovery | Hardcoded `jq` filters | Dynamic via CRD annotations |
| Backup Mechanism | `oc get` + jq + OADP (for PVCs) | Single OADP Backup CR |
| Restore Order | Hardcoded in playbook | Declared in CRD annotations |
| Adding New CRD | Update Ansible playbook | Add CRD annotation only |
| Manual Restore | Run full playbook | Create OADP Restore CRs |
| Automation | Ansible playbook | Golang controller |
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

2. **Database Restore Automation**: Should controller exec into pods or require manual intervention?
   - Automated mode: Controller execs into pods
   - Manual mode: User runs commands

3. **OpenStackBackupConfig Scope**: Should the config CR be namespace-scoped or cluster-scoped?
   - Namespace-scoped: Different configs per OpenStack deployment
   - Cluster-scoped: Single config for all OpenStack deployments

4. **Default vs Custom Order Precedence**: How should the order precedence work?
   - Current proposal: Manual labels > OpenStackBackupConfig > Hardcoded defaults
   - Alternative: OpenStackBackupConfig > Manual labels > Hardcoded defaults

## Next Steps

1. Sketch detailed implementation for Phase 1 (controller)
2. Define exact CRD annotation schema
3. Implement controller in openstack-operator
4. Test with sample resources
5. Document migration path from current Ansible approach
