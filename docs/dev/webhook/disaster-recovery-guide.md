# Disaster Recovery Guide for OpenStack on Kubernetes

## Overview

This guide covers disaster recovery (DR) scenarios where an OpenStack deployment needs to be restored to a **new cluster** due to catastrophic failure of the original cluster. This is different from in-cluster backup/restore where the cluster infrastructure remains intact.

⚠️ **CRITICAL WARNING - Local Storage Users**:

If your cluster uses **local storage** (LVM, LocalPV, HostPath), the current POC backup approach using CSI volume snapshots is **NOT A BACKUP** for disaster recovery purposes. CSI snapshots with local storage are stored on the same nodes/disks as your data and will be lost if the cluster is lost.

**For local storage, you MUST use Restic/Kopia** (documented in this guide) to have any disaster recovery capability. CSI snapshots with local storage only provide protection against application errors, NOT against hardware or cluster failures.

See the [Understanding Backup Approaches](#understanding-backup-approaches) section below for detailed explanation.

## DR vs In-Cluster Restore

| Aspect | In-Cluster Restore | Disaster Recovery |
|--------|-------------------|-------------------|
| **Scenario** | Fix deployment issues, rollback changes | Total cluster loss, datacenter failure |
| **Target** | Same cluster | Different cluster |
| **Storage** | CSI volume snapshots (local to cluster) | Object storage (external/replicated) |
| **Speed** | Fast (snapshots) | Slower (data transfer from object storage) |
| **Complexity** | Lower | Higher (mapping, reconfiguration) |
| **Data Location** | Same storage backend | External S3-compatible storage |

## Understanding Backup Approaches

### Determining Your Storage Type

Before choosing a backup strategy, identify whether you're using local or external storage:

**Check your storage classes:**
```bash
# List storage classes
oc get storageclass

# Check provisioner for a specific storage class
oc get storageclass <storage-class-name> -o jsonpath='{.provisioner}'
```

**Local Storage Provisioners (⚠️ CSI snapshots NOT sufficient for DR):**
- `kubernetes.io/no-provisioner` (static LocalPV)
- `topolvm.io` (TopoLVM - local LVM)
- `local.storage.openshift.io` (OpenShift local storage)
- `rancher.io/local-path` (Rancher local path provisioner)

**External Storage Provisioners (⚠️ CSI snapshots provide limited DR):**
- `rbd.csi.ceph.com` (Ceph RBD - ODF, Rook)
- `cephfs.csi.ceph.com` (CephFS)
- `ebs.csi.aws.com` (AWS EBS)
- `disk.csi.azure.com` (Azure Disk)
- `csi.trident.netapp.io` (NetApp Trident)

**Check your PVCs:**
```bash
# See what storage class your PVCs use
oc get pvc -n openstack -o custom-columns=NAME:.metadata.name,STORAGECLASS:.spec.storageClassName

# Check a specific PVC's volume
oc get pvc glance -n openstack -o yaml | grep -E "storageClassName|volumeName"

# Get details about the PV
oc get pv <pv-name> -o yaml | grep -E "csi|local"
```

**Example - Local LVM (TopoLVM):**
```bash
$ oc get sc topolvm-provisioner -o jsonpath='{.provisioner}'
topolvm.io

# This is LOCAL storage - CSI snapshots are NOT backups!
# You MUST use Restic/Kopia for DR
```

**Example - Ceph RBD (External):**
```bash
$ oc get sc ocs-storagecluster-ceph-rbd -o jsonpath='{.provisioner}'
rbd.csi.ceph.com

# This is EXTERNAL storage - CSI snapshots can work IF Ceph survives
# Recommended: Use Restic/Kopia for true DR
```

### CSI Volume Snapshots (Current POC)

The current POC implementation uses CSI volume snapshots:

**How CSI Snapshots Work with Velero:**
- Velero stores VolumeSnapshot **metadata** (YAML manifests) in S3 object storage
- The actual snapshot **data** remains in the storage backend (Ceph, AWS EBS, LVM, etc.)
- During restore, Velero creates VolumeSnapshot objects which reference the snapshots in the storage backend

**What Actually Gets Backed Up:**

| What's Backed Up | Location | Survives Cluster Loss? |
|-----------------|----------|----------------------|
| VolumeSnapshot metadata (YAML) | S3 object storage | ✅ Yes (but useless without snapshot data) |
| Actual snapshot data (PVC contents) | Storage backend (same as cluster) | ❌ **NO** |

**CRITICAL LIMITATION - Local Storage (LVM, HostPath, Local):**

⚠️ **WARNING**: If using local storage (LVM, LocalPV, HostPath), CSI snapshots are **NOT A BACKUP AT ALL** for disaster recovery.

**What happens with local storage:**
1. CSI driver creates snapshots on the **same local storage** (same node/disk)
2. Velero uploads only the VolumeSnapshot metadata (YAML) to S3
3. **NO ACTUAL DATA** is uploaded to S3
4. If node/disk/cluster is lost → Snapshots are lost
5. The VolumeSnapshot metadata in S3 references snapshot IDs that no longer exist
6. **Result**: Complete data loss, zero recovery capability

**Example - Local LVM:**
```
Node worker-0:
├── LVM Volume Group: vg-data
│   ├── PVC: glance-images (10GB)
│   └── LVM Snapshot: glance-images-snap (references same VG)
└── [Node lost] → Both PVC and snapshot are GONE

S3 Object Storage:
└── VolumeSnapshot YAML: {"snapshotHandle": "snapshot-12345"}
    └── This ID is now meaningless - the snapshot doesn't exist anymore
```

**Verdict for Local Storage**: CSI snapshots are only useful for:
- Quick rollback on the same node (storage-level undo)
- Protection against application errors (corrupted data)
- **NOT** protection against hardware failure
- **NOT** protection against cluster loss
- **NOT** disaster recovery

**External/Shared Storage (Ceph, AWS EBS, NetApp):**

For external storage, CSI snapshots can survive cluster loss **IF**:

| Storage Type | Snapshot Location | Survives Cluster Loss? | DR Capable? |
|-------------|------------------|----------------------|-------------|
| **Local LVM** | Same node/disk | ❌ NO | ❌ NO |
| **HostPath/LocalPV** | Same node/disk | ❌ NO | ❌ NO |
| **Ceph RBD** | Ceph cluster (separate infrastructure) | ⚠️ Only if Ceph survives | ⚠️ Complex (see below) |
| **AWS EBS** | AWS backend (regional) | ⚠️ Only if same AWS account/region | ⚠️ With replication |
| **NetApp** | NetApp storage (separate infrastructure) | ⚠️ Only if NetApp survives | ⚠️ With SnapMirror |

**Ceph RBD Example:**
```
Scenario: OCP cluster lost, but Ceph cluster is independent and survived

Possible to restore? Technically yes, but very complex:
1. Deploy new OCP cluster
2. Configure Ceph CSI driver pointing to SAME Ceph cluster
3. Find snapshot IDs in Ceph (rbd snap ls pool/image)
4. Manually create VolumeSnapshotContent objects with correct snapshot handles
5. Create VolumeSnapshot objects referencing the VolumeSnapshotContent
6. Create PVCs from VolumeSnapshots

Practical? No - requires deep knowledge of CSI internals and Ceph
Reliable? No - error-prone, manual, not tested in most environments
Recommended? No - use Restic/Kopia instead
```

**Problem**: Even with external storage, if the storage backend is lost (datacenter failure), the CSI snapshots are gone even though the metadata is in S3.

**Use Case**: CSI snapshots work well for:
- Same-cluster restores (rollback, recovery from operator issues)
- **ONLY** when storage backend has independent DR/replication
- Fast backup/restore operations (storage-level snapshots are quick)
- **NOT** as a standalone DR solution

### Restic/Kopia File-Level Backup

**How Restic/Kopia Work with Velero:**
- OADP deploys a `node-agent` daemonset (one pod per node)
- During backup: node-agent pods mount PVCs and stream file data to S3 object storage
- During restore: node-agent pods mount new PVCs and write data from S3 back to volumes
- **Application pods do NOT need to be running** - the node-agent pods can access the volumes independently

**What Gets Backed Up:**

| What's Backed Up | Location | Survives Cluster Loss? |
|-----------------|----------|----------------------|
| VolumeSnapshot metadata (YAML) | S3 object storage | ✅ Yes |
| **Actual PVC file data** | **S3 object storage** | ✅ **Yes** |

**Node-Agent Pods:**
```
# node-agent daemonset runs on each node
$ oc get pods -n openshift-adp

NAME                    READY   STATUS    NODE
velero-xxx              1/1     Running   -
node-agent-abc          1/1     Running   worker-0
node-agent-def          1/1     Running   worker-1
node-agent-ghi          1/1     Running   worker-2
```

**During Backup:**
```
1. Velero identifies PVCs to backup (via annotation or defaultVolumesToFsBackup)
2. Velero creates a backup pod spec
3. node-agent on the node where PVC is used mounts the volume
4. node-agent reads files from PVC and streams to S3
5. Backup completes when all data uploaded
```

**During Restore:**
```
1. Velero creates new PVCs in target cluster
2. node-agent pods mount the empty PVCs
3. node-agent downloads data from S3 and writes to PVCs
4. Restore completes when all data written
5. Application pods can then mount the restored PVCs
```

**Key Point**: Your application pods (Galera, Glance, etc.) do NOT need to run during backup. The node-agent pods access the underlying volumes directly.

**Detailed: Which Pods Access Storage?**

❓ **Common Question**: "Do my application pods need to be running for Restic/Kopia backup?"

✅ **Answer**: NO! Your application pods do NOT need to be running.

**Who accesses the storage:**

| Pod | Provided By | Accesses PVC? | Required for Backup? |
|-----|-------------|---------------|---------------------|
| node-agent-xxx | OADP (daemonset) | ✅ Yes | ✅ **Required** |
| galera-0 | OpenStack operator | ✅ Yes | ❌ Not needed |
| glance-api-xxx | OpenStack operator | ✅ Yes | ❌ Not needed |

**How node-agent accesses storage:**

The node-agent pods use the **same Kubernetes volume mount API** that your application uses:

```yaml
# What node-agent does internally (conceptual)
apiVersion: v1
kind: Pod
metadata:
  name: node-agent-abc
  namespace: openshift-adp
spec:
  volumes:
  - name: backup-target
    persistentVolumeClaim:
      claimName: mysql-db-galera-0  # ← Same PVC your app uses
  containers:
  - name: node-agent
    volumeMounts:
    - name: backup-target
      mountPath: /host/pvc-data
      readOnly: true  # ← Read-only to avoid data corruption
    # Then runs: restic backup /host/pvc-data --repo s3:...
```

**Scenario 1: Application Running**
```bash
# Your application pod is running
$ oc get pods -n openstack
NAME        READY   STATUS    NODE
galera-0   1/1     Running   worker-0

# galera-0 is using PVC mysql-db-galera-0
# node-agent on worker-0 ALSO mounts mysql-db-galera-0 (read-only)
# Both pods access same underlying volume simultaneously
# Safe because node-agent uses read-only mount

# Backup proceeds normally - no impact to galera-0
```

**Scenario 2: Application Stopped**
```bash
# Scale down your application
$ oc scale statefulset galera --replicas=0 -n openstack

# galera-0 is now gone, PVC not mounted by any app pod
$ oc get pvc -n openstack
NAME                  STATUS   VOLUME
mysql-db-galera-0    Bound    pv-456

# node-agent can STILL mount and backup the PVC
# The PVC exists, so the PV data is accessible
# Backup works perfectly - might even be safer (no writes during backup)
```

**Scenario 3: Application Deleted**
```bash
# Delete the entire StatefulSet
$ oc delete statefulset galera -n openstack

# As long as PVC exists (not deleted), backup works
# PVC is independent of the StatefulSet/Pods
# node-agent mounts the orphaned PVC and backs it up
```

**Volume Access Modes:**

How node-agent accesses volumes depends on access mode:

| Access Mode | Description | node-agent Behavior |
|------------|-------------|---------------------|
| **ReadWriteOnce (RWO)** | Volume mounted by one node only | node-agent on same node as app pod mounts it |
| **ReadOnlyMany (ROX)** | Multiple nodes, read-only | node-agent on any node can mount it |
| **ReadWriteMany (RWX)** | Multiple nodes, read-write | node-agent on any node can mount it |

**For most OpenStack PVCs (RWO - LVM, block storage):**
- PVC is bound to specific node where app pod runs
- node-agent daemonset pod on **that same node** performs backup
- This is automatic - Velero schedules backup on correct node

**Example with RWO volume:**
```bash
# App pod using RWO volume
$ oc get pod galera-0 -n openstack -o wide
NAME       NODE
galera-0  worker-0

# PVC is mounted on worker-0
$ oc get pvc mysql-db-galera-0 -n openstack -o yaml | grep nodeName

# node-agent on worker-0 will perform the backup
$ oc get pod -n openshift-adp -o wide | grep worker-0
NAME              NODE
node-agent-abc   worker-0  ← This pod backs up the PVC

# If app pod moves to worker-1, different node-agent handles it
```

**Summary:**
- ✅ **node-agent pods** (OADP-provided) do ALL the backup work
- ❌ **Application pods** (Galera, Glance) are NOT involved
- ✅ **Application can be running, stopped, or deleted** - doesn't matter
- ✅ **node-agent mounts PVCs directly** using Kubernetes volume API
- ✅ **Read-only mounts** ensure no data corruption
- ✅ **Automatic node selection** based on PVC location (for RWO volumes)

## Backup Strategies for DR

### Strategy 1: Velero with Restic/Kopia (Recommended)

This is the **recommended approach** for true disaster recovery as it stores **all data** (resources AND PVC contents) in external object storage.

#### Why This Works for DR

1. **Backup Phase**: Velero backs up both Kubernetes resources AND complete PVC data to S3-compatible object storage
2. **Storage**: All data stored externally (survives cluster AND storage backend loss)
3. **Restore Phase**: New cluster with different storage backend can restore everything from S3

#### Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│ Source Cluster (Production)                                     │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  OpenStack Deployment                                           │
│    ├── CRs (ControlPlane, DataPlane, etc.)                     │
│    ├── Secrets, ConfigMaps, NADs                               │
│    └── PVCs (Glance, MariaDB backups)                          │
│                                                                  │
│  OADP/Velero Backup                                             │
│    ├── Serializes resources → Object Storage                   │
│    └── Restic/Kopia: File-level backup of PVC data → Object    │
│                       Storage                                    │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
                              ↓
                    ┌─────────────────────┐
                    │   Object Storage    │
                    │   (S3-compatible)   │
                    │                     │
                    │ - Resources (JSON)  │
                    │ - PVC data (tar.gz) │
                    │ - Metadata          │
                    └─────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│ Target Cluster (DR Site)                                        │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  OADP/Velero Restore                                            │
│    ├── Reads resources from Object Storage                     │
│    ├── Recreates CRs, Secrets, ConfigMaps                      │
│    ├── Creates PVCs                                             │
│    └── Restic/Kopia: Restores file data into PVCs              │
│                                                                  │
│  OpenStack Deployment (Restored)                                │
│    ├── ControlPlane reconciles                                 │
│    ├── Databases restored from backup data                     │
│    └── Services come online                                     │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

#### Special Note: LVMS/Local Storage

**If you are using LVMS (Logical Volume Manager Storage) or any local storage**, Restic/Kopia is **MANDATORY** for disaster recovery. This is not optional.

**Why LVMS requires Restic/Kopia:**

| Aspect | LVMS with CSI Snapshots | LVMS with Restic/Kopia |
|--------|------------------------|------------------------|
| **Snapshot location** | Same node's local disk | N/A - file-level backup |
| **Backup to S3** | ❌ Metadata only | ✅ Complete data |
| **Survives node loss** | ❌ NO | ✅ Yes |
| **Survives cluster loss** | ❌ NO | ✅ Yes |
| **DR capability** | ❌ ZERO | ✅ Full DR |
| **Restore to new cluster** | ❌ Impossible | ✅ Works perfectly |

**How Restic/Kopia works with LVMS:**

**Backup Phase:**
```bash
# Source cluster with LVMS
Node worker-0:
└── LVMS Volume Group: vg-storage
    └── LVM Volume: mysql-db (10GB)
        └── PVC: mysql-db-galera-0 → Mounted by galera-0 pod

# During backup:
# 1. node-agent on worker-0 mounts the LVM volume (same as galera-0)
# 2. node-agent reads ALL files from the volume
# 3. node-agent uploads to S3: s3://bucket/backups/mysql-db-galera-0.tar.gz
# 4. Result: Complete 10GB of data in S3

# If node/cluster is lost:
# - LVM volume is gone
# - But S3 has complete data ✅
```

**Restore Phase (to new cluster with LVMS):**
```bash
# DR cluster (different nodes, different LVMS setup)
Node worker-5:  # Different node!
└── LVMS Volume Group: vg-storage  # Different VG, same type
    └── (empty - will create new volume)

# During restore:
# 1. Velero creates PVC mysql-db-galera-0 (status: Pending)
# 2. Velero creates restore pod to mount the PVC
# 3. Kubernetes scheduler selects worker-5 (has LVMS capacity)
# 4. LVMS CSI driver creates NEW 10GB volume on worker-5
# 5. PVC binds to the new volume
# 6. node-agent on worker-5 downloads data from S3
# 7. node-agent writes all files to the new LVM volume
# 8. Restore complete - galera-0 can now use restored data

# Result: Data restored to DIFFERENT node/volume ✅
```

**Key Benefits for LVMS:**
- ✅ **Storage-agnostic**: Restore to any cluster with LVMS (or even different storage type)
- ✅ **Node-agnostic**: Don't need same node names or IPs
- ✅ **Automatic node selection**: Kubernetes finds node with LVMS capacity
- ✅ **True DR**: Survives complete cluster and hardware loss
- ✅ **No manual mapping**: Velero handles everything automatically

**Important Considerations for LVMS DR:**

1. **Capacity Planning**: Ensure DR cluster has enough LVMS capacity on nodes
   ```bash
   # Check LVMS capacity on nodes
   oc get logicalvolume -A

   # Ensure total capacity >= source cluster PVC sizes
   ```

2. **Storage Class**: DR cluster needs compatible LVMS storage class
   ```bash
   # Can use different storage class name with mapping
   storageClassMapping:
     lvms-provisioner: lvms-dr
   ```

3. **Node Scheduling**: PVCs will bind to nodes with available capacity
   - May be different nodes than source cluster
   - LVMS scheduler handles this automatically
   - No manual node affinity needed

4. **Performance**: Restore is slower than CSI snapshots (downloads from S3)
   - But this is the ONLY way to achieve DR with local storage
   - Acceptable trade-off for disaster recovery capability

**Example LVMS DR Configuration:**

```yaml
# DataProtectionApplication for LVMS clusters
apiVersion: oadp.openshift.io/v1alpha1
kind: DataProtectionApplication
metadata:
  name: velero
  namespace: openshift-adp
spec:
  configuration:
    velero:
      defaultPlugins:
      - openshift
      - aws
      # No CSI plugin - not useful for local storage DR

    # REQUIRED for LVMS
    nodeAgent:
      enable: true
      uploaderType: kopia  # Kopia recommended (faster than Restic)

  backupLocations:
  - velero:
      provider: aws
      default: true
      credential:
        name: cloud-credentials
        key: cloud
      config:
        s3Url: https://s3.example.com
        s3ForcePathStyle: "true"
      objectStorage:
        bucket: openstack-lvms-dr
        prefix: prod-cluster
```

```yaml
# Backup configuration for LVMS
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: openstack-lvms-backup
spec:
  includedNamespaces:
  - openstack

  # CRITICAL: Must use file-level backup for LVMS
  defaultVolumesToFsBackup: true  # ← REQUIRED

  # Don't use CSI snapshots
  snapshotVolumes: false

  storageLocation: default
```

**Pod Affinity/Anti-Affinity Considerations:**

⚠️ **IMPORTANT**: If your StatefulSets use pod anti-affinity rules (e.g., spreading Galera replicas across nodes), you may encounter scheduling conflicts during restore with local storage.

**The Problem:**

```yaml
# StatefulSet with anti-affinity (common for HA)
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: galera
spec:
  replicas: 3
  template:
    spec:
      affinity:
        podAntiAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
          - labelSelector:
              matchLabels:
                app: mariadb-galera
            topologyKey: kubernetes.io/hostname
      # Hard rule: Each galera pod MUST be on different node
```

**What can happen during LVMS restore:**

```bash
# Restore phase:
# 1. Restic creates restore pods (no affinity rules applied)
# 2. Scheduler places them based on LVMS capacity:
mysql-db-galera-0 → worker-5 (50GB free)
mysql-db-galera-1 → worker-5 (40GB free after first PVC)
mysql-db-galera-2 → worker-6 (50GB free)

# Application pods start:
galera-0 → Must run on worker-5 (PVC bound there, RWO)
galera-1 → Must run on worker-5 (PVC bound there, RWO)
           BUT anti-affinity says "cannot be with galera-0"
           → Pod stuck in Pending! Conflict!

$ oc get pods -n openstack
NAME       READY   STATUS    NODE
galera-0   1/1     Running   worker-5
galera-1   0/1     Pending   -
galera-2   1/1     Running   worker-6

$ oc describe pod galera-1 -n openstack
Events:
  Warning  FailedScheduling  pod didn't match pod anti-affinity rules
```

**Solutions:**

**Solution 1: Use Soft Anti-Affinity (Recommended for DR)**

Change anti-affinity to **preferred** instead of **required**:

```yaml
# Modify StatefulSet to use soft anti-affinity
spec:
  template:
    spec:
      affinity:
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:  # Soft rule
          - weight: 100
            podAffinityTerm:
              labelSelector:
                matchLabels:
                  app: mariadb-galera
              topologyKey: kubernetes.io/hostname
      # Prefers different nodes, but allows co-location if necessary
```

**Benefits:**
- ✅ Normal operation: Pods spread across nodes (when possible)
- ✅ DR restore: Pods can co-locate if PVCs ended up on same node
- ✅ Service stays online during DR (data recovery prioritized)

**Trade-off:**
- ⚠️ Temporarily reduced HA (pods might be on same node)
- ⚠️ Can fix after restore by draining nodes and re-balancing

**Solution 2: Ensure Sufficient Nodes in DR Cluster**

Make sure DR cluster has **at least as many nodes** as your StatefulSet replicas:

```bash
# Source cluster:
3 nodes (worker-0, worker-1, worker-2)
3 Galera replicas

# DR cluster: Need at least 3 nodes with LVMS
# But better to have MORE to handle uneven distribution

# Recommended: N+2 nodes for N replicas
# Example: 5 nodes for 3 Galera replicas
# Gives scheduler more flexibility
```

**Check nodes before restore:**
```bash
# Count nodes with LVMS
oc get nodes -l node-role.kubernetes.io/worker
# Should have >= number of StatefulSet replicas

# Check LVMS capacity per node
oc get logicalvolume -A -o wide
# Ensure even distribution of capacity
```

**Solution 3: Control PVC Node Distribution (Advanced)**

Force restore pods to spread across nodes by temporarily applying affinity to restore operation.

**Create a restore hook ConfigMap:**

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: restore-pod-spread
  namespace: openshift-adp
  labels:
    velero.io/pod-volume-restore: RestoreItemAction
data:
  spread-config: |
    {
      "affinity": {
        "podAntiAffinity": {
          "preferredDuringSchedulingIgnoredDuringExecution": [{
            "weight": 100,
            "podAffinityTerm": {
              "labelSelector": {
                "matchLabels": {
                  "velero.io/restore-name": "openstack-dr-restore"
                }
              },
              "topologyKey": "kubernetes.io/hostname"
            }
          }]
        }
      }
    }
```

**Note**: This is advanced and may require Velero plugins. Simpler to use Solution 1 or 2.

**Solution 4: Manual PVC Rebalancing After Restore**

If PVCs end up unevenly distributed, rebalance after restore:

```bash
# 1. Identify problem PVCs
oc get pvc -n openstack -o custom-columns=NAME:.metadata.name,NODE:.metadata.annotations.'volume\.kubernetes\.io/selected-node'

# Example output:
# NAME                  NODE
# mysql-db-galera-0    worker-5
# mysql-db-galera-1    worker-5  ← Same node!
# mysql-db-galera-2    worker-6

# 2. Scale down StatefulSet
oc scale statefulset galera --replicas=0 -n openstack

# 3. Delete problematic PVC (galera-1)
oc delete pvc mysql-db-galera-1 -n openstack

# 4. Restore just that PVC to a different backup
# (Complex - might need to restore to new namespace then move)

# 5. Or accept co-location temporarily and fix during maintenance
```

**Solution 5: Topology-Aware Restore (Future Enhancement)**

File an enhancement request for Velero to support topology-aware restore:
- Velero could read StatefulSet affinity rules
- Distribute restore pods according to those rules
- Ensure PVCs bind to appropriate nodes

**Recommendations by Environment:**

| Environment | Recommendation |
|------------|----------------|
| **Production DR** | Use soft anti-affinity + ensure N+2 nodes in DR cluster |
| **Test/Dev DR** | Accept co-location, rebalance later |
| **Mission Critical** | Pre-plan DR cluster with same node count + topology labels |
| **Small Deployments** | Use soft anti-affinity, prioritize data recovery over HA |

**Best Practice for OpenStack:**

Modify operator-generated StatefulSets to use **soft anti-affinity** by default:

```go
// In operator code
antiAffinity := &corev1.PodAntiAffinity{
    PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
        {
            Weight: 100,
            PodAffinityTerm: corev1.PodAffinityTerm{
                LabelSelector: &metav1.LabelSelector{
                    MatchLabels: labels,
                },
                TopologyKey: "kubernetes.io/hostname",
            },
        },
    },
}
// Instead of RequiredDuringSchedulingIgnoredDuringExecution
```

**Checking After Restore:**

```bash
# Verify pod distribution
oc get pods -n openstack -o wide | grep galera

