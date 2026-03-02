# Backup/Restore CRD Design

**Status:** Draft / Design Discussion

## Overview

Design for implementing CRD-based backup/restore automation for OpenStack control plane and data plane, eliminating the need for external client systems.

## Goals

1. **In-cluster execution** - No external client needed to run backups/restores
2. **Consistent patterns** - Reuse existing GaleraBackup/GaleraRestore and ansible-runner approaches
3. **Flexibility** - Allow playbook override via secrets for bugs or environment-specific needs
4. **Schedulable** - Support automated, scheduled backups
5. **Observable** - CRD status tracks backup/restore progress

## Current State

**Backup/Restore today:**
- Requires external system with ansible, oc CLI, jq
- Manual execution via playbooks
- External scheduling (cron, Tower, etc.)
- Documentation in `docs/dev/backup-restore-ctlplane.md` and `docs/dev/backup-restore-dataplane.md`
- Playbooks in `docs/dev/playbooks/`

**Existing patterns to leverage:**
- **GaleraBackup/GaleraRestore** (mariadb-operator) - CRD-based database backup/restore
- **OpenStackDataPlaneDeployment** - Uses ansible-runner to execute playbooks in-cluster
- **Playbook override via Secret** - EDPM allows custom playbooks for environment-specific needs

## Proposed Architecture

### CRDs

Four new CRDs in openstack-operator:

```yaml
# 1. OpenStackControlPlaneBackup
apiVersion: infra.openstack.org/v1beta1
kind: OpenStackControlPlaneBackup
metadata:
  name: ctlplane-backup-daily
  namespace: openstack
spec:
  # Target namespace to backup
  namespace: openstack

  # Optional: cron schedule for automatic backups
  schedule: "0 2 * * *"

  # Optional: override default backup playbook via secret
  # Allows fixing bugs or environment-specific customization
  playbookOverride: my-custom-backup-playbook

  # OADP integration settings
  oadp:
    enabled: true
    namespace: openshift-adp
    snapshotMoveData: true  # Copy snapshot data to S3 (default: true)
    ttl: 720h  # Backup retention (default: 30 days)

  # Backup storage location
  storage:
    pvc: openstack-backup-storage  # PVC to store backup archives

status:
  phase: Completed  # Pending, Running, Completed, Failed
  backupName: openstack-ctlplane-backup-20260302-020000
  startTimestamp: "2026-03-02T02:00:00Z"
  completionTimestamp: "2026-03-02T02:05:00Z"
  backupLocation: /backup/openstack-ctlplane-backup-20260302-020000.tar.gz
  oadpBackupName: openstack-volumes-20260302-020000
  conditions:
    - type: Ready
      status: "True"
      reason: BackupCompleted
      message: "Backup completed successfully"

---
# 2. OpenStackDataPlaneBackup
apiVersion: infra.openstack.org/v1beta1
kind: OpenStackDataPlaneBackup
metadata:
  name: dataplane-backup-daily
  namespace: openstack
spec:
  namespace: openstack
  schedule: "0 3 * * *"
  playbookOverride: my-custom-dataplane-backup
  oadp:
    enabled: true
    namespace: openshift-adp
    snapshotMoveData: true
    ttl: 720h
  storage:
    pvc: openstack-backup-storage

status:
  phase: Completed
  backupName: openstack-dataplane-backup-20260302-030000
  startTimestamp: "2026-03-02T03:00:00Z"
  completionTimestamp: "2026-03-02T03:02:00Z"
  backupLocation: /backup/openstack-dataplane-backup-20260302-030000.tar.gz

---
# 3. OpenStackControlPlaneRestore
apiVersion: infra.openstack.org/v1beta1
kind: OpenStackControlPlaneRestore
metadata:
  name: ctlplane-restore
  namespace: openstack
spec:
  # Backup to restore from
  backupName: openstack-ctlplane-backup-20260302-020000

  # Or reference a backup CR
  backupRef:
    name: ctlplane-backup-daily

  # Optional: override default restore playbook
  playbookOverride: my-custom-restore-playbook

  # OADP restore settings
  oadp:
    enabled: true
    namespace: openshift-adp

  # Storage location where backup is stored
  storage:
    pvc: openstack-backup-storage

status:
  phase: InProgress  # Pending, InProgress, Completed, Failed
  stage: RestoringDatabase  # RestoringCRs, RestoringPVCs, RestoringDatabase, ResumingDeployment
  message: "Restoring Galera database from dumps..."
  startTimestamp: "2026-03-02T08:00:00Z"
  conditions:
    - type: InfrastructureReady
      status: "True"
      reason: InfrastructureDeployed
    - type: PVCsRestored
      status: "True"
      reason: OADPRestoreCompleted
    - type: DatabaseRestored
      status: "False"
      reason: InProgress

---
# 4. OpenStackDataPlaneRestore
apiVersion: infra.openstack.org/v1beta1
kind: OpenStackDataPlaneRestore
metadata:
  name: dataplane-restore
  namespace: openstack
spec:
  backupName: openstack-dataplane-backup-20260302-030000
  playbookOverride: my-custom-dataplane-restore
  storage:
    pvc: openstack-backup-storage

status:
  phase: Completed
  startTimestamp: "2026-03-02T08:30:00Z"
  completionTimestamp: "2026-03-02T08:32:00Z"
```

