# RHOSO Backup & Restore - Demo Script

## Prerequisites
- OpenStack control plane deployed and healthy
- asciinema installed (`pip install asciinema`)

## Recording

```bash
asciinema rec demo-backup-restore.cast -t "RHOSO Backup & Restore Demo"
```

## Demo Flow

### 1. Show the environment (~1 min)

```bash
# Show the healthy control plane
oc get openstackcontrolplane -n openstack

# Show the OpenStackBackupConfig (auto-created by the operator)
oc get openstackbackupconfig -n openstack

# Show labeled resources
oc get secret -n openstack -l backup.openstack.org/restore=true --no-headers | wc -l
oc get configmap -n openstack -l backup.openstack.org/restore=true --no-headers | wc -l

# Show labeled PVCs
oc get pvc -n openstack -l backup.openstack.org/backup=true
```

### 2. Install dependencies - MinIO & OADP (~2 min, FAST-FORWARD)

```bash
cd docs/dev/backup-restore/role

# Install MinIO and OADP
ansible-playbook cifmw_backup_restore_test.yaml \
  -e backup=false -e cleanup=false -e restore=false

# Show MinIO is running
oc get pods -n minio
oc get routes -n minio

# Show OADP is running
oc get pods -n openshift-adp
oc get backupstoragelocation -n openshift-adp
```

### 3. Create backup (~3 min, FAST-FORWARD PVC backup wait)

```bash
# Run backup
ansible-playbook cifmw_backup_restore_test.yaml \
  -e install_deps=false -e cleanup=false -e restore=false

# Show the GaleraBackup CRs were created
oc get galerabackup -n openstack

# Show the OADP backups
oc get backup -n openshift-adp

# Note the backup timestamp for restore
# e.g., 20260325-101234
```

### 4. Simulate disaster - Cleanup (~1 min, FAST-FORWARD)

```bash
# Delete the control plane (simulates disaster)
ansible-playbook cifmw_backup_restore_test.yaml \
  -e install_deps=false -e backup=false -e restore=false

# Show everything is gone
oc get openstackcontrolplane -n openstack
oc get pods -n openstack
oc get secret -n openstack | wc -l
```

### 5. Restore (~5 min, FAST-FORWARD waits)

```bash
# Restore from backup
ansible-playbook cifmw_backup_restore_test.yaml \
  -e install_deps=false -e backup=false -e cleanup=false \
  -e backup_timestamp=<TIMESTAMP>

# --- FAST-FORWARD: PVC restore ---
# --- FAST-FORWARD: Wait for infrastructure (infra-only annotation) ---
# --- FAST-FORWARD: Wait for control plane ready ---
# --- FAST-FORWARD: EDPM deployment ---
```

### 6. Verify restored environment (~1 min)

```bash
# Show control plane is back
oc get openstackcontrolplane -n openstack

# Show pods are running
oc get pods -n openstack | head -20

# Show databases are restored
oc get galera -n openstack

# Show secrets are back
oc get secret osp-secret -n openstack

# Verify an OpenStack endpoint
oc exec -t openstackclient -- openstack endpoint list | head -10
```

## Post-recording

```bash
# Stop recording
exit

# Play back (can use --speed for fast-forward sections)
asciinema play demo-backup-restore.cast

# Upload (optional)
asciinema upload demo-backup-restore.cast
```

## Fast-forward tips

- Use `asciinema play --speed=10` for fast-forward sections during playback
- Alternatively, edit the .cast file to compress wait times:
  - The .cast format is JSON lines with timestamps
  - Reduce large time gaps between lines to speed up boring sections
- Or use `agg` to convert to GIF with speed adjustments

---
