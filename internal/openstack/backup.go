/*
Copyright 2026 Red Hat

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

package openstack

import (
	"context"
	"fmt"

	backupv1beta1 "github.com/openstack-k8s-operators/openstack-operator/api/backup/v1beta1"
	corev1beta1 "github.com/openstack-k8s-operators/openstack-operator/api/core/v1beta1"

	"github.com/openstack-k8s-operators/lib-common/modules/common/backup"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// ReconcileBackupConfig - reconciles OpenStackBackupConfig CR
// Automatically creates an OpenStackBackupConfig CR when OpenStackControlPlane is created
// Similar pattern to ReconcileVersion
func ReconcileBackupConfig(ctx context.Context, instance *corev1beta1.OpenStackControlPlane, helper *helper.Helper) (ctrl.Result, *backupv1beta1.OpenStackBackupConfig, error) {
	Log := GetLogger(ctx)

	// Check if a BackupConfig already exists (may have been pre-created by the user)
	configList, err := backupv1beta1.GetOpenStackBackupConfigs(ctx, instance.Namespace, helper.GetClient())
	if err != nil {
		return ctrl.Result{}, nil, fmt.Errorf("failed to list OpenStackBackupConfigs: %w", err)
	}
	if len(configList.Items) > 0 {
		existing := &configList.Items[0]
		Log.Info("Using existing OpenStackBackupConfig", "name", existing.Name)
		return ctrl.Result{}, existing, nil
	}

	backupConfig := &backupv1beta1.OpenStackBackupConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
		},
	}

	defaultLabeling := backupv1beta1.BackupLabelingEnabled

	op, err := controllerutil.CreateOrPatch(ctx, helper.GetClient(), backupConfig, func() error {
		// Note: We do NOT set ownerReference here. OpenStackBackupConfig is a configuration
		// resource that users may customize. It should persist even if the ControlPlane is
		// deleted, and should be backed up/restored with user customizations intact.

		// Set spec defaults only on create. CRD schema defaults only apply when
		// fields are absent from the request, but Go serializes zero-value structs
		// as empty objects which bypasses CRD defaulting. On update we must not
		// override user customizations (e.g. clearing ExcludeNames to []).
		if backupConfig.CreationTimestamp.IsZero() {
			if backupConfig.Spec.DefaultRestoreOrder == "" {
				backupConfig.Spec.DefaultRestoreOrder = backup.RestoreOrder10
			}
			if backupConfig.Spec.Secrets.Labeling == nil {
				backupConfig.Spec.Secrets.Labeling = &defaultLabeling
			}
			if backupConfig.Spec.ConfigMaps.Labeling == nil {
				backupConfig.Spec.ConfigMaps.Labeling = &defaultLabeling
			}
			if len(backupConfig.Spec.ConfigMaps.ExcludeNames) == 0 {
				backupConfig.Spec.ConfigMaps.ExcludeNames = []string{"kube-root-ca.crt", "openshift-service-ca.crt"}
			}
			if backupConfig.Spec.NetworkAttachmentDefinitions.Labeling == nil {
				backupConfig.Spec.NetworkAttachmentDefinitions.Labeling = &defaultLabeling
			}
		}
		return nil
	})

	if err != nil {
		return ctrl.Result{}, nil, err
	}
	if op != controllerutil.OperationResultNone {
		Log.Info(fmt.Sprintf("OpenStackBackupConfig %s - %s", backupConfig.Name, op))
	}

	return ctrl.Result{}, backupConfig, nil
}
