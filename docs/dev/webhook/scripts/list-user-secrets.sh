#!/bin/bash
# Lists secrets that should be backed up:
# - No ownerReferences (user-provided)
# - Excludes service-cert managed secrets (identified by service-cert label)

set -e

NAMESPACE="${1:-openstack}"

echo "=== Secrets without ownerReferences (user-provided) ==="
echo "Namespace: $NAMESPACE"
echo ""

# Get all secrets in namespace
all_secrets=$(oc get secrets -n "$NAMESPACE" -o json)

# Filter secrets without ownerReferences and without service-cert label
filtered=$(echo "$all_secrets" | jq -r '
  .items[] |
  select(
    (.metadata.ownerReferences == null or .metadata.ownerReferences == []) and
    (.metadata.labels["service-cert"] == null)
  ) |
  {
    name: .metadata.name,
    type: .type,
    labels: .metadata.labels,
    annotations: .metadata.annotations,
    age: .metadata.creationTimestamp
  }
')

# Count and display
count=$(echo "$filtered" | jq -s 'length')
echo "Found $count user-provided secrets:"
echo ""

# Display in table format
echo "$filtered" | jq -r '
  ["NAME", "TYPE", "HAS_LABELS"],
  ["----", "----", "----------"],
  (.name, .type, (if .labels then "yes" else "no" end))
' | column -t

echo ""
echo "=== Detailed view ==="
echo "$filtered" | jq -s '.'

echo ""
echo "=== Summary by type ==="
echo "$all_secrets" | jq -r '
  [.items[] |
    select(
      (.metadata.ownerReferences == null or .metadata.ownerReferences == []) and
      (.metadata.labels["service-cert"] == null)
    )
  ] |
  group_by(.type) |
  map({type: .[0].type, count: length}) |
  ["TYPE", "COUNT"],
  ["----", "-----"],
  (.[] | [.type, .count])
' | column -t
