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
	label "github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	util "github.com/openstack-k8s-operators/lib-common/modules/common/util"
	nmstate "github.com/openstack-k8s-operators/openstack-operator/pkg/net/nmstate"

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

		cl := condition.CreateList()
		//	condition.UnknownCondition(corev1beta1.OpenStackControlPlaneRabbitMQReadyCondition, condition.InitReason, corev1beta1.OpenStackControlPlaneRabbitMQReadyInitMessage),
		//	condition.UnknownCondition(corev1beta1.OpenStackControlPlaneMariaDBReadyCondition, condition.InitReason, corev1beta1.OpenStackControlPlaneMariaDBReadyInitMessage),
		//	condition.UnknownCondition(corev1beta1.OpenStackControlPlaneKeystoneAPIReadyCondition, condition.InitReason, corev1beta1.OpenStackControlPlaneKeystoneAPIReadyInitMessage),
		//	condition.UnknownCondition(corev1beta1.OpenStackControlPlanePlacementAPIReadyCondition, condition.InitReason, corev1beta1.OpenStackControlPlanePlacementAPIReadyInitMessage),
		//	condition.UnknownCondition(corev1beta1.OpenStackControlPlaneGlanceReadyCondition, condition.InitReason, corev1beta1.OpenStackControlPlaneGlanceReadyInitMessage),
		//)

		instance.Status.Conditions.Init(&cl)

		// Register overall status immediately to have an early feedback e.g. in the cli
		if err := r.Status().Update(ctx, instance); err != nil {
			return ctrl.Result{}, err
		}
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
		labels := o.GetLabels()
		//
		// verify object has OwnerNameLabelSelector
		//
		serviceName := o.GetObjectKind().GroupVersionKind().Kind
		owner, ok := labels[label.GetOwnerNameLabelSelector(label.GetGroupLabel(serviceName))]
		if !ok {
			return []reconcile.Request{}
		}
		namespace := labels[label.GetOwnerNameSpaceLabelSelector(label.GetGroupLabel(serviceName))]
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
	// 2. Clean up resources used by the operator
	///
	// NNCP resources
	/*
		ctrlResult, err := r.cleanupNodeNetworkConfigurationPolicy(ctx, instance)
		if err != nil && !k8s_errors.IsNotFound(err) {
			return ctrl.Result{}, err
		} else if !reflect.DeepEqual(ctrlResult, ctrl.Result{}) {
			return ctrlResult, nil
		}
		// SRIOV resources
		err = r.sriovResourceCleanup(ctx, instance)
		if err != nil && !k8s_errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	*/

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

func (r *OpenStackNetAttachmentReconciler) reconcileNormal(ctx context.Context, instance *netv1.OpenStackNetAttachment, helper *helper.Helper) (ctrl.Result, error) {
	r.Log.Info("Reconciling Service")

	if !controllerutil.ContainsFinalizer(instance, helper.GetFinalizer()) {
		// If the service object doesn't have our finalizer, add it.
		controllerutil.AddFinalizer(instance, helper.GetFinalizer())
		// Register the finalizer immediately to avoid orphaning resources on delete
		err := r.Update(ctx, instance)

		return ctrl.Result{}, err
	}

	r.Log.Info("Reconciled Service successfully")
	return ctrl.Result{}, nil
}