### Controller Implementation

**Location:** openstack-operator

**Two Controllers:**

1. **BackupController** - Handles backup operations
   - Watches: `OpenStackControlPlaneBackup` and `OpenStackDataPlaneBackup` CRs
   - Responsibilities:
     - Create/manage backup storage PVC
     - Create ansible-runner Job with backup playbook
     - Trigger OADP backup
     - Update backup CR status
     - Manage CronJobs for scheduled backups

2. **RestoreController** - Handles restore operations
   - Watches: `OpenStackControlPlaneRestore` and `OpenStackDataPlaneRestore` CRs
   - Responsibilities:
     - Validate backup exists and is compatible
     - Create ansible-runner Job with restore playbook
     - Monitor multi-stage restore progress
     - Update restore CR status
     - Trigger post-restore validation (optional)

**Execution Model:** Ansible Runner (like OpenStackDataPlaneDeployment)

Each controller:
1. Watches its CRD
2. Creates a Job with ansible-runner container
3. Job executes the corresponding playbook:
   - `docs/dev/playbooks/backup-openstack-ctlplane.yaml`
   - `docs/dev/playbooks/backup-openstack-dataplane.yaml`
   - `docs/dev/playbooks/restore-openstack-ctlplane.yaml`
   - `docs/dev/playbooks/restore-openstack-dataplane.yaml`
4. Updates CR status based on Job progress
5. Handles cleanup (Job retention, old backup deletion based on retention policy)

### Playbook Override Mechanism

Like EDPM, allow users to override playbooks via Secret reference:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-custom-backup-playbook
  namespace: openstack
type: Opaque
data:
  backup-playbook.yaml: <base64-encoded playbook>
```

**Benefits:**
- Fix bugs without waiting for operator release
- Customize for environment-specific requirements
- Test playbook changes before upstreaming
- Handle edge cases that don't fit default playbook

**Controller behavior:**
- If `playbookOverride` is set, mount Secret and use custom playbook
- Otherwise, use embedded default playbook

### Scheduling

**Option 1: Controller-managed CronJobs**
- Controller creates CronJob based on `schedule` field
- CronJob creates backup CR instances
- Simple, leverages Kubernetes native scheduling

**Option 2: Controller-managed scheduling**
- Controller implements scheduling logic internally
- Creates backup CR instances on schedule
- More control, but reinvents Kubernetes scheduling

**Recommendation:** Option 1 (CronJob-based)

### Backup Storage

**Primary approach:** PVC-based storage (ties into "Backup Storage on PVC with OADP Integration" enhancement)

```yaml
spec:
  storage:
    pvc: openstack-backup-storage
    size: 10Gi  # Optional, default: 10Gi
    storageClass: lvms-vg1  # Optional, uses default if not specified
