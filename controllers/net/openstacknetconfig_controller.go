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

package net

import (
	"context"
	"fmt"
	"reflect"

	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/go-logr/logr"

	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	labels "github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	util "github.com/openstack-k8s-operators/lib-common/modules/common/util"

	netv1 "github.com/openstack-k8s-operators/openstack-operator/apis/net/v1beta1"

	osnetatt "github.com/openstack-k8s-operators/openstack-operator/pkg/net/openstacknetattachment"
	osnetcfg "github.com/openstack-k8s-operators/openstack-operator/pkg/net/openstacknetconfig"
)

// OpenStackNetConfigReconciler reconciles a OpenStackNetConfig object
type OpenStackNetConfigReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Kclient kubernetes.Interface
	Log     logr.Logger
}

//+kubebuilder:rbac:groups=net.openstack.org,resources=openstacknetconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=net.openstack.org,resources=openstacknetconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=net.openstack.org,resources=openstacknetconfigs/finalizers,verbs=update
//+kubebuilder:rbac:groups=net.openstack.org,resources=openstacknetattachments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=net.openstack.org,resources=openstacknets,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *OpenStackNetConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	// Fetch the OpenStackNetConfig instance
	instance := &netv1.OpenStackNetConfig{}
	err := r.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected.
			// For additional cleanup logic use finalizers. Return and don't requeue.
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return ctrl.Result{}, err
	}

	//
	// initialize status
	//
	if instance.Status.Conditions == nil {
		instance.Status.Conditions = condition.Conditions{}
		// initialize conditions used later as Status=Unknown

		var cl condition.Conditions

		cl = condition.CreateList(
			condition.UnknownCondition(netv1.NNCPReadyCondition, condition.InitReason, netv1.NNCPReadyInitMessage),
		)

		instance.Status.Conditions.Init(&cl)

		// Register overall status immediately to have an early feedback e.g. in the cli
		return ctrl.Result{}, r.Status().Update(ctx, instance)
	}

	helper, err := helper.NewHelper(
		instance,
		r.Client,
		r.Kclient,
		r.Scheme,
		r.Log,
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Always patch the instance status when exiting this function so we can persist any changes.
	defer func() {
		// update the overall status condition if service is ready
		if instance.IsReady() {
			instance.Status.Conditions.MarkTrue(condition.ReadyCondition, condition.ReadyMessage)
		}

		if err := helper.SetAfter(instance); err != nil {
			util.LogErrorForObject(helper, err, "Set after and calc patch/diff", instance)
		}

		if changed := helper.GetChanges()["status"]; changed {
			patch := client.MergeFrom(helper.GetBeforeObject())

			if err := r.Status().Patch(ctx, instance, patch); err != nil && !k8s_errors.IsNotFound(err) {
				util.LogErrorForObject(helper, err, "Update status", instance)
			}
		}
	}()

	// Handle service delete
	if !instance.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, instance, helper)
	}

	// Handle non-deleted clusters
	return r.reconcileNormal(ctx, instance, helper)
}

// SetupWithManager sets up the controller with the Manager.
func (r *OpenStackNetConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&netv1.OpenStackNetConfig{}).
		Owns(&netv1.OpenStackNetAttachment{}).
		Complete(r)
}

func (r *OpenStackNetConfigReconciler) reconcileDelete(ctx context.Context, instance *netv1.OpenStackNetConfig, helper *helper.Helper) (ctrl.Result, error) {
	r.Log.Info("Reconciling Service delete")

	//
	// 1. check if finalizer is there
	//
	// Reconcile if finalizer got already removed
	if !controllerutil.ContainsFinalizer(instance, helper.GetFinalizer()) {
		return ctrl.Result{}, nil
	}

	// TODO

	//
	// 3. as last step remove the finalizer on the operator CR to finish delete
	//
	controllerutil.RemoveFinalizer(instance, helper.GetFinalizer())
	r.Log.Info("Reconciled Service delete successfully")
	if err := r.Update(ctx, instance); err != nil && !k8s_errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	util.LogForObject(helper, fmt.Sprintf("CR %s deleted", instance.Name), instance)

	return ctrl.Result{}, nil
}

func (r *OpenStackNetConfigReconciler) reconcileNormal(ctx context.Context, instance *netv1.OpenStackNetConfig, helper *helper.Helper) (ctrl.Result, error) {
	r.Log.Info("Reconciling Service")

	if !controllerutil.ContainsFinalizer(instance, helper.GetFinalizer()) {
		// If the service object doesn't have our finalizer, add it.
		controllerutil.AddFinalizer(instance, helper.GetFinalizer())
		// Register the finalizer immediately to avoid orphaning resources on delete
		return ctrl.Result{}, r.Update(ctx, instance)
	}

	//
	// reconcile if OpenStackNetworkAttachment
	//
	ctrlResult, err := r.reconcileOpenStackNetAttachment(ctx, instance, helper)
	if err != nil && !k8s_errors.IsNotFound(err) {
		return ctrl.Result{}, err
	} else if !reflect.DeepEqual(ctrlResult, ctrl.Result{}) {
		return ctrlResult, nil
	}
	//
	// OpenStackNetworkAttachment end
	//

	r.Log.Info("Reconciled Service successfully")
	return ctrl.Result{}, nil
}

func (r *OpenStackNetConfigReconciler) reconcileOpenStackNetAttachment(ctx context.Context, instance *netv1.OpenStackNetConfig, helper *helper.Helper) (ctrl.Result, error) {
	r.Log.Info("Reconciling OpenStackNetAttachment")

	var cond *condition.Condition
	var ctrlResult reconcile.Result
	var err error
	for netAttName, netAttSpec := range instance.Spec.AttachConfigurations {

		netAttLabels := labels.GetLabels(instance, labels.GetGroupLabel(osnetcfg.OwnerLabel), map[string]string{})

		osNetAttObj := osnetatt.NewOpenStackNetworkAttachment(
			netAttName,
			&netAttSpec,
			netAttLabels,
			map[string]string{},
			5,
		)

		ctrlResult, err = osNetAttObj.CreateOrPatch(ctx, helper)
		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(
				netv1.OpenStackNetAttachmentReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				netv1.OpenStackNetAttachmentReadyErrorMessage,
				err.Error()))
			return ctrl.Result{}, err
		}

		osNetAtt := osNetAttObj.GetOpenStackNetAttachment()

		// If this OpenStackNetAttachment is not IsReady, mirror the condition to get the latest step it is in.
		if !osNetAtt.IsReady() {
			c := osNetAtt.Status.Conditions.Mirror(netv1.OpenStackNetAttachmentReadyCondition)
			// Get the condition with higher priority
			cond = condition.GetHigherPrioCondition(c, cond).DeepCopy()
		}
	}

	if cond != nil {
		// If there was a Status=False condition, set that as the OpenStackNetAttachmentReadyCondition
		instance.Status.Conditions.Set(cond)
	} else {
		// The OpenStackNetAttachment are ready.
		// Using "condition.DeploymentReadyMessage" here because that is what gets mirrored
		// as the message for the other Cinder children when they are successfully-deployed
		instance.Status.Conditions.MarkTrue(netv1.OpenStackNetAttachmentReadyCondition, netv1.OpenStackNetAttachmentReadyMessage)
	}

	r.Log.Info("Reconciled OpenStackNetAttachment successfully")
	return ctrlResult, nil
}