# If uneven distribution, options:
# 1. Accept it temporarily (service is running)
# 2. Plan maintenance window to rebalance
# 3. Use node draining to force rescheduling (requires PVC migration)
```

**Summary for LVMS Users:**
- ⚠️ **CSI snapshots provide ZERO DR** for LVMS
- ✅ **Restic/Kopia is the ONLY DR solution** for local storage
- ✅ **Works perfectly** - tested and reliable
- ✅ **Automatic restore** to different nodes
- ⚠️ **Configure OADP correctly** - enable nodeAgent with Kopia
- ⚠️ **Use soft anti-affinity** to avoid scheduling conflicts during DR restore
- ⚠️ **Plan DR cluster capacity** - N+2 nodes for N replicas recommended

### Strategy 2: CSI Snapshots with Storage Backend Replication

Uses CSI volume snapshots combined with storage-level replication. **Storage provider dependent**.

#### How It Works

1. **Backup**: CSI snapshots created in storage backend (fast, storage-level)
2. **Replication**: Storage backend replicates snapshots to DR site (e.g., Ceph RBD mirroring, AWS EBS snapshot copy)
3. **Restore**: Import replicated snapshots into DR cluster's storage backend

#### Limitations

- **Storage backend dependency**: Requires storage backend to support cross-site replication
- **Not all CSI drivers support this**: Many CSI drivers don't have cross-cluster snapshot import
- **Complex setup**: Requires coordination between Velero and storage replication
- **Vendor lock-in**: DR cluster must use same storage backend type
- **Datacenter failure risk**: If storage backend is in same datacenter, replication may be affected

**Example Working Scenarios:**
- AWS EBS: Snapshots can be copied across regions, then imported in DR cluster
- Ceph RBD Mirroring: Replicate RBD images to remote Ceph cluster
- NetApp Trident: SnapMirror for volume replication

**Why This Is Complex:**
- VolumeSnapshot objects contain cluster-specific metadata (UIDs, CSI driver handles)
- DR cluster needs to create VolumeSnapshotContent pointing to replicated snapshots
- Manual intervention often required to map snapshot IDs

### Strategy 3: Hybrid Approach

- Resources backed up to object storage (portable)
- PVC data replicated using storage-native replication (e.g., Ceph RBD mirroring)
- Requires coordination between Velero and storage layer

### Strategy 4: OADP Data Mover (Recommended for Production)

**Available in OADP 1.2+ (GA in 1.3+)**

OADP Data Mover combines the **best of CSI snapshots and Restic/Kopia** to provide fast, consistent, and portable backups.

#### How Data Mover Works

Data Mover uses a two-phase approach:

1. **Phase 1: CSI Snapshot** - Create fast, crash-consistent, point-in-time snapshot (storage-level)
2. **Phase 2: Move to S3** - Copy snapshot data to object storage using VolSync/Kopia (portable)

**Architecture:**

```
┌─────────────────────────────────────────────────────────────────┐
│ Source Cluster                                                   │
├─────────────────────────────────────────────────────────────────┤
│                                                                   │
│  OpenStack Deployment                                            │
│    └── PVC: mysql-db (10GB on LVMS)                             │
│                                                                   │
│  Phase 1: CSI Snapshot (instant, crash-consistent)              │
│    └── VolumeSnapshot: mysql-db-snap                            │
│        └── Snapshot on local LVMS (point-in-time)               │
│                                                                   │
│  Phase 2: Data Mover (powered by VolSync/Kopia)                 │
│    ├── Mounts VolumeSnapshot (not live PVC)                     │
│    ├── Reads snapshot data                                       │
│    └── Uploads to S3: s3://bucket/mysql-db-data (10GB)          │
│                                                                   │
│  Result:                                                         │
│    ✅ Fast backup (snapshot instant)                             │
│    ✅ Crash-consistent (snapshot point-in-time)                  │
│    ✅ Portable (data in external S3)                             │
│    ✅ No application impact (reads from snapshot, not live vol)  │
│                                                                   │
└─────────────────────────────────────────────────────────────────┘
                              ↓
                    ┌─────────────────────┐
                    │   Object Storage    │
                    │   (S3-compatible)   │
                    │                     │
                    │ - Snapshot data     │
                    │ - Resources (JSON)  │
                    │ - Metadata          │
                    └─────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│ Target Cluster (DR Site)                                         │