```

**PVC Management:**
- **BackupController creates PVC** if it doesn't exist
- PVC labeled with `openstack.org/backup=true` (included in OADP backup)
- Access mode: `ReadWriteMany` (for concurrent backup jobs)
- Controller mounts PVC into ansible-runner Job
- Backup archives written to `/backup/`

**PVC lifecycle:**
- Created on first backup
- Persists across backups (archives overwritten)
- User can pre-create PVC with specific settings if needed

**Future:** Could support external storage (S3, NFS) directly:

```yaml
spec:
  storage:
    s3:
      bucket: my-openstack-backups
      endpoint: s3.amazonaws.com
      credentialsSecret: s3-creds
```

## Related Components

These backup/restore capabilities complement but remain separate from:
- **GaleraBackup/GaleraRestore** - In mariadb-operator, database-specific dumps
- **OVNBackup/OVNRestore** - To be implemented, OVN database-specific
- **test-operator** - Used for post-restore validation (Tempest tests)

The generic backup/restore controller in openstack-operator orchestrates the full backup/restore workflow using playbooks, while these components handle specific subsystems.

## User Workflow

### Backup Workflow

**One-time backup:**
```bash
# Create backup CR
cat <<EOF | oc apply -f -
apiVersion: infra.openstack.org/v1beta1
kind: OpenStackControlPlaneBackup
metadata:
  name: ctlplane-backup-manual
  namespace: openstack
spec:
  namespace: openstack
  oadp:
    enabled: true
    namespace: openshift-adp
  storage:
    pvc: openstack-backup-storage
EOF

# Watch progress
oc get openstackcontrolplanebackup ctlplane-backup-manual -w

# Check status
oc describe openstackcontrolplanebackup ctlplane-backup-manual
```

**Scheduled backup:**
```bash
# Create backup CR with schedule
cat <<EOF | oc apply -f -
apiVersion: infra.openstack.org/v1beta1
kind: OpenStackControlPlaneBackup
metadata:
  name: ctlplane-backup-daily
  namespace: openstack
spec:
  namespace: openstack
  schedule: "0 2 * * *"  # Daily at 2 AM
  oadp:
    enabled: true
    namespace: openshift-adp
    snapshotMoveData: true
    ttl: 720h  # 30 days
  storage:
    pvc: openstack-backup-storage
EOF

# Controller creates CronJob, which creates backup CR instances daily
```

### Restore Workflow

```bash
# List available backups
oc get openstackcontrolplanebackup -o custom-columns=NAME:.metadata.name,STATUS:.status.phase,BACKUP:.status.backupName,TIME:.status.completionTimestamp

# Create restore CR
cat <<EOF | oc apply -f -
apiVersion: infra.openstack.org/v1beta1
kind: OpenStackControlPlaneRestore
metadata:
  name: ctlplane-restore
  namespace: openstack
spec:
  backupName: openstack-ctlplane-backup-20260302-020000
  oadp:
    enabled: true
    namespace: openshift-adp
  storage:
    pvc: openstack-backup-storage
EOF

# Watch progress
oc get openstackcontrolplanerestore ctlplane-restore -w

# Check detailed status
oc describe openstackcontrolplanerestore ctlplane-restore
```

## Status Tracking

The Restore CRs need detailed status tracking due to multi-stage restore:

```yaml
status:
  phase: InProgress  # High-level phase
  stage: RestoringDatabase  # Current stage in restore process
  conditions:
    - type: InfrastructureReady
      status: "True"
      reason: InfrastructureDeployed
      message: "Infrastructure stage completed"
    - type: PVCsRestored
      status: "True"
      reason: OADPRestoreCompleted
      message: "Storage volumes restored from OADP"
    - type: DatabaseRestored
      status: "False"
      reason: InProgress
      message: "Restoring Galera database from dumps..."
    - type: DeploymentResumed
      status: "False"
      reason: Pending
