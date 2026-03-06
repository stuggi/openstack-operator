# Backup/Restore Webhook Implementation Guide

This document provides step-by-step implementation details for the webhook-based backup/restore design. Use this as a reference when implementing backup/restore support in OpenStack operators.

## Overview

The POC implementation spans multiple repositories:
- **lib-common**: Shared backup/restore helper functions, constants, and CRD label cache
- **openstack-operator**: CRD labels for OpenStackControlPlane, DataPlaneNodeSet, OpenStackVersion
- **glance-operator**: Webhook implementation example (CR + user resource labeling) + PVC labeling
- **mariadb-operator**: CRD labels + database password secret labeling + PVC labeling

All repositories have a `backup_restore` branch checked out for this work.

**Implementation Flow:**
1. **CRD labels** declare which types participate in backup/restore
2. **CRD label cache** built once at operator startup for fast lookup
3. **Webhooks** label CR instances and user-provided resources based on cache
4. **Operators** label managed resources (PVCs, database password secrets)
5. **OADP** backs up/restores resources with `openstack.org/backup=true` label

## General Implementation Rules

### Code Patterns
- **Use lib-common helpers**: Always check lib-common for existing helpers before creating new functions
  - `util.MergeStringMaps()` for merging labels and annotations
  - `controllerutil.CreateOrPatch()` for creating/updating resources
  - `secret.CreateOrPatchSecret()` for secrets
  - `pvc.CreateOrPatch()` for PVCs
  - `backup.GetBackupLabels()` for resource instance labels
  - `backup.BuildCRDLabelCache()` for CRD label cache at startup
- **Use label constants**: Always use constants from `backup` package for label keys
  - `backup.BackupLabel` = `"openstack.org/backup"`
  - `backup.BackupRestoreLabel` = `"openstack.org/backup-restore"`
  - `backup.BackupCategoryLabel` = `"openstack.org/backup-category"`
  - `backup.BackupRestoreOrderLabel` = `"openstack.org/backup-restore-order"`
  - Never hardcode label strings
- **Cache CRD labels**: Build CRD label cache once at operator startup
  - Call `backup.BuildCRDLabelCache(ctx, mgr.GetClient())` in `main.go`
  - Pass cache to webhook via `SetupWebhookWithManager(mgr, cache)`
  - No API calls during webhook execution (use cache lookup)
  - Cache refreshes automatically on operator restart
- **Return maps, don't modify in-place**: Functions should return label/annotation maps, not modify objects directly
- **Use CreateOrPatch pattern**: Operators typically use CreateOrPatch or CreateOrUpdate (not direct Create)
- **Merge labels**: Use `util.MergeStringMaps()` to merge backup labels with existing service labels

### Testing Requirements
- **Functional tests required**: All new functions must have functional tests
- **Table-driven tests**: Use table-driven test pattern (lib-common standard)
- **Test edge cases**: Include nil, empty, and error cases in tests
- **Test file naming**: Tests go in `*_test.go` files alongside the implementation

### Helper Usage
Check these lib-common modules before implementing new helpers:
- `modules/common/backup` - **NEW**: Backup/restore labels, constants, and CRD cache
- `modules/common/util` - General utilities, map merging
- `modules/common/labels` - Label generation and selectors
- `modules/common/object` - Object metadata operations
- `modules/common/secret` - Secret creation and management
- `modules/common/pvc` - PVC creation and management

## Phase 1: lib-common Implementation

### Create common/backup Package

Create a new package `common/backup` in lib-common to provide shared functionality.

#### File: common/backup/labels.go

Defines backup/restore label constants and helper functions.

```go
package backup

const (
    // Label keys for CRD metadata (declare backup behavior for the type)
    BackupRestoreLabel      = "openstack.org/backup-restore"       // CRD label: "true" means instances participate in backup/restore
    BackupCategoryLabel     = "openstack.org/backup-category"      // CRD & instance label: "controlplane" or "dataplane"
    BackupRestoreOrderLabel = "openstack.org/backup-restore-order" // CRD & instance label: "00"-"60"

    // Label key for resource instance metadata (mark individual resources for backup)
    BackupLabel = "openstack.org/backup" // Resource instance label: "true" marks for OADP backup
)

// GetBackupLabels returns a map with backup/restore labels for CR instances
// Use with util.MergeStringMaps() to merge with existing labels
func GetBackupLabels(restoreOrder string) map[string]string

// GetBackupLabelsWithCategory returns backup labels including category
func GetBackupLabelsWithCategory(restoreOrder, category string) map[string]string

// GetBackupLabelsWithOverrides returns backup labels with annotation-based overrides
// Annotations on the instance can override the default restoreOrder and category
func GetBackupLabelsWithOverrides(defaultRestoreOrder string, annotations map[string]string) map[string]string

// ShouldBackup returns true if the resource should be backed up
func ShouldBackup(labels map[string]string) bool
```

**Key Points:**
- Label key constants ensure consistency across all operators
- **Two-level labeling approach**:
  - **CRD level**: `BackupRestoreLabel` declares that instances of this CRD type participate in backup/restore
  - **Instance level**: `BackupLabel` marks individual resources (PVCs, Secrets, ConfigMaps, CRs) for OADP backup
- `BackupCategoryLabel` and `BackupRestoreOrderLabel` used on both CRDs and instances
- BackupRestoreOrderLabel uses values: 00, 10, 20, 30, 40, 50, 60 (gaps of 10)
- GetBackupLabels returns a label map (follows lib-common pattern of not modifying in-place)
- Use with `util.MergeStringMaps()` to merge with existing labels
- ShouldBackup is nil-safe for validation/testing

#### File: common/backup/labels_test.go

Functional tests for label helper functions.

```go
package backup

import (
    "testing"
)

func TestGetBackupLabels(t *testing.T) {
    tests := []struct {
        name         string
        restoreOrder string
        want         map[string]string
    }{
        {
            name:         "PVC labels",
            restoreOrder: RestoreOrder00,
            want: map[string]string{
                BackupLabel:             "true",
                BackupRestoreOrderLabel: "00",
            },
        },
        {
            name:         "Secret labels",
            restoreOrder: RestoreOrder10,
            want: map[string]string{
                BackupLabel:             "true",
                BackupRestoreOrderLabel: "10",
            },
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := GetBackupLabels(tt.restoreOrder)
            if len(got) != len(tt.want) {
                t.Errorf("GetBackupLabels() returned %d labels, want %d", len(got), len(tt.want))
            }
            for k, v := range tt.want {
                if got[k] != v {
                    t.Errorf("GetBackupLabels()[%q] = %q, want %q", k, got[k], v)
                }
            }
        })
    }
}

func TestGetBackupLabelsWithCategory(t *testing.T) {
    tests := []struct {
        name         string
        restoreOrder string
        category     string
        want         map[string]string
    }{
        {
            name:         "with category",
            restoreOrder: RestoreOrder30,
            category:     CategoryControlPlane,
            want: map[string]string{
                BackupLabel:             "true",
                BackupRestoreOrderLabel: "30",
                BackupCategoryLabel:     "controlplane",
            },
        },
        {
            name:         "without category (empty string)",
            restoreOrder: RestoreOrder10,
            category:     "",
            want: map[string]string{
                BackupLabel:             "true",
                BackupRestoreOrderLabel: "10",
            },
        },
        {
            name:         "dataplane category",
            restoreOrder: RestoreOrder60,
            category:     CategoryDataPlane,
            want: map[string]string{
                BackupLabel:             "true",
                BackupRestoreOrderLabel: "60",
                BackupCategoryLabel:     "dataplane",
            },
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := GetBackupLabelsWithCategory(tt.restoreOrder, tt.category)
            if len(got) != len(tt.want) {
                t.Errorf("GetBackupLabelsWithCategory() returned %d labels, want %d", len(got), len(tt.want))
            }
            for k, v := range tt.want {
                if got[k] != v {
                    t.Errorf("GetBackupLabelsWithCategory()[%q] = %q, want %q", k, got[k], v)
                }
            }
        })
    }
}

func TestShouldBackup(t *testing.T) {
    tests := []struct {
        name   string
        labels map[string]string
        want   bool
    }{
        {
            name:   "nil labels",
            labels: nil,
            want:   false,
        },
        {
            name:   "empty labels",
            labels: map[string]string{},
            want:   false,
        },
        {
            name: "backup label true",
            labels: map[string]string{
                BackupLabel: "true",
            },
            want: true,
        },
        {
            name: "backup label false",
            labels: map[string]string{
                BackupLabel: "false",
            },
            want: false,
        },
        {
            name: "no backup label",
            labels: map[string]string{
                "other": "label",
            },
            want: false,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            if got := ShouldBackup(tt.labels); got != tt.want {
                t.Errorf("ShouldBackup() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

**Key Points:**
- Functional tests are required for all new functions
- Tests cover normal cases and edge cases (nil, empty, with/without category)
- Tests use table-driven test pattern (lib-common standard)

#### File: common/backup/restore.go

Defines restore order constants for consistency across operators.

```go
package backup