├─────────────────────────────────────────────────────────────────┤
│                                                                   │
│  OADP/Velero Restore                                             │
│    ├── Creates PVCs in target cluster                           │
│    ├── Data Mover downloads from S3                             │
│    └── Populates PVCs with data                                 │
│                                                                   │
│  OpenStack Deployment (Restored)                                 │
│    └── Application pods mount restored PVCs                      │
│                                                                   │
└─────────────────────────────────────────────────────────────────┘
```

#### Benefits Over Other Approaches

**vs Direct Restic/Kopia:**
- ✅ **Faster backup** - Snapshot is instant (seconds vs minutes/hours)
- ✅ **Better consistency** - Snapshot captures exact point-in-time, not rolling window
- ✅ **Less application impact** - Reads from snapshot, not live volume
- ✅ **Leverages existing CSI infrastructure** - Uses your POC work!

**vs CSI Snapshots Only:**
- ✅ **Portable** - Data copied to S3, survives cluster/storage loss
- ✅ **Works with local storage** - LVMS, TopoLVM, LocalPV all supported
- ✅ **True DR capability** - Can restore to different cluster/storage

**vs Storage-Level Replication:**
- ✅ **Storage agnostic** - Restore to any storage type
- ✅ **No storage vendor lock-in** - Works with any CSI driver
- ✅ **Simpler** - No complex storage replication setup

#### Configuration

**Enable Data Mover in DataProtectionApplication:**

```yaml
apiVersion: oadp.openshift.io/v1alpha1
kind: DataProtectionApplication
metadata:
  name: velero
  namespace: openshift-adp
