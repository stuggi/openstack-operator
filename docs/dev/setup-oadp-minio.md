# Setting Up OADP with MinIO on OpenShift

This guide shows how to set up OADP (OpenShift API for Data Protection) using MinIO as the S3-compatible storage backend. This approach does **NOT** use ODF (OpenShift Data Foundation).

## Overview

- **OADP**: OpenShift API for Data Protection - provides backup and restore capabilities using Velero
- **MinIO**: S3-compatible object storage that runs on OpenShift
- **Storage Backend**: MinIO (not ODF)

## Prerequisites

- OpenShift cluster with cluster-admin access
- `oc` CLI tool installed and configured
- Sufficient storage for MinIO (PVs available in your cluster)

## Quick Start (Using Ansible Playbooks)

For automated setup, use the provided Ansible playbooks:

```bash
# 1. Deploy MinIO (with custom storage class if needed)
ansible-playbook playbooks/setup-minio.yaml
# Or with custom parameters:
ansible-playbook playbooks/setup-minio.yaml -e minio_storage_class=local-storage -e minio_storage_size=100Gi

# 2. Install and configure OADP (use credentials from MinIO setup output)
ansible-playbook playbooks/setup-oadp.yaml -e minio_access_key_id=<ACCESS_KEY_ID> -e minio_secret_access_key=<SECRET_ACCESS_KEY>
```

The playbooks will:
- Deploy MinIO with persistent storage
- Create the Velero bucket
- Generate service account credentials
- Install the OADP operator
- Configure DataProtectionApplication with MinIO backend
- Verify the installation

For manual step-by-step setup, continue with the sections below.

## Part 1: Deploy MinIO (Manual Setup)

MinIO will serve as the S3-compatible storage backend for OADP backups.

### Step 1: Create MinIO Namespace

```bash
oc create namespace minio
```

### Step 2: Create MinIO Deployment

Create a file `minio-deployment.yaml`:

```yaml
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: minio-pvc
  namespace: minio
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi
---
apiVersion: v1
kind: Secret
metadata:
  name: minio-credentials
  namespace: minio
type: Opaque
stringData:
  MINIO_ROOT_USER: minio
  MINIO_ROOT_PASSWORD: minio123  # Change this in production!
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: minio
  namespace: minio
spec:
  selector:
    matchLabels:
      app: minio
  strategy:
    type: Recreate
  template:
    metadata:
      labels:
        app: minio
    spec:
      containers:
      - name: minio
        image: quay.io/minio/minio:latest
        args:
        - server
        - /data
        - --console-address
        - :9001
        env:
        - name: MINIO_ROOT_USER
          valueFrom:
            secretKeyRef:
              name: minio-credentials
              key: MINIO_ROOT_USER
        - name: MINIO_ROOT_PASSWORD
          valueFrom:
            secretKeyRef:
              name: minio-credentials
              key: MINIO_ROOT_PASSWORD
        ports:
        - containerPort: 9000
          name: api
        - containerPort: 9001
          name: console
        volumeMounts:
        - name: data
          mountPath: /data
        livenessProbe:
          httpGet:
            path: /minio/health/live
            port: 9000
          initialDelaySeconds: 30
          periodSeconds: 20
        readinessProbe:
          httpGet:
            path: /minio/health/ready
            port: 9000
          initialDelaySeconds: 30
          periodSeconds: 20
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: minio-pvc
---
apiVersion: v1
kind: Service
metadata:
  name: minio
  namespace: minio
spec:
  type: ClusterIP
  ports:
  - port: 9000
    targetPort: 9000
    name: api
  - port: 9001
    targetPort: 9001
    name: console
  selector:
    app: minio
---
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: minio-console
  namespace: minio
spec:
  to:
    kind: Service
    name: minio
  port:
    targetPort: console
  tls:
    termination: edge
    insecureEdgeTerminationPolicy: Redirect
---
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: minio-api
  namespace: minio
spec:
  to:
    kind: Service
    name: minio
  port:
    targetPort: api
  tls:
    termination: edge
    insecureEdgeTerminationPolicy: Redirect
```

Deploy MinIO:

```bash
oc apply -f minio-deployment.yaml
```

### Step 3: Verify MinIO Deployment

Wait for MinIO to be ready:

```bash
oc wait --for=condition=available --timeout=300s deployment/minio -n minio
```

Check MinIO pod status:

```bash
oc get pods -n minio
```

**MinIO Web Console Access:**

MinIO provides a web console for management and verification. To access it:

```bash
echo "MinIO Console: https://$(oc get route minio-console -n minio -o jsonpath='{.spec.host}')"
echo "Credentials: minio / minio123"
```