const (
    // Restore order constants (gaps of 10 allow insertion)
    RestoreOrder00 = "00" // Storage foundation - PVCs
    RestoreOrder10 = "10" // Foundation - NADs, Secrets, ConfigMaps
    RestoreOrder20 = "20" // TLS & infrastructure - Issuers, MariaDB, NetConfig
    RestoreOrder30 = "30" // CtlPlane + networking
    RestoreOrder40 = "40" // Backup config & user resources
    RestoreOrder50 = "50" // Manual steps - database/RabbitMQ restore, resume deployment
    RestoreOrder60 = "60" // DataPlane
)

const (
    // Category constants
    CategoryControlPlane = "controlplane"
    CategoryDataPlane    = "dataplane"
    CategoryAll          = "all"
)
```

**Key Points:**
- Named constants ensure consistency across all operators
- Comments explain the purpose of each order
- Order 00 is critical for database restore (PVCs must exist first)
- Category constants for controlplane/dataplane separation

#### File: common/backup/cache.go

CRD label cache for fast lookup of backup configuration.

```go
package backup

import (
    "context"

    apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
    "sigs.k8s.io/controller-runtime/pkg/client"
)

// BackupConfig holds backup/restore configuration for a CRD
type BackupConfig struct {
    Enabled      bool
    RestoreOrder string
    Category     string
}

// CRDLabelCache maps CRD names to their backup configuration
type CRDLabelCache map[string]BackupConfig

// BuildCRDLabelCache reads all CRDs and caches their backup labels
func BuildCRDLabelCache(ctx context.Context, c client.Client) (CRDLabelCache, error)

// GetBackupConfig looks up backup configuration for a resource
// Returns BackupConfig with Enabled=false if not found
func (c CRDLabelCache) GetBackupConfig(gvk string) BackupConfig
```

**Key Points:**
- Cache built once at operator startup (not on every webhook call)
- Uses label constants for consistency
- No API calls during webhook execution (fast)
- Automatically refreshed on operator restart
- Works with operator upgrade cycle (CRD changes = operator restart)

#### File: common/backup/cache_test.go

Functional tests for CRD label cache.

```go
package backup