```

## Integration with Existing Components

### GaleraBackup Integration

Control plane backup should trigger Galera database dumps:

```yaml
# In backup playbook execution:
# 1. Trigger Galera backup jobs (existing procedure)
# 2. Wait for completion
# 3. OADP backup includes Galera backup PVCs
```

This is already in the playbook, just needs to be executed by the controller.

### OADP Integration

Backup/Restore CRs manage OADP Backup/Restore CRs:

```yaml
# Controller creates Velero Backup CR
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: openstack-volumes-20260302-020000
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
```

Controller tracks OADP backup/restore status and reflects in CR status.

## Decisions

### 1. Operator location: **openstack-operator**

**Rationale:**
- ✅ Natural pairing with top-level CRDs (OpenStackControlPlane, OpenStackDataPlaneNodeSet)
- ✅ Avoids import loop issues (infra-operator already imported by openstack-operator)
- ✅ Enables tight integration with main resource controllers
- ✅ Unified lifecycle management (deploy, backup, restore)

**Note:** GaleraBackup/GaleraRestore remain in mariadb-operator (database-specific, already implemented).

### 2. Playbook versioning: **Embedded in operator**

**Approach:**
- Default playbooks embedded in openstack-operator
- Playbook version tied to operator version
- Custom playbooks (via `playbookOverride`) are user responsibility to maintain

**Implementation:**
- Playbooks in `docs/dev/playbooks/` are embedded into operator container image
- Controller uses embedded playbooks by default
- When operator is upgraded, new playbook version is used automatically
- Custom playbooks must be updated manually by user when upgrading operator

**Documentation requirements:**
- Playbook changes **MUST** be thoroughly documented in release notes
- Breaking changes in playbooks must be highlighted
- Migration guide for custom playbook users
- Version compatibility matrix if needed

**Example release note:**
```
## Backup/Restore Playbook Changes

### Breaking Changes
- Step 12: Database restore now requires CSI snapshots (was Restic)
- Custom playbooks must update OADP restore spec

### New Features
- Step 9: Added automatic Galera backup timestamp passing

### Migration Guide for Custom Playbooks
If using `playbookOverride`:
1. Update OADP restore spec: change `defaultVolumesToRestic` to `snapshotVolumes`
2. Add `BACKUP_TIMESTAMP` env var to Galera backup job creation
```

### 3. Backup retention: **Tiered backup strategy with snapshots + S3**

**Approach:**
- Support multiple backup CRs for tiered strategies
- Default: `snapshotMoveData: true` (external backup)
- Allow: `snapshotMoveData: false` for local-only snapshots
- OADP TTL controls retention per backup CR

**Recommended Pattern: Tiered Backup**

Combine local snapshots (fast, frequent) with external backups (slow, less frequent):

```yaml
# Tier 1: Hourly local snapshots (fast recovery, short retention)
apiVersion: infra.openstack.org/v1beta1
kind: OpenStackControlPlaneBackup
metadata:
  name: ctlplane-backup-hourly
  namespace: openstack
spec:
  namespace: openstack
  schedule: "5 * * * *"  # Every hour at :05 (staggered from daily)
  oadp:
    enabled: true
    snapshotMoveData: false  # Local snapshots only (fast)
    ttl: 24h  # Keep 24 hourly snapshots
  storage:
    pvc: openstack-backup-storage

---
# Tier 2: Daily external backups (disaster recovery, long retention)
apiVersion: infra.openstack.org/v1beta1
kind: OpenStackControlPlaneBackup
metadata:
  name: ctlplane-backup-daily
  namespace: openstack
