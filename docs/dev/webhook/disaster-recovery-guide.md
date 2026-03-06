# Disaster Recovery Guide for OpenStack on Kubernetes

## Overview

This guide covers disaster recovery (DR) scenarios where an OpenStack deployment needs to be restored to a **new cluster** due to catastrophic failure of the original cluster. This is different from in-cluster backup/restore where the cluster infrastructure remains intact.

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

### CSI Volume Snapshots (Current POC)

The current POC implementation uses CSI volume snapshots:

**How CSI Snapshots Work with Velero:**
- Velero stores VolumeSnapshot **metadata** (YAML manifests) in S3 object storage
- The actual snapshot **data** remains in the storage backend (Ceph, AWS EBS, etc.)
- During restore, Velero creates VolumeSnapshot objects which reference the snapshots in the storage backend

**Limitations for Disaster Recovery:**

| What's Backed Up | Location | Survives Cluster Loss? |
|-----------------|----------|----------------------|
| VolumeSnapshot metadata (YAML) | S3 object storage | ✅ Yes |
| Actual snapshot data (PVC contents) | Storage backend (Ceph/EBS/etc) | ❌ No (unless storage is replicated separately) |

**Problem**: If the entire cluster AND storage backend are lost (datacenter failure), the CSI snapshots are gone even though the metadata is in S3.

**Use Case**: CSI snapshots work well for:
- Same-cluster restores (rollback, recovery from operator issues)
- When storage backend has independent replication/backup
- Fast backup/restore operations (storage-level snapshots are quick)

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

### Comparison: CSI Snapshots vs Restic/Kopia

| Aspect | CSI Volume Snapshots | Restic/Kopia File-Level Backup |
|--------|---------------------|-------------------------------|
| **Backup Speed** | ✅ Fast (storage-level snapshot) | ⚠️ Slower (file-by-file copy to S3) |
| **Restore Speed** | ✅ Fast (storage-level restore) | ⚠️ Slower (download from S3) |
| **Data Location** | ❌ Storage backend (Ceph/EBS/etc) | ✅ S3 object storage |
| **Survives Cluster Loss** | ⚠️ Only if storage backend survives | ✅ Yes (data in external S3) |
| **Survives Datacenter Loss** | ❌ No (unless storage replicated separately) | ✅ Yes (S3 in different region) |
| **DR to Different Storage Backend** | ❌ Very difficult/impossible | ✅ Yes (storage-agnostic) |
| **Storage Overhead** | ✅ Minimal (delta snapshots) | ⚠️ Full data in S3 |
| **Pod Requirements** | ✅ None (CSI driver handles it) | ℹ️ node-agent daemonset (OADP-managed) |
| **Application Downtime** | ✅ None required | ✅ None required (node-agent accesses volumes) |
| **S3 Bandwidth Usage** | ✅ Minimal (metadata only) | ⚠️ High (full data transfer) |
| **Cross-Provider DR** | ❌ No (tied to storage backend) | ✅ Yes (AWS → GCP, on-prem → cloud, etc.) |
| **Best For** | Same-cluster restore, fast recovery | True DR, cross-cluster, cross-datacenter |

**Recommendation for OpenStack DR:**
- **In-cluster backups**: Use CSI snapshots (fast, efficient) - Current POC approach
- **Disaster recovery backups**: Use Restic/Kopia (portable, survives datacenter loss)
- **Production**: Implement both - CSI for quick recovery, Restic/Kopia for true DR

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

**PVC not binding:**
- Check storage class exists in DR cluster
- Verify storage class mapping is correct
- Check node affinity requirements

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
