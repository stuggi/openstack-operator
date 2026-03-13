#!/bin/bash
# Lists resources that should be backed up:
# - Resources without ownerReferences (user-provided)
# - For secrets: also exclude:
#   - service-cert managed (identified by service-cert label)
#   - dataplane service certs (identified by osdp-service label, recreated on deployment)
# Checks: Secrets, ConfigMaps, NetworkAttachmentDefinitions

set -e

NAMESPACE="${1:-openstack}"

echo "=========================================="
echo "Backup Candidates Analysis"
echo "Namespace: $NAMESPACE"
echo "=========================================="
echo ""

# Function to check resources
check_resources() {
  local resource_type=$1
  local exclude_labels=$2
  local exclude_names=$3

  echo "=== $resource_type without ownerReferences ==="

  if ! oc get "$resource_type" -n "$NAMESPACE" &>/dev/null; then
    echo "No $resource_type found or resource type not available"
    echo ""
    return
  fi

  local all_resources=$(oc get "$resource_type" -n "$NAMESPACE" -o json)

  # Build jq filter
  local jq_filter='
    .items[] |
    select(
      (.metadata.ownerReferences == null or .metadata.ownerReferences == [])
  '

  # Add label key exclusions (for managed secrets/resources)
  if [ -n "$exclude_labels" ]; then
    IFS=',' read -ra LABELS <<< "$exclude_labels"
    for label in "${LABELS[@]}"; do
      jq_filter+=" and (.metadata.labels[\"$label\"] == null)"
    done
  fi

  # Add name exclusions (for system-managed resources)
  if [ -n "$exclude_names" ]; then
    IFS=',' read -ra NAMES <<< "$exclude_names"
    for name in "${NAMES[@]}"; do
      jq_filter+=" and (.metadata.name != \"$name\")"
    done
  fi

  jq_filter+='
    ) |
    {
      name: .metadata.name,
      labels: (.metadata.labels // {}),
      creationTimestamp: .metadata.creationTimestamp
    }
  '

  local filtered=$(echo "$all_resources" | jq -r "$jq_filter")
  local count=$(echo "$filtered" | jq -s 'length')

  echo "Found: $count user-provided $resource_type"

  if [ "$count" -gt 0 ]; then
    echo ""
    echo "$filtered" | jq -r '
      .name as $name |
      .labels as $labels |
      if ($labels | length) > 0 then
        "\($name)\n  Labels: \($labels | to_entries | map("\(.key)=\(.value)") | join(", "))"
      else
        "\($name)\n  Labels: (none)"
      end
    '
  fi

  echo ""
  echo "---"
  echo ""
}

# Check Secrets (exclude service-cert and dataplane service certs - no ownerRef but auto-recreated)
check_resources "secrets" "service-cert,osdp-service" ""

# Check ConfigMaps (exclude system-managed ones that auto-recreate)
check_resources "configmaps" "" "kube-root-ca.crt,openshift-service-ca.crt"

# Check NetworkAttachmentDefinitions (NADs)
check_resources "network-attachment-definitions" "" ""

echo "=========================================="
echo "Summary"
echo "=========================================="

# Count all candidates
secret_count=$(oc get secrets -n "$NAMESPACE" -o json | jq '[.items[] | select((.metadata.ownerReferences == null or .metadata.ownerReferences == []) and (.metadata.labels["service-cert"] == null) and (.metadata.labels["osdp-service"] == null))] | length')
cm_count=$(oc get configmaps -n "$NAMESPACE" -o json 2>/dev/null | jq '[.items[] | select((.metadata.ownerReferences == null or .metadata.ownerReferences == []) and (.metadata.name != "kube-root-ca.crt") and (.metadata.name != "openshift-service-ca.crt"))] | length' || echo "0")
nad_count=$(oc get network-attachment-definitions -n "$NAMESPACE" -o json 2>/dev/null | jq '[.items[] | select(.metadata.ownerReferences == null or .metadata.ownerReferences == [])] | length' || echo "0")

echo "Secrets (user-provided):              $secret_count"
echo "ConfigMaps (user-provided):           $cm_count"
echo "NetworkAttachmentDefinitions:         $nad_count"
echo "----------------------------------------"
total=$((secret_count + cm_count + nad_count))
echo "Total resources to backup:            $total"
echo ""
echo "Note: These resources would get backup.openstack.org/backup=true label"
echo "      because they have no ownerReferences (user-provided)"
