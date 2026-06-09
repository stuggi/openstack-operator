/*
Copyright 2026.

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

// Package v1beta1 contains webhooks for backup API resources.
package v1beta1

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	backupv1beta1 "github.com/openstack-k8s-operators/openstack-operator/api/backup/v1beta1"
)

var openstackbackupconfiglog = logf.Log.WithName("openstackbackupconfig-resource")

var backupConfigWebhookClient client.Client

// SetupOpenStackBackupConfigWebhookWithManager registers the webhook for OpenStackBackupConfig in the manager.
func SetupOpenStackBackupConfigWebhookWithManager(mgr ctrl.Manager) error {
	if backupConfigWebhookClient == nil {
		backupConfigWebhookClient = mgr.GetClient()
	}

	return ctrl.NewWebhookManagedBy(mgr).For(&backupv1beta1.OpenStackBackupConfig{}).
		WithValidator(&OpenStackBackupConfigCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-backup-openstack-org-v1beta1-openstackbackupconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=backup.openstack.org,resources=openstackbackupconfigs,verbs=create;update,versions=v1beta1,name=vopenstackbackupconfig-v1beta1.kb.io,admissionReviewVersions=v1

// OpenStackBackupConfigCustomValidator struct is responsible for validating the OpenStackBackupConfig resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type OpenStackBackupConfigCustomValidator struct{}

var _ webhook.CustomValidator = &OpenStackBackupConfigCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type OpenStackBackupConfig.
func (v *OpenStackBackupConfigCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	backupConfig, ok := obj.(*backupv1beta1.OpenStackBackupConfig)
	if !ok {
		return nil, fmt.Errorf("expected an OpenStackBackupConfig object but got %T", obj)
	}
	openstackbackupconfiglog.Info("Validation for OpenStackBackupConfig upon creation", "name", backupConfig.GetName())

	configList, err := backupv1beta1.GetOpenStackBackupConfigs(ctx, backupConfig.Namespace, backupConfigWebhookClient)
	if err != nil {
		return nil, apierrors.NewForbidden(
			schema.GroupResource{
				Group:    backupv1beta1.GroupVersion.WithKind("OpenStackBackupConfig").Group,
				Resource: backupv1beta1.GroupVersion.WithKind("OpenStackBackupConfig").Kind,
			}, backupConfig.GetName(), &field.Error{
				Type:   field.ErrorTypeForbidden,
				Detail: err.Error(),
			},
		)
	}

	if len(configList.Items) >= 1 {
		return nil, apierrors.NewForbidden(
			schema.GroupResource{
				Group:    backupv1beta1.GroupVersion.WithKind("OpenStackBackupConfig").Group,
				Resource: backupv1beta1.GroupVersion.WithKind("OpenStackBackupConfig").Kind,
			}, backupConfig.GetName(), &field.Error{
				Type:   field.ErrorTypeForbidden,
				Detail: "Only one OpenStackBackupConfig instance is supported per namespace.",
			},
		)
	}

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type OpenStackBackupConfig.
func (v *OpenStackBackupConfigCustomValidator) ValidateUpdate(_ context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	backupConfig, ok := newObj.(*backupv1beta1.OpenStackBackupConfig)
	if !ok {
		return nil, fmt.Errorf("expected an OpenStackBackupConfig object for the newObj but got %T", newObj)
	}
	openstackbackupconfiglog.Info("Validation for OpenStackBackupConfig upon update", "name", backupConfig.GetName())

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type OpenStackBackupConfig.
func (v *OpenStackBackupConfigCustomValidator) ValidateDelete(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	backupConfig, ok := obj.(*backupv1beta1.OpenStackBackupConfig)
	if !ok {
		return nil, fmt.Errorf("expected an OpenStackBackupConfig object but got %T", obj)
	}
	openstackbackupconfiglog.Info("Validation for OpenStackBackupConfig upon deletion", "name", backupConfig.GetName())

	return nil, nil
}