The web console can be used to browse buckets, verify backups, and manage access. For initial setup, use the CLI commands below.

### Step 4: Create Backup Bucket in MinIO

Using MinIO client (`mc`):

Install MinIO client:

```bash
# Download mc
curl -o /tmp/mc https://dl.min.io/client/mc/release/linux-amd64/mc
chmod +x /tmp/mc

# Configure mc to use your MinIO instance
MINIO_API_URL=$(oc get route minio-api -n minio -o jsonpath='{.spec.host}')
/tmp/mc alias set minio https://${MINIO_API_URL} minio minio123

# Create bucket
/tmp/mc mb minio/velero
```

### Step 5: Create MinIO Service Account for OADP

Create a service account for OADP to use:

```bash
# This creates a service account with full access
# Save the output Access Key and Secret Key
MINIO_API_URL=$(oc get route minio-api -n minio -o jsonpath='{.spec.host}')
/tmp/mc admin user svcacct add minio minio
```

**Important**: Save the generated **Access Key** and **Secret Key** from the output - you'll need these for OADP configuration.

## Part 2: Install and Configure OADP Operator

### Step 1: Install OADP Operator

Install the OADP Operator from OperatorHub:

```bash
cat <<EOF | oc apply -f -
apiVersion: v1
kind: Namespace
metadata:
  name: openshift-adp
---
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: openshift-adp-operator-group
  namespace: openshift-adp
spec:
  targetNamespaces:
  - openshift-adp
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: redhat-oadp-operator
  namespace: openshift-adp
spec:
  channel: stable-1.4
  installPlanApproval: Automatic
  name: redhat-oadp-operator
  source: redhat-operators
  sourceNamespace: openshift-marketplace
EOF
```

Wait for the operator to be ready:

```bash
oc wait --for=condition=ready --timeout=300s pod -l control-plane=controller-manager -n openshift-adp
```

### Step 2: Create Cloud Credentials Secret

Create a credentials file for MinIO (using the service account credentials from MinIO):

```bash
cat <<EOF > /tmp/credentials-velero
[default]
aws_access_key_id=<MINIO_ACCESS_KEY>
aws_secret_access_key=<MINIO_SECRET_KEY>
EOF
```

**Important**: Replace `<MINIO_ACCESS_KEY>` and `<MINIO_SECRET_KEY>` with the actual credentials from the MinIO service account you created.

Create the secret:

```bash
oc create secret generic cloud-credentials \
  --from-file cloud=/tmp/credentials-velero \
  -n openshift-adp

# Clean up the file
rm /tmp/credentials-velero
```

### Step 3: Create DataProtectionApplication (DPA)

Get the MinIO API endpoint:

```bash
MINIO_API_URL=$(oc get route minio-api -n minio -o jsonpath='{.spec.host}')
echo "MinIO API URL: https://${MINIO_API_URL}"
```

Create the DataProtectionApplication:

```bash
cat <<EOF | oc apply -f -
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
    restic:
      enable: true
  backupLocations:
  - velero:
      provider: aws
      default: true
      objectStorage:
        bucket: velero
        prefix: rhoso
      config:
        region: minio
        s3ForcePathStyle: "true"
        s3Url: https://${MINIO_API_URL}
        insecureSkipTLSVerify: "true"
      credential:
        name: cloud-credentials
        key: cloud
  snapshotLocations:
  - velero:
      provider: aws
      config:
        region: minio
        profile: default
EOF
```

**Notes**:
- `s3ForcePathStyle: "true"` is required for MinIO
- `insecureSkipTLSVerify: "true"` is used because the MinIO route uses OpenShift's self-signed certificate. In production, you may want to configure proper TLS.
- This configuration uses **Restic** for volume backups (not CSI snapshots, which would require ODF)

### Step 4: Verify OADP Installation

Check that all OADP pods are running:

```bash
oc get pods -n openshift-adp
```

You should see:
- `openshift-adp-controller-manager-*`
- `velero-*`
- `restic-*` (one per node)

Check the BackupStorageLocation:

```bash
oc get backupstoragelocation -n openshift-adp
```

The status should show `Available` or `Phase: Available`:

```bash
oc get backupstoragelocation -n openshift-adp -o yaml
```

Check Velero logs for any errors:

```bash
oc logs -n openshift-adp deployment/velero
```

## Part 3: Test OADP Backup and Restore (Optional)

This section demonstrates how to test OADP backup and restore functionality with a simple application. You can skip this if you want to proceed directly to using OADP for OpenStack backups.

### Step 1: Create a Test Application

```bash
oc create namespace test-backup
oc run nginx --image=nginx -n test-backup
oc expose pod nginx --port=80 -n test-backup
oc create configmap test-data --from-literal=key=value -n test-backup
```