spec:
  configuration:
    velero:
      defaultPlugins:
      - openshift
      - aws
      - csi  # ← Required for CSI snapshots

    # Enable Data Mover feature
    features:
      dataMover:
        enable: true
        credentialName: cloud-credentials  # S3 credentials for data transfer
        timeout: 10m  # Timeout for snapshot data transfer

    # Optional: Also enable node-agent for direct Restic fallback
    nodeAgent:
      enable: true
      uploaderType: kopia

  backupLocations:
  - velero:
      provider: aws
      default: true
      credential:
        name: cloud-credentials
        key: cloud
      config:
        region: us-east-1
        s3Url: https://s3.example.com
        s3ForcePathStyle: "true"
      objectStorage:
        bucket: openstack-dr-backups
        prefix: prod-cluster

  # VolumeSnapshotLocation for CSI snapshots
  snapshotLocations:
  - velero:
      provider: aws  # Or your CSI driver provider
      config:
        region: us-east-1
```

**Create Backup with Data Mover:**

```yaml
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: openstack-backup-datamover
  namespace: openshift-adp
spec:
  includedNamespaces:
  - openstack

  # Enable CSI snapshots
  snapshotVolumes: true

  # Enable Data Mover to copy snapshots to S3
  snapshotMoveData: true  # ← KEY: This triggers Data Mover!

  # Specify data mover configuration
  datamover:
    enable: true
    timeout: 10m  # Adjust based on PVC sizes

  storageLocation: default
  volumeSnapshotLocations:
  - velero-sample-1

  ttl: 720h0m0s
```

**What Happens During Backup:**

1. Velero creates VolumeSnapshots for labeled PVCs (uses your POC labels!)
2. CSI driver creates storage-level snapshots (instant, crash-consistent)
3. Data Mover detects `snapshotMoveData: true`
4. Data Mover creates temporary pods to mount each snapshot
5. Data is read from snapshots and uploaded to S3 via Kopia
6. Backup completes when all snapshot data in S3
7. (Optional) Local CSI snapshots can be deleted to save space

**Restore to DR Cluster:**

```yaml
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-dr-restore
  namespace: openshift-adp
spec:
  backupName: openstack-backup-datamover

  includedNamespaces:
  - openstack

  # Data Mover automatically handles restore
  restorePVs: true

  # No CSI snapshots needed in target cluster
  # Data comes from S3

  # Storage class mapping (if needed)
  # storageClassMapping:
  #   lvms-provisioner: ceph-rbd
```

**What Happens During Restore:**

1. Velero creates PVCs in DR cluster
2. Data Mover creates restore pods
3. Data downloaded from S3 to new PVCs
4. Works even if DR cluster has different storage backend
5. Application pods can mount restored PVCs

#### Data Mover with LVMS (Local Storage)

**Perfect match for local storage DR!**

**Backup Phase:**
```bash
# Source cluster with LVMS
Node worker-0:
└── LVMS Volume Group: vg-storage
    └── LVM Volume: mysql-db (10GB)
        └── PVC: mysql-db-galera-0

# Data Mover backup:
# 1. CSI snapshot created (instant, local LVMS snapshot)
# 2. Data Mover mounts the snapshot
# 3. Reads all data from snapshot
# 4. Uploads to S3: s3://bucket/mysql-db-galera-0/ (10GB)
# 5. Local snapshot can be deleted (data safe in S3)

# If cluster/node lost:
# - LVMS and local snapshot are gone
# - S3 has complete data ✅
```

**Restore Phase:**
```bash
# DR cluster (different hardware/nodes)
Node worker-5:  # Different node!
└── LVMS Volume Group: vg-storage
    └── (will create new volume)

# Data Mover restore:
# 1. Creates PVC mysql-db-galera-0 (Pending)
# 2. Creates restore pod
# 3. Kubernetes schedules to worker-5 (has LVMS capacity)
# 4. LVMS creates NEW 10GB volume on worker-5
# 5. Data Mover downloads data from S3
# 6. Writes to new LVM volume
# 7. galera-0 can now use restored data

# Result: Full DR from local storage! ✅
```

#### Advantages for Your POC

**Your CSI snapshot POC is the perfect foundation for Data Mover!**

All your work is used:
- ✅ CRD labels → Velero uses these to identify resources
- ✅ PVC labels (`openstack.org/backup: "true"`) → Identifies which PVCs to snapshot
- ✅ CR instance labels → Used for restore ordering
- ✅ Backup/Restore CRs → Just add `snapshotMoveData: true`

**Migration path:**

```yaml
# Step 1: Test your current POC (CSI snapshots only)
# - Validates labeling works
# - Tests in-cluster restore
# - Proves backup/restore ordering

# Step 2: Enable Data Mover (add one line to backups)
apiVersion: velero.io/v1
kind: Backup
spec:
  snapshotVolumes: true
  snapshotMoveData: true  # ← Add this!

# Step 3: Test DR restore to different cluster
# - Everything else stays the same
# - Data Mover handles portability automatically
```

#### Version Requirements

| OADP Version | Data Mover Status | Notes |
|-------------|------------------|-------|
| OADP 1.1.x | ❌ Not available | Use Restic/Kopia |
| OADP 1.2.x | ⚠️ Tech Preview | Early adopter, may have issues |
| OADP 1.3.x | ✅ GA (Generally Available) | Recommended minimum version |
| OADP 1.4.x+ | ✅ Enhanced | Performance improvements, bug fixes |

**Check your version:**
```bash
oc get csv -n openshift-adp | grep oadp
```

#### Limitations and Considerations

**Current Limitations:**

1. **Snapshot must succeed first** - If CSI snapshot fails, Data Mover doesn't run
2. **Storage overhead** - Both snapshot AND S3 storage used (temporarily)
3. **Time window** - Snapshot creation + data transfer to S3 (longer than snapshot-only)
4. **CSI driver dependency** - Requires working CSI snapshot capability

**Best Practices:**

1. **Set appropriate timeout** - Large PVCs need longer `datamover.timeout`
2. **Monitor transfer progress** - Check Data Mover pod logs
3. **Test restore regularly** - Verify DR capability works
4. **Clean up old snapshots** - Delete local CSI snapshots after data in S3
5. **S3 capacity planning** - Need space for all PVC data

**Cleanup of Local Snapshots:**

```yaml
# Optional: Delete CSI snapshots after Data Mover completes
# Saves local storage space, data is safely in S3
spec:
  snapshotMoveData: true
  datamover:
    enable: true
    timeout: 10m
    deleteSnapshotAfterUpload: true  # Clean up local snapshot