spec:
  namespace: openstack
  schedule: "0 2 * * *"  # Daily at 2:00 AM
  oadp:
    enabled: true
    snapshotMoveData: true  # Copy to S3 (disaster recovery)
    ttl: 720h  # Keep 30 daily backups
  storage:
    pvc: openstack-backup-storage
```

**Result:**
- **24 hourly snapshots** (local storage, fast recovery within 1 day)
- **30 daily backups** in S3 (survives cluster failure, 30-day history)
- **Low overhead**: Hourly is snapshot-only (no S3 copy)
- **RTO**: Minutes for hourly restore, longer for daily S3 restore

**Schedule Overlap Consideration:**

If schedules overlap (e.g., both at 2:00 AM), both backups create snapshots of the same data:
- Hourly: Creates local snapshot
- Daily: Creates snapshot + S3 copy
- Result: **Two snapshots at 2 AM** (duplicate, but harmless)

**Recommendations:**
1. **Stagger schedules** (recommended): Hourly at `:05`, daily at `2:00`
2. **Accept overlap** (simplest): Minimal storage overhead due to COW snapshots
3. **Skip hourly during daily**: Complex cron (`0 0,1,3-23 * * *`)

**Single-Tier Options:**

**Production (external backup only):**
```yaml
spec:
  schedule: "0 2 * * *"
  oadp:
    snapshotMoveData: true  # Default
    ttl: 720h
```

**Dev/Test (local snapshots only):**
```yaml
spec:
  schedule: "0 2 * * *"
  oadp:
    snapshotMoveData: false  # Explicit opt-in
    ttl: 168h  # 7 days
```

**Benefits:**
- ✅ Flexible tiered strategies
- ✅ Production-safe defaults (external backup)
- ✅ Fast local recovery option
- ✅ OADP handles all retention
- ✅ PVC efficient (only latest archive)

**Note:** Remove `retentionPolicy` field from CRD spec (OADP TTL handles this).

## Open Questions

### 4. Partial restore: **Not supported - consistency is critical**

**Decision:** No partial restore support in CRDs.

**Rationale:**
- ControlPlane/DataPlane must be restored as complete units for consistency
- CRs, Secrets, ConfigMaps, Database, PVCs are interdependent
- Partial restore breaks references and state consistency
- Example: Secrets without CRs = broken references
- Example: Database without secrets = authentication fails

**Granularity already provided:**
- OpenStackControlPlaneBackup/Restore - complete control plane
- OpenStackDataPlaneBackup/Restore - complete data plane
- GaleraBackup/Restore - database only
- (future) OVNBackup/Restore - OVN database only

**Manual partial restore:**
- If needed, users can manually extract from backup archive
- Apply specific resources manually
- User responsibility to ensure consistency

### 5. Backup and restore validation

**Two levels of validation:**

#### 5a. Backup validation (pre-restore)

**Challenge:** Hard to validate backup is restorable without actually restoring.

**Possible basic checks:**
- ✅ Backup archive exists and is readable
- ✅ Archive contains expected files (CRs, secrets, etc.)
- ✅ JSON files are valid JSON
- ✅ OADP backup CR exists and status is Completed
- ❌ Cannot verify database dumps are valid without restore
- ❌ Cannot verify PVC snapshots are valid without restore

**Decision:** Implement basic sanity checks (archive integrity), but full validation requires restore.

#### 5b. Restore validation (post-restore)

**Recommended:** Validate restored cloud is functional.

**Approach: Use test-operator to run Tempest tests**

```yaml
apiVersion: infra.openstack.org/v1beta1
kind: OpenStackControlPlaneRestore
metadata:
  name: ctlplane-restore
spec:
  backupName: openstack-ctlplane-backup-20260302

  # Optional: Run validation after restore
  validation:
    enabled: true
    testOperator:
      # Run basic Tempest smoke tests
      tempest:
        tests: smoke
        # Create temporary project for testing
        projectCleanup: true