### Step 2: Create a Backup

```bash
cat <<EOF | oc apply -f -
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: test-backup
  namespace: openshift-adp
spec:
  includedNamespaces:
  - test-backup
  defaultVolumesToRestic: true
  storageLocation: velero-1
EOF
```

Watch backup progress:

```bash
oc get backup -n openshift-adp -w
```

Check backup details:

```bash
oc describe backup test-backup -n openshift-adp
```

Verify backup in MinIO:
1. Open MinIO console
2. Navigate to "Buckets" → "velero"
3. You should see backup data under `backups/test-backup/`

### Step 3: Delete the Test Application

```bash
oc delete namespace test-backup
```

### Step 4: Restore from Backup

```bash
cat <<EOF | oc apply -f -
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: test-restore
  namespace: openshift-adp
spec:
  backupName: test-backup
EOF
```

Watch restore progress:

```bash
oc get restore -n openshift-adp -w
```

Verify the namespace and resources are restored:

```bash
oc get all -n test-backup
oc get configmap test-data -n test-backup
```

### Step 5: Clean Up Test Resources

```bash
oc delete namespace test-backup
oc delete backup test-backup -n openshift-adp
oc delete restore test-restore -n openshift-adp
```

## Verifying Backups in MinIO

After creating backups with OADP, you can manually verify that backup data is stored in MinIO.

### Option 1: Using MinIO Web Console (GUI)

The easiest way to verify backups is through the MinIO web console:

```bash
# Get the MinIO console route
echo "MinIO Console: https://$(oc get route minio-console -n minio -o jsonpath='{.spec.host}')"

# Get MinIO credentials (if using playbook-created credentials)
echo "Username: minio"
echo "Password: minio123"
```

**Steps:**
1. Open the MinIO console URL in your browser
2. Log in with the MinIO root credentials
3. Navigate to **Buckets** → **velero**
4. Browse the backup structure:
   ```
   velero/
   └── rhoso/
       ├── backups/
       │   ├── openstack-volumes-20260225-140530/
       │   │   ├── openstack-volumes-20260225-140530.tar.gz
       │   │   ├── openstack-volumes-20260225-140530-logs.gz
       │   │   ├── openstack-volumes-20260225-140530-podvolumebackups.json.gz
       │   │   ├── openstack-volumes-20260225-140530-volumesnapshots.json.gz
       │   │   └── velero-backup.json
       │   └── <other-backups>/
       └── restic/
           └── openstack/
               └── <volume-data>/
   ```

5. **Verify backup files exist**:
   - `<backup-name>.tar.gz` - Main backup archive with Kubernetes resources
   - `<backup-name>-logs.gz` - Backup operation logs
   - `<backup-name>-podvolumebackups.json.gz` - Restic backup metadata
   - `velero-backup.json` - Backup CR metadata

6. **Check Restic data** (for PVC backups):
   - Navigate to `restic/openstack/` directory
   - You should see subdirectories for each PVC backup
   - Data is deduplicated and stored in Restic repository format

### Option 2: Using MinIO Client (mc) CLI

For automation or scripting, use the MinIO Client:

```bash
# Install mc (MinIO Client)
curl https://dl.min.io/client/mc/release/linux-amd64/mc \
  --create-dirs -o $HOME/bin/mc
chmod +x $HOME/bin/mc

# Configure mc with MinIO server
# Get MinIO API endpoint
MINIO_ENDPOINT=$(oc get route minio-api -n minio -o jsonpath='{.spec.host}')

# Set up mc alias (using root credentials)
mc alias set minio-oadp https://${MINIO_ENDPOINT} minio minio123 --insecure

# List velero bucket contents
mc ls minio-oadp/velero --insecure

# List all backups
mc ls minio-oadp/velero/rhoso/backups/ --insecure

# List specific backup contents
mc ls minio-oadp/velero/rhoso/backups/openstack-volumes-20260225-140530/ --insecure

# Check backup size
mc du minio-oadp/velero/rhoso/backups/openstack-volumes-20260225-140530/ --insecure

# Download a backup for inspection (optional)
mc cp minio-oadp/velero/rhoso/backups/openstack-volumes-20260225-140530/velero-backup.json \
  ./velero-backup.json --insecure
```

### Option 3: Using oc exec into MinIO Pod

Directly access MinIO from inside the cluster:

```bash
# Exec into MinIO pod
oc exec -it -n minio deployment/minio -- sh

# Inside the pod, use mc
mc alias set local http://localhost:9000 minio minio123

# List backups
mc ls local/velero/rhoso/backups/

# Check specific backup
mc ls local/velero/rhoso/backups/openstack-volumes-20260225-140530/

# Exit pod
exit
```