import (
    "context"
    "testing"

    apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/runtime"
    "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBuildCRDLabelCache(t *testing.T)

func TestGetBackupConfig(t *testing.T)
```

**Key Points:**
- Comprehensive tests cover BuildCRDLabelCache() and GetBackupConfig()
- Tests use fake client with CRD objects
- Tests multiple scenarios: labeled CRDs, unlabeled CRDs, missing CRDs, etc.

### Update lib-common Dependencies

After creating the backup package:
1. Run `go mod tidy` in lib-common
2. Tag a new version (e.g., v0.X.Y)
3. Update go.mod in consuming operators to use the new lib-common version

## Phase 2: CRD Labels

### Label Format

Add labels to CRD metadata for dynamic discovery. Label keys are defined as constants in lib-common `backup` package.

```go
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=oscplane;oscplanerestore
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[0].status",description="Status"
// +kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.conditions[0].message",description="Message"
// +operator-sdk:csv:customresourcedefinitions:displayName="OpenStack ControlPlane"
// +kubebuilder:metadata:labels:openstack.org/backup-restore=true
// +kubebuilder:metadata:labels:openstack.org/backup-category=controlplane
// +kubebuilder:metadata:labels:openstack.org/backup-restore-order=30
type OpenStackControlPlane struct {
```

**Key Points:**
- **CRD Labels** (for dynamic discovery):
  - `openstack.org/backup-restore=true` - marks CRD as participating in backup/restore (use `backup.BackupRestoreLabel` constant)
  - `openstack.org/backup-category=controlplane` - categorizes the CRD (use `backup.BackupCategoryLabel` and `backup.CategoryControlPlane` constants)
  - `openstack.org/backup-restore-order=30` - defines restore sequence (use `backup.BackupRestoreOrderLabel` and `backup.RestoreOrder30` constants)
  - Enables dynamic discovery: `oc get crd -l openstack.org/backup-restore=true`
  - Controller-friendly: list/watch CRDs by label selector
  - Labels are read once at operator startup and cached for fast webhook lookups
- **Volume handling**: Use CSI snapshots for PVCs (configured in OADP Backup CR with `snapshotVolumes: true`)

**Label Constants Reference:**

From lib-common `backup` package:
- `BackupRestoreLabel` = `"openstack.org/backup-restore"`
- `BackupCategoryLabel` = `"openstack.org/backup-category"`
- `BackupRestoreOrderLabel` = `"openstack.org/backup-restore-order"`
- `CategoryControlPlane` = `"controlplane"`
- `CategoryDataPlane` = `"dataplane"`
- `RestoreOrder00` through `RestoreOrder60` = `"00"` through `"60"`

### OpenStack Operator CRDs

Add labels to CRDs using kubebuilder metadata annotations. Add the annotations with the other kubebuilder annotations before the type definition.

1. **OpenStackControlPlane** (`api/core/v1beta1/openstackcontrolplane_types.go`)
   ```go
   // +kubebuilder:object:root=true
   // +kubebuilder:subresource:status
   // +operator-sdk:csv:customresourcedefinitions:displayName="OpenStack ControlPlane"
   // +kubebuilder:resource:shortName=osctlplane;osctlplanes;oscp;oscps
   // +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[0].status",description="Status"
   // +kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.conditions[0].message",description="Message"
   // +kubebuilder:metadata:labels=openstack.org/backup-restore=true
   // +kubebuilder:metadata:labels=openstack.org/backup-category=controlplane
   // +kubebuilder:metadata:labels=openstack.org/backup-restore-order=30

   // OpenStackControlPlane is the Schema for the openstackcontrolplanes API
   type OpenStackControlPlane struct {
   ```

2. **OpenStackDataPlaneNodeSet** (`api/dataplane/v1beta1/openstackdataplanenodeset_types.go`)
   ```go
   // +kubebuilder:object:root=true
   // +kubebuilder:subresource:status
   // ... other kubebuilder annotations ...
   // +kubebuilder:metadata:labels=openstack.org/backup-restore=true
   // +kubebuilder:metadata:labels=openstack.org/backup-category=dataplane
   // +kubebuilder:metadata:labels=openstack.org/backup-restore-order=60

   // OpenStackDataPlaneNodeSet is the Schema for the openstackdataplanenodesets API
   type OpenStackDataPlaneNodeSet struct {
   ```

3. **OpenStackVersion** (`api/core/v1beta1/openstackversion_types.go`)
   ```go
   // +kubebuilder:object:root=true
   // +kubebuilder:subresource:status
   // ... other kubebuilder annotations ...
   // +kubebuilder:metadata:labels=openstack.org/backup-restore=true
   // +kubebuilder:metadata:labels=openstack.org/backup-category=controlplane
   // +kubebuilder:metadata:labels=openstack.org/backup-restore-order=20

   // OpenStackVersion is the Schema for the openstackversions API
   type OpenStackVersion struct {
   ```

**Key Points:**
- Use `// +kubebuilder:metadata:labels=key=value` annotation, one per label (multi-line is easier to read)
- Place the metadata:labels annotations with the other kubebuilder annotations (after printcolumn, before the type comment)
- OpenStackVersion has order "20" (restored before ControlPlane)
- OpenStackControlPlane has order "30" (restored after Version)
- OpenStackDataPlaneNodeSet has order "60" (restored last)

**After modifying type definitions:**
```bash
make generate manifests
```

This will update the generated CRD YAML files in `config/crd/bases/` with the labels.

**Verify labels were added to generated CRDs:**
```bash
grep -A 5 "metadata:" config/crd/bases/core.openstack.org_openstackcontrolplanes.yaml
oc get crd openstackcontrolplanes.core.openstack.org -o yaml | grep -A 5 labels
oc get crd -l openstack.org/backup-restore=true
```

### MariaDB Operator CRDs

Update the following CRDs in mariadb-operator:

1. **MariaDBAccount** (`api/v1beta1/mariadbaccount_types.go`)
   ```go
   // +kubebuilder:metadata:labels:openstack.org/backup-restore=true
   // +kubebuilder:metadata:labels:openstack.org/backup-category=controlplane
   // +kubebuilder:metadata:labels:openstack.org/restore-order=20
   type MariaDBAccount struct {
   ```

2. **MariaDBDatabase** (`api/v1beta1/mariadbdatabase_types.go`)
   ```go
   // +kubebuilder:metadata:labels:openstack.org/backup-restore=true
   // +kubebuilder:metadata:labels:openstack.org/backup-category=controlplane
   // +kubebuilder:metadata:labels:openstack.org/restore-order=20
   type MariaDBDatabase struct {
   ```

**After modifying CRDs:**
```bash
make generate manifests
```

**Verify labels were added to CRDs:**
```bash
oc get crd mariadbaccounts.mariadb.openstack.org -o yaml | grep -A 5 labels
oc get crd -l openstack.org/backup-restore=true -l openstack.org/backup-category=controlplane
```

### Service Operator CRDs (NOT Included)

**Important:** Service operator CRDs (GlanceAPI, NovaAPI, KeystoneAPI, etc.) should **NOT** have `backup-restore` labels.

**Why?**
- These CRs are operator-managed (created by OpenStackControlPlane reconciliation)
- They are backed up (full namespace backup) but **NOT** restored
- After restoring OpenStackControlPlane (order 30), the openstack-operator will reconcile and recreate all service CRs
- This ensures service CRs match the current OpenStack version and configuration
- Avoids version mismatches and configuration drift

**Examples of CRDs that should NOT have backup-restore labels:**
- GlanceAPI, GlanceCacheCleanup
- NovaAPI, NovaScheduler, NovaCell, NovaConductor, NovaCompute, etc.
- KeystoneAPI
- CinderAPI, CinderScheduler, CinderVolume, CinderBackup, etc.
- NeutronAPI
- PlacementAPI
- HeatAPI, HeatEngine
- IronicAPI, IronicConductor, IronicInspector
- ManilaAPI, ManilaScheduler, ManilaShare
- OctaviaAPI, OctaviaAmphoraController, etc.
- All other service operator CRDs

**Note:** The service operator webhook implementations (e.g., glance-operator in Phase 3) are for labeling **user-provided resources** (Secrets, ConfigMaps, PVCs), not for labeling service CR instances (e.g., GlanceAPI instances).

**Backup Flow:**
1. All service CRs are backed up (full namespace backup)
2. Service CRs are **not** restored (no `backup-restore` label on CRD)
3. OpenStackControlPlane is restored (order 30)
4. Openstack-operator reconciles and recreates all service CRs
5. Service operators reconcile and recreate deployments/pods
6. User-provided resources (Secrets, ConfigMaps, PVCs) are already restored and available

## Phase 3: Webhook Implementation

**Webhook Architecture Overview:**

OpenStack operators use different webhook patterns:
1. **OpenStack-operator**: Runs webhook for OpenStackControlPlane, calls service operator functions
2. **Service operators** (glance, nova, etc.): NO webhooks, export functions called by openstack-operator
3. **Infra-operator**: Independent webhook for infrastructure CRs
4. **MariaDB-operator**: Independent webhook for GaleraBackup CRs

### Setup: Build CRD Label Cache at Startup

All operators with webhooks need the CRD label cache. Build once at operator startup.

#### OpenStack-Operator: main.go

```go
import (
    "github.com/openstack-k8s-operators/lib-common/modules/common/backup"
)

func main() {
    // ... manager setup ...

    // Build CRD label cache at startup
    ctx := context.Background()
    crdLabelCache, err := backup.BuildCRDLabelCache(ctx, mgr.GetClient())
    if err != nil {
        setupLog.Error(err, "unable to build CRD label cache")
        os.Exit(1)
    }
    setupLog.Info("CRD label cache built", "entries", len(crdLabelCache))

    // Pass cache to webhook setup
    if os.Getenv("ENABLE_WEBHOOKS") != "false" {
        if err = (&corev1beta1.OpenStackControlPlane{}).SetupWebhookWithManager(mgr, crdLabelCache); err != nil {
            setupLog.Error(err, "unable to create webhook", "webhook", "OpenStackControlPlane")
            os.Exit(1)
        }
    }

    // ... rest of main ...
}
```

### OpenStack-Operator Webhook

The openstack-operator webhook handles OpenStackControlPlane CR and calls service operator functions for user resource labeling.

#### File: api/core/v1beta1/openstackcontrolplane_webhook.go

```go
import (
    "context"

    "github.com/openstack-k8s-operators/lib-common/modules/common/backup"
    "github.com/openstack-k8s-operators/lib-common/modules/common/util"

    // Import service operator packages for their labeling functions
    glance "github.com/openstack-k8s-operators/glance-operator/api/v1beta1"
    keystone "github.com/openstack-k8s-operators/keystone-operator/api/v1beta1"
    // ... other service operators

    "k8s.io/apimachinery/pkg/runtime"
    "k8s.io/apimachinery/pkg/runtime/schema"
    ctrl "sigs.k8s.io/controller-runtime"
    "sigs.k8s.io/controller-runtime/pkg/client"
    "sigs.k8s.io/controller-runtime/pkg/webhook"
)

type OpenStackControlPlaneWebhook struct {
    client.Client
    CRDLabelCache backup.CRDLabelCache
}

func (r *OpenStackControlPlane) SetupWebhookWithManager(mgr ctrl.Manager, cache backup.CRDLabelCache) error {
    webhook := &OpenStackControlPlaneWebhook{
        Client:        mgr.GetClient(),
        CRDLabelCache: cache,
    }

    return ctrl.NewWebhookManagedBy(mgr).
        For(r).
        WithDefaulter(webhook).
        Complete()
}

var _ webhook.CustomDefaulter = &OpenStackControlPlaneWebhook{}

func (w *OpenStackControlPlaneWebhook) Default(ctx context.Context, obj runtime.Object) error {
    instance := obj.(*OpenStackControlPlane)

    // 1. Label the OpenStackControlPlane instance itself
    if err := w.labelControlPlaneInstance(ctx, instance); err != nil {
        return err
    }

    // 2. Call service operator functions to label user-provided resources
    if err := w.labelServiceUserResources(ctx, instance); err != nil {
        return err
    }

    return nil
}
```

#### Webhook Logic: Label ControlPlane Instance

```go
func (w *OpenStackControlPlaneWebhook) labelControlPlaneInstance(ctx context.Context, instance *OpenStackControlPlane) error {
    // Fast cache lookup (no API call)
    gvk := schema.GroupVersionKind{
        Group:   "core.openstack.org",
        Version: "v1beta1",
        Kind:    "OpenStackControlPlane",
    }

    config, ok := w.CRDLabelCache.GetBackupConfig(gvk)
    if !ok || !config.ShouldBackup {
        // CRD not configured for backup, skip labeling
        return nil
    }

    // Add backup labels to ControlPlane instance
    if instance.Labels == nil {
        instance.Labels = make(map[string]string)
    }

    instance.Labels = util.MergeStringMaps(
        instance.Labels,
        backup.GetBackupLabelsWithCategory(config.RestoreOrder, config.Category),
    )

    return nil
}
```

#### Webhook Logic: Call Service Operator Functions

```go
func (w *OpenStackControlPlaneWebhook) labelServiceUserResources(ctx context.Context, instance *OpenStackControlPlane) error {
    // Call glance-operator function to label user resources
    if instance.Spec.Glance.Enabled {
        if err := glance.LabelUserResources(ctx, w.Client, instance.Namespace, &instance.Spec.Glance.Template); err != nil {
            return fmt.Errorf("failed to label glance resources: %w", err)
        }
    }

    // Call keystone-operator function
    if instance.Spec.Keystone.Enabled {
        if err := keystone.LabelUserResources(ctx, w.Client, instance.Namespace, &instance.Spec.Keystone.Template); err != nil {
            return fmt.Errorf("failed to label keystone resources: %w", err)
        }
    }

    // ... call other service operators ...

    return nil
}
```

**Key Points:**
- OpenStack-operator webhook runs for OpenStackControlPlane
- Labels the ControlPlane instance based on CRD cache
- Calls exported functions from service operators (no separate webhooks in service operators)
- Each service operator exports a `LabelUserResources()` function
- Error handling can be lenient (if service not enabled, skip)

### Service Operator Exported Functions

Service operators (glance, nova, keystone, etc.) do NOT run webhooks. They export labeling functions called by openstack-operator.

#### Glance-Operator Example

File: `api/v1beta1/backup.go` (new file in glance-operator)

```go
package v1beta1

import (
    "context"

    "github.com/openstack-k8s-operators/lib-common/modules/common/backup"
    "github.com/openstack-k8s-operators/lib-common/modules/common/util"
    corev1 "k8s.io/api/core/v1"
    "k8s.io/apimachinery/pkg/types"
    "sigs.k8s.io/controller-runtime/pkg/client"
)

// LabelUserResources labels user-provided Secrets and ConfigMaps referenced by GlanceAPI spec
// This function is called by openstack-operator webhook, NOT by a webhook in glance-operator
func LabelUserResources(ctx context.Context, c client.Client, namespace string, spec *GlanceAPISpec) error {
    // Label user-provided secret
    if spec.Secret != "" {
        if err := labelSecret(ctx, c, namespace, spec.Secret); err != nil {
            // Log but don't fail - resource might not exist yet
            return nil
        }
    }

    // Label user-provided ConfigMaps
    for _, configMapName := range spec.CustomServiceConfig {
        if err := labelConfigMap(ctx, c, namespace, configMapName); err != nil {
            // Log but don't fail
            return nil
        }
    }

    return nil
}

func labelSecret(ctx context.Context, c client.Client, namespace, name string) error {
    secret := &corev1.Secret{}
    err := c.Get(ctx, types.NamespacedName{
        Name:      name,
        Namespace: namespace,
    }, secret)
    if err != nil {
        return err
    }

    // Only label if user-provided (no ownerReferences)
    if len(secret.GetOwnerReferences()) > 0 {
        return nil
    }

    patch := client.MergeFrom(secret.DeepCopy())
    secret.Labels = util.MergeStringMaps(
        secret.Labels,
        backup.GetBackupLabels(backup.RestoreOrder10),
    )

    return c.Patch(ctx, secret, patch)
}

func labelConfigMap(ctx context.Context, c client.Client, namespace, name string) error {
    configMap := &corev1.ConfigMap{}
    err := c.Get(ctx, types.NamespacedName{
        Name:      name,
        Namespace: namespace,
    }, configMap)
    if err != nil {
        return err
    }

    // Only label if user-provided (no ownerReferences)
    if len(configMap.GetOwnerReferences()) > 0 {
        return nil
    }

    patch := client.MergeFrom(configMap.DeepCopy())
    configMap.Labels = util.MergeStringMaps(
        configMap.Labels,
        backup.GetBackupLabels(backup.RestoreOrder10),
    )

    return c.Patch(ctx, configMap, patch)
}
```

**Key Points:**
- Service operators export `LabelUserResources()` function (NOT a webhook handler)
- Function is called by openstack-operator webhook
- Labels only user-provided resources (check ownerReferences)
- Use `client.Patch()` with MergeFrom for safe updates
- Error handling is lenient (resources might not exist yet)

### Infra-Operator Webhook

Infra-operator runs its own independent webhook for infrastructure CRs (NetConfig, IPSet, Reservation, etc.).

#### Infra-Operator: main.go

```go
import (
    "github.com/openstack-k8s-operators/lib-common/modules/common/backup"
)

func main() {
    // ... manager setup ...

    // Build CRD label cache at startup
    ctx := context.Background()
    crdLabelCache, err := backup.BuildCRDLabelCache(ctx, mgr.GetClient())
    if err != nil {
        setupLog.Error(err, "unable to build CRD label cache")
        os.Exit(1)
    }

    // Setup webhooks for infrastructure CRs
    if os.Getenv("ENABLE_WEBHOOKS") != "false" {
        if err = (&infranetworkv1.NetConfig{}).SetupWebhookWithManager(mgr, crdLabelCache); err != nil {
            setupLog.Error(err, "unable to create webhook", "webhook", "NetConfig")
            os.Exit(1)
        }
        if err = (&infranetworkv1.IPSet{}).SetupWebhookWithManager(mgr, crdLabelCache); err != nil {
            setupLog.Error(err, "unable to create webhook", "webhook", "IPSet")
            os.Exit(1)
        }
        // ... other infra CRs ...
    }

    // ... rest of main ...
}
```

#### Infra-Operator Webhook Example: NetConfig

File: `apis/network/v1beta1/netconfig_webhook.go`

```go
package v1beta1

import (
    "context"

    "github.com/openstack-k8s-operators/lib-common/modules/common/backup"
    "github.com/openstack-k8s-operators/lib-common/modules/common/util"
    "k8s.io/apimachinery/pkg/runtime"
    "k8s.io/apimachinery/pkg/runtime/schema"
    ctrl "sigs.k8s.io/controller-runtime"
    "sigs.k8s.io/controller-runtime/pkg/webhook"
)

type NetConfigWebhook struct {
    CRDLabelCache backup.CRDLabelCache
}

func (r *NetConfig) SetupWebhookWithManager(mgr ctrl.Manager, cache backup.CRDLabelCache) error {
    webhook := &NetConfigWebhook{
        CRDLabelCache: cache,
    }

    return ctrl.NewWebhookManagedBy(mgr).
        For(r).
        WithDefaulter(webhook).
        Complete()
}

var _ webhook.CustomDefaulter = &NetConfigWebhook{}

func (w *NetConfigWebhook) Default(ctx context.Context, obj runtime.Object) error {
    instance := obj.(*NetConfig)

    // Label the NetConfig instance based on CRD labels
    gvk := schema.GroupVersionKind{
        Group:   "network.openstack.org",
        Version: "v1beta1",
        Kind:    "NetConfig",
    }

    config, ok := w.CRDLabelCache.GetBackupConfig(gvk)
    if !ok || !config.ShouldBackup {
        return nil
    }

    if instance.Labels == nil {
        instance.Labels = make(map[string]string)
    }

    instance.Labels = util.MergeStringMaps(
        instance.Labels,
        backup.GetBackupLabelsWithCategory(config.RestoreOrder, config.Category),
    )

    return nil
}
```

**Key Points:**
- Infra-operator runs independent webhooks
- Each infrastructure CR type has its own webhook
- Labels CR instances based on CRD cache
- Infrastructure CRs don't typically reference user resources (no secondary labeling needed)

### MariaDB-Operator Webhook

MariaDB-operator runs independent webhook for GaleraBackup CRs.

#### MariaDB-Operator: main.go

```go
import (
    "github.com/openstack-k8s-operators/lib-common/modules/common/backup"
)

func main() {
    // ... manager setup ...

    // Build CRD label cache at startup
    ctx := context.Background()
    crdLabelCache, err := backup.BuildCRDLabelCache(ctx, mgr.GetClient())
    if err != nil {
        setupLog.Error(err, "unable to build CRD label cache")
        os.Exit(1)
    }

    // Setup webhook for GaleraBackup
    if os.Getenv("ENABLE_WEBHOOKS") != "false" {
        if err = (&mariadbv1.GaleraBackup{}).SetupWebhookWithManager(mgr, crdLabelCache); err != nil {
            setupLog.Error(err, "unable to create webhook", "webhook", "GaleraBackup")
            os.Exit(1)
        }
    }

    // ... rest of main ...
}
```

#### MariaDB-Operator Webhook: GaleraBackup

File: `api/v1beta1/galerabackup_webhook.go`

```go
package v1beta1

import (
    "context"

    "github.com/openstack-k8s-operators/lib-common/modules/common/backup"
    "github.com/openstack-k8s-operators/lib-common/modules/common/util"
    "k8s.io/apimachinery/pkg/runtime"
    "k8s.io/apimachinery/pkg/runtime/schema"
    ctrl "sigs.k8s.io/controller-runtime"
    "sigs.k8s.io/controller-runtime/pkg/webhook"
)

type GaleraBackupWebhook struct {
    CRDLabelCache backup.CRDLabelCache
}

func (r *GaleraBackup) SetupWebhookWithManager(mgr ctrl.Manager, cache backup.CRDLabelCache) error {
    webhook := &GaleraBackupWebhook{
        CRDLabelCache: cache,
    }

    return ctrl.NewWebhookManagedBy(mgr).
        For(r).
        WithDefaulter(webhook).
        Complete()
}

var _ webhook.CustomDefaulter = &GaleraBackupWebhook{}

func (w *GaleraBackupWebhook) Default(ctx context.Context, obj runtime.Object) error {
    instance := obj.(*GaleraBackup)

    // Label the GaleraBackup instance based on CRD labels
    gvk := schema.GroupVersionKind{
        Group:   "mariadb.openstack.org",
        Version: "v1beta1",
        Kind:    "GaleraBackup",
    }

    config, ok := w.CRDLabelCache.GetBackupConfig(gvk)
    if !ok || !config.ShouldBackup {
        return nil
    }

    if instance.Labels == nil {
        instance.Labels = make(map[string]string)
    }

    instance.Labels = util.MergeStringMaps(
        instance.Labels,
        backup.GetBackupLabelsWithCategory(config.RestoreOrder, config.Category),
    )

    return nil
}
```

**Key Points:**
- MariaDB-operator runs webhook for GaleraBackup CRs
- GaleraBackup is a user-created CR (backup configuration)
- Labels CR instance based on CRD cache
- No secondary resource labeling needed (backup config only)

### Webhook Implementation Summary

| Operator | Webhook? | What it does |
|----------|----------|--------------|
| **openstack-operator** | YES | Labels OpenStackControlPlane instance + calls service operator functions |
| **service operators** (glance, nova, etc.) | NO | Export `LabelUserResources()` functions called by openstack-operator |
| **infra-operator** | YES | Labels infrastructure CR instances (NetConfig, IPSet, Reservation, etc.) |
| **mariadb-operator** | YES | Labels GaleraBackup CR instances |

**Labeling Flow:**
1. User creates/updates OpenStackControlPlane
2. OpenStack-operator webhook runs:
   - Labels ControlPlane instance (based on CRD cache)
   - Calls `glance.LabelUserResources()` → labels glance Secrets/ConfigMaps
   - Calls `keystone.LabelUserResources()` → labels keystone Secrets/ConfigMaps
   - Calls other service operator functions
3. User creates NetConfig → infra-operator webhook labels it
4. User creates GaleraBackup → mariadb-operator webhook labels it
5. Operators create PVCs/secrets → labeled in controllers (Phase 4)

## Phase 4: Operator-Managed Resource Labeling

This phase covers labeling resources created by operator controllers (not webhooks). These resources are created during reconciliation and need backup labels even though they have ownerReferences.

**When to label in controllers:**
- **PVCs** created by operators - must be backed up with CSI snapshots (storage foundation)
- **Database password secrets** - user credentials that must be restored before MariaDBAccount CRs
- **Operator-managed sub-CRs** that need backup (if any future requirements)

**Key Difference from Phase 3:**
- **Phase 3 (Webhooks)**: Labels user-provided resources (no ownerReferences)
  - Example: User creates Secret, OpenStackControlPlane references it, webhook labels it
- **Phase 4 (Controllers)**: Labels operator-managed resources (HAS ownerReferences, but explicitly backed up)
  - Example: Operator creates PVC with ownerReference to GlanceAPI, controller labels it at creation

**Why Label Resources with ownerReferences?**
- PVCs: Must be backed up (contain data) and restored before pods start
- Database password secrets: Must be restored before MariaDBAccount reconciliation
- OADP will backup these resources even with ownerReferences (full backup)
- Labels ensure they're restored in correct order

**Where This Happens:**
- In controller reconciliation loops
- When creating PVCs, secrets, or other resources
- Labels are set during resource creation (before CreateOrPatch)
- Labels merged with service-specific labels

### PVC Labeling

Operators must label PVCs they create for backup. PVCs use restore order 00 (storage foundation - restored first).

#### Glance Operator PVC Labeling

In the reconciliation code where PVCs are created using CreateOrPatch pattern:

```go
import (
    "github.com/openstack-k8s-operators/lib-common/modules/common/backup"
    "github.com/openstack-k8s-operators/lib-common/modules/common/pvc"
    "github.com/openstack-k8s-operators/lib-common/modules/common/util"
)

func (r *GlanceAPIReconciler) ensurePVC(
    ctx context.Context,
    h *helper.Helper,
    instance *glancev1.GlanceAPI,
) error {
    // Build PVC spec with backup labels
    pvcDef := &corev1.PersistentVolumeClaim{
        ObjectMeta: metav1.ObjectMeta{
            Name:      fmt.Sprintf("%s-pvc", instance.Name),
            Namespace: instance.Namespace,
            // Merge service labels with backup labels
            Labels: util.MergeStringMaps(
                map[string]string{
                    "app": "glance",
                },
                backup.GetBackupLabels(backup.RestoreOrder00),
            ),
        },
        Spec: corev1.PersistentVolumeClaimSpec{
            AccessModes: []corev1.PersistentVolumeAccessMode{
                corev1.ReadWriteOnce,
            },
            Resources: corev1.VolumeResourceRequirements{
                Requests: corev1.ResourceList{
                    corev1.ResourceStorage: resource.MustParse("10Gi"),
                },
            },
        },
    }

    // Use lib-common PVC helper which handles CreateOrPatch
    pvcHelper := pvc.NewPvc(pvcDef, 5*time.Second)
    ctrlResult, err := pvcHelper.CreateOrPatch(ctx, h)
    if err != nil {
        return err
    }
    if !ctrlResult.IsZero() {
        return fmt.Errorf("PVC not ready, requeuing")
    }

    return nil
}
```

**Key Points:**
- PVCs use restore order 00 (must be restored before databases in order 50)
- Use `util.MergeStringMaps()` to merge service labels with backup labels
- Use lib-common `pvc.CreateOrPatch()` helper (handles creation and updates)
- The CreateOrPatch pattern merges labels automatically (see lib-common pvc.go:60)
- Labels are added at creation and preserved on updates

#### MariaDB Operator PVC Labeling

Similar pattern in mariadb-operator for database PVCs:

```go
import (
    "github.com/openstack-k8s-operators/lib-common/modules/common/backup"
    "github.com/openstack-k8s-operators/lib-common/modules/common/pvc"
    "github.com/openstack-k8s-operators/lib-common/modules/common/util"
)

func (r *GaleraReconciler) ensurePVC(
    ctx context.Context,
    h *helper.Helper,
    instance *mariadbv1.Galera,
) error {
    pvcDef := &corev1.PersistentVolumeClaim{
        ObjectMeta: metav1.ObjectMeta{
            Name:      fmt.Sprintf("%s-db-pvc", instance.Name),
            Namespace: instance.Namespace,
            // Merge service labels with backup labels
            Labels: util.MergeStringMaps(
                map[string]string{
                    "app": "galera",
                },
                backup.GetBackupLabels(backup.RestoreOrder00),
            ),
        },
        Spec: corev1.PersistentVolumeClaimSpec{
            // ... PVC spec
        },
    }

    // Use lib-common PVC helper
    pvcHelper := pvc.NewPvc(pvcDef, 5*time.Second)
    ctrlResult, err := pvcHelper.CreateOrPatch(ctx, h)
    if err != nil {
        return err
    }
    if !ctrlResult.IsZero() {
        return fmt.Errorf("PVC not ready, requeuing")
    }

    return nil
}
```

### Database Password Secret Labeling

MariaDB operator must label database password secrets that need to be restored.

#### MariaDB Operator Secret Labeling

```go
import (
    "github.com/openstack-k8s-operators/lib-common/modules/common/backup"
    "github.com/openstack-k8s-operators/lib-common/modules/common/secret"
    "github.com/openstack-k8s-operators/lib-common/modules/common/util"
)

func (r *MariaDBAccountReconciler) ensurePasswordSecret(
    ctx context.Context,
    h *helper.Helper,
    account *mariadbv1.MariaDBAccount,
    password string,
) error {
    secretDef := &corev1.Secret{
        ObjectMeta: metav1.ObjectMeta{
            Name:      fmt.Sprintf("%s-db-password", account.Spec.UserName),
            Namespace: account.Namespace,
            // Merge service labels with backup labels
            Labels: util.MergeStringMaps(
                map[string]string{
                    "dbName": account.Spec.UserName,
                },
                backup.GetBackupLabels(backup.RestoreOrder10),
            ),
        },
        Data: map[string][]byte{
            "password": []byte(password),
        },
    }

    // Use lib-common secret helper which handles CreateOrPatch
    _, op, err := secret.CreateOrPatchSecret(ctx, h, account, secretDef)
    if err != nil {
        return err
    }
    if op != controllerutil.OperationResultNone {
        h.GetLogger().Info(fmt.Sprintf("Secret %s - %s", secretDef.Name, op))
    }

    return nil
}
```

**Key Points:**
- Only label database password secrets (user credentials)
- Don't label internal secrets (like RabbitMQ credentials - auto-generated, will be recreated)
- Use `util.MergeStringMaps()` to merge service labels with backup labels
- Use lib-common `secret.CreateOrPatchSecret()` helper (handles creation and updates)
- The CreateOrPatchSecret pattern merges labels automatically (see lib-common secret.go:135)
- Use restore order 10 (Secrets - restored before MariaDBAccount in order 20)
- Sets controller reference automatically (ownerReferences will be added)

### Operator-Managed Resource Labeling Summary

| Operator | Resource Type | Restore Order | Where Labeled | Notes |
|----------|--------------|---------------|---------------|-------|
| **glance-operator** | PVC | 00 | Controller (ensurePVC) | Storage for Glance images |
| **mariadb-operator** | PVC | 00 | Controller (ensurePVC) | Database storage |
| **mariadb-operator** | Secret (password) | 10 | Controller (ensurePasswordSecret) | Database user credentials |

**Pattern Consistency:**
- All resources labeled in controllers during creation
- Labels merged using `util.MergeStringMaps()`
- Use lib-common helpers (pvc.CreateOrPatch, secret.CreateOrPatchSecret)
- Labels set in ObjectMeta before calling CreateOrPatch
- These resources HAVE ownerReferences (unlike webhook-labeled resources)

**Important Notes:**
- Resources are labeled even though they have ownerReferences
- OADP backs them up (full namespace backup)
- Labels ensure correct restore order
- PVCs restored first (order 00) - required for database restore
- Database password secrets restored second (order 10) - required for MariaDBAccount reconciliation

## Phase 5: OADP Integration and Testing

### OADP Resource Directory

Create `oadp/` directory in this repository for test resources:

```
docs/dev/oadp/
├── backup-openstack-controlplane.yaml
├── restore-openstack-controlplane.yaml
└── README.md
```

### Backup CR Example

File: `oadp/backup-openstack-controlplane.yaml`

```yaml
apiVersion: velero.io/v1
kind: Backup
metadata:
  name: openstack-controlplane-backup
  namespace: openstack-oadp-operator
spec:
  includedNamespaces:
  - openstack

  # NO labelSelector - backup ALL resources in namespace
  # Full backup approach: all CRs, Secrets, ConfigMaps, etc.
  # Selective restore is done via labelSelector in Restore CRs

  # Exclude pod volumes (we use CSI snapshots for PVCs)
  defaultVolumesToFsBackup: false

  # Include CSI volume snapshots for PVCs
  snapshotVolumes: true

  # Storage location
  storageLocation: default

  # Volume snapshot location (CSI)
  volumeSnapshotLocations:
  - default

  # TTL for backup
  ttl: 720h  # 30 days
```

**Key Points:**
- **NO labelSelector** on Backup CR - backup everything in namespace
- **Full backup approach**: All CRs, Secrets, ConfigMaps backed up
- **Selective restore**: Use labelSelector in Restore CRs (not Backup)
- `defaultVolumesToFsBackup: false` - don't use file system backup
- `snapshotVolumes: true` - use CSI snapshots for PVCs with labels
- Only PVCs with `openstack.org/backup: "true"` label get CSI snapshots

### Restore CR Example

File: `oadp/restore-openstack-controlplane.yaml`

```yaml
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: openstack-controlplane-restore
  namespace: openstack-oadp-operator
spec:
  backupName: openstack-controlplane-backup

  # Restore to same namespace
  includedNamespaces:
  - openstack

  # Remove ownerReferences and last-applied-configuration
  resourceModifiers:
  - conditions: {}  # Match all resources
    patches:
    - operation: remove
      path: "/metadata/ownerReferences"
    - operation: remove
      path: "/metadata/annotations/kubectl.kubernetes.io~1last-applied-configuration"

  # Restore PVCs from volume snapshots
  restorePVs: true
```

**Key Points:**
- resourceModifiers apply to ALL resources (conditions: {})
- Removes ownerReferences to prevent UID mismatch issues
- Removes last-applied-configuration to prevent size limit failures
- restorePVs: true restores PVCs from CSI snapshots

### Multi-Phase Restore Example

For staged restore (different restore orders), create multiple Restore CRs. Each restore order is applied sequentially.

#### Order 00: PVCs (Storage Foundation)

File: `oadp/restore-order-00-pvcs.yaml`

```yaml
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: restore-00-pvcs
  namespace: openstack-oadp-operator
spec:
  backupName: openstack-controlplane-backup
  includedNamespaces:
  - openstack

  labelSelector:
    matchLabels:
      openstack.org/restore-order: "00"

  resourceModifiers:
  - conditions: {}
    patches:
    - operation: remove
      path: "/metadata/ownerReferences"
    - operation: remove
      path: "/metadata/annotations/kubectl.kubernetes.io~1last-applied-configuration"

  restorePVs: true
```

**Resources Restored:** PVCs (Galera database storage, Glance images, etc.)

#### Order 10: Secrets and ConfigMaps

File: `oadp/restore-order-10-secrets.yaml`

```yaml
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: restore-10-secrets
  namespace: openstack-oadp-operator
spec:
  backupName: openstack-controlplane-backup
  includedNamespaces:
  - openstack

  labelSelector:
    matchLabels:
      openstack.org/restore-order: "10"

  resourceModifiers:
  - conditions: {}
    patches:
    - operation: remove
      path: "/metadata/ownerReferences"
    - operation: remove
      path: "/metadata/annotations/kubectl.kubernetes.io~1last-applied-configuration"

  restorePVs: false
```

**Resources Restored:** User-provided Secrets, ConfigMaps, database password secrets

#### Order 20: Infrastructure Base

File: `oadp/restore-order-20-infrastructure.yaml`

```yaml
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: restore-20-infrastructure
  namespace: openstack-oadp-operator
spec:
  backupName: openstack-controlplane-backup
  includedNamespaces:
  - openstack

  labelSelector:
    matchLabels:
      openstack.org/restore-order: "20"

  resourceModifiers:
  - conditions: {}
    patches:
    - operation: remove
      path: "/metadata/ownerReferences"
    - operation: remove
      path: "/metadata/annotations/kubectl.kubernetes.io~1last-applied-configuration"

  restorePVs: false
```

**Resources Restored:** OpenStackVersion, MariaDBDatabase, MariaDBAccount, NetConfig, DNSData

#### Order 30: OpenStackControlPlane (Staged)

File: `oadp/restore-order-30-controlplane.yaml`

```yaml
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: restore-30-controlplane
  namespace: openstack-oadp-operator
spec:
  backupName: openstack-controlplane-backup
  includedNamespaces:
  - openstack

  labelSelector:
    matchLabels:
      openstack.org/restore-order: "30"

  resourceModifiers:
  - conditions: {}
    patches:
    - operation: remove
      path: "/metadata/ownerReferences"
    - operation: remove
      path: "/metadata/annotations/kubectl.kubernetes.io~1last-applied-configuration"
    - operation: add
      path: "/metadata/annotations/openstack.org~1deployment-stage"
      value: "infrastructure-only"

  restorePVs: false
```

**Resources Restored:** OpenStackControlPlane (with staged deployment annotation)

**Important:** The `deployment-stage: infrastructure-only` annotation prevents full deployment until database is restored.

#### Order 40: Backup Configuration

File: `oadp/restore-order-40-backup-config.yaml`

```yaml
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: restore-40-backup-config
  namespace: openstack-oadp-operator
spec:
  backupName: openstack-controlplane-backup
  includedNamespaces:
  - openstack

  labelSelector:
    matchLabels:
      openstack.org/restore-order: "40"

  resourceModifiers:
  - conditions: {}
    patches:
    - operation: remove
      path: "/metadata/ownerReferences"
    - operation: remove
      path: "/metadata/annotations/kubectl.kubernetes.io~1last-applied-configuration"

  restorePVs: false
```

**Resources Restored:** GaleraBackup CRs, user-created RabbitMQUser/RabbitMQVhost

#### Order 50: Manual Database Restore

**Note:** Order 50 is NOT an OADP Restore CR. This is manual database restore using scripts.

```bash
# Execute database restore from PVC backup
./docs/dev/scripts/restore-galera-latest.sh openstackrestore openstack
```

After database restore, remove staged deployment annotation:

```bash
oc annotate openstackcontrolplane openstack openstack.org/deployment-stage- -n openstack
```

#### Order 60: DataPlane

File: `oadp/restore-order-60-dataplane.yaml`

```yaml
apiVersion: velero.io/v1
kind: Restore
metadata:
  name: restore-60-dataplane
  namespace: openstack-oadp-operator
spec:
  backupName: openstack-controlplane-backup
  includedNamespaces:
  - openstack

  labelSelector:
    matchLabels:
      openstack.org/restore-order: "60"

  resourceModifiers:
  - conditions: {}
    patches:
    - operation: remove
      path: "/metadata/ownerReferences"
    - operation: remove
      path: "/metadata/annotations/kubectl.kubernetes.io~1last-applied-configuration"

  restorePVs: false
```

**Resources Restored:** OpenStackDataPlaneNodeSet, IPSet, Reservation

### Restore Order Summary

| Order | Resources | Restore Method | Notes |
|-------|-----------|----------------|-------|
| 00 | PVCs | OADP Restore CR | Storage foundation, CSI snapshots restored |
| 10 | Secrets, ConfigMaps | OADP Restore CR | User resources, database passwords |
| 20 | MariaDBDatabase, MariaDBAccount, NetConfig | OADP Restore CR | Infrastructure base |
| 30 | OpenStackControlPlane | OADP Restore CR | With staged annotation |
| 40 | GaleraBackup, RabbitMQUser | OADP Restore CR | Backup configuration |
| 50 | Database data | **Manual script** | Restore from PVC, remove staged annotation |
| 60 | DataPlaneNodeSet, IPSet, Reservation | OADP Restore CR | DataPlane resources |

**Key Points:**
- Each order is a separate Restore CR (except order 50 which is manual)
- All restores use same resourceModifiers for metadata cleanup
- Order 30 adds `deployment-stage: infrastructure-only` annotation
- Order 50 is manual database restore + annotation removal
- Execute restores sequentially, wait for each to complete before next

## Testing Checklist

### CRD Label Verification

- [ ] Verify CRD labels exist:
  ```bash
  oc get crd -l openstack.org/backup-restore=true
  oc get crd openstackcontrolplanes.core.openstack.org -o jsonpath='{.metadata.labels}'
  ```
- [ ] Verify CRD label cache builds at operator startup (check logs)
- [ ] Verify cache contains expected GVKs (check operator startup logs)

### Backup Testing

- [ ] Create OpenStackControlPlane with user-provided Secrets/ConfigMaps
- [ ] Verify OpenStackControlPlane instance has backup labels:
  ```bash
  oc get openstackcontrolplane openstack -o jsonpath='{.metadata.labels}' | grep backup
  ```
- [ ] Verify openstack-operator webhook labels user resources:
  - User-provided Secrets labeled with `openstack.org/backup: "true"`
  - User-provided ConfigMaps labeled
  - Check openstack-operator webhook logs for labeling activity
- [ ] Verify PVCs created by operators are labeled with restore order 00:
  ```bash
  oc get pvc -l openstack.org/restore-order=00
  ```
- [ ] Verify database password secrets are labeled with restore order 10:
  ```bash
  oc get secret -l openstack.org/restore-order=10
  ```
- [ ] Create NetConfig and verify infra-operator webhook labels it
- [ ] Create GaleraBackup and verify mariadb-operator webhook labels it
- [ ] Create OADP Backup CR (without labelSelector - full backup)
- [ ] Verify backup completes successfully:
  ```bash
  oc get backup openstack-controlplane-backup -n openstack-oadp-operator -o jsonpath='{.status.phase}'
  ```
- [ ] Verify CSI volume snapshots are created for labeled PVCs:
  ```bash
  oc get volumesnapshot -n openstack
  ```
- [ ] Verify backup includes ALL resources in namespace (not just labeled ones)

### Restore Testing

**Preparation:**
- [ ] Delete OpenStackControlPlane and all resources from namespace
- [ ] Verify namespace is clean (or create new namespace)

**Order 00: PVCs**
- [ ] Create Restore CR: `oc apply -f oadp/restore-order-00-pvcs.yaml`
- [ ] Wait for restore to complete:
  ```bash
  oc get restore restore-00-pvcs -n openstack-oadp-operator -o jsonpath='{.status.phase}'
  ```
- [ ] Verify PVCs are restored from volume snapshots:
  ```bash
  oc get pvc -n openstack
  oc get volumesnapshotcontent
  ```

**Order 10: Secrets and ConfigMaps**
- [ ] Create Restore CR: `oc apply -f oadp/restore-order-10-secrets.yaml`
- [ ] Wait for restore to complete
- [ ] Verify secrets/configmaps are restored:
  ```bash
  oc get secret,configmap -l openstack.org/restore-order=10
  ```

**Order 20: Infrastructure Base**
- [ ] Create Restore CR: `oc apply -f oadp/restore-order-20-infrastructure.yaml`
- [ ] Wait for restore to complete
- [ ] Verify MariaDBDatabase, MariaDBAccount, NetConfig restored:
  ```bash
  oc get mariadbdatabase,mariadbaccount,netconfig
  ```

**Order 30: OpenStackControlPlane (Staged)**
- [ ] Create Restore CR: `oc apply -f oadp/restore-order-30-controlplane.yaml`
- [ ] Wait for restore to complete
- [ ] Verify OpenStackControlPlane is restored:
  ```bash
  oc get openstackcontrolplane
  ```
- [ ] Verify deployment-stage annotation is added:
  ```bash
  oc get openstackcontrolplane openstack -o jsonpath='{.metadata.annotations}' | grep deployment-stage
  ```
- [ ] Verify operators reconcile but in infrastructure-only mode
- [ ] Verify no service CRs are fully deployed (staged mode)

**Order 40: Backup Configuration**
- [ ] Create Restore CR: `oc apply -f oadp/restore-order-40-backup-config.yaml`
- [ ] Wait for restore to complete
- [ ] Verify GaleraBackup CRs restored:
  ```bash
  oc get galerabackup
  ```

**Order 50: Manual Database Restore**
- [ ] Perform manual database restore from PVC backup:
  ```bash
  ./docs/dev/scripts/restore-galera-latest.sh openstackrestore openstack
  ```
- [ ] Verify database restore completed successfully
- [ ] Remove deployment-stage annotation:
  ```bash
  oc annotate openstackcontrolplane openstack openstack.org/deployment-stage- -n openstack
  ```
- [ ] Verify full deployment starts (service CRs created)

**Order 60: DataPlane**
- [ ] Create Restore CR: `oc apply -f oadp/restore-order-60-dataplane.yaml`
- [ ] Wait for restore to complete
- [ ] Verify DataPlaneNodeSet, IPSet, Reservation restored:
  ```bash
  oc get openstackdataplanenodeset,ipset,reservation
  ```

**Post-Restore Verification:**
- [ ] Verify operators reconcile and recreate child resources (service CRs)
- [ ] Verify no orphaned resources (ownerReferences were removed)
- [ ] Verify service deployments are created and running
- [ ] Verify database connectivity from services
- [ ] Verify OpenStack API endpoints are accessible

### Validation

- [ ] Check for ownerReferences in restored resources (should be removed)
- [ ] Check for last-applied-configuration annotation (should be removed)
- [ ] Verify restored resources have correct restore-order labels
- [ ] Verify CSI volume snapshots are properly restored
- [ ] Verify database integrity after restore

## Troubleshooting

### CRD Label Cache Issues

**Symptom:** Resources not being labeled by webhooks

**Check:**
- Verify CRD labels exist:
  ```bash
  oc get crd -l openstack.org/backup-restore=true
  ```
- Check operator startup logs for cache build errors:
  ```bash
  oc logs -n openstack-operators deployment/openstack-operator-controller-manager | grep "CRD label cache"
  ```
- Verify cache built successfully (should see "CRD label cache built, entries: N")
- If cache is empty or has errors, check CRD generation:
  ```bash
  cd openstack-operator
  make generate manifests
  oc apply -f config/crd/bases/
  ```

### OpenStack-Operator Webhook Not Labeling Resources

**Symptom:** OpenStackControlPlane or user resources (Secrets/ConfigMaps) not labeled

**Check:**
- Verify webhook is enabled:
  ```bash
  oc get mutatingwebhookconfiguration | grep openstack
  ```
- Check webhook pod logs:
  ```bash
  oc logs -n openstack-operators deployment/openstack-operator-controller-manager
  ```
- Verify RBAC permissions for webhook to patch Secrets/ConfigMaps
- Verify lib-common version includes backup package
- Check if service operator exported functions exist (e.g., `glance.LabelUserResources`)

### Infra-Operator Webhook Not Labeling Resources

**Symptom:** NetConfig, IPSet, Reservation not labeled

**Check:**
- Verify infra-operator webhook is running
- Check CRD labels on infrastructure CRDs:
  ```bash
  oc get crd netconfigs.network.openstack.org -o jsonpath='{.metadata.labels}'
  ```
- Check infra-operator webhook logs
- Verify CRD label cache built in infra-operator

### MariaDB-Operator Webhook Not Labeling Resources

**Symptom:** GaleraBackup CRs not labeled

**Check:**
- Verify mariadb-operator webhook is running
- Check CRD labels on GaleraBackup CRD:
  ```bash
  oc get crd galerabackups.mariadb.openstack.org -o jsonpath='{.metadata.labels}'
  ```
- Check mariadb-operator webhook logs
- Verify CRD label cache built in mariadb-operator

### Resources Not Backed Up

**Symptom:** Some resources missing from backup

**Check:**
- Verify Backup CR does NOT have labelSelector (full backup):
  ```bash
  oc get backup openstack-controlplane-backup -n openstack-oadp-operator -o yaml | grep labelSelector
  ```
  Should be empty - we backup ALL resources in namespace
- Check OADP operator logs for errors:
  ```bash
  oc logs -n openstack-oadp-operator deployment/oadp-operator
  ```
- Verify resources are in included namespace:
  ```bash
  oc get all,secret,configmap,pvc,openstackcontrolplane -n openstack
  ```
- Check backup status for errors:
  ```bash
  oc describe backup openstack-controlplane-backup -n openstack-oadp-operator
  ```
- For PVCs specifically: Verify they have backup labels for CSI snapshots:
  ```bash
  oc get pvc -l openstack.org/backup=true
  ```
  Only labeled PVCs get CSI snapshots (selective PVC backup)

### PVC Restore Failures

- Verify CSI driver supports snapshots
- Check VolumeSnapshotClass exists
- Check volume snapshot location configuration
- Verify restorePVs: true in Restore CR

### OwnerReferences Issues

- Verify resourceModifiers are configured correctly
- Check path uses JSON Pointer format with escaped slashes (~1)
- Verify conditions: {} matches all resources
- Check Restore CR logs for patch failures

### Database Restore Issues

- Verify PVCs were restored first (order 00)
- Check PVC is mounted and contains backup data
- Verify manual restore script executed successfully
- Check database pod logs