// createOrUpdateNetworkConfigurationPolicy - create or update NetworkConfigurationPolicy
func (r *OpenStackNetAttachmentReconciler) createOrUpdateNodeNetworkConfigurationPolicy(
	ctx context.Context,
	instance *netv1.OpenStackNetAttachment,
	helper *helper.Helper,
) error {
	//
	// get bridgeName from desiredState
	//
	bridgeName, err := nmstate.GetDesiredStateBridgeName(instance.Spec.AttachConfiguration.NodeNetworkConfigurationPolicy.DesiredState.Raw)
	if err != nil {
		msg := fmt.Sprintf("Error get bridge name from NetworkConfigurationPolicy desired state - %s", instance.Name)
		//cond.Message = fmt.Sprintf("Error get bridge name from NetworkConfigurationPolicy desired state - %s", instance.Name)
		//cond.Type = shared.NetAttachError

		err = util.WrapErrorForObject(msg, instance, err)

		return err
	}

	networkConfigurationPolicy := &nmstatev1.NodeNetworkConfigurationPolicy{}
	networkConfigurationPolicy.Name = bridgeName

	// set bridgeName to instance status to be able to consume information from there
	instance.Status.BridgeName = bridgeName

	apply := func() error {
		util.InitMap(&networkConfigurationPolicy.Labels)
		networkConfigurationPolicy.Labels[label.GetOwnerUIDLabelSelector(helper.GetFinalizer())] = string(instance.UID)
		networkConfigurationPolicy.Labels[label.GetOwnerNameLabelSelector(helper.GetFinalizer())] = instance.Name
		networkConfigurationPolicy.Labels[label.GetOwnerNameLabelSelector(helper.GetFinalizer())] = instance.Namespace
		//networkConfigurationPolicy.Labels[common.OwnerControllerNameLabelSelector] = openstacknetattachment.AppLabel
		networkConfigurationPolicy.Labels[fmt.Sprintf("%s/controller", label.GetGroupLabel(helper.GetFinalizer()))] = helper.GetFinalizer()
		//networkConfigurationPolicy.Labels[openstacknetattachment.BridgeLabel] = bridgeName
		networkConfigurationPolicy.Labels["osp-bridge"] = bridgeName
		//networkConfigurationPolicy.Labels[shared.OpenStackNetConfigReconcileLabel] = instance.GetLabels()[label.OwnerNameLabelSelector]
		// TODO:
		//networkConfigurationPolicy.Labels["osnetconfig-ref"] = instance.GetLabels()[label.OwnerNameLabelSelector]

		networkConfigurationPolicy.Spec = instance.Spec.AttachConfiguration.NodeNetworkConfigurationPolicy

		return nil
	}

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, networkConfigurationPolicy, apply)
	if err != nil {
		msg := fmt.Sprintf("Updating %s networkConfigurationPolicy", bridgeName)
		//cond.Message = fmt.Sprintf("Updating %s networkConfigurationPolicy", bridgeName)
		//cond.Type = shared.NetAttachError

		err = util.WrapErrorForObject(msg, networkConfigurationPolicy, err)

		return err
	}

	if op != controllerutil.OperationResultNone {
		msg := fmt.Sprintf("NodeNetworkConfigurationPolicy %s is %s", networkConfigurationPolicy.Name, string(op))
		//cond.Message = fmt.Sprintf("NodeNetworkConfigurationPolicy %s is %s", networkConfigurationPolicy.Name, string(op))
		//cond.Type = shared.NetAttachConfiguring

		util.LogForObject(helper, string(op), networkConfigurationPolicy)
		util.LogForObject(helper, msg, instance)
	}

	if instance.ObjectMeta.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(networkConfigurationPolicy, helper.GetFinalizer()) {
			controllerutil.AddFinalizer(networkConfigurationPolicy, helper.GetFinalizer())
			if err := r.Update(ctx, networkConfigurationPolicy); err != nil {
				return err
			}
			util.LogForObject(helper, fmt.Sprintf("Finalizer %s added to %s", helper.GetFinalizer(), networkConfigurationPolicy.Name), instance)
		}
	}

	return nil
}

func (r *OpenStackNetAttachmentReconciler) getNodeNetworkConfigurationPolicyStatus(
	ctx context.Context,
	instance *netv1.OpenStackNetAttachment,
	helper *helper.Helper,
) error {
	networkConfigurationPolicy := &nmstatev1.NodeNetworkConfigurationPolicy{}

	err := r.Get(ctx, types.NamespacedName{Name: instance.Status.BridgeName}, networkConfigurationPolicy)
	if err != nil {
		msg := fmt.Sprintf("Failed to get %s %s ", networkConfigurationPolicy.Kind, networkConfigurationPolicy.Name)

		//cond.Reason = shared.CommonCondReasonNNCPError
		//cond.Type = shared.NetAttachError

		err = util.WrapErrorForObject(msg, instance, err)
		return err
	}

	msg := fmt.Sprintf("%s %s is configuring targeted node(s)", networkConfigurationPolicy.Kind, networkConfigurationPolicy.Name)
	//cond.Type = shared.NetAttachConfiguring

	/*
		//
		// sync latest status of the nncp object to the osnetattach
		//
		if networkConfigurationPolicy.Status.Conditions != nil && len(networkConfigurationPolicy.Status.Conditions) > 0 {
			condition := nmstate.GetCurrentCondition(networkConfigurationPolicy.Status.Conditions)
			if condition != nil {
				msg = fmt.Sprintf("%s %s: %s", networkConfigurationPolicy.Kind, networkConfigurationPolicy.Name, condition.Message)
				//cond.Message = fmt.Sprintf("%s %s: %s", networkConfigurationPolicy.Kind, networkConfigurationPolicy.Name, condition.Message)
				//cond.Reason = shared.ConditionReason(condition.Reason)
				//cond.Type = shared.ConditionType(condition.Type)

				if condition.Type == nmstateshared.NodeNetworkConfigurationPolicyConditionAvailable &&
					condition.Reason == nmstateshared.NodeNetworkConfigurationPolicyConditionSuccessfullyConfigured {
					cond.Type = shared.NetAttachConfigured
				} else if condition.Type == nmstateshared.NodeNetworkConfigurationPolicyConditionDegraded {
					cond.Type = shared.NetAttachError

					return util.WrapErrorForObject(cond.Message, instance, err)
				}
			}
		}
	*/
	util.LogForObject(helper, msg, instance)

	return nil
}

