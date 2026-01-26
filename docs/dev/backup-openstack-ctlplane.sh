#!/bin/bash
set -e

# OpenStack Control Plane Backup Script
# This script performs a backup of the stateless OpenStack control plane
# following the documented procedure in backup-restore-stateless.md

NAMESPACE="${NAMESPACE:-openstack}"
BACKUP_DIR_BASE="${BACKUP_DIR_BASE:-./backups}"

echo "========================================"
echo "OpenStack Control Plane Backup"
echo "========================================"
echo "Namespace: ${NAMESPACE}"
echo "Backup base directory: ${BACKUP_DIR_BASE}"
echo ""

# Create timestamped backup directory
BACKUP_DATE=$(date +%Y%m%d-%H%M%S)
BACKUP_DIR="${BACKUP_DIR_BASE}/openstack-ctlplane-backup-${BACKUP_DATE}"
mkdir -p ${BACKUP_DIR}

echo "Backup directory: ${BACKUP_DIR}"
echo ""

# Change to backup directory
pushd ${BACKUP_DIR} > /dev/null

echo "Step 1: Backup OpenStackControlPlane CR..."
oc get openstackcontrolplane -n ${NAMESPACE} -o json | \
  jq 'del(.items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > openstackcontrolplane-backup.json
echo "✓ OpenStackControlPlane CR backed up"
echo ""

echo "Step 2: Backup Related Custom Resources..."
# Backup OpenStackVersion if it exists
oc get openstackversion -n ${NAMESPACE} -o json | \
  jq 'del(.items[].metadata.ownerReferences,
          .items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > openstackversion-backup.json 2>/dev/null || echo "No OpenStackVersion found"

# Backup NetConfig (CRITICAL)
oc get netconfig -n ${NAMESPACE} -o json | \
  jq 'del(.items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > netconfig-backup.json
echo "✓ NetConfig backed up"

# Backup Topology if it exists
oc get topology -n ${NAMESPACE} -o json | \
  jq 'del(.items[].metadata.ownerReferences,
          .items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > topology-backup.json 2>/dev/null || echo "No Topology found"
echo ""

echo "Step 3: Backup NetworkAttachmentDefinitions..."
oc get network-attachment-definition -n ${NAMESPACE} -o json | \
  jq 'del(.items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > network-attachment-definitions-backup.json
echo "✓ NetworkAttachmentDefinitions backed up"
echo ""

echo "Step 4: Backup Secrets..."
echo "  Excluding auto-generated secrets (dockercfg, service-account-token)"
echo "  Preserving ownerReferences for restore filtering"
oc get secrets -n ${NAMESPACE} -o json | \
  jq '.items |= map(select(.type != "kubernetes.io/dockercfg" and .type != "kubernetes.io/service-account-token")) |
      del(.items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > secrets-all-backup.json

BACKED_UP_SECRETS=$(jq '.items | length' secrets-all-backup.json)
echo "✓ ${BACKED_UP_SECRETS} secrets backed up (excludes auto-generated system secrets)"
echo "  Note: ownerReferences preserved for restore filtering"
echo ""

echo "Step 5: Backup MariaDB Resources..."
# Backup MariaDBDatabase CRs
oc get mariadbdatabase -n ${NAMESPACE} -o json | \
  jq 'del(.items[].metadata.ownerReferences,
          .items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > mariadbdatabase-backup.json
echo "✓ MariaDBDatabase CRs backed up"

# Backup MariaDBAccount CRs
oc get mariadbaccount -n ${NAMESPACE} -o json | \
  jq 'del(.items[].metadata.ownerReferences,
          .items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > mariadbaccount-backup.json
echo "✓ MariaDBAccount CRs backed up"
echo ""

echo "Step 6: Backup TLS Issuers and CA Secrets..."
# Backup Issuer CRs
oc get issuer -n ${NAMESPACE} -o json | \
  jq 'del(.items[].metadata.ownerReferences,
          .items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > issuer-backup.json
echo "✓ Issuers backed up"

# Backup Certificate CRs (for disaster recovery archive - not restored)
oc get certificate -n ${NAMESPACE} -o json | \
  jq 'del(.items[].metadata.ownerReferences,
          .items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > certificates-backup.json
echo "✓ Certificates backed up"
echo ""

echo "Step 7: Backup ConfigMaps..."
echo "  Preserving ownerReferences for restore filtering"
oc get configmaps -n ${NAMESPACE} -o json | \
  jq 'del(.items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > configmaps-all-backup.json
echo "✓ All ConfigMaps backed up"
echo "  Note: ownerReferences preserved for restore filtering"
echo ""

echo "Step 8: Document Operator and Platform Versions..."
cat > operator-versions.txt <<EOF
========================================
OpenStack Control Plane Backup Metadata
========================================
Backup Date: $(date)
Backup Created By: $(oc whoami)

OpenShift Cluster Information:
- Cluster Version: $(oc version -o json | jq -r '.openshiftVersion')
- Kubernetes Version: $(oc version -o json | jq -r '.serverVersion.gitVersion')
- Console URL: $(oc whoami --show-console)

Namespace: ${NAMESPACE}

Installed Operators (ClusterServiceVersions):
EOF

oc get csv -n openstack-operators >> operator-versions.txt

echo "" >> operator-versions.txt
echo "Operator Subscriptions:" >> operator-versions.txt
oc get subscription -n openstack-operators >> operator-versions.txt

echo "" >> operator-versions.txt
echo "Install Plans:" >> operator-versions.txt
oc get installplan -n openstack-operators >> operator-versions.txt

echo "" >> operator-versions.txt
echo "Storage Configuration:" >> operator-versions.txt
echo "Note: Services can override the global storageClass with service-specific parameters" >> operator-versions.txt
echo "- Global StorageClass: $(oc get openstackcontrolplane -n ${NAMESPACE} -o jsonpath='{.items[0].spec.storageClass}')" >> operator-versions.txt
echo "- Available StorageClasses in cluster:" >> operator-versions.txt
oc get storageclass --no-headers | awk '{print "  - " $1}' >> operator-versions.txt

# Save detailed CSV and subscription information
oc get csv -n openstack-operators -o json | \
  jq 'del(.items[].metadata.ownerReferences,
          .items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > csv-backup.json

oc get subscription -n openstack-operators -o json | \
  jq 'del(.items[].metadata.ownerReferences,
          .items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > subscription-backup.json

oc get installplan -n openstack-operators -o json | \
  jq 'del(.items[].metadata.ownerReferences,
          .items[].metadata.uid,
          .items[].metadata.resourceVersion,
          .items[].metadata.creationTimestamp,
          .items[].metadata.managedFields,
          .items[].metadata.annotations."kubectl.kubernetes.io/last-applied-configuration",
          .metadata)' > installplan-backup.json

