# OpenStack Control Plane Backup and Restore - Alternative Approaches

This document contains alternative approaches and ideas for backup/restore that were considered but not adopted as the primary method.

For the main tested backup/restore procedure, see [backup-restore-ctlplane.md](backup-restore-ctlplane.md).

---

## Alternative Approaches & Ideas

### Using must-gather for Comprehensive Backup

The [openstack-must-gather](https://github.com/openstack-k8s-operators/openstack-must-gather) tool provides a comprehensive alternative for backing up the OpenStack control plane.

**What must-gather captures:**
- All Custom Resources (OpenStackControlPlane, OpenStackVersion, and all operator-created service CRs)
- All ConfigMaps and Secrets (including service configs)
- Operator information (CSVs, Subscriptions, InstallPlans, OperatorGroups)
- Pod logs, events, and status
- Network configuration (NetworkAttachmentDefinitions, etc.)
- DataPlane resources (NetConfig, IPSets, DataPlaneNodeSets, etc.)
- Optional: Database dumps (via `OPENSTACK_DATABASES` environment variable)
- Optional: SOS reports from OpenShift nodes and EDPM nodes

**Usage for backup:**

```bash
# Standard must-gather (secrets are MASKED by default - not suitable for restore)
oc adm must-gather --image=quay.io/openstack-k8s-operators/openstack-must-gather

# For backup/restore purposes: Capture unmasked secrets
oc adm must-gather \
  --image=quay.io/openstack-k8s-operators/openstack-must-gather \
  -- DO_NOT_MASK=1 gather

# The output will be in must-gather.local.<timestamp>/ directory
```

**Benefits:**
- **Single command** captures everything comprehensively
- **Standard tool** - already used for OpenStack troubleshooting and support cases
- **Includes all sub-CRs** - useful for debugging and comparison even if not needed for restore
- **Well-maintained** - kept up to date with operator changes
- **Structured output** - organized by namespace and resource type

**Considerations:**
- **DO_NOT_MASK=1 is required** - By default, must-gather masks secrets for security. For backup/restore, you need unmasked secrets.
- **Security warning** - The backup will contain unmasked credentials. Store securely.
- **Large archive size** - Includes logs and additional diagnostic data beyond what's needed for restore
- **Extraction needed** - Would need to extract specific resources from the must-gather archive for restore

**Potential workflow:**

1. **Backup**: Use must-gather with `DO_NOT_MASK=1` to capture complete state
2. **Extract for restore**: Extract only the essential resources from the archive:
   - `namespaces/openstack/crs/openstackcontrolplanes.core.openstack.org/`
   - `namespaces/openstack/crs/openstackversions.core.openstack.org/`
   - `namespaces/openstack/crs/netconfigs.network.openstack.org/`
   - `namespaces/openstack/secrets/` (filter for essential secrets)
   - `csv/`, `subscriptions/`, `installplans/` (for operator version info)
3. **Restore**: Apply extracted resources using the manual restore procedure

**Why this approach is not the default in this document:**

- The manual backup procedure is **minimal and focused** on only what's needed for restore
- must-gather is primarily designed for **diagnostics and troubleshooting**, not backup/restore
- Extracting specific resources from must-gather output requires additional processing
- The manual approach gives explicit control over what's backed up and restored

**Future consideration:**

As the backup/restore workflow matures, a dedicated script could be developed to:
- Extract the essential resources from must-gather output
- Strip ownerReferences and runtime metadata
- Organize them into a restore-ready format
- This would combine the comprehensiveness of must-gather with the simplicity of the manual restore procedure

