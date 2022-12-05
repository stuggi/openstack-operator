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

	//	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	labels "github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	util "github.com/openstack-k8s-operators/lib-common/modules/common/util"

	nmstate "github.com/openstack-k8s-operators/openstack-operator/pkg/net/nmstate"
	nncp "github.com/openstack-k8s-operators/openstack-operator/pkg/net/nncp"
	"github.com/openstack-k8s-operators/openstack-operator/pkg/net/openstacknetattachment"
	osnetatt "github.com/openstack-k8s-operators/openstack-operator/pkg/net/openstacknetattachment"
	osnetattach "github.com/openstack-k8s-operators/openstack-operator/pkg/net/openstacknetattachment"

	nmstateshared "github.com/nmstate/kubernetes-nmstate/api/shared"
	nmstatev1 "github.com/nmstate/kubernetes-nmstate/api/v1"

	//sriovnetworkv1 "github.com/openshift/sriov-network-operator/api/v1"
	netv1 "github.com/openstack-k8s-operators/openstack-operator/apis/net/v1beta1"

	"github.com/go-logr/logr"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
)

// OpenStackNetAttachmentReconciler reconciles a OpenStackNetAttachment object
type OpenStackNetAttachmentReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Kclient kubernetes.Interface
	Log     logr.Logger
}