```

### Comparison: CSI-Only vs Restic/Kopia vs Data Mover

| Aspect | CSI Snapshots (Local) | CSI Snapshots (External) | Restic/Kopia | **Data Mover** |
|--------|----------------------|-------------------------|--------------|----------------|
| **Backup Speed** | ✅ Instant (snapshot) | ✅ Instant (snapshot) | ⚠️ Slow (file-scan) | ✅ Fast (snapshot + async upload) |
| **Restore Speed** | ✅ Instant (snapshot) | ✅ Instant (snapshot) | ⚠️ Slow (download) | ⚠️ Medium (download from S3) |
| **Data in S3** | ❌ **Metadata only** | ❌ **Metadata only** | ✅ **Full data** | ✅ **Full data** |
| **Data Location** | ❌ Same node/disk | ⚠️ Storage backend | ✅ S3 | ✅ S3 |
| **Consistency** | ✅ Point-in-time | ✅ Point-in-time | ⚠️ Rolling window | ✅ Point-in-time (snapshot) |
| **Application Impact** | ✅ None | ✅ None | ⚠️ Reads live volume | ✅ None (reads snapshot) |
| **Survives Node Loss** | ❌ **NO** | ✅ Yes | ✅ Yes | ✅ Yes |
| **Survives Cluster Loss** | ❌ **NO** | ⚠️ Only if storage survives | ✅ Yes | ✅ Yes |
| **Survives Datacenter Loss** | ❌ **NO** | ❌ NO (unless replicated) | ✅ Yes | ✅ Yes |
| **DR to Different Storage** | ❌ Impossible | ❌ Very difficult | ✅ Yes | ✅ Yes (storage-agnostic) |
| **Local Storage (LVMS) DR** | ❌ **ZERO** | N/A | ✅ Full DR | ✅ **Full DR** |
| **Storage Overhead** | ✅ Minimal (COW) | ✅ Minimal (delta) | ⚠️ Full data in S3 | ⚠️ Snapshot + S3 data |
| **S3 Bandwidth** | ✅ Minimal | ✅ Minimal | ⚠️ High during backup | ⚠️ High (async after snapshot) |
| **Requires CSI Snapshots** | ✅ Yes | ✅ Yes | ❌ No | ✅ Yes |
| **Works Without CSI** | ❌ No | ❌ No | ✅ Yes | ❌ No |
| **OADP Version** | Any | Any | Any | 1.3+ (GA) |
| **Complexity** | ✅ Simple | ✅ Simple | ⚠️ Medium | ⚠️ Medium |
| **Production Ready** | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes (OADP 1.3+) |
| **DR Capability** | ❌ **NONE** | ⚠️ Limited/Complex | ✅ Full DR | ✅ **Full DR** |
| **Best For** | In-cluster rollback | In-cluster restore | Any storage, no CSI | **CSI + DR portability** |

**Storage Type Examples:**

| Storage Class | Type | CSI Snapshot DR Capable? |
|--------------|------|-------------------------|
| Local LVM (topolvm, local-storage) | Local | ❌ **NO - Not a backup** |
| HostPath, LocalPV | Local | ❌ **NO - Not a backup** |
| Ceph RBD (ODF, Rook) | External | ⚠️ Complex (requires Ceph to survive) |
| AWS EBS | External | ⚠️ Regional (requires same AWS account) |
| Azure Disk | External | ⚠️ Regional (requires same subscription) |
| NetApp Trident | External | ⚠️ Requires NetApp to survive |
| **Any with Restic/Kopia** | **Any** | ✅ **Yes - True DR** |

**Critical Recommendations:**

| Environment | Backup Strategy | Why |
|------------|----------------|-----|
| **Local Storage (LVMS, HostPath) + OADP 1.3+** | ✅ **Data Mover** | Fast snapshots + S3 portability + works with local storage |
| **Local Storage + OADP < 1.3** | ⚠️ **Restic/Kopia** | Data Mover not available, Restic is only DR option |
| **External Storage (Ceph, etc.) + OADP 1.3+** | ✅ **Data Mover** | Best of both: snapshot speed + DR capability |
| **External Storage + OADP < 1.3** | Use both: CSI for fast recovery, Restic/Kopia for DR | |
| **Production DR** | ✅ **Data Mover (preferred)** or Restic/Kopia | Portable backup required |
| **Development/Testing** | CSI snapshots acceptable | Data loss acceptable |
| **CSI Not Available** | Restic/Kopia | Only option without CSI support |

**Decision Tree:**

```
Do you have OADP 1.3+ ?
├─ YES
│  └─ Does your storage support CSI snapshots?
│     ├─ YES → ✅ Use Data Mover (best option)
│     └─ NO  → Use Restic/Kopia
│
└─ NO (OADP < 1.3)
   └─ Local storage (LVMS, etc.)?
      ├─ YES → ⚠️ MUST use Restic/Kopia (CSI snapshots = no DR)
      └─ NO  → Use both: CSI (fast) + Restic/Kopia (DR)
```

**Summary by Approach:**

| Approach | Summary | DR Capability |
|----------|---------|---------------|
| **CSI Snapshots (Local)** | Not a backup - local snapshot only (like LVM snapshot) | ❌ **ZERO** |
| **CSI Snapshots (External)** | Backup IF storage survives (not true DR) | ⚠️ Limited |
| **Restic/Kopia** | True backup, all data in S3, storage-agnostic | ✅ Full DR |
| **Data Mover** | Snapshot consistency + S3 portability, best of both worlds | ✅ **Full DR** |

**Updated Recommendation for OpenStack DR:**

| Your Situation | Recommended Solution |
|---------------|---------------------|
| **LVMS/Local Storage + OADP 1.3+** | ✅ **Data Mover** - Perfect match! |
| **LVMS/Local Storage + OADP < 1.3** | ⚠️ **Restic/Kopia** - Only DR option |
| **External Storage + OADP 1.3+** | ✅ **Data Mover** - Fast + portable |
| **External Storage + OADP < 1.3** | CSI (daily, fast) + Restic/Kopia (weekly, DR) |
| **Production (any storage)** | ✅ **Data Mover** if available, else Restic/Kopia |
| **CSI POC Working** | ✅ Add `snapshotMoveData: true` to enable Data Mover! |

**Why Data Mover is Preferred (when available):**
- ✅ Leverages your CSI snapshot POC work
- ✅ Fast backup (snapshot is instant)
- ✅ Crash-consistent (point-in-time snapshot)
- ✅ Portable (data copied to S3)
- ✅ Works with local storage (LVMS, TopoLVM)
- ✅ Storage-agnostic restore
- ✅ Less application impact (reads snapshot, not live volume)

## Migration Path: POC to Production DR

If you've completed the CSI snapshot POC and want to add DR capability, here's the migration path:

### Current POC (CSI Snapshots Only)

✅ **What works:**
- Fast in-cluster backup and restore
- CRD, PVC, and CR instance labeling
- Backup/restore ordering
- Works great for rollback and testing

❌ **What's missing:**
- No DR capability (especially for local storage)
- Snapshots don't survive cluster loss
- Can't restore to different cluster

### Step 1: Verify OADP Version

```bash
# Check OADP version
oc get csv -n openshift-adp | grep oadp

# Data Mover available in OADP 1.3+
# If OADP < 1.3, you'll need to use Restic/Kopia instead
```

### Step 2: Enable Data Mover (OADP 1.3+)

Update your existing DataProtectionApplication:

```yaml
# Your existing DPA (from POC)
apiVersion: oadp.openshift.io/v1alpha1
kind: DataProtectionApplication
metadata:
  name: velero
  namespace: openshift-adp
spec:
  configuration:
    velero:
      defaultPlugins:
      - openshift
      - aws
      - csi  # ← Already have this from POC

    # ADD THIS: Enable Data Mover
    features:
      dataMover:
        enable: true
        credentialName: cloud-credentials
        timeout: 10m

  backupLocations:
  - velero:
      provider: aws
      default: true
      credential:
        name: cloud-credentials
        key: cloud
      config:
        s3Url: https://s3.example.com
        s3ForcePathStyle: "true"
      objectStorage:
        bucket: openstack-dr-backups  # Same or new bucket

  snapshotLocations:
  - velero:
      provider: aws  # Or your CSI provider
      config:
        region: us-east-1
```

### Step 3: Update Backup CRs

Add one line to your existing backup CRs:

```yaml
# Before (POC - CSI snapshots only):
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: openstack-backup-pvcs
  namespace: openshift-adp
spec:
  includedNamespaces:
  - openstack

  labelSelector:
    matchLabels:
      openstack.org/backup: "true"

  snapshotVolumes: true  # ← Already have this

  storageLocation: default
  ttl: 720h0m0s

# After (Production - CSI snapshots + Data Mover):
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: openstack-backup-pvcs-dr
  namespace: openshift-adp
spec:
  includedNamespaces:
  - openstack

  labelSelector:
    matchLabels:
      openstack.org/backup: "true"

  snapshotVolumes: true
  snapshotMoveData: true  # ← ADD THIS LINE for DR!

  storageLocation: default
  ttl: 720h0m0s
```

### Step 4: Test DR Backup

```bash
# Create backup with Data Mover
oc create -f backup-with-datamover.yaml

# Watch backup progress
oc get backup openstack-backup-pvcs-dr -n openshift-adp -w

# Check Data Mover pods (appear during backup)
oc get pods -n openstack | grep datamover

# Verify backup completed
oc describe backup openstack-backup-pvcs-dr -n openshift-adp

# Check S3 bucket - should see snapshot data uploaded
```

### Step 5: Test DR Restore (Optional)

Test restore to a different cluster (or different namespace):

```bash
# On DR cluster:
# 1. Install OADP and configure same S3 backend
# 2. Verify backup is visible
oc get backups -n openshift-adp

# 3. Create restore
oc apply -f - <<EOF
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-dr-test
  namespace: openshift-adp
spec:
  backupName: openstack-backup-pvcs-dr
  includedNamespaces:
  - openstack
  restorePVs: true
EOF

# 4. Watch restore
oc get restore openstack-dr-test -n openshift-adp -w

# 5. Verify PVCs restored with data
oc get pvc -n openstack
```

### Alternative: Use Restic/Kopia (OADP < 1.3)

If you can't use Data Mover, add Restic/Kopia for DR:

**See the detailed Restic/Kopia configuration in the sections below.**

## DR Backup Configuration

### Prerequisites

1. **External Object Storage**
   - S3-compatible endpoint (AWS S3, MinIO, Ceph RGW, etc.)
   - Accessible from both source and DR clusters
   - Sufficient capacity for PVC data + resources
   - Credentials with read/write access

2. **OADP Operator Installed**
   ```bash
   oc create namespace openshift-adp
   # Install OADP operator via OperatorHub or subscription
   ```

3. **For Data Mover: OADP 1.3+**
   ```bash
   # Verify version
   oc get csv -n openshift-adp | grep oadp
   # Should show version 1.3.x or higher
   ```

### Configure DataProtectionApplication for DR

Create a secret with object storage credentials:

```bash
cat > credentials-velero <<EOF
[default]
aws_access_key_id=<ACCESS_KEY>
aws_secret_access_key=<SECRET_KEY>
EOF

oc create secret generic cloud-credentials \
  --from-file=cloud=credentials-velero \
  -n openshift-adp
```

Create DataProtectionApplication with Restic enabled:

```yaml
apiVersion: oadp.openshift.io/v1alpha1
kind: DataProtectionApplication
metadata:
  name: velero
  namespace: openshift-adp
spec:
  configuration:
    velero:
      defaultPlugins:
      - openshift
      - aws
      - csi

    # Enable Restic for file-level PVC backup
    restic:
      enable: true
      supplementalGroups:
      - 65534

    # OR use Kopia (newer, more efficient)
    nodeAgent:
      enable: true
      uploaderType: kopia

  backupLocations:
  - velero:
      provider: aws
      default: true
      credential:
        name: cloud-credentials
        key: cloud
      config:
        region: us-east-1
        s3Url: https://s3.example.com  # External S3 endpoint
        s3ForcePathStyle: "true"
        insecureSkipTLSVerify: "false"
      objectStorage:
        bucket: openstack-dr-backups
        prefix: prod-cluster-1

  snapshotLocations:
  - velero:
      provider: aws
      config:
        region: us-east-1
```

**Important configuration notes:**
- `restic.enable: true` OR `nodeAgent.enable: true` with `uploaderType: kopia`
- `objectStorage.prefix` helps separate backups from multiple clusters
- External S3 endpoint must be reachable from DR site

**What This Deploys:**

When you create the DataProtectionApplication with Restic or Kopia enabled, OADP automatically deploys:

```bash
# Velero controller pod
velero-<hash>                1/1     Running

