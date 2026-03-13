# Webhook Design Compatibility Analysis

This document analyzes the [webhook-based backup/restore design](backup-restore-controller-design.md) against the existing backup/restore procedures to identify compatibility issues and discussion topics.

## Document References

- [backup-restore-ctlplane.md](backup-restore-ctlplane.md) - Current control plane backup/restore
- [backup-restore-dataplane.md](backup-restore-dataplane.md) - Current data plane backup/restore
- [backup-restore-storage-volumes.md](backup-restore-storage-volumes.md) - Current OADP storage backup/restore
- [backup-restore-controller-design.md](backup-restore-controller-design.md) - Proposed webhook approach

---

## Compatibility Summary

| Aspect | Current Approach | Webhook Design | Compatible? | Discussion Needed? |
|--------|------------------|----------------|-------------|-------------------|
| Backup Method | `oc get` + jq | OADP full namespace backup | ⚠️ Partial | ✅ Yes - full vs selective |
| Restore Method | `oc apply` from JSON files | OADP Restore with label selectors | ⚠️ Partial | ✅ Yes - migration path |
| Restore Order | Hardcoded in playbook | CRD annotations + OADP label selectors | ✅ Yes | ✅ Yes - granularity |
| ownerReferences | Manually removed in jq | Webhook skips owned resources | ✅ Yes | ❌ No |
| PVC Backup | OADP with manual labels | OADP with automatic labels | ⚠️ Partial | ✅ Yes - labeling mechanism |
| Label Convention | `openstack.org/backup: "true"` | `openstack.org/backup-restore: "true"` | ❌ No | ✅ Yes - naming conflict |
| Database Restore | GaleraRestore CRs (manual) | Order 10 (manual) | ✅ Yes | ❌ No |
| RabbitMQ Restore | Manual credential restoration | Order 11 (manual) | ✅ Yes | ❌ No |
| Staged Deployment | Annotation-based | Incorporated in order 6 | ✅ Yes | ❌ No |
| DataPlane Resources | Separate manual backup | Included in namespace backup | ⚠️ Partial | ✅ Yes - exclusions |

---

## Discussion Topics

### 1. Label Naming Convention Conflict

**Issue:** Existing PVC labeling uses different convention than webhook design.

**Current:**
```yaml
# backup-restore-storage-volumes.md
metadata:
  labels:
    openstack.org/backup: "true"
```

**Webhook Design:**
```yaml
# backup-restore-controller-design.md
metadata:
  labels:
    openstack.org/backup-restore: "true"
    openstack.org/backup-restore-order: "8"
    openstack.org/backup-category: "all"
```

**Options:**
1. **Rename PVC labels** to match webhook design (`openstack.org/backup-restore`)
   - Pros: Consistent naming across all resources
   - Cons: Breaking change, requires relabeling existing PVCs

2. **Keep both labels** during transition period
   - Pros: Backward compatibility
   - Cons: Label proliferation, confusion

3. **Use `openstack.org/backup` for PVCs only** (special case)
   - Pros: No breaking changes
   - Cons: Inconsistent naming, need to document exception

**Recommendation:** Option 1 (rename) with migration script to relabel existing PVCs.

**Migration script example:**
```bash
# Relabel existing PVCs from old to new convention
oc get pvc -n openstack -l openstack.org/backup=true -o name | while read pvc; do
  oc label $pvc -n openstack \
    openstack.org/backup-restore=true \
    openstack.org/backup-category=all \
    openstack.org/backup-restore-order=8
  oc label $pvc -n openstack openstack.org/backup-
done
```

---

### 2. Full Namespace Backup vs Selective Backup

**Issue:** Current approach selectively backs up specific CRD types. Webhook design backs up everything.

**Current Approach:**
```bash
# backup-restore-ctlplane.md - Selective backup
oc get openstackcontrolplane -n $NAMESPACE -o json | jq '...' > ctlplane-backup.json
oc get mariadbdatabase -n $NAMESPACE -o json | jq '...' > mariadbdatabase-backup.json
# ... selective for each CRD type
```