```

**Validation workflow:**
1. Restore completes (deployment resumed, services ready)
2. Controller triggers test-operator Tempest CR
3. Tempest creates temporary project
4. Runs basic smoke tests (auth, compute, network, storage)
5. Cleanup temporary project
6. Update restore status with validation results

**Benefits:**
- ✅ Verifies restored cloud is actually functional
- ✅ Tests critical services (Keystone, Nova, Neutron, Glance)
- ✅ Automated validation in restore workflow
- ✅ Leverages existing test-operator
- ✅ Clean separation (test project deleted after)

**Status tracking:**
```yaml
status:
  phase: Completed
  validation:
    phase: Passed  # or Failed
    tempestResults: tempest-restore-validation-xyz
    message: "All smoke tests passed"
```

**Future enhancement:** Support custom validation workflows (user-provided tests).

### 6. Controller architecture: **Two controllers (Backup and Restore)**

**Decision:** Two controllers - BackupController and RestoreController.

**Rationale:**

**Different responsibilities:**
- **Backup:** Resource creation (PVCs, Jobs, OADP backups, CronJobs)
- **Restore:** Resource consumption, multi-stage orchestration, validation
- Different reconciliation patterns justify separate controllers

**BackupController responsibilities (minimal Go logic):**
- Create/manage backup storage PVC (if doesn't exist)
- Watch ControlPlane and DataPlane Backup CRs
- Create ansible-runner Job with backup playbook
- Trigger OADP backup via playbook
- Create CronJob for scheduled backups
- Update backup CR status

**RestoreController responsibilities (minimal Go logic):**
- Watch ControlPlane and DataPlane Restore CRs
- Validate backup compatibility (operator versions)
- Create ansible-runner Job with restore playbook
- Monitor multi-stage restore (via playbook status)
- Update restore CR status with stage tracking

**Key operational requirement: Customizability**
- If customer hits bug in production → needs immediate fix without operator release
- Playbook override allows emergency fixes, skipping broken steps, environment-specific logic
- Same pattern as EDPM (proven in production)

**Playbook responsibility (maximum logic, customizable):**
- All backup/restore steps
- OADP Backup/Restore CR creation (`oc apply`)
- Waiting for infrastructure-only stage (`oc wait`)
- Database restore
- Deployment resumption (`oc annotate`)
- Validation (trigger Tempest via `oc apply`)

**Controller pseudo-code:**
```go
// BackupController
func (r *BackupController) Reconcile(backup *Backup) {
  // Ensure PVC exists
  ensurePVCExists(backup.Spec.Storage.PVC)

  // Determine playbook based on CR kind
  playbook := getBackupPlaybook(backup.Kind)
  // backup-openstack-ctlplane.yaml
  // backup-openstack-dataplane.yaml

  // Create ansible-runner Job
  job := createAnsibleRunnerJob(playbook, backup.Spec)

  // Update status
  updateStatusFromJob(backup, job)

  // Create CronJob if schedule specified
  if backup.Spec.Schedule != "" {
    ensureCronJobExists(backup)
  }
}

