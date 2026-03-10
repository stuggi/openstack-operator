/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package backup

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	backupv1beta1 "github.com/openstack-k8s-operators/openstack-operator/api/backup/v1beta1"

	"github.com/openstack-k8s-operators/lib-common/modules/common/backup"
	"github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"

	"k8s.io/client-go/kubernetes"

	k8s_networkingv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// OpenStackBackupConfigReconciler reconciles a OpenStackBackupConfig object
type OpenStackBackupConfigReconciler struct {
	client.Client
	Kclient       kubernetes.Interface
	Scheme        *runtime.Scheme
	CRDLabelCache backup.CRDLabelCache
}

// getGVKFromCRD looks up a CRD by name and returns its GVK
func (r *OpenStackBackupConfigReconciler) getGVKFromCRD(ctx context.Context, crdName string) (schema.GroupVersionKind, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := r.Get(ctx, types.NamespacedName{Name: crdName}, crd); err != nil {
		return schema.GroupVersionKind{}, err
	}

	// Find the served version (prefer storage version, fall back to first served)
	var version string
	for _, v := range crd.Spec.Versions {
		if v.Storage {
			version = v.Name
			break
		}
		if v.Served && version == "" {
			version = v.Name
		}
	}

	return schema.GroupVersionKind{
		Group:   crd.Spec.Group,
		Version: version,
		Kind:    crd.Spec.Names.Kind,
	}, nil
}

// shouldLabelResource checks if a resource should be labeled based on ownerReferences and config
func shouldLabelResource(obj client.Object, config backupv1beta1.ResourceBackupConfig) bool {
	// Check if enabled
	if !config.Enabled {
		return false
	}

	// Only label resources without ownerReferences (user-provided)
	if len(obj.GetOwnerReferences()) > 0 {
		return false
	}

	labels := obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}

	// Check exclude label keys
	for _, excludeKey := range config.ExcludeLabelKeys {
		if _, exists := labels[excludeKey]; exists {
			return false
		}
	}

	// Check exclude names
	for _, excludeName := range config.ExcludeNames {
		if obj.GetName() == excludeName {
			return false
		}
	}

	// Check include label selector (if specified, resource must match)
	if len(config.IncludeLabelSelector) > 0 {
		for key, value := range config.IncludeLabelSelector {
			if labels[key] != value {
				return false
			}
		}
	}

	return true
}

// labelResource adds backup labels to a resource
func (r *OpenStackBackupConfigReconciler) labelResource(ctx context.Context, log logr.Logger, obj client.Object, restoreOrder string) error {
	labels := obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}

	// Check if already labeled
	if labels[backup.BackupRestoreLabel] == "true" && labels[backup.BackupRestoreOrderLabel] == restoreOrder {
		return nil
	}

	// Add restore labels for ordered restore
	backupLabels := backup.GetRestoreLabels(restoreOrder, "")
	labels = util.MergeStringMaps(labels, backupLabels)
	obj.SetLabels(labels)

	log.Info("Labeled resource for backup", "kind", obj.GetObjectKind().GroupVersionKind().Kind, "name", obj.GetName(),
		"restoreOrder", restoreOrder)
	return r.Update(ctx, obj)
}

// labelSecrets labels secrets in the target namespace
func (r *OpenStackBackupConfigReconciler) labelSecrets(ctx context.Context, log logr.Logger, instance *backupv1beta1.OpenStackBackupConfig) (int, error) {
	secretList := &corev1.SecretList{}
	if err := r.List(ctx, secretList, client.InNamespace(instance.Spec.TargetNamespace)); err != nil {
		return 0, err
	}

	count := 0
	for i := range secretList.Items {
		secret := &secretList.Items[i]
		if shouldLabelResource(secret, instance.Spec.Secrets) {
			if err := r.labelResource(ctx, log, secret, instance.Spec.DefaultRestoreOrder); err != nil {
				log.Error(err, "Failed to label secret", "name", secret.Name)
				continue
			}
			count++
		}
	}

	return count, nil
}

// labelConfigMaps labels configmaps in the target namespace
func (r *OpenStackBackupConfigReconciler) labelConfigMaps(ctx context.Context, log logr.Logger, instance *backupv1beta1.OpenStackBackupConfig) (int, error) {
	cmList := &corev1.ConfigMapList{}
	if err := r.List(ctx, cmList, client.InNamespace(instance.Spec.TargetNamespace)); err != nil {
		return 0, err
	}

	count := 0
	for i := range cmList.Items {
		cm := &cmList.Items[i]
		if shouldLabelResource(cm, instance.Spec.ConfigMaps) {
			if err := r.labelResource(ctx, log, cm, instance.Spec.DefaultRestoreOrder); err != nil {
				log.Error(err, "Failed to label configmap", "name", cm.Name)
				continue
			}
			count++
		}
	}

	return count, nil
}