**Webhook Design:**
```yaml
# Full namespace backup (everything)
spec:
  includedNamespaces:
  - openstack
  # NO labelSelector - backup everything
```

**Concerns:**

1. **Resources we DON'T want in backup**
   - Example: Operator-generated Pods, Jobs, Deployments
   - Example: Transient resources (Events, etc.)

2. **Resources we want to BACKUP but NOT RESTORE**
   - Example: `OpenStackDataPlaneDeployment` (backup for reference, never restore)
   - Example: Job history, audit logs

3. **Backup size and performance**
   - Backing up everything increases backup size
   - May include unnecessary resources

**Options:**

1. **Full namespace backup with restore-time filtering** (webhook design approach)
   - Backup: Everything in namespace
   - Restore: Only resources with `openstack.org/backup-restore: "true"`
   - Pros: Simple backup, complete snapshot, no risk of missing resources
   - Cons: Larger backup size

2. **Selective backup with label selector** (modified webhook design)
   - Backup: Only resources with `openstack.org/backup: "true"` or `openstack.org/backup-restore: "true"`
   - Restore: Only resources with `openstack.org/backup-restore: "true"`
   - Pros: Smaller backup size, explicit inclusion
   - Cons: Risk of missing resources, need to label everything

3. **Namespace backup with exclusions** (OADP excludedResources)
   - Backup: Everything except excluded resource types
   - Restore: Only labeled resources
   - Pros: Balance between completeness and size
   - Cons: Need to maintain exclusion list

**Recommendation:** Option 1 (full namespace backup) for Phase 1-3, evaluate Option 2 for Phase 4 based on experience.

**OADP excludedResources example:**
```yaml
spec:
  includedNamespaces:
  - openstack
  excludedResources:
  - pods
  - replicasets
  - jobs
  - events
```

---

### 3. Resources to Backup but NOT Restore

**Issue:** Some resources should be backed up for reference but never restored.

**Examples:**
- `OpenStackDataPlaneDeployment` - Ephemeral, backed up for history, should NOT be restored
- Job execution history
- Audit logs

**Current Approach:**
```bash
# backup-restore-dataplane.md
# Backup DataPlaneDeployments (for reference, NOT restored)
oc get openstackdataplanedeployment -n $NAMESPACE -o json > deployment-backup.json

# Restore: Explicitly NOT applied
# (Documented in procedure, but no enforcement)
```

**Webhook Design:**
- CRDs without `openstack.org/backup-restore: "true"` annotation → webhook doesn't label → OADP restore skips
- Full namespace backup includes all CRs (including DataPlaneDeployment)
- OADP restore only restores resources with `openstack.org/backup-restore: "true"` label
- Result: Backed up for reference, never restored ✓

**How it works:**

```yaml
# OpenStackDataPlaneDeployment CRD - NO backup-restore annotation
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: openstackdataplanedeployments.dataplane.openstack.org
  # NO openstack.org/backup-restore annotation
  # Webhook will NOT add restore labels to instances
```

**Backup:**
- Full namespace backup includes all DataPlaneDeployment instances
- Backed up for reference and history ✓

