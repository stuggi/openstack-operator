/*
Copyright 2025 Red Hat

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

	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// ReconcileBackupConfig - reconciles OpenStackBackupConfig CR
// Automatically creates an OpenStackBackupConfig CR when OpenStackControlPlane is created
// Similar pattern to ReconcileVersion
func ReconcileBackupConfig(ctx context.Context, instance *corev1beta1.OpenStackControlPlane, helper *helper.Helper) (ctrl.Result, *backupv1beta1.OpenStackBackupConfig, error) {
	backupConfig := &backupv1beta1.OpenStackBackupConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
		},
	}

	Log := GetLogger(ctx)

	// return if OpenStackBackupConfig CR already exists
	if err := helper.GetClient().Get(ctx, types.NamespacedName{
		Name:      instance.Name,
		Namespace: instance.Namespace,
	},
		backupConfig); err == nil {
		Log.Info(fmt.Sprintf("OpenStackBackupConfig found. Name: %s", backupConfig.Name))
	} else {
		Log.Info(fmt.Sprintf("OpenStackBackupConfig does not exist. Creating: %s", backupConfig.Name))
	}

	op, err := controllerutil.CreateOrPatch(ctx, helper.GetClient(), backupConfig, func() error {
		// Note: We do NOT set ownerReference here. OpenStackBackupConfig is a configuration
		// resource that users may customize. It should persist even if the ControlPlane is
		// deleted, and should be backed up/restored with user customizations intact.

		// Set spec defaults if not already set (CreateOrPatch might be updating existing)
		if backupConfig.Spec.DefaultRestoreOrder == "" {
			backupConfig.Spec.DefaultRestoreOrder = "10"
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