// labelNetworkAttachmentDefinitions labels NADs in the target namespace
func (r *OpenStackBackupConfigReconciler) labelNetworkAttachmentDefinitions(ctx context.Context, log logr.Logger, instance *backupv1beta1.OpenStackBackupConfig) (int, error) {
	nadList := &k8s_networkingv1.NetworkAttachmentDefinitionList{}
	if err := r.List(ctx, nadList, client.InNamespace(instance.Spec.TargetNamespace)); err != nil {
		return 0, err
	}

	count := 0
	for i := range nadList.Items {
		nad := &nadList.Items[i]
		if shouldLabelResource(nad, instance.Spec.NetworkAttachmentDefinitions) {
			if err := r.labelResource(ctx, log, nad, instance.Spec.DefaultRestoreOrder); err != nil {
				log.Error(err, "Failed to label network-attachment-definition", "name", nad.Name)
				continue
			}
			count++
		}
	}

	return count, nil
}

// labelCRInstances labels CR instances based on CRD backup-restore labels
// This labels CRs like OpenStackControlPlane, OpenStackVersion, MariaDBAccount, etc.
// based on their CRD's backup/restore configuration.
func (r *OpenStackBackupConfigReconciler) labelCRInstances(ctx context.Context, log logr.Logger, instance *backupv1beta1.OpenStackBackupConfig) (int, error) {
	// Build cache lazily on first use (the manager's client cache is ready by reconcile time)
	if len(r.CRDLabelCache) == 0 {
		cache, err := backup.BuildCRDLabelCache(ctx, r.Client)
		if err != nil {
			return 0, fmt.Errorf("failed to build CRD label cache: %w", err)
		}
		r.CRDLabelCache = cache
		log.Info("Built CRD label cache", "entries", len(cache))
	}

	count := 0

	// Iterate through all CRDs that have backup-restore enabled
	for crdName, backupConfig := range r.CRDLabelCache {
		if !backupConfig.Enabled {
			continue
		}

		// Look up the CRD to get proper group, version, and kind
		gvk, err := r.getGVKFromCRD(ctx, crdName)
		if err != nil {
			log.Error(err, "Failed to get CRD", "name", crdName)
			continue
		}

		// Create a metadata-only list for this CRD type
		list := &metav1.PartialObjectMetadataList{}
		list.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   gvk.Group,
			Version: gvk.Version,
			Kind:    gvk.Kind + "List",
		})

		if err := r.List(ctx, list, client.InNamespace(instance.Spec.TargetNamespace)); err != nil {
			log.Error(err, "Failed to list CR instances", "crd", crdName)
			continue
		}

		// Label each CR instance
		for i := range list.Items {
			obj := &list.Items[i]

			labels := obj.GetLabels()
			if labels == nil {
				labels = make(map[string]string)
			}

			// Check if already labeled with correct values
			if labels[backup.BackupRestoreLabel] == "true" &&
				labels[backup.BackupRestoreOrderLabel] == backupConfig.RestoreOrder &&
				(backupConfig.Category == "" || labels[backup.BackupCategoryLabel] == backupConfig.Category) {
				continue
			}

			patch := client.MergeFrom(obj.DeepCopy())
			labels = util.MergeStringMaps(
				labels,
				backup.GetRestoreLabels(backupConfig.RestoreOrder, backupConfig.Category),
			)
			obj.SetLabels(labels)

			if err := r.Patch(ctx, obj, patch); err != nil {
				log.Error(err, "Failed to label CR instance", "kind", gvk.Kind, "name", obj.GetName())
				continue
			}
			log.Info("Labeled CR instance for backup", "kind", gvk.Kind, "name", obj.GetName(),
				"restoreOrder", backupConfig.RestoreOrder, "category", backupConfig.Category)
			count++
		}
	}

	return count, nil
}

// +kubebuilder:rbac:groups=backup.openstack.org,resources=openstackbackupconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=backup.openstack.org,resources=openstackbackupconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=backup.openstack.org,resources=openstackbackupconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=k8s.cni.cncf.io,resources=network-attachment-definitions,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
// RBAC for labeling CR instances across all openstack.org API groups.
// Kubernetes RBAC does not support wildcard group patterns (*.openstack.org),
// so each group must be listed explicitly.
// +kubebuilder:rbac:groups=barbican.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=baremetal.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=cinder.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=client.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=core.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=dataplane.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=designate.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=glance.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=heat.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=horizon.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=instanceha.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=ironic.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=keystone.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=manila.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=mariadb.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=memcached.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=network.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=neutron.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=nova.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=octavia.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=ovn.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=placement.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=rabbitmq.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=redis.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=swift.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=telemetry.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=topology.openstack.org,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=watcher.openstack.org,resources=*,verbs=get;list;watch;update;patch