# node-agent daemonset (one pod per node)
node-agent-<hash>           1/1     Running   worker-0
node-agent-<hash>           1/1     Running   worker-1
node-agent-<hash>           1/1     Running   worker-2
```

**Node-Agent Responsibilities:**
- **During Backup**: Mounts PVCs, reads file data, uploads to S3
- **During Restore**: Mounts PVCs, downloads data from S3, writes to volumes
- **Access Method**: Uses Kubernetes volume mount API (same as application pods)
- **Application Impact**: Your OpenStack pods (Galera, Glance, etc.) do NOT need to be running or restarted

**How Node-Agent Accesses PVC Data:**
1. Velero controller identifies PVCs to backup (via annotation or `defaultVolumesToFsBackup`)
2. Velero schedules backup on appropriate node-agent pod (node where PVC is attached)
3. Node-agent pod creates a mount to the PVC volume (read-only during backup)
4. Node-agent streams file contents to S3 object storage
5. Mount is removed when backup completes

This is completely transparent to your application - no pod restarts or downtime required.

### Create DR Backup

#### Approach A: Single Full Backup with Restic

```yaml
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: openstack-dr-full
  namespace: openshift-adp
spec:
  # Backup entire openstack namespace
  includedNamespaces:
  - openstack

  # Backup PVC data to object storage (not snapshots)
  defaultVolumesToFsBackup: true

  # Storage location (external S3)
  storageLocation: default

  # Retention
  ttl: 720h0m0s  # 30 days

  # Hooks for application consistency (optional)
  hooks:
    resources:
    - name: galera-backup-hook
      includedNamespaces:
      - openstack
      labelSelector:
        matchLabels:
          app: mariadb-galera
      pre:
      - exec:
          command:
          - /bin/bash
          - -c
          - mysql -e "FLUSH TABLES WITH READ LOCK; SYSTEM sync;"
          timeout: 30s
      post:
      - exec:
          command:
          - /bin/bash
          - -c
          - mysql -e "UNLOCK TABLES;"
```

**What gets backed up:**
- All OpenStack CRs (ControlPlane, DataPlane, Version, etc.)
- All Secrets and ConfigMaps
- All NetworkAttachmentDefinitions
- All MariaDB CRs (Account, Database, Backup)
- **PVC data** backed up to object storage (Glance images, MariaDB backups)

#### Approach B: Selective Backup with Labels

```yaml
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: openstack-dr-selective
  namespace: openshift-adp
spec:
  includedNamespaces:
  - openstack

  # Only backup labeled resources
  labelSelector:
    matchLabels:
      openstack.org/backup-restore: "true"

  # Backup PVCs to object storage
  defaultVolumesToFsBackup: true

  # OR annotate specific PVCs for backup
  # Add annotation to PVCs: backup.velero.io/backup-volumes=<volume-name>

  storageLocation: default
  ttl: 720h0m0s
```

**To annotate PVCs for backup:**

```bash
# Annotate Glance PVC
oc annotate pvc glance \
  backup.velero.io/backup-volumes=storage \
  -n openstack

# Annotate GaleraBackup PVCs
oc annotate pvc openstack-galera-backup-0 \
  backup.velero.io/backup-volumes=mysql-db \
  -n openstack
```

#### Approach C: Two-Backup Strategy (Current POC Adapted)

Keep the POC approach but use Restic for PVCs:

**Backup 1: PVCs with Restic**
```yaml
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: openstack-dr-pvcs
  namespace: openshift-adp
spec:
  includedNamespaces:
  - openstack

  includedResources:
  - persistentvolumeclaims

  labelSelector:
    matchLabels:
      openstack.org/backup: "true"

  # Use Restic instead of CSI snapshots
  defaultVolumesToFsBackup: true
  snapshotVolumes: false  # Don't use CSI snapshots

  storageLocation: default
```

**Backup 2: All Other Resources**
```yaml
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: openstack-dr-resources
  namespace: openshift-adp
spec:
  includedNamespaces:
  - openstack

  excludedResources:
  - persistentvolumeclaims
  - persistentvolumes

  storageLocation: default
```

### Schedule Regular DR Backups

```yaml
apiVersion: velero.io/v1
kind: Schedule
metadata:
  name: openstack-dr-daily
  namespace: openshift-adp
spec:
  schedule: "0 2 * * *"  # Daily at 2 AM
  template:
    includedNamespaces:
    - openstack
    defaultVolumesToFsBackup: true
    storageLocation: default
    ttl: 720h0m0s
```

### Verify Backup

```bash
# Check backup status
oc get backup -n openshift-adp
oc describe backup openstack-dr-full -n openshift-adp

# Check backup contents
velero backup describe openstack-dr-full --details

# Check object storage
# Should see backup files in S3 bucket under backups/openstack-dr-full/
```

## DR Restore Process

### Prerequisites for DR Site

1. **New Kubernetes/OpenShift Cluster**
   - Version compatible with source cluster (same or newer minor version)
   - Sufficient compute and storage capacity

2. **Network Access**
   - Connectivity to object storage endpoint
   - Appropriate firewall rules for OpenStack services

3. **Operators Installed**
   - Install OpenStack operators (same versions as source)
   - Install OADP operator

4. **Storage Classes**
   - Create storage classes matching source cluster
   - Or plan for storage class mapping during restore

### Step 1: Configure OADP in DR Cluster

Install OADP operator and configure **same object storage backend**:

```bash
# Create namespace
oc create namespace openshift-adp

# Create credentials (same as source cluster)
oc create secret generic cloud-credentials \
  --from-file=cloud=credentials-velero \
  -n openshift-adp

# Apply DPA configuration (same S3 endpoint and bucket)
oc apply -f - <<EOF
apiVersion: oadp.openshift.io/v1alpha1
kind: DataProtectionApplication
metadata:
  name: velero
  namespace: openshift-adp
spec:
  configuration:
    velero:
      defaultPlugins:
      - openshift
      - aws
      - csi
    restic:
      enable: true
      supplementalGroups:
      - 65534
  backupLocations:
  - velero:
      provider: aws
      default: true
      credential:
        name: cloud-credentials
        key: cloud
      config:
        region: us-east-1
        s3Url: https://s3.example.com  # SAME as source
        s3ForcePathStyle: "true"
      objectStorage:
        bucket: openstack-dr-backups  # SAME as source
        prefix: prod-cluster-1         # SAME as source
EOF
```

### Step 2: Verify Backup Visibility

```bash
# Wait for Velero to sync
oc wait --for=condition=Available dpa/velero -n openshift-adp --timeout=300s

# List backups (should show backups from source cluster)
oc get backups -n openshift-adp

# Should see: openstack-dr-full (or your backup name)

# Describe backup to verify
oc describe backup openstack-dr-full -n openshift-adp
```

### Step 3: Prepare Target Namespace

```bash
# Create openstack namespace
oc create namespace openstack

# Apply any required labels
oc label namespace openstack \
  pod-security.kubernetes.io/enforce=privileged \
  pod-security.kubernetes.io/audit=privileged \
  pod-security.kubernetes.io/warn=privileged

# Create any prerequisite resources (e.g., network configuration)
```

### Step 4: Restore OpenStack Deployment

#### Option A: Single Full Restore

If using a single backup with Restic:

```yaml
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-dr-restore
  namespace: openshift-adp
spec:
  backupName: openstack-dr-full

  includedNamespaces:
  - openstack

  # Restore PVCs from object storage
  restorePVs: true

  # Remove problematic metadata
  resourceModifiers:
  - conditions: {}
    patches:
    - operation: remove
      path: "/metadata/ownerReferences"
    - operation: remove
      path: "/metadata/annotations/kubectl.kubernetes.io~1last-applied-configuration"

  # Storage class mapping (if needed)
  # storageClassMapping:
  #   old-storage-class: new-storage-class

  # Namespace mapping (if restoring to different namespace)
  # namespaceMapping:
  #   openstack: openstack-restored
```

Apply the restore:

```bash
oc apply -f restore-dr.yaml

# Watch restore progress
oc get restore -n openshift-adp -w

# Check restore details
velero restore describe openstack-dr-restore --details

# Check for errors
velero restore logs openstack-dr-restore
```

#### Option B: Ordered Restore (Adapted POC Approach)

If using the two-backup approach, restore in order:

**Step 4a: Restore PVCs First**

```yaml
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-dr-restore-pvcs
  namespace: openshift-adp
spec:
  backupName: openstack-dr-pvcs

  includedNamespaces:
  - openstack

  restorePVs: true

  resourceModifiers:
  - conditions: {}
    patches:
    - operation: remove
      path: "/metadata/ownerReferences"
    - operation: remove
      path: "/metadata/annotations/kubectl.kubernetes.io~1last-applied-configuration"
```

```bash
oc apply -f restore-dr-pvcs.yaml

# Wait for PVC restore to complete
oc wait --for=condition=Completed restore/openstack-dr-restore-pvcs \
  -n openshift-adp --timeout=1800s

# Verify PVCs are created and bound
oc get pvc -n openstack
```

**Step 4b: Restore Resources**

Then use the same restore order as the POC (orders 10, 20, 30, 40):

```yaml
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-dr-restore-10-foundation
  namespace: openshift-adp
spec:
  backupName: openstack-dr-resources

  includedNamespaces:
  - openstack

  labelSelector:
    matchLabels:
      openstack.org/backup-restore: "true"
      openstack.org/backup-restore-order: "10"

  resourceModifiers:
  - conditions: {}
    patches:
    - operation: remove
      path: "/metadata/ownerReferences"
    - operation: remove
      path: "/metadata/annotations/kubectl.kubernetes.io~1last-applied-configuration"
```

Repeat for orders 20, 30, 40, 60 (see [restore examples](restore/)).

### Step 5: Post-Restore Configuration

#### A. Verify Resource Restoration

```bash
# Check OpenStackControlPlane
oc get openstackcontrolplane -n openstack

# Check secrets and configmaps
oc get secrets,configmaps -n openstack

# Check PVCs are bound
oc get pvc -n openstack
```

#### B. Handle ControlPlane Deployment Stage

If the ControlPlane was restored with `deployment-stage: infrastructure-only` annotation:

```bash
# Wait for infrastructure to be ready
oc get pods -n openstack | grep galera
oc get pods -n openstack | grep rabbitmq