//+kubebuilder:rbac:groups=net.openstack.org,resources=openstacknetattachments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=net.openstack.org,resources=openstacknetattachments/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=net.openstack.org,resources=openstacknetattachments/finalizers,verbs=update
//+kubebuilder:rbac:groups=k8s.cni.cncf.io,resources=network-attachment-definitions,verbs=create;delete;get;list;patch;update;watch
//+kubebuilder:rbac:groups=nmstate.io,resources=nodenetworkconfigurationpolicies,verbs=create;delete;get;list;patch;update;watch
//+kubebuilder:rbac:groups=nmstate.io,resources=nodenetworkconfigurationenactments,verbs=get;list;watch
//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnetworknodepolicies,verbs=create;delete;get;list;patch;update;watch
//+kubebuilder:rbac:groups=sriovnetwork.openshift.io,resources=sriovnetworks,verbs=create;delete;get;list;patch;update;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *OpenStackNetAttachmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	// Fetch the OpenStackNetAttachment instance
	instance := &netv1.OpenStackNetAttachment{}
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

		// init conditions list depending if it is nncp or sriov
		if instance.Spec.AttachConfiguration.NodeSriovConfigurationPolicy.DesiredState.Port != "" {
			cl = condition.CreateList(
			// TODO sriov nncps
			)
		} else {

			cl = condition.CreateList(
				condition.UnknownCondition(netv1.NNCPReadyCondition, condition.InitReason, netv1.NNCPReadyInitMessage),
			)
		}

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
func (r *OpenStackNetAttachmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	//
	// Schedule reconcile on openstacknetattachment if any of the global cluster objects
	// (nncp/sriov) change
	//
	ownerLabelWatcher := handler.EnqueueRequestsFromMapFunc(func(o client.Object) []reconcile.Request {
		objLabels := o.GetLabels()
		//
		// verify object has OwnerNameLabelSelector
		//
		owner, ok := objLabels[labels.GetOwnerNameLabelSelector(labels.GetGroupLabel(osnetattach.OwnerLabel))]
		if !ok {
			return []reconcile.Request{}
		}
		namespace := objLabels[labels.GetOwnerNameSpaceLabelSelector(labels.GetGroupLabel(osnetattach.OwnerLabel))]
		return []reconcile.Request{
			{NamespacedName: types.NamespacedName{
				Name:      owner,
				Namespace: namespace,
			}},
		}
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&netv1.OpenStackNetAttachment{}).
		Watches(&source.Kind{Type: &nmstatev1.NodeNetworkConfigurationPolicy{}}, ownerLabelWatcher).
		//Watches(&source.Kind{Type: &sriovnetworkv1.SriovNetwork{}}, ownerLabelWatcher).
		//Watches(&source.Kind{Type: &sriovnetworkv1.SriovNetworkNodePolicy{}}, ownerLabelWatcher).
		Complete(r)
}

func (r *OpenStackNetAttachmentReconciler) reconcileDelete(ctx context.Context, instance *netv1.OpenStackNetAttachment, helper *helper.Helper) (ctrl.Result, error) {
	r.Log.Info("Reconciling Service delete")

	//
	// 1. check if finalizer is there
	//
	// Reconcile if finalizer got already removed
	if !controllerutil.ContainsFinalizer(instance, helper.GetFinalizer()) {
		return ctrl.Result{}, nil
	}

	//
	// 2. check if there are other finalizers
	//
	if len(instance.GetFinalizers()) > 1 {
		msg := fmt.Sprintf("NodeNetworkConfigurationPolicy has multiple finalizers, waiting for those to be removed %s", instance.GetFinalizers())
		util.LogForObject(helper, msg, instance)
	}

	//
	// 3. Clean up resources used by the operator
	///
	// NNCP resources
	ctrlResult, err := r.nncpCleanup(ctx, instance, helper)
	if err != nil && !k8s_errors.IsNotFound(err) {
		return ctrl.Result{}, err
	} else if !reflect.DeepEqual(ctrlResult, ctrl.Result{}) {
		return ctrlResult, nil
	}

	/*
		// SRIOV resources
		err = r.sriovResourceCleanup(ctx, instance)
		if err != nil && !k8s_errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	*/

	//
	// 4. as last step remove the finalizer on the operator CR to finish delete
	//
	controllerutil.RemoveFinalizer(instance, helper.GetFinalizer())
	r.Log.Info("Reconciled Service delete successfully")
	if err := r.Update(ctx, instance); err != nil && !k8s_errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	util.LogForObject(helper, fmt.Sprintf("CR %s deleted", instance.Name), instance)

	return ctrl.Result{}, nil
}

func (r *OpenStackNetAttachmentReconciler) reconcileNormal(ctx context.Context, instance *netv1.OpenStackNetAttachment, helper *helper.Helper) (ctrl.Result, error) {
	r.Log.Info("Reconciling Service")

	if !controllerutil.ContainsFinalizer(instance, helper.GetFinalizer()) {
		// If the service object doesn't have our finalizer, add it.
		controllerutil.AddFinalizer(instance, helper.GetFinalizer())
		// Register the finalizer immediately to avoid orphaning resources on delete
		return ctrl.Result{}, r.Update(ctx, instance)
	}

	if instance.Spec.AttachConfiguration.NodeSriovConfigurationPolicy.DesiredState.Port != "" {
		//
		// reconcile if SRIOV
		//
		ctrlResult, err := r.reconcileSRIOV(ctx, instance, helper)
		if err != nil && !k8s_errors.IsNotFound(err) {
			return ctrl.Result{}, err
		} else if !reflect.DeepEqual(ctrlResult, ctrl.Result{}) {
			return ctrlResult, nil
		}
		//
		// SRIOV end
		//

	} else {
		//
		// reconcile if NNCP
		//
		ctrlResult, err := r.reconcileNodeNetworkConfigurationPolicy(ctx, instance, helper)
		if err != nil && !k8s_errors.IsNotFound(err) {
			return ctrl.Result{}, err
		} else if !reflect.DeepEqual(ctrlResult, ctrl.Result{}) {
			return ctrlResult, nil
		}
		//
		// NNCP end
		//
	}

	//
	// SRIOV end
	//

	//instance.Status.AttachType = netv1.AttachTypeBridge

	r.Log.Info("Reconciled Service successfully")
	return ctrl.Result{}, nil
}

func (r *OpenStackNetAttachmentReconciler) reconcileNodeNetworkConfigurationPolicy(ctx context.Context, instance *netv1.OpenStackNetAttachment, helper *helper.Helper) (ctrl.Result, error) {
	r.Log.Info("Reconciling NodeNetworkConfigurationPolicy")

	// if this netAttSpec depends on another one, add the netAttName as finalizer
	ctrlResult, err := r.nncpDependency(ctx, instance, helper)
	if err != nil {
		// TODO conditions Messages
		instance.Status.Conditions.Set(condition.FalseCondition(
			netv1.NNCPReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			netv1.NNCPReadyErrorMessage,
			err.Error()))
		return ctrlResult, err
	} else if (ctrlResult != ctrl.Result{}) {
		instance.Status.Conditions.Set(condition.FalseCondition(
			netv1.NNCPReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			netv1.NNCPReadyRunningMessage))
		return ctrlResult, nil
	}

	nncpLabels := labels.GetLabels(instance, labels.GetGroupLabel(openstacknetattachment.OwnerLabel), map[string]string{})

	//TODO do we need those?
	//networkConfigurationPolicy.Labels[common.OwnerControllerNameLabelSelector] = openstacknetattachment.AppLabel
	//networkConfigurationPolicy.Labels[openstacknetattachment.BridgeLabel] = bridgeName
	//networkConfigurationPolicy.Labels[shared.OpenStackNetConfigReconcileLabel] = instance.GetLabels()[common.OwnerNameLabelSelector]

	// Define a new NNCP object
	// NNCPs are not namespaced, lets add the namespace to the name for better separation
	// TODO webhook to validate that there are not multiple NNCPs which manage the same devices
	nncpObj := nncp.NewNNCP(
		fmt.Sprintf("%s-%s", instance.Namespace, instance.Name),
		&instance.Spec.AttachConfiguration.NodeNetworkConfigurationPolicy,
		nncpLabels,
		map[string]string{},
		5,
	)

	ctrlResult, err = nncpObj.CreateOrPatch(ctx, helper)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			netv1.NNCPReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			netv1.NNCPReadyErrorMessage,
			err.Error()))
		return ctrlResult, err
	} else if (ctrlResult != ctrl.Result{}) {
		instance.Status.Conditions.Set(condition.FalseCondition(
			netv1.NNCPReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			netv1.NNCPReadyRunningMessage))
		return ctrlResult, nil
	}

	if nncpObj.GetNNCP().Status.Conditions != nil && len(nncpObj.GetNNCP().Status.Conditions) > 0 {
		nncpCond := nmstate.GetCurrentCondition(nncpObj.GetNNCP().Status.Conditions)
		if nncpCond != nil {
			msg := fmt.Sprintf("%s %s: %s", nncpObj.GetNNCP().Kind, nncpObj.GetNNCP().Name, nncpCond.Message)

			if nncpCond.Type == nmstateshared.NodeNetworkConfigurationPolicyConditionDegraded {
				//if nncpCond.Message != nil
				instance.Status.Conditions.Set(condition.FalseCondition(
					netv1.NNCPReadyCondition,
					condition.ErrorReason,
					condition.SeverityWarning,
					nncpCond.Message))

				return ctrl.Result{}, util.WrapErrorForObject(msg, instance, err)

			} else if nncpCond.Type == nmstateshared.NodeNetworkConfigurationPolicyConditionAvailable &&
				nncpCond.Reason == nmstateshared.NodeNetworkConfigurationPolicyConditionSuccessfullyConfigured {

				instance.Status.Conditions.MarkTrue(netv1.NNCPReadyCondition, nncpCond.Message)
				util.LogForObject(helper, msg, instance)
			}
		}
	}
	// create NNCP - end

	r.Log.Info("Reconciled NodeNetworkConfigurationPolicy successfully")
	return ctrl.Result{}, nil
}