### What to Verify

After accessing MinIO, verify:

1. **Backup exists**: The backup directory with your backup name is present
2. **Backup is complete**: Check `velero-backup.json` for status:
   ```bash
   # Download and check backup metadata
   mc cp minio-oadp/velero/rhoso/backups/<backup-name>/velero-backup.json - --insecure | jq '.status.phase'
   # Should show: "Completed"
   ```

3. **Backup size is reasonable**:
   ```bash
   mc du minio-oadp/velero/rhoso/backups/<backup-name>/ --insecure
   # Compare with expected PVC sizes
   ```

4. **Restic data exists** (for PVC backups):
   ```bash
   mc ls minio-oadp/velero/rhoso/restic/openstack/ --insecure
   # Should show directories for each backed-up PVC
   ```

5. **Recent backups**: Verify backup timestamp matches when you created it
   ```bash
   mc ls minio-oadp/velero/rhoso/backups/ --insecure
   # Check modification times
   ```

### Backup Retention

Check backup retention and cleanup:

```bash
# List all backups sorted by date
mc ls --recursive minio-oadp/velero/rhoso/backups/ --insecure | sort

# Count total backups
mc ls minio-oadp/velero/rhoso/backups/ --insecure | wc -l

# Check total storage used
mc du minio-oadp/velero/ --insecure
```

Velero automatically deletes backups older than their TTL (time-to-live). Check the Backup CR for TTL:

```bash
oc get backup <backup-name> -n openshift-adp -o jsonpath='{.spec.ttl}'
```

## Troubleshooting

### BackupStorageLocation Shows "Unavailable"

Check Velero logs:

```bash
oc logs -n openshift-adp deployment/velero
```

Common issues:
- **Authentication failed**: Verify cloud-credentials secret contains correct MinIO access/secret keys
- **Connection refused**: Verify MinIO API URL is correct and accessible
- **TLS errors**: If using custom certificates, may need to configure certificate bundle
- **Bucket not found**: Verify the `velero` bucket exists in MinIO

Test MinIO connectivity from within the cluster:

```bash
oc run -it --rm debug --image=curlimages/curl --restart=Never -- \
  curl -v https://$(oc get route minio-api -n minio -o jsonpath='{.spec.host}')
```

### Restic Pods Not Running

Check node selector and tolerations if using specialized nodes:

```bash
oc get daemonset restic -n openshift-adp -o yaml
```

### Backup Fails with "Volume Snapshot" Errors

This indicates OADP is trying to use CSI snapshots. Ensure `defaultVolumesToRestic: true` is set in the Backup spec to use Restic instead:

```yaml
spec:
  defaultVolumesToRestic: true
```

Or configure DPA to default to Restic:

```yaml
spec:
  configuration:
    restic:
      enable: true
    velero:
      defaultVolumesToRestic: true
```

### MinIO Storage Full

Check MinIO storage usage:

```bash
oc get pvc -n minio
```

Expand PVC if needed:

```bash
oc patch pvc minio-pvc -n minio -p '{"spec":{"resources":{"requests":{"storage":"100Gi"}}}}'
```

## Production Recommendations

### MinIO Configuration

1. **Use external MinIO**: For production, consider running MinIO outside the OpenShift cluster or using a managed S3-compatible service
2. **High Availability**: Deploy MinIO in distributed mode with multiple nodes
3. **Persistent Storage**: Use high-performance storage class for MinIO PVCs
4. **Strong Credentials**: Use strong passwords and rotate credentials regularly
5. **TLS Certificates**: Configure proper TLS certificates instead of using insecure skip verify

### OADP Configuration

1. **Multiple Backup Locations**: Configure multiple backup locations for redundancy
2. **Backup Scheduling**: Use Schedule CRs for automated backups:

```yaml
apiVersion: velero.io/v1
kind: Schedule
metadata:
  name: daily-backup
  namespace: openshift-adp
spec:
  schedule: "0 2 * * *"  # Daily at 2 AM
  template:
    includedNamespaces:
    - openstack
    defaultVolumesToRestic: true
```

3. **Backup Retention**: Configure TTL for automatic cleanup:

```yaml
spec:
  template:
    ttl: 720h  # 30 days
```

4. **Resource Limits**: Set appropriate resource limits for Velero and Restic

5. **Monitoring**: Set up monitoring and alerts for backup failures

## References

- [OADP Documentation](https://docs.openshift.com/container-platform/latest/backup_and_restore/application_backup_and_restore/oadp-intro.html)
- [MinIO Documentation](https://min.io/docs/minio/kubernetes/upstream/)
- [Velero Documentation](https://velero.io/docs/)
