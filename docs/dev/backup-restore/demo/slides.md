# RHOSO Backup & Restore - Slide Content

## Slide 1: Overview - What & Why

**Title: RHOSO Control Plane Backup & Restore**

- Full backup and restore of the OpenStack control plane on OpenShift
- Covers: databases (Galera), Kubernetes resources (CRs, Secrets, ConfigMaps), persistent volumes
- Uses OADP (OpenShift API for Data Protection) / Velero as the backup engine
- CSI volume snapshots for PVC data (Galera dumps, other persistent data)
- Goal: recover from catastrophic control plane failure or migrate between clusters

**Key points:**
- Non-disruptive backup (no downtime during backup)
- Ordered restore (dependencies respected: secrets -> infrastructure -> control plane)
- Data plane nodes are untouched during backup/restore

---

## Slide 2: Architecture - How It Works

**Title: Architecture**

```
+-------------------+     +--------------------+
| OpenStackBackup-  |     | Service Operators  |
| Config Controller |     | (glance, mariadb,  |
+--------+----------+     |  swift, ...)       |
         |                +---------+----------+
  Labels user resources             |
  (secrets, configmaps,      Label their PVCs
   NADs, issuers)             and secrets (CA certs)
         |                   at creation time
         +----------+---------------+
                    |
            +-------+-------+         +------------------+
            | OADP / Velero +---------+ S3 Storage       |
            +-------+-------+         | (MinIO/ODF/S3)   |
                    |                 +------------------+
             Creates backups
             (2 Backup CRs):
             1. PVCs (CSI snapshots)
             2. Resources (CRs, etc.)
                    |
+-------------------+---+                     +--------------------------+
| GaleraBackup CRs      |                     | Ordered Restore          |
| (mariadb-operator)    |                     | (phases):                |
+-----------------------+                     |  00: PVCs                |
  Defines backup config                       |  10: User Cfg            |
  (CronJobs); Jobs                            |  20: Infra CRs           |
  triggered before OADP                       |  30: CtlPlane            |
                                              |  40: IPSet, GaleraBackup |
                                              |  50: Manual (DB, RMQ)    |
                                              |  60: DataPlane           |
                                              +--------------------------+
```

**Labeling responsibilities:**
- **Operators** (e.g. glance-operator, mariadb-operator): label their own PVCs and secrets (e.g. CA certs) at creation time
- **BackupConfig controller**: labels user-provided resources (secrets, configmaps, NADs, issuers) that have no ownerReferences

**Backup flow:**
1. GaleraBackup CRs define backup config; ad-hoc Jobs are triggered from the resulting CronJobs
2. OADP Backup #1: CSI snapshots of labeled PVCs (includes fresh DB dumps)
3. OADP Backup #2: All labeled Kubernetes resources

**Restore flow:**
1. Ordered Velero Restores (PVCs -> Secrets -> Infrastructure -> ControlPlane)
2. GaleraRestore CRs restore databases from dumps
3. RabbitMQ credential restore (original credentials from backup)
4. DataPlane restore and EDPM re-deployment

---

## Slide 3: Automation & CI Integration

**Title: Ansible Automation**

- Three independent Ansible roles:
  - `deploy_minio` - S3 storage backend (dev/test)
  - `openshift_adp` - OADP operator installation
  - `cifmw_backup_restore` - Backup/restore/cleanup lifecycle

- Single test playbook runs full end-to-end:
  ```
  ansible-playbook cifmw_backup_restore_test.yaml
  ```

- Each step independently controllable:
  ```
  -e install_deps=false -e backup=false -e cleanup=false -e restore=true
  ```

- Dynamic discovery: Galera instances and RabbitMQ clusters read from OpenStackControlPlane CR

- BackupConfig controller integrated into openstack-operator:
  - Auto-created with OpenStackControlPlane
  - Labels managed automatically, user can override via annotations

---