# Perform manual database restore (see next step)

# Remove deployment-stage annotation to trigger full deployment
CTLPLANE_NAME=$(oc get openstackcontrolplane -n openstack -o jsonpath='{.items[0].metadata.name}')
oc annotate openstackcontrolplane $CTLPLANE_NAME \
  -n openstack \
  openstack.org/deployment-stage-
```

#### C. Restore MariaDB Databases

The GaleraBackup PVCs are restored, but databases need to be restored from backups:

```bash
# List GaleraBackup CRs
oc get galerabackup -n openstack

# Create GaleraRestore for main cell
cat <<EOF | oc apply -f -
apiVersion: mariadb.openstack.org/v1beta1
kind: GaleraRestore
metadata:
  name: openstackrestore
  namespace: openstack
spec:
  backupSource: openstack
EOF

# Wait for restore pod
oc wait --for=condition=Ready pod -l job-name=openstackrestore \
  -n openstack --timeout=300s

# Run restore script
docs/dev/scripts/restore-galera-latest.sh openstackrestore openstack

# For multi-cell deployments, repeat for each cell
cat <<EOF | oc apply -f -
apiVersion: mariadb.openstack.org/v1beta1
kind: GaleraRestore
metadata:
  name: openstackrestorecell1
  namespace: openstack
spec:
  backupSource: openstack-cell1
EOF

docs/dev/scripts/restore-galera-latest.sh openstackrestorecell1 openstack
```

#### D. Verify Database Restoration

```bash
# Check databases are present
oc exec -it galera-0 -n openstack -- mysql -e "SHOW DATABASES;"

# Should see: nova, nova_cell0, nova_cell1, cinder, glance, etc.

# Check table counts (should match source)
oc exec -it galera-0 -n openstack -- mysql nova -e "SHOW TABLES;" | wc -l
```

#### E. Update Network Configuration

If the DR cluster has different network configuration:

```bash
# Update NetworkAttachmentDefinitions if needed
oc edit nad <nad-name> -n openstack

# Update IP ranges, interfaces, etc. to match DR environment
```

#### F. Update External Endpoints

If using different external IPs or hostnames:

```bash
# Update OpenStackControlPlane external endpoints
oc edit openstackcontrolplane -n openstack

# Update spec.*.externalEndpoints or spec.*.override.service annotations
```

### Step 6: Validate OpenStack Services

```bash
# Watch pods come online
oc get pods -n openstack -w

# Check ControlPlane status
oc get openstackcontrolplane -n openstack -o yaml

# Verify services are ready
oc get openstackcontrolplane -n openstack \
  -o jsonpath='{.items[0].status.conditions[?(@.type=="Ready")].status}'
# Should output: True

# Check individual service readiness
oc get openstackcontrolplane -n openstack \
  -o jsonpath='{.items[0].status.conditions[*].type}' | tr ' ' '\n'
```

### Step 7: Restore DataPlane (if applicable)

```yaml
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-dr-restore-60-dataplane
  namespace: openshift-adp
spec:
  backupName: openstack-dr-resources

  includedNamespaces:
  - openstack

  labelSelector:
    matchLabels:
      openstack.org/backup-restore: "true"
      openstack.org/backup-restore-order: "60"

  resourceModifiers:
  - conditions: {}
    patches:
    - operation: remove
      path: "/metadata/ownerReferences"
    - operation: remove
      path: "/metadata/annotations/kubectl.kubernetes.io~1last-applied-configuration"
```

**Note**: DataPlane nodes may require reconfiguration if:
- SSH keys changed
- Node IP addresses changed
- Network configuration differs

### Step 8: OpenStack Validation

```bash
# Get keystone endpoint
KEYSTONE_URL=$(oc get openstackcontrolplane -n openstack \
  -o jsonpath='{.items[0].status.apiEndpoints.keystone.public}')

# Get admin credentials
ADMIN_PASSWORD=$(oc get secret openstack-admin-password -n openstack \
  -o jsonpath='{.data.password}' | base64 -d)

# Test OpenStack CLI
openstack --os-auth-url $KEYSTONE_URL \
  --os-username admin \
  --os-password $ADMIN_PASSWORD \
  --os-project-name admin \
  --os-user-domain-name default \
  --os-project-domain-name default \
  server list

# Check Glance images restored
openstack image list

# Check networks, volumes, etc.
openstack network list
openstack volume list
```

## DR Considerations and Challenges

### Storage Class Mapping

If DR cluster has different storage classes:

```yaml
apiVersion: velero.io/v1
kind: Restore
spec:
  backupName: openstack-dr-full

  # Map old storage class to new
  storageClassMapping:
    ocs-storagecluster-ceph-rbd: local-storage
    ocs-storagecluster-cephfs: nfs-storage
```

**Important**: Storage class characteristics must be compatible (RWO, RWX, block vs filesystem).

### PVC Size Limitations

If DR cluster storage has size constraints:

- Cannot reduce PVC size during restore
- Ensure DR cluster can accommodate PVC sizes
- May need to clean up data before backup

### Node Affinity and Taints

PVCs with node affinity may fail to bind if:
- DR cluster has different node labels
- Storage topology differs

**Solution**: Remove node affinity from PVCs during restore:

```yaml
resourceModifiers:
- conditions:
    groupResource: persistentvolumeclaims
  patches:
  - operation: remove
    path: "/spec/nodeAffinity"
```

### Service IPs and Load Balancers

- ClusterIP services will get new IPs
- LoadBalancer services may need reconfiguration
- Update DNS records to point to new endpoints

### Certificates and Keys

- SSL/TLS certificates may need updating if hostnames changed
- Regenerate certificates if bound to old cluster

### Database Considerations

**GaleraBackup PVC Restore:**
- PVCs restored contain backup files, not live databases
- Must run GaleraRestore to populate databases
- Database restore can take significant time for large databases

**Data Consistency:**
- Ensure backup was taken during low-activity period
- Consider application-level consistency (backup hooks)

### External Service Dependencies

Services that depend on external systems:
- Ceph/Cinder backends
- External networking (provider networks)
- DNS servers
- NTP servers
- LDAP/AD integration

Update configuration to match DR environment.

### Compute Node Registration

If using bare metal DataPlane nodes:
- Nodes may need re-registration
- SSH connectivity must be verified
- Ansible inventory may need updates

## Testing DR Procedures

### Periodic DR Drills

1. **Schedule regular DR tests** (quarterly recommended)
2. **Use separate DR test cluster** (don't impact production DR site)
3. **Document restore time objectives** (RTO)
4. **Measure actual restore times**
5. **Identify and address bottlenecks**

### DR Test Checklist

- [ ] Verify backup is accessible from DR site
- [ ] Restore completes without errors
- [ ] All PVCs bound successfully
- [ ] Database restoration successful
- [ ] All pods running and ready
- [ ] OpenStack API endpoints accessible
- [ ] Sample workloads can be created
- [ ] Data integrity validated (compare with source)
- [ ] Performance acceptable
- [ ] Network connectivity verified
- [ ] External dependencies configured
- [ ] Document any issues encountered
- [ ] Calculate actual RTO/RPO metrics

### Automated DR Validation

Consider creating a validation script:

```bash
#!/bin/bash
# dr-validate.sh

set -e

echo "Starting DR validation..."

# Check namespace exists
oc get namespace openstack

# Check ControlPlane exists and is ready
oc wait --for=condition=Ready openstackcontrolplane --all -n openstack --timeout=1800s

# Check critical pods are running
EXPECTED_PODS=("galera-0" "rabbitmq-server-0" "keystone" "glance")
for pod in "${EXPECTED_PODS[@]}"; do
  oc wait --for=condition=Ready pod -l app=$pod -n openstack --timeout=300s
done

# Check PVCs are bound
UNBOUND=$(oc get pvc -n openstack -o json | jq -r '.items[] | select(.status.phase != "Bound") | .metadata.name')
if [ -n "$UNBOUND" ]; then
  echo "ERROR: Unbound PVCs found: $UNBOUND"
  exit 1
fi

# Validate OpenStack API
openstack --os-cloud admin server list > /dev/null

echo "DR validation completed successfully!"
```

## Monitoring and Alerting

### Backup Monitoring

Monitor backup health:

```bash
# Check last backup status
oc get backup -n openshift-adp \
  --sort-by=.metadata.creationTimestamp

# Alert if backup fails
oc get backup openstack-dr-full -n openshift-adp \
  -o jsonpath='{.status.phase}'
# Should be: Completed

# Alert if backup is too old
LAST_BACKUP=$(oc get backup openstack-dr-full -n openshift-adp \
  -o jsonpath='{.status.completionTimestamp}')
# Calculate age and alert if > 24 hours
```

### Backup Size Tracking

```bash
# Check backup size
oc get backup openstack-dr-full -n openshift-adp \
  -o jsonpath='{.status.volumeSnapshotsAttempted}'

# Monitor object storage usage
# Use S3 API or storage provider monitoring
```

### Integration with Monitoring Stack

Create PrometheusRule for backup monitoring:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: velero-backup-alerts
  namespace: openshift-adp
spec:
  groups:
  - name: velero.rules
    interval: 30s
    rules:
    - alert: VeleroBackupFailure
      expr: |
        velero_backup_failure_total{schedule="openstack-dr-daily"} > 0
      for: 5m
      labels:
        severity: critical
      annotations:
        summary: "Velero backup failed"
        description: "OpenStack DR backup has failed"

    - alert: VeleroBackupTooOld
      expr: |
        time() - velero_backup_last_successful_timestamp{schedule="openstack-dr-daily"} > 86400
      for: 1h
      labels:
        severity: warning
      annotations:
        summary: "Velero backup is outdated"
        description: "Last successful backup is older than 24 hours"
```

## Backup-to-PVC Enhancement and DR

The [backup-to-PVC enhancement](backup-restore-pvc-enhancement.md) needs adaptation for DR scenarios:

### Approach 1: Hybrid Strategy

Use both PVC backup and object storage:

```yaml
apiVersion: backup.openstack.org/v1beta1
kind: OpenStackBackup
spec:
  # For fast in-cluster restore
  backupToPVC: true
  pvcName: openstack-backup-resources

  # For DR portability
  backupToObjectStorage: true
  storageLocation: default
  useRestic: true  # Backup the PVC itself to object storage
```

**Workflow:**
1. OpenStackBackup controller serializes resources to PVC
2. Velero backup includes the resource backup PVC using Restic
3. DR restore: Velero restores the resource backup PVC
4. OpenStackRestore controller reads from restored PVC

### Approach 2: PVC Backup for In-Cluster, Velero for DR

Maintain two separate backup mechanisms:

**In-Cluster Backups** (daily, fast restore):
- Use OpenStackBackup to PVC
- Use OADP with CSI snapshots

**DR Backups** (weekly, external storage):
- Use Velero with Restic
- Full backup to object storage

### Approach 3: Enhanced Controller with Dual Targets

Extend OpenStackBackup controller to write to both:

```go
func (r *OpenStackBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // 1. Serialize resources
    resources := r.discoverAndSerializeResources(ctx)

    // 2. Write to PVC (for in-cluster restore)
    if backup.Spec.BackupToPVC {
        r.writeToBackupPVC(ctx, resources)
    }

    // 3. Write to object storage (for DR)
    if backup.Spec.BackupToObjectStorage {
        r.writeToObjectStorage(ctx, resources, backup.Spec.StorageLocation)
    }

    return ctrl.Result{}, nil
}
```

## Best Practices

1. **Test restores regularly** - Untested backups are not backups
2. **Use external object storage** - Don't rely on cluster-local storage for DR
3. **Document RTO/RPO requirements** - Know your recovery time and data loss tolerance
4. **Automate where possible** - Reduce human error during stress
5. **Monitor backup health** - Alert on backup failures immediately
6. **Version compatibility** - Test restores to different cluster versions
7. **Secure backup data** - Encrypt data at rest and in transit
8. **Access control** - Limit who can delete or modify backups
9. **Geographic distribution** - Store backups in different region/datacenter
10. **Maintain runbooks** - Document step-by-step procedures with screenshots

## Recovery Time Objectives (RTO)

Estimated restore times (will vary based on data size and network):

| Component | Estimated Time | Notes |
|-----------|---------------|-------|
| OADP Setup in DR Cluster | 15-30 min | One-time setup |
| Backup Sync/Discovery | 5-10 min | Velero lists backups from S3 |
| PVC Restore (Restic) | 30-120 min | Depends on data size, 10-50 GB/hour typical |
| Resource Restore | 5-15 min | Fast, just API objects |
| Database Restore | 15-60 min | Depends on database size |
| Service Initialization | 15-30 min | Pods starting, reconciliation |
| Validation and Testing | 30-60 min | Verify OpenStack functionality |
| **Total RTO** | **2-5 hours** | For typical deployment |

## Recovery Point Objectives (RPO)

RPO depends on backup frequency:

| Backup Schedule | RPO | Use Case |
|----------------|-----|----------|
| Hourly | 1 hour | Critical production (high cost) |
| Every 6 hours | 6 hours | Production (balanced) |
| Daily | 24 hours | Development/testing |
| Weekly | 7 days | Long-term archives |

## Troubleshooting

### Backup Issues

**Backup stuck in "InProgress":**
```bash
# Check Velero logs
oc logs -n openshift-adp deployment/velero

# Check Restic/Kopia daemonset logs
oc logs -n openshift-adp ds/node-agent
```

**Restic backup slow:**
- Check network bandwidth to object storage
- Consider using Kopia (faster than Restic)
- Reduce backup frequency or use incremental backups

**PVC backup fails:**
```bash
# Check PVC has annotation
oc get pvc <pvc-name> -n openstack -o yaml | grep backup.velero.io

# Check Restic pod on node
NODE=$(oc get pod <app-pod> -n openstack -o jsonpath='{.spec.nodeName}')
oc get pod -n openshift-adp -l name=node-agent --field-selector spec.nodeName=$NODE
```

### Restore Issues

**Restore stuck:**
```bash
# Check restore logs
velero restore logs openstack-dr-restore

# Check for errors
oc get restore openstack-dr-restore -n openshift-adp -o yaml
```

**PVC stuck in "Pending" or "Waiting for first consumer":**

This is a common issue with local storage (LVMS/TopoLVM) or any storage class with `volumeBindingMode: WaitForFirstConsumer`.

```bash
# Check PVC status
$ oc get pvc -n openstack
NAME                STATUS    VOLUME
mysql-db-galera-0  Pending

$ oc describe pvc mysql-db-galera-0 -n openstack
Events:
  Type     Reason                Age   Message
  ----     ------                ----  -------
  Normal   WaitForFirstConsumer  30s   waiting for first consumer to be created before binding
```

**What's happening:**
- Storage class has `volumeBindingMode: WaitForFirstConsumer`
- PVC won't bind until a Pod tries to use it
- Common with local storage (LVMS, TopoLVM, LocalPV)
- Storage needs to know which node the pod will run on

**Check your storage class:**
```bash
# Check volumeBindingMode
oc get storageclass <storage-class-name> -o yaml | grep volumeBindingMode

# Example - LVMS/TopoLVM (local storage)
volumeBindingMode: WaitForFirstConsumer  # ← Causes this issue

# Example - Some storage with immediate binding
volumeBindingMode: Immediate  # ← Binds immediately
```

**Solution 1: Using Restic/Kopia (Recommended - Works Automatically)**

With Restic/Kopia, this is **handled automatically**:

1. Velero creates empty PVC (stays in Pending/WaitForFirstConsumer)
2. Velero creates temporary restore pod to mount the PVC
3. PVC binding is triggered (pod is the "consumer")
4. For local storage (LVMS), volume is created on the node where pod scheduled
5. node-agent writes data from S3 to the PVC
6. Temporary pod is deleted
7. Your application pods can use the restored PVC

**No manual intervention needed!**

```yaml
# This works automatically with WaitForFirstConsumer storage
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-dr-restore
spec:
  backupName: openstack-dr-full
  includedNamespaces:
  - openstack
  restorePVs: true  # Restic/Kopia handles WaitForFirstConsumer automatically
```

**How Restic/Kopia chooses which node (for LVMS/local storage):**

For local storage, the node is selected automatically by Kubernetes scheduler:

```bash
# During restore:
# 1. Velero creates PVC (Pending state)
# 2. Velero creates restore pod with PVC mount
# 3. Scheduler looks at nodes with available LVMS capacity
# 4. Scheduler selects node with enough free space
# 5. LVMS CSI driver creates volume on that node
# 6. PVC binds to that node
# 7. node-agent on that node writes data from S3

# The node might be DIFFERENT from original cluster
# This is OK - data is restored from S3, not from original volume
```

**Check restore pod and node selection:**
```bash
# Watch restore pods being created
oc get pods -n openstack -w

# Check which node the restore pod landed on
oc get pods -n openstack -o wide

# Verify PVC bound after restore pod created
oc get pvc -n openstack
```

**Solution 2: Using CSI Snapshots (More Complex)**

With CSI snapshots, you need to restore PVCs together with pods:

```yaml
# Don't restore only PVCs - also restore StatefulSets/Deployments
apiVersion: velero.io/v1
kind: Restore
spec:
  backupName: openstack-backup

  # Include both PVCs and the workloads that use them
  includedResources:
  - persistentvolumeclaims
  - statefulsets
  - deployments
```

**Solution 3: Temporary Storage Class with Immediate Binding**

For testing, create temporary storage class:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: lvms-immediate  # Temporary
provisioner: topolvm.io  # Same as original
volumeBindingMode: Immediate  # ← Bind immediately
allowVolumeExpansion: true
parameters:
  # Same parameters as original storage class
```

Use storage class mapping during restore:

```yaml
apiVersion: velero.io/v1
kind: Restore
spec:
  backupName: openstack-dr-full
  storageClassMapping:
    lvms-provisioner: lvms-immediate  # Map to Immediate binding
```

**Important Notes for LVMS/Local Storage:**

- ✅ **Restic/Kopia is REQUIRED for local storage DR** (CSI snapshots don't provide DR)
- ✅ **Node selection is automatic** - Kubernetes scheduler picks node with capacity
- ✅ **Data comes from S3** - original node/volume doesn't matter
- ⚠️ **Ensure sufficient capacity** - DR cluster nodes need free LVMS space
- ⚠️ **Node might differ** - Restored PVC might be on different node than original

**PVC not binding (other reasons):**
- Check storage class exists in DR cluster
- Verify storage class mapping is correct
- Check node affinity requirements (if using pod affinity)
- Verify CSI driver is installed and running
- Check storage backend has available capacity

**Pods stuck in Init or CrashLoopBackOff:**
- Check PVC bindings
- Verify Secrets and ConfigMaps restored
- Check for missing dependencies

**Database restore fails:**
- Verify GaleraBackup PVC is bound
- Check backup files exist in PVC
- Verify sufficient disk space

### Post-Restore Issues

**Services not accessible:**
- Verify Service and Route objects created
- Check if LoadBalancer IPs assigned
- Update DNS if endpoints changed

**Authentication failures:**
- Verify Keystone pod running
- Check database connectivity
- Verify admin credentials Secret restored

**Data inconsistencies:**
- Backup may have been taken during active writes
- Consider using backup hooks for consistency
- Validate data against known good state

## Conclusion

Disaster recovery requires careful planning, testing, and documentation. Key takeaways:

1. **Use Restic/Kopia with Velero** for true DR capability (external object storage)
2. **Test restore procedures regularly** in a non-production environment
3. **Document cluster-specific configurations** that need adjustment in DR
4. **Monitor backup health** and alert on failures
5. **Measure and track RTO/RPO** to validate recovery capabilities
6. **Keep runbooks updated** as the deployment evolves

The combination of OADP/Velero with external object storage provides a robust foundation for OpenStack disaster recovery on Kubernetes.

## References

- [Backup/Restore POC Implementation](backup-restore-webhook-implementation.md)
- [OADP Documentation](https://docs.openshift.com/container-platform/latest/backup_and_restore/index.html)
- [Velero Documentation](https://velero.io/docs/)
- [Backup-to-PVC Enhancement Proposal](backup-restore-pvc-enhancement.md)
- [OpenStack Backup Scripts](../scripts/)