**Restore:**
- OADP restore uses label selector: `openstack.org/backup-restore: "true"`
- DataPlaneDeployment instances have NO label (webhook didn't add it)
- OADP restore skips all DataPlaneDeployment instances ✓

**Recommendation:** No new annotation needed. Absence of `openstack.org/backup-restore` annotation on CRD is sufficient.

**Alternative (if safety mechanism needed):**
If you want the controller to prevent reconciliation of manually restored objects, use a controller-specific annotation (not part of backup/restore design):
```go
// In DataPlaneDeployment controller (optional safety mechanism)
if _, ok := instance.Annotations["dataplane.openstack.org/restored"]; ok {
    Log.Info("Deployment was manually restored, skipping reconciliation")
    return ctrl.Result{}, nil
}
```
This is a separate concern from backup/restore and would be controller-specific logic.

---

### 4. PVC Labeling Mechanism

**Issue:** How do PVCs get labeled with backup/restore labels?

**Current:**
```bash
# backup-restore-storage-volumes.md - Manual labeling
oc label pvc mysql-backup-openstack-backup-openstack \
  openstack.org/backup=true
```

**Webhook Design:**
- Mentioned as open question
- No concrete proposal

**Options:**

1. **Service operators label PVCs on creation**
   ```go
   // In service operator when creating PVC
   pvc.Labels["openstack.org/backup-restore"] = "true"
   pvc.Labels["openstack.org/backup-category"] = "controlplane"
   pvc.Labels["openstack.org/backup-restore-order"] = "8"
   ```
   - Pros: Explicit, service knows which PVCs need backup
   - Cons: Code changes in every operator

2. **Separate mutating webhook for PVCs**
   ```go
   // Webhook watches PVC creation
   // Adds labels based on PVC annotations or name patterns
   if pvc.Annotations["openstack.org/backup"] == "true" {
       pvc.Labels["openstack.org/backup-restore"] = "true"
   }
   ```
   - Pros: Centralized logic, no operator changes
   - Cons: Pattern matching may be error-prone

3. **Manual labeling with documentation**
   - Document which PVCs need labels
   - Operators manually label after creation
   - Pros: Simple, no code changes
   - Cons: Error-prone, not automated

4. **Hybrid approach**
   - Service operators add annotation: `openstack.org/backup: "true"`
   - Webhook converts annotation to labels (restore order, category)
   - Pros: Simple operator changes, centralized restore logic
   - Cons: Two-step process

**Recommendation:** Option 4 (hybrid) for Phase 1, migrate to Option 1 (operators add labels directly) in Phase 4.

**Operator code example:**
```go
// Glance operator creates PVC with annotation
pvc := &corev1.PersistentVolumeClaim{
    ObjectMeta: metav1.ObjectMeta{
        Name:      "glance-data",
        Namespace: namespace,
        Annotations: map[string]string{
            "openstack.org/backup": "true",  // Service operator adds this
        },
    },
    // ... spec
}
```

**Webhook converts annotation to labels:**
```go
// Webhook sees annotation, adds labels
if pvc.Annotations["openstack.org/backup"] == "true" {
    pvc.Labels["openstack.org/backup-restore"] = "true"
    pvc.Labels["openstack.org/backup-category"] = "all"
    pvc.Labels["openstack.org/backup-restore-order"] = "8"
}
```

---

### 5. Restore Order Granularity

**Issue:** Current dataplane restore has very specific order. Webhook design groups resources into same order.

**Current DataPlane Restore Order:**
```bash
# backup-restore-dataplane.md
1. NetConfig
2. Reservations
3. IPSets
4. DataPlaneServices
5. DataPlaneNodeSets
```

**Webhook Design:**
```yaml
# backup-restore-controller-design.md - All in order "5"
NetConfig:         order: 5
IPSet:             order: 5
Reservation:       order: 5
DataPlaneService:  order: 5 (user-created only)
DataPlaneNodeSet:  order: 5
```

**Problem:** If all are order 5, they restore in parallel. This breaks dependencies:
- IPSets depend on NetConfig (network topology)
- Reservations depend on NetConfig
- NodeSets may depend on IPSets/Reservations

**Options:**

1. **Use sub-orders within order 5**
   - Not supported by current label-based approach
   - Would need controller to sequence

2. **Assign different orders to match dependencies**
   ```yaml
   NetConfig:         order: 5
   Reservation:       order: 6
   IPSet:             order: 7
   DataPlaneService:  order: 8
   DataPlaneNodeSet:  order: 9
   ```
   - Pros: Preserves current ordering, explicit dependencies
   - Cons: Uses more restore orders

3. **Accept parallel restore and rely on controller reconciliation**
   - Restore all at order 5 in parallel
   - Controllers handle missing dependencies via reconciliation
   - Pros: Simpler restore logic
   - Cons: May cause temporary errors, longer reconciliation time

**Recommendation:** Option 2 (different orders) - preserves proven restore sequence.

**Updated restore order table:**

| Order | Resources | Notes |
|-------|-----------|-------|
| 1 | NetworkAttachmentDefinitions, Secrets, ConfigMaps | Foundation |
| 2 | TLS Issuers | Requires CA secrets |
| 3 | MariaDBDatabase | Requires database password secrets |
| 4 | MariaDBAccount | Requires MariaDBDatabase |
| 5 | NetConfig, Topology, BGPConfiguration, DNSData, InstanceHa, OpenStackVersion | Infrastructure config |
| 6 | OpenStackControlPlane, Reservation | Reservation needs NetConfig |
| 7 | RabbitMQUser, RabbitMQVhost, IPSet | IPSet needs NetConfig |
| 8 | PVCs, DataPlaneService | User-created only |
| 9 | GaleraBackup, DataPlaneNodeSet | NodeSet needs IPSets/Reservations |
| 10 | Database Restore (Manual) | |
| 11 | RabbitMQ Credentials (Manual) | |
| 12 | Resume Deployment (Manual) | |

---

### 6. User-Provided vs Operator-Managed Resources

**Issue:** How to distinguish user-provided from operator-managed resources?

**Current Approach:**
```bash
# backup-restore-ctlplane.md - Manually remove ownerReferences
oc get secret -n $NAMESPACE -o json | \
  jq 'del(.items[].metadata.ownerReferences, ...)' > secrets-backup.json
```

**Webhook Design:**
```go
// Only label resources without ownerReferences
if len(obj.GetOwnerReferences()) > 0 {
    return nil  // Skip operator-managed resources
}
```

**Edge Cases:**

1. **RabbitMQUser**
   - User-created: No ownerReferences → Should be labeled and restored
   - Operator-created: Has ownerReferences → Should NOT be labeled/restored
   - Webhook approach: ✅ Works correctly

2. **Secrets**
   - User-provided (CA cert, passwords): No ownerReferences → Should be labeled
   - Operator-created (service secrets): Has ownerReferences → Should NOT be labeled
   - Webhook approach: ✅ Works correctly

3. **ConfigMaps**
   - User-provided (custom configs): No ownerReferences → Should be labeled
   - Operator-created (default configs): Has ownerReferences → Should NOT be labeled
   - Webhook approach: ✅ Works correctly

4. **DataPlaneService**
   - User-created custom services: No ownerReferences → Should be labeled
   - Operator default services: Has ownerReferences → Should NOT be labeled
   - Webhook approach: ✅ Works correctly

**Recommendation:** Webhook approach (check ownerReferences) is correct and compatible.

---

### 7. Secrets and ConfigMaps - Which to Backup?

**Issue:** Should we backup ALL secrets/configmaps or only user-provided?

**Current Approach:**
```bash
# backup-restore-ctlplane.md - Backs up ALL secrets (with ownerReferences removed)
oc get secret -n $NAMESPACE -o json | \
  jq 'del(.items[].metadata.ownerReferences, ...)' > secrets-backup.json
```

**Webhook Design:**
- Only labels user-provided secrets (no ownerReferences)
- Operator-created secrets are NOT labeled
- Operators recreate their secrets on reconciliation

**Concerns:**

1. **Operator-created secrets with important data**
   - Example: Database passwords generated by operator
   - If not backed up, will operator regenerate with SAME password?
   - Answer: Depends on operator implementation

2. **Secrets referenced by user resources**
   - User creates Secret with SSH key
   - DataPlaneNodeSet references it
   - Webhook labels the Secret ✅
   - Restore works ✅

3. **TLS certificates**
   - Cert-manager generates certificates
   - These have ownerReferences (cert-manager controller)
   - Webhook does NOT label them
   - Cert-manager regenerates on restore ✅

**Recommendation:** Webhook approach is correct. Operator-created secrets should be regenerated by operators on restore.

**Exception:** If operators generate random passwords and don't persist the seed, we need special handling. This is an operator bug, not a backup/restore issue.

---

### 8. Staged Deployment Annotation for ControlPlane Restore

**Issue:** How to restore OpenStackControlPlane without immediately starting all services?

**Problem with naive approach:**
```bash
# WRONG: Race condition
1. OADP restores OpenStackControlPlane (no annotation)
2. Operator sees CR, starts full deployment immediately
3. Services start before PVCs/database/RabbitMQ are ready → failures
```

**Solution:** Use OADP Resource Modifiers to add annotation during restore.

**OADP Resource Modifier approach:**

```yaml
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
  # Add staged deployment annotation via ConfigMap-based resource modifiers
  # The ConfigMap adds core.openstack.org/deployment-stage=infrastructure-only
  # to OpenStackControlPlane during restore
  # See: docs/dev/webhook/restore/00-resource-modifiers-configmap.yaml
  resourceModifier:
    kind: ConfigMap
    name: openstack-restore-resource-modifiers
```

**Restore flow:**

1. **Order 6:** OADP restores OpenStackControlPlane with annotation
   - Operator reconciles, sees `deployment-stage=infrastructure-only`
   - Creates infrastructure (MariaDB, RabbitMQ) but NOT services
   - No race condition ✓

2. **Wait for infrastructure ready:**
   ```bash
   oc wait --for=condition=OpenStackControlPlaneInfrastructureReady \
     openstackcontrolplane/<name> -n openstack --timeout=20m
   ```

3. **Continue with remaining restore orders** (7-9):
   - Order 7: RabbitMQUser, IPSet
   - Order 8: PVCs, DataPlaneService
   - Order 9: GaleraBackup, DataPlaneNodeSet

4. **Order 10-11: Manual steps** (database restore, RabbitMQ credentials)

5. **Order 12: Resume full deployment:**
   ```bash
   # Remove annotation to resume services
   oc annotate openstackcontrolplane <name> \
     -n openstack core.openstack.org/deployment-stage-

   # Wait for full deployment
   oc wait --for=condition=Ready \
     openstackcontrolplane/<name> -n openstack --timeout=30m
   ```

**Why this approach:**
- ✅ No race condition (annotation added atomically during restore)
- ✅ No risk to production cluster (backup is clean, no annotation)
- ✅ Safe for scheduled backups (backup script can't leave annotation behind)
- ✅ PVCs/database/RabbitMQ restored BEFORE services start

**Why staged deployment is critical:**
- PVCs must be restored from CSI snapshots BEFORE services start
- Database must be restored BEFORE services connect
- RabbitMQ credentials must be restored BEFORE services connect

**Compatibility:** This approach matches the current restore procedure and is documented in webhook design (orders 6 and 12).

---

### 9. DataPlane Resources in Webhook Design

**Issue:** DataPlane resources have specific restore order and exclusions.

**Current Approach:**
- Separate backup procedure (backup-restore-dataplane.md)
- Explicit ordering: NetConfig → Reservations → IPSets → Services → NodeSets
- DataPlaneDeployment backed up but NOT restored

**Webhook Design:**
- DataPlane resources included in full namespace backup
- Restore order defined by CRD annotations
- Need mechanism to exclude DataPlaneDeployment from restore

**Recommendations:**

1. **Merge DataPlane into unified backup/restore** (webhook approach)
   - Single OADP backup for entire namespace
   - Restore order includes DataPlane resources
   - Benefits: Simpler workflow, single backup artifact

2. **Add annotations to DataPlane CRDs**
   ```yaml
   # NetConfig
   annotations:
     openstack.org/backup-restore: "true"
     openstack.org/backup-category: "dataplane"
     openstack.org/backup-restore-order: "5"

   # Reservation
   annotations:
     openstack.org/backup-restore: "true"
     openstack.org/backup-category: "dataplane"
     openstack.org/backup-restore-order: "6"

   # IPSet
   annotations:
     openstack.org/backup-restore: "true"
     openstack.org/backup-category: "dataplane"
     openstack.org/backup-restore-order: "7"

   # OpenStackDataPlaneService
   annotations:
     openstack.org/backup-restore: "true"
     openstack.org/backup-category: "dataplane"
     openstack.org/backup-restore-order: "8"

   # OpenStackDataPlaneNodeSet
   annotations:
     openstack.org/backup-restore: "true"
     openstack.org/backup-category: "dataplane"
     openstack.org/backup-restore-order: "9"

   # OpenStackDataPlaneDeployment
   # NO annotations - backed up but NOT restored
   # Absence of backup-restore annotation means webhook doesn't label instances
   # Full namespace backup includes them, but OADP restore skips them
   ```

3. **Webhook handles ownerReferences removal**
   - Webhook labels resources without ownerReferences
   - No need to manually remove ownerReferences in jq
   - Operators re-establish ownership on reconciliation

---

### 10. Backup Categories - Useful or Complex?

**Issue:** Webhook design introduces backup categories (controlplane, dataplane, infrastructure, all). Are they useful?

**Use Cases:**

1. **Selective restore by category**
   ```yaml
   # Restore only controlplane resources
   labelSelector:
     matchLabels:
       openstack.org/backup-restore: "true"
       openstack.org/backup-category: "controlplane"
   ```

2. **Category-specific backup schedules**
   - Daily: controlplane + infrastructure
   - Weekly: dataplane + all
   - Hourly: PVCs only

3. **Disaster recovery scenarios**
   - Restore controlplane first, verify, then dataplane
   - Restore infrastructure only (network, messaging)

**Concerns:**

1. **Added complexity**
   - More labels to manage
   - More restore scenarios to test

2. **Unclear boundaries**
   - Where does "controlplane" end and "infrastructure" begin?
   - Is MariaDBDatabase "controlplane" or "infrastructure"?

3. **Not used in current procedures**
   - Current approach restores everything
   - No selective restore by category

**Recommendation:** Keep categories for Phase 4 (future flexibility), but make them optional in Phase 1-3.

**Simplified Phase 1-3 approach:**
- All resources get `openstack.org/backup-category: "all"`
- Restore by order only, ignore category
- Phase 4 can introduce category-based restore if needed

---

## Action Items

### Immediate (Phase 1)

1. **Resolve label naming conflict**
   - Decision: Rename to `openstack.org/backup-restore` or keep both?
   - Create migration script if renaming

2. **Define backup strategy**
   - Decision: Full namespace backup or selective backup?
   - Document exclusions if full namespace

3. **Refine restore order table**
   - Separate NetConfig (5), Reservation (6), IPSet (7), etc.
   - Update webhook design document

4. **Document PVC labeling approach**
   - Hybrid: Operators add annotation, webhook adds labels
   - Update operator documentation

### Phase 2

6. **Update Ansible playbook to use OADP backup**
   - Replace `oc get` + jq with OADP Backup CR
   - Keep restore procedure unchanged

### Phase 3

7. **Update Ansible playbook to use OADP restore**
   - Replace `oc apply` with OADP Restore CRs by order
   - Keep manual steps (DB restore, RabbitMQ, staged deployment)

### Phase 4

8. **Implement OpenStackBackupRestore controller**
   - Automate restore sequence
   - Handle special cases (DB, RabbitMQ, staged deployment)

9. **Implement OpenStackBackupConfig CRD**
   - Configure restore order defaults
   - Support category-based restore

---

## Conclusion

The webhook design is **largely compatible** with the current backup/restore procedures, but requires:

1. **Label naming standardization** - Resolve `openstack.org/backup` vs `openstack.org/backup-restore`
2. **Backup strategy decision** - Full namespace vs selective backup
3. **Exclusion mechanism** - Handle resources that backup but don't restore
4. **Restore order refinement** - More granular ordering for dependencies
5. **PVC labeling approach** - Hybrid annotation→label conversion

The migration path through 4 phases provides a gradual transition from the current Ansible-based approach to the webhook-based approach, with validation at each step.

**Next Steps:**
1. Review and discuss this analysis
2. Make decisions on open questions
3. Update webhook design based on decisions
4. Begin Phase 1 implementation (webhook + CRD annotations)