// RestoreController
func (r *RestoreController) Reconcile(restore *Restore) {
  // Validate backup
  if err := validateBackupCompatibility(restore); err != nil {
    return err
  }

  // Determine playbook
  playbook := getRestorePlaybook(restore.Kind)
  // restore-openstack-ctlplane.yaml
  // restore-openstack-dataplane.yaml

  // Create ansible-runner Job
  job := createAnsibleRunnerJob(playbook, restore.Spec)

  // Update status with stage tracking
  updateStatusFromJob(restore, job)
}
```

**Benefits:**
- ✅ Clear separation: backup creates, restore consumes
- ✅ BackupController manages PVC lifecycle
- ✅ Different reconciliation logic per controller
- ✅ Maximum flexibility via playbook override
- ✅ Customer can fix bugs immediately
- ✅ Each controller handles both ControlPlane and DataPlane (similar logic)

### 7. OADP backup strategy: **One atomic backup for all PVCs**

**Decision:** Single OADP backup captures all PVCs at same point-in-time.

**What gets backed up in one OADP backup:**
- Archive PVC (control plane backup archive + metadata)
- Galera backup PVCs (database dumps)
- Glance PVCs (images)
- Cinder PVCs (volumes)
- Swift PVCs (objects)
- Manila PVCs (shares)
- All PVCs labeled `openstack.org/backup=true`

**Benefits:**
- ✅ Consistent point-in-time snapshot of all data
- ✅ Single atomic operation
- ✅ Simpler workflow (one backup to track)
- ✅ Easier restore (one OADP restore CR)
- ✅ Fewer failure modes

**Selective restore capability:**

Even though it's one backup, you can restore selectively.

**Example 1: Restore only archive PVC (for inspection)**
```yaml
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: restore-archive-inspection
  namespace: openshift-adp
spec:
  backupName: openstack-volumes-20260302-020000
  includedNamespaces:
  - openstack-backup-inspection  # Temporary namespace
  namespaceMapping:
    openstack: openstack-backup-inspection
  labelSelector:
    matchLabels:
      app: backup-storage  # Only the archive PVC
```

**Example 2: Inspect archive in temporary namespace**
```bash
# 1. Create temporary namespace
oc create namespace openstack-backup-inspection

# 2. Restore archive PVC to temp namespace (see above)

# 3. Create helper pod to inspect
oc run -n openstack-backup-inspection archive-inspector \
  --image=registry.redhat.io/ubi9/ubi:latest \
  --command -- sleep infinity

oc set volume -n openstack-backup-inspection \
  deployment/archive-inspector \
  --add --mount-path=/backup \
  --name=backup-pvc \
  --claim-name=openstack-backup-storage

# 4. Inspect archive
oc exec -n openstack-backup-inspection archive-inspector -- \
  tar -tzf /backup/openstack-ctlplane-backup-20260302.tar.gz

oc exec -n openstack-backup-inspection archive-inspector -- \
  cat /backup/openstack-ctlplane-backup-20260302/operator-versions.txt

# 5. If good, cleanup temp and do real restore
oc delete namespace openstack-backup-inspection

# 6. Full restore to production namespace
# (see disaster recovery workflow below)
```

**Full restore (all PVCs):**
```yaml
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: restore-all-volumes
  namespace: openshift-adp
spec:
  backupName: openstack-volumes-20260302-020000
  includedNamespaces:
  - openstack
  # No label selector = restore all PVCs from backup
```

### 8. Backup metadata and disaster recovery workflow

**Decision:** No dedicated metadata PVC needed. Use OADP backup list + backup archive contents.

**Disaster recovery workflow (fresh cluster):**

```bash
# 1. Fresh cluster with OADP configured (same S3 backend)
oc get backup -n openshift-adp
# Velero syncs from S3, shows all available backups:
# NAME                                   CREATED               AGE
# openstack-volumes-20260301-020000      2026-03-01 02:00:00   2d
# openstack-volumes-20260302-020000      2026-03-02 02:00:00   1d

# 2. Restore OADP backup (includes PVCs with backup archives)
oc apply -f - <<EOF
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: restore-volumes-20260302
  namespace: openshift-adp
spec:
  backupName: openstack-volumes-20260302-020000
  includedNamespaces:
  - openstack
EOF

# 3. Wait for PVCs to be restored (with CSI snapshots)
oc get pvc -n openstack -l openstack.org/backup=true

# 4. Create restore CR
oc apply -f - <<EOF
apiVersion: infra.openstack.org/v1beta1
kind: OpenStackControlPlaneRestore
metadata:
  name: restore-20260302
  namespace: openstack
