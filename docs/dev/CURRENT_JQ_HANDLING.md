# Current jq-Based Backup/Restore Metadata Handling

This document summarizes all metadata and field manipulations currently done via jq in the backup/restore playbooks.

## 1. Metadata Fields Removed During Backup

All resources have these fields removed during backup:

```bash
jq 'del(.items[].metadata.uid,
        .items[].metadata.resourceVersion,
        .items[].metadata.creationTimestamp,
        .items[].metadata.managedFields,
        .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
        .metadata)'
```

**Fields:**
- `.metadata.uid` - Cluster-unique identifier (auto-assigned by Kubernetes)
- `.metadata.resourceVersion` - Optimistic concurrency control version
- `.metadata.creationTimestamp` - Resource creation time
- `.metadata.managedFields` - Server-side apply field tracking
- `.metadata.annotations."kubectl.kubernetes.io/last-applied-configuration"` - Client-side apply annotation
- `.metadata` (root level) - List metadata

**Why:**
- These are Kubernetes-managed fields that get new values on restore
- Including them causes conflicts during restore

## 2. ownerReferences Removal

Most resources have ownerReferences removed:

```bash
jq 'del(.items[].metadata.ownerReferences, ...)'
```

**Why:**
- UID mismatch: Backed-up ownerReferences contain OLD UIDs
- Restored owners get NEW UIDs
- Causes orphaned resources
- **STATUS: ADDRESSED** in webhook design using OADP resourceModifiers

**Exceptions:**
- OpenStackControlPlane: ownerReferences NOT removed (has no owner)
- NetworkAttachmentDefinitions: ownerReferences NOT removed (why?)
- RabbitMQUser: ownerReferences NOT removed during backup, filtered during restore

## 3. Status Field Removal

GaleraBackup has status removed:

```bash
jq 'del(.items[].status, ...)'
```

**Why:**
- Status is runtime state, not desired state
- Velero strips status by default anyway

## 4. Filtering by ownerReferences

### DNSData (Backup)
```bash
jq '.items |= map(select(.metadata.ownerReferences == null or (.metadata.ownerReferences | length) == 0))'
```
**Why:** Only backup user-created DNSData (no ownerReferences)

### Secrets (Restore)
```bash
jq '.items |= map(
  select((.metadata.name | startswith("rabbitmq-")) | not) |
  select(.metadata.labels."service-cert" | not) |
  select(
    (.metadata.ownerReferences == null) or
    (.metadata.name | startswith("rootca-")) or
    (.metadata.name | contains("-db-password")) or
    (.metadata.name | contains("-dbpassword"))
  )
)'
```

**Includes:**
- User-provided secrets (no ownerReferences)
- CA certificates (rootca-*)
- Database passwords (*-db-password, *-dbpassword)

**Excludes:**
- rabbitmq-* secrets
- Secrets with label "service-cert"
- Other operator-managed secrets

### ConfigMaps (Restore)
```bash
jq '.items |= map(select(.metadata.ownerReferences == null))'
```
**Includes:** Only user-provided ConfigMaps (no ownerReferences)

### RabbitMQUser (Restore)
```bash
jq '.items |= map(select(.metadata.ownerReferences == null))'
```
**Includes:** Only user-created RabbitMQUser (no ownerReferences)

## 5. Secret Type Filtering (Backup)

```bash
jq '.items |= map(select(.type != "kubernetes.io/dockercfg" and .type != "kubernetes.io/service-account-token"))'
```

**Excludes:**
- `kubernetes.io/dockercfg` - Image pull secrets (auto-generated)
- `kubernetes.io/service-account-token` - Service account tokens (auto-generated)

## 6. Apply Strategy (Restore)

Resources are applied differently based on annotations:

```bash
if [ -n "${has_annotation}" ]; then
  oc apply -f "${temp_file}"
else
  oc apply --server-side=true -f "${temp_file}"
fi
```

**Logic:**
- Has `kubectl.kubernetes.io/last-applied-configuration` → use `oc apply` (client-side)
- No annotation → use `oc apply --server-side=true`

**Why:**
- Server-side apply for resources never applied with kubectl
- Client-side apply for resources that have last-applied-configuration

## 7. Staged Deployment Annotation (Restore)

OpenStackControlPlane gets staged annotation added during restore:

```bash
jq '.items[0].metadata.annotations["core.openstack.org/deployment-stage"] = "infrastructure-only"'
```

**Why:** Prevents full deployment until database/RabbitMQ are restored

**STATUS: ADDRESSED** in webhook design using OADP resourceModifiers

---

## Summary: What Needs Handling in OADP Design

### ✅ Already Addressed
1. **ownerReferences removal** - Using OADP resourceModifiers with `conditions: {}`
2. **Staged deployment annotation** - Using OADP resourceModifiers
3. **last-applied-configuration annotation** - Using OADP resourceModifiers to remove (can be too large and cause failures)

### ⚠️ Needs Discussion
1. **Secret type filtering** - Exclude dockercfg and service-account-token types
2. **Apply strategy** - Can OADP handle server-side vs client-side apply?
3. **Filtering by ownerReferences** - Handled by webhook labels, but need to verify coverage

### ✅ OADP Handles Automatically
1. **uid, resourceVersion, creationTimestamp, managedFields** - Kubernetes auto-assigns
2. **status** - Velero strips by default
3. **List metadata** - Velero handles