/*

func (r *OpenStackNetAttachmentReconciler) cleanupNodeNetworkConfigurationPolicy(
	ctx context.Context,
	instance *netv1.OpenStackNetAttachment,
	helper *helper.Helper,
) (ctrl.Result, error) {
	networkConfigurationPolicy := &nmstatev1.NodeNetworkConfigurationPolicy{}

	err := r.Get(ctx, types.NamespacedName{Name: instance.Status.BridgeName}, networkConfigurationPolicy)
	if err != nil && !k8s_errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	//
	// Set/update CR status from NNCP status
	//
	err = r.getNodeNetworkConfigurationPolicyStatus(ctx, instance, helper)

	// in case of nncp cond.Reason == FailedToConfigure, still continue to try to
	// cleanup and delete the nncp
	if err != nil &&
		cond.Reason != shared.ConditionReason(nmstateshared.NodeNetworkConfigurationPolicyConditionFailedToConfigure) {
		return ctrl.Result{}, err
	}

	bridgeState, err := nmstate.GetDesiredStateBridgeInterfaceState(networkConfigurationPolicy.Spec.DesiredState.Raw)
	if err != nil {
		cond.Message = fmt.Sprintf("Error getting interface state for bride %s from %s networkConfigurationPolicy", instance.Status.BridgeName, networkConfigurationPolicy.Name)
		cond.Reason = shared.ConditionReason(cond.Message)
		cond.Type = shared.NetAttachError

		err = util.WrapErrorForObject(cond.Message, networkConfigurationPolicy, err)
		return ctrl.Result{}, err
	}

	if bridgeState != "absent" && bridgeState != "down" {
		apply := func() error {
			desiredState, err := nmstate.GetDesiredStateAsString(networkConfigurationPolicy.Spec.DesiredState.Raw)
			if err != nil {
				return err
			}

			//
			// Update nncp desired state to absent of all interfaces from the NNCP to unconfigure the device on the worker nodes
			// https://docs.openshift.com/container-platform/4.9/networking/k8s_nmstate/k8s-nmstate-updating-node-network-config.html
			//
			re := regexp.MustCompile(`"state":"up"`)
			desiredStateAbsent := re.ReplaceAllString(desiredState, `"state":"absent"`)

			networkConfigurationPolicy.Spec.DesiredState = nmstateshared.State{
				Raw: nmstateshared.RawState(desiredStateAbsent),
			}

			return nil
		}

		//
		// 1) Update nncp desired state to down to unconfigure the device on the worker nodes
		//
		op, err := controllerutil.CreateOrPatch(ctx, r.Client, networkConfigurationPolicy, apply)
		if err != nil {
			cond.Message = fmt.Sprintf("Updating %s networkConfigurationPolicy", instance.Status.BridgeName)
			cond.Reason = shared.ConditionReason(cond.Message)
			cond.Type = shared.NetAttachError

			err = util.WrapErrorForObject(cond.Message, networkConfigurationPolicy, err)
			return ctrl.Result{}, err
		}

		if op != controllerutil.OperationResultNone {
			util.LogForObject(r, string(op), networkConfigurationPolicy)
		}

		//
		// 2) Delete nncp that DeletionTimestamp get set
		//
		if err := r.Delete(ctx, networkConfigurationPolicy); err != nil && !k8s_errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}

	} else if bridgeState == "absent" && networkConfigurationPolicy.DeletionTimestamp != nil {
		deletionTime := networkConfigurationPolicy.GetDeletionTimestamp().Time
		condition := nmstate.GetCurrentCondition(networkConfigurationPolicy.Status.Conditions)
		if condition != nil {
			nncpStateChangeTime := condition.LastTransitionTime.Time

			//
			// 3) Remove finalizer if nncp update finished
			//
			if nncpStateChangeTime.Sub(deletionTime).Seconds() > 0 &&
				condition.Type == "Available" &&
				condition.Reason == "SuccessfullyConfigured" {

				controllerutil.RemoveFinalizer(networkConfigurationPolicy, openstacknetattachment.FinalizerName)
				if err := r.Update(ctx, networkConfigurationPolicy); err != nil && !k8s_errors.IsNotFound(err) {
					cond.Message = fmt.Sprintf("Failed to update %s %s", instance.Kind, instance.Name)
					cond.Reason = shared.CommonCondReasonRemoveFinalizerError
					cond.Type = shared.CommonCondTypeError

					err = util.WrapErrorForObject(cond.Message, instance, err)

					return ctrl.Result{}, err
				}

				util.LogForObject(r, fmt.Sprintf("NodeNetworkConfigurationPolicy is no longer required and has been deleted: %s", networkConfigurationPolicy.Name), instance)

				return ctrl.Result{}, nil

			}
		}
	}
	//
	// RequeueAfter after 20s and get the nncp CR deleted when the device got removed from the worker
	//
	return ctrl.Result{RequeueAfter: time.Second * 20}, nil
}

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