spec:
  backupName: openstack-ctlplane-backup-20260302-020000
  storage:
    pvc: openstack-backup-storage  # Already restored
  oadp:
    enabled: true
    namespace: openshift-adp
EOF

# 5. Controller reads metadata from backup archive
#    - Mounts PVC
#    - Reads operator-versions.txt from archive
#    - Validates operator versions match current cluster
#    - Proceeds with restore playbook
```

**Metadata storage:**
- Backup archive contains `operator-versions.txt` (OCP version, operator versions, storage classes)
- Archive contains `README.md` with backup metadata
- OADP Backup CR name includes timestamp (user can identify backups)
- No separate metadata PVC needed

**Operator version validation:**
Controller reads `operator-versions.txt` from restored archive and validates:
- ✅ Operator versions match current cluster
- ❌ Fail if mismatch (with clear error message)
- User must install matching operator versions before restore

### 8. Cross-namespace restore (DEFERRED - design to not block)

**Question:** Should we support restoring to a different namespace?

**Design considerations:**
- Ensure CRD design doesn't block this capability
- Namespace references in backup files may need transformation
- Secret/ConfigMap references may need namespace updates
- Could add `spec.targetNamespace` field later if needed

**Deferred because:**
- Initial use case is same-namespace restore
- Can be added later without breaking changes if designed carefully
- Need to ensure backup format supports it (namespace-agnostic where possible)

## Benefits

1. **No external client** - Runs entirely in-cluster
2. **Consistent with existing patterns** - GaleraBackup, OpenStackDataPlaneDeployment
3. **Flexible** - Playbook override for customization
4. **Kubernetes-native** - CRDs, Jobs, RBAC, status tracking
5. **Observable** - Status subresource shows progress
6. **Schedulable** - Built-in scheduling support
7. **Extensible** - Can add new backup types (OVN, etc.)

## Risks and Mitigations

1. **Risk:** Playbook override could introduce errors
   - **Mitigation:** Validate playbook syntax before execution, provide clear error messages

2. **Risk:** Backup job failure leaves cluster in unknown state
   - **Mitigation:** Transactional approach, status tracking, rollback capability

3. **Risk:** Retention policy deletes backups too aggressively
   - **Mitigation:** Clear defaults, user confirmation for manual deletes

4. **Risk:** Controller complexity (managing Jobs, CronJobs, OADP CRs)
   - **Mitigation:** Start simple, iterate based on feedback

## Implementation Plan

**Phase 1: Basic backup CRs**
- Implement OpenStackControlPlaneBackup controller
- Execute existing backup playbook via ansible-runner
- Status tracking
- No scheduling yet (manual trigger only)

**Phase 2: Restore CRs**
- Implement OpenStackControlPlaneRestore controller
- Execute existing restore playbook
- Multi-stage status tracking

**Phase 3: Scheduling**
- Add schedule support (CronJob-based)
- Configure OADP TTL from CR spec
- Controller-managed CronJob lifecycle

**Phase 4: DataPlane backup/restore**
- Implement OpenStackDataPlaneBackup/Restore controllers
- Follow same pattern as ControlPlane

**Phase 5: Advanced features**
- Playbook override support
- Backup validation
- Cross-namespace restore
- S3 storage backend

## References

- Existing playbooks: `docs/dev/playbooks/backup-openstack-ctlplane.yaml`
- GaleraBackup CRD: `mariadb-operator` repository
- OpenStackDataPlaneDeployment: `dataplane-operator` repository
- OADP Backup/Restore: Velero CRDs

## Open Implementation Questions

1. Should playbook override be in Phase 1 or later phases?
2. How to handle ansible-runner Job cleanup (retention of completed jobs)?
3. Status update frequency during long-running operations?
4. Error handling strategy: retries, backoff, manual intervention?
5. RBAC model: cluster-admin or limited permissions?
6. Should we support backing up to multiple OADP backends simultaneously?