// Reconcile labels user-provided resources (without ownerReferences) for backup/restore
//
// TODO(backup-restore): This is iteration 1 - controller-based labeling approach.
// Review whether this is sufficient or if webhook-based labeling is needed.
// See docs/dev/webhook/backup-restore-webhook-design.md for alternative design.
func (r *OpenStackBackupConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, _err error) {
	log := ctrl.LoggerFrom(ctx)

	// Fetch the OpenStackBackupConfig instance
	instance := &backupv1beta1.OpenStackBackupConfig{}
	err := r.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("OpenStackBackupConfig resource not found, ignoring")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get OpenStackBackupConfig")
		return ctrl.Result{}, err
	}

	h, err := helper.NewHelper(instance, r.Client, r.Kclient, r.Scheme, log)
	if err != nil {
		log.Error(err, "Failed to create helper")
		return ctrl.Result{}, err
	}

	// Initialize status if needed
	if instance.Status.Conditions == nil {
		instance.Status.Conditions = condition.Conditions{}
	}

	// Save a copy of the conditions for LastTransitionTime restore
	savedConditions := instance.Status.Conditions.DeepCopy()

	// Always patch the instance status when exiting this function
	defer func() {
		condition.RestoreLastTransitionTimes(&instance.Status.Conditions, savedConditions)
		if err := h.PatchInstance(ctx, instance); err != nil {
			_err = err
			return
		}
	}()

	// Label resources in target namespace
	secretCount, err := r.labelSecrets(ctx, log, instance)
	if err != nil {
		log.Error(err, "Failed to label secrets")
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			"Failed to label secrets: %v", err))
		return ctrl.Result{}, err
	}

	configMapCount, err := r.labelConfigMaps(ctx, log, instance)
	if err != nil {
		log.Error(err, "Failed to label configmaps")
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			"Failed to label configmaps: %v", err))
		return ctrl.Result{}, err
	}

	nadCount, err := r.labelNetworkAttachmentDefinitions(ctx, log, instance)
	if err != nil {
		log.Error(err, "Failed to label network-attachment-definitions")
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			"Failed to label network-attachment-definitions: %v", err))
		return ctrl.Result{}, err
	}

	// Label CR instances based on CRD backup-restore labels
	crCount, err := r.labelCRInstances(ctx, log, instance)
	if err != nil {
		log.Error(err, "Failed to label CR instances")
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			"Failed to label CR instances: %v", err))
		return ctrl.Result{}, err
	}

	// Update status
	instance.Status.LabeledResources.Secrets = secretCount
	instance.Status.LabeledResources.ConfigMaps = configMapCount
	instance.Status.LabeledResources.NetworkAttachmentDefinitions = nadCount

	instance.Status.Conditions.Set(condition.TrueCondition(
		condition.ReadyCondition,
		"Labeled %d secrets, %d configmaps, %d NADs, %d CRs", secretCount, configMapCount, nadCount, crCount))

	log.Info("Successfully labeled resources", "secrets", secretCount, "configmaps", configMapCount, "nads", nadCount)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *OpenStackBackupConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// findBackupConfigForResource maps a resource back to the BackupConfig that should process it
	findBackupConfigForResource := func(ctx context.Context, obj client.Object) []reconcile.Request {
		// For now, reconcile all BackupConfigs when any resource changes
		// TODO: optimize to only reconcile the BackupConfig for the resource's namespace
		configList := &backupv1beta1.OpenStackBackupConfigList{}
		if err := mgr.GetClient().List(ctx, configList); err != nil {
			return []reconcile.Request{}
		}

		requests := make([]reconcile.Request, len(configList.Items))
		for i, config := range configList.Items {
			requests[i] = reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      config.GetName(),
					Namespace: config.GetNamespace(),
				},
			}
		}
		return requests
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&backupv1beta1.OpenStackBackupConfig{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(findBackupConfigForResource)).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(findBackupConfigForResource)).
		Watches(&k8s_networkingv1.NetworkAttachmentDefinition{}, handler.EnqueueRequestsFromMapFunc(findBackupConfigForResource)).
		Named("openstackbackupconfig").
		Complete(r)
}