func (r *OpenStackNetAttachmentReconciler) nncpDependency(ctx context.Context, instance *netv1.OpenStackNetAttachment, helper *helper.Helper) (ctrl.Result, error) {

	// if this netAttSpec depends on another one, add the netAttName as finalizer
	if instance.Spec.AttachConfiguration.AttachConfigurationDependency != "" {
		osNetAttObj, err := osnetatt.GetOpenStackNetworkAttachmentByName(ctx, helper, instance.Spec.AttachConfiguration.AttachConfigurationDependency, instance.Namespace)
		if err != nil {
			// if the osnetatt which this one depends does not exist,
			// wait for it
			if k8s_errors.IsNotFound(err) {
				msg := fmt.Sprintf("NodeNetworkConfigurationPolicy %s does not exist", instance.Spec.AttachConfiguration.AttachConfigurationDependency)
				util.LogForObject(helper, msg, instance)

				return ctrl.Result{}, nil
			}

			return ctrl.Result{}, err
		}

		if err := osNetAttObj.AddFinalizer(ctx, helper, instance.Name); err != nil && !k8s_errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *OpenStackNetAttachmentReconciler) nncpCleanup(ctx context.Context, instance *netv1.OpenStackNetAttachment, helper *helper.Helper) (ctrl.Result, error) {

	nncpObj, err := nncp.GetNNCPByName(ctx, helper, fmt.Sprintf("%s-%s", instance.Namespace, instance.Name))
	if err != nil {

		if k8s_errors.IsNotFound(err) {
			// TODO: cleanup does not work properly.
			// if this netAttSpec depends on another one, remove the finalizer on this
			if instance.Spec.AttachConfiguration.AttachConfigurationDependency != "" {

				r.Log.Info(fmt.Sprintf("nncp not found, removing finalizer on upper dependency %s", instance.Spec.AttachConfiguration.AttachConfigurationDependency))

				osNetAttObj, err := osnetatt.GetOpenStackNetworkAttachmentByName(ctx, helper, instance.Spec.AttachConfiguration.AttachConfigurationDependency, instance.Namespace)
				if err != nil && !k8s_errors.IsNotFound(err) {
					return ctrl.Result{}, err
				}

				if err := osNetAttObj.DeleteFinalizer(ctx, helper, instance.Name); err != nil && !k8s_errors.IsNotFound(err) {
					return ctrl.Result{}, err
				}
			}
		}

		return ctrl.Result{}, err
	}

	ctrlResult, err := nncpObj.Delete(ctx, helper)
	if err != nil && !k8s_errors.IsNotFound(err) {
		return ctrl.Result{}, err
	} else if !reflect.DeepEqual(ctrlResult, ctrl.Result{}) {
		return ctrlResult, nil
	}

	return ctrl.Result{}, nil
}

func (r *OpenStackNetAttachmentReconciler) reconcileSRIOV(ctx context.Context, instance *netv1.OpenStackNetAttachment, helper *helper.Helper) (ctrl.Result, error) {
	// TODO
	r.Log.Info("Reconciling SrIOV")

	r.Log.Info("Reconciled SrIOV successfully")
	return ctrl.Result{}, nil
}

/*
func (r *OpenStackNetAttachmentReconciler) sriovResourceCleanup(
	ctx context.Context,
	instance *netv1.OpenStackNetAttachment,
	helper *helper.Helper,
) error {
	labelSelectorMap := map[string]string{
		label.OwnerUIDLabelSelector:       string(instance.UID),
		label.OwnerNameSpaceLabelSelector: instance.Namespace,
		label.OwnerNameLabelSelector:      instance.Name,
	}

	// Delete sriovnetworks in openshift-sriov-network-operator namespace
	sriovNetworks, err := openstacknetattachment.GetSriovNetworksWithLabel(ctx, r, labelSelectorMap, "openshift-sriov-network-operator")
	if err != nil {
		return err
	}

	for _, sn := range sriovNetworks {
		err = r.Delete(ctx, &sn, &client.DeleteOptions{})

		if err != nil {
			return err
		}

		util.LogForObject(r, fmt.Sprintf("SriovNetwork deleted: name %s - %s", sn.Name, sn.UID), instance)
	}

	// Delete sriovnetworknodepolicies in openshift-sriov-network-operator namespace
	sriovNetworkNodePolicies, err := openstacknet.GetSriovNetworkNodePoliciesWithLabel(ctx, r, labelSelectorMap, "openshift-sriov-network-operator")
	if err != nil {
		return err
	}

	for _, snnp := range sriovNetworkNodePolicies {
		err = r.Delete(ctx, &snnp, &client.DeleteOptions{})

		if err != nil {
			return err
		}

		util.LogForObject(r, fmt.Sprintf("SriovNetworkNodePolicy deleted: name %s - %s", snnp.Name, snnp.UID), instance)
	}

	return nil
}
*/