echo "✓ Operator versions documented"
echo ""

# Create a README for the backup
cat > README.md <<EOF
# OpenStack Control Plane Backup

Created: $(date)
Source Cluster: $(oc whoami --show-console)
Namespace: ${NAMESPACE}

## Contents

All backup files are in JSON format for consistency.

### Control Plane CRs
- openstackcontrolplane-backup.json: Main control plane CR
- openstackversion-backup.json: OpenStack version and container images
- netconfig-backup.json: Network configuration (CRITICAL)
- topology-backup.json: Topology configuration (if used)

### Network Resources (ownerReferences removed)
- network-attachment-definitions-backup.json: NetworkAttachmentDefinitions (CRITICAL)

### Secrets and ConfigMaps (ownerReferences removed)
- secrets-all-backup.json: All secrets (includes OpenStack secrets, RabbitMQ, TLS CAs, certificates, database passwords)
- configmaps-all-backup.json: All ConfigMaps

### MariaDB Resources (ownerReferences removed)
- mariadbdatabase-backup.json: MariaDBDatabase CRs (database definitions)
- mariadbaccount-backup.json: MariaDBAccount CRs (database account definitions, reference secrets for passwords)

### TLS Infrastructure (ownerReferences removed)
- issuer-backup.json: cert-manager Issuer CRs
- certificates-backup.json: cert-manager Certificate CRs

### Operator Version Information
- operator-versions.txt: Operator and platform versions (human-readable summary)
- csv-backup.json: ClusterServiceVersion details
- subscription-backup.json: Subscription details
- installplan-backup.json: InstallPlan details

## Restore Instructions

See: docs/dev/backup-restore-stateless.md

**IMPORTANT**: Target cluster must have matching operator versions!
Check operator-versions.txt for required versions.
EOF

echo "✓ README.md created"
echo ""

# Create archive (return to original directory, then go to backup base)
popd > /dev/null
pushd ${BACKUP_DIR_BASE} > /dev/null
ARCHIVE_NAME="openstack-ctlplane-backup-${BACKUP_DATE}.tar.gz"
tar -czf ${ARCHIVE_NAME} openstack-ctlplane-backup-${BACKUP_DATE}
popd > /dev/null

echo "========================================"
echo "Backup completed successfully!"
echo "========================================"
echo "Backup directory: ${BACKUP_DIR}"
echo "Archive created: ${BACKUP_DIR_BASE}/${ARCHIVE_NAME}"
echo "Archive size: $(du -h ${BACKUP_DIR_BASE}/${ARCHIVE_NAME} | cut -f1)"
echo ""
echo "Next steps:"
echo "1. Review operator-versions.txt for required operator versions"
echo "2. Store backup archive securely"
echo "3. Test restore on a test cluster before relying on this backup"
echo ""
echo "Example storage commands:"
echo "  scp ${BACKUP_DIR_BASE}/${ARCHIVE_NAME} backup-server:/backups/"
echo "  oc create secret generic openstack-backup-${BACKUP_DATE} --from-file=${BACKUP_DIR_BASE}/${ARCHIVE_NAME} -n openstack-backups"
echo ""
