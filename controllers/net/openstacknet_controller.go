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
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	networkv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	labels "github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	util "github.com/openstack-k8s-operators/lib-common/modules/common/util"
	netv1 "github.com/openstack-k8s-operators/openstack-operator/apis/net/v1beta1"

	osnet "github.com/openstack-k8s-operators/openstack-operator/pkg/net"

	"github.com/go-logr/logr"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
)

// OpenStackNetReconciler reconciles a OpenStackNet object
type OpenStackNetReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Kclient kubernetes.Interface
	Log     logr.Logger
}

//+kubebuilder:rbac:groups=net.openstack.org,resources=openstacknets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=net.openstack.org,resources=openstacknets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=net.openstack.org,resources=openstacknets/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *OpenStackNetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	// Fetch the OpenStackNet instance
	instance := &netv1.OpenStackNet{}
	err := r.Client.Get(ctx, req.NamespacedName, instance)
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

	//
	// If RoleReservations status map is nil, create it
	//
	if instance.Status.Reservations == nil {
		instance.Status.Reservations = map[string]netv1.NodeIPReservation{}
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
		// update the overall status condition if osnet is ready
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

	return r.reconcileNormal(ctx, instance, helper)
}

// SetupWithManager sets up the controller with the Manager.
func (r *OpenStackNetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&netv1.OpenStackNet{}).
		Complete(r)
}

func (r *OpenStackNetReconciler) reconcileDelete(ctx context.Context, instance *netv1.OpenStackNet, helper *helper.Helper) (ctrl.Result, error) {
	r.Log.Info("Reconciling Net delete")

	//
	// 1. check if finalizer is there
	// Reconcile if finalizer got already removed
	if !controllerutil.ContainsFinalizer(instance, helper.GetFinalizer()) {
		return ctrl.Result{}, nil
	}

	/*
		//
		// 2. Clean up resources used by the operator
		//
		// osnet resources
		err := r.cleanupNetworkAttachmentDefinition(ctx, instance, helper)
		if err != nil {
			return ctrl.Result{}, err
		}
	*/

	// Service is deleted so remove the finalizer.
	controllerutil.RemoveFinalizer(instance, helper.GetFinalizer())
	r.Log.Info("Reconciled Net delete successfully")
	if err := r.Update(ctx, instance); err != nil && !k8s_errors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *OpenStackNetReconciler) reconcileNormal(
	ctx context.Context,
	instance *netv1.OpenStackNet,
	helper *helper.Helper,
) (ctrl.Result, error) {

	//
	// Create/update NetworkAttachmentDefinition
	//
	if err := r.createOrUpdateNetworkAttachmentDefinition(ctx, instance, helper, false); err != nil {
		return ctrl.Result{}, err
	}

	//
	// Create/update static NetworkAttachmentDefinition used for openstackclient
	//
	if err := r.createOrUpdateNetworkAttachmentDefinition(ctx, instance, helper, true); err != nil {
		return ctrl.Result{}, err
	}

	//
	// Update status with reservation count and flattened reservations
	//
	reservedIPCount := 0
	reservations := map[string]netv1.NodeIPReservation{}
	for _, roleReservation := range instance.Spec.RoleReservations {
		for _, reservation := range roleReservation.Reservations {
			reservedIPCount++
			reservations[reservation.Hostname] = netv1.NodeIPReservation{
				IP:      reservation.IP,
				Deleted: reservation.Deleted,
			}
		}
	}

	instance.Status.ReservedIPCount = reservedIPCount
	instance.Status.Reservations = reservations

	// If we get this far, we assume the NAD been successfully created (NAD does not
	// have a status block we can examine)
	util.LogForObject(helper, fmt.Sprintf("OpenStackNet %s has been successfully configured on targeted node(s)", instance.Name), instance)

	//cond.Type = shared.NetConfigured

	return ctrl.Result{}, nil
}

//
// createOrUpdateNetworkAttachmentDefinition - create or update NetworkAttachmentDefinition
//
func (r *OpenStackNetReconciler) createOrUpdateNetworkAttachmentDefinition(
	ctx context.Context,
	instance *netv1.OpenStackNet,
	helper *helper.Helper,
	nadStatic bool,
) error {
	networkAttachmentDefinition := &networkv1.NetworkAttachmentDefinition{}
	networkAttachmentDefinition.Name = instance.Name

	//
	// get bridge name from referenced osnetattach CR status
	//
	bridgeName, err := osnet.GetOpenStackNetAttachmentBridgeName(
		ctx,
		r.Client,
		instance.Namespace,
		instance.Spec.AttachConfiguration,
	)
	if err != nil {
		//cond.Message = fmt.Sprintf("OpenStackNet %s failure get bridge for OpenStackNetAttachment %s", instance.Name, instance.Spec.AttachConfiguration)
		//cond.Type = shared.NetError

		return util.WrapErrorForObject(
			fmt.Sprintf("failure get bridge name for OpenStackNetAttachment referenc: %s", instance.Spec.AttachConfiguration), instance, err)
	}

	templateData := map[string]string{
		"Name":       instance.Name,
		"BridgeName": bridgeName,
		"Vlan":       strconv.Itoa(instance.Spec.Vlan),
		"MTU":        strconv.Itoa(instance.Spec.MTU),
	}

	//
	// NAD static for openstackclient pods
	//
	if nadStatic {
		networkAttachmentDefinition.Name = fmt.Sprintf("%s-static", instance.Name)
		templateData["Name"] = fmt.Sprintf("%s-static", instance.Name)
		templateData["Static"] = "true"
	}

	// render CNIConfigTemplate
	CNIConfig, err := util.ExecuteTemplateData(osnet.CniConfigTemplate, templateData)
	if err != nil {
		return err
	}

	if err := util.IsJSON(CNIConfig); err != nil {
		//cond.Message = fmt.Sprintf("OpenStackNet %s failure rendering CNIConfig for NetworkAttachmentDefinition", instance.Name)
		//cond.Type = shared.NetError

		return util.WrapErrorForObject(fmt.Sprintf("failure rendering CNIConfig for NetworkAttachmentDefinition %s: %v", instance.Name, templateData), instance, err)
	}

	networkAttachmentDefinition.Namespace = instance.Namespace

	apply := func() error {
		util.InitMap(&networkAttachmentDefinition.Labels)
		util.InitMap(&networkAttachmentDefinition.Annotations)

		//
		// Labels
		//
		// TODO add controller label to lib-common
		networkAttachmentDefinition.Labels = labels.GetLabels(
			networkAttachmentDefinition,
			labels.GetGroupLabel(helper.GetFinalizer()),
			map[string]string{
				fmt.Sprintf("%s/controller", labels.GetGroupLabel(helper.GetFinalizer())): strings.ToLower(helper.GetFinalizer()),
			},
		)

		//
		// Annotations
		//
		networkAttachmentDefinition.Annotations["k8s.v1.cni.cncf.io/resourceName"] = fmt.Sprintf("bridge.network.kubevirt.io/%s", templateData["BridgeName"])

		//
		// Spec
		//
		networkAttachmentDefinition.Spec = networkv1.NetworkAttachmentDefinitionSpec{
			Config: CNIConfig,
		}

		return controllerutil.SetControllerReference(instance, networkAttachmentDefinition, r.Scheme)
	}

	op, err := controllerutil.CreateOrPatch(ctx, r.Client, networkAttachmentDefinition, apply)
	if err != nil {
		//cond.Message = fmt.Sprintf("Updating %s networkAttachmentDefinition", instance.Name)
		//cond.Type = shared.NetError

		err = util.WrapErrorForObject(fmt.Sprintf("Updating %s networkAttachmentDefinition", instance.Name), networkAttachmentDefinition, err)
		return err
	}

	if op != controllerutil.OperationResultNone {
		util.LogForObject(helper, string(op), networkAttachmentDefinition)

		//cond.Message = fmt.Sprintf("NetworkAttachmentDefinition %s is %s", networkAttachmentDefinition.Name, string(op))
		//cond.Type = shared.NetConfiguring
	} else {
		util.LogForObject(helper, fmt.Sprintf("NetworkAttachmentDefinition %s configured targeted node(s)", networkAttachmentDefinition.Name), networkAttachmentDefinition)
		//cond.Message = fmt.Sprintf("NetworkAttachmentDefinition %s configured targeted node(s)", networkAttachmentDefinition.Name)
		//cond.Type = shared.NetConfigured
	}

	if instance.ObjectMeta.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(networkAttachmentDefinition, helper.GetFinalizer()) {
			controllerutil.AddFinalizer(networkAttachmentDefinition, helper.GetFinalizer())
			if err := r.Update(ctx, networkAttachmentDefinition); err != nil {
				return err
			}
			util.LogForObject(helper, fmt.Sprintf("Finalizer %s added to %s", helper.GetFinalizer(), networkAttachmentDefinition.Name), instance)

		}
	}

	return nil
}

/*
func (r *OpenStackNetReconciler) cleanupNetworkAttachmentDefinition(
	ctx context.Context,
	instance *netv1.OpenStackNet,
	helper *helper.Helper,
) error {

	networkAttachmentDefinitionList := &networkv1.NetworkAttachmentDefinitionList{}

	labelSelector := map[string]string{
		labels.GetOwnerNameLabelSelector(): instance.Name,
		util.OwnerNameLabelSelector: instance.Name,
	}

	listOpts := []client.ListOption{
		client.InNamespace(instance.Namespace),
		client.MatchingLabels(labelSelector),
	}

	if err := r.GetClient().List(ctx, networkAttachmentDefinitionList, listOpts...); err != nil {
		return err
	}

	//
	// Remove finalizer on NADs
	//
	for _, nad := range networkAttachmentDefinitionList.Items {
		controllerutil.RemoveFinalizer(&nad, openstacknet.FinalizerName)
		if err := r.Update(ctx, &nad); err != nil && !k8s_errors.IsNotFound(err) {
			cond.Message = fmt.Sprintf("Failed to update %s %s", instance.Kind, instance.Name)
			cond.Reason = shared.CommonCondReasonRemoveFinalizerError
			cond.Type = shared.CommonCondTypeError

			err = common.WrapErrorForObject(cond.Message, instance, err)

			return err
		}
	}

	//
	// Delete NADs
	//
	if err := r.GetClient().DeleteAllOf(
		ctx,
		&networkv1.NetworkAttachmentDefinition{},
		client.InNamespace(instance.Namespace),
		client.MatchingLabels(map[string]string{
			common.OwnerNameLabelSelector: instance.Name,
		}),
	); err != nil && !k8s_errors.IsNotFound(err) {
		return common.WrapErrorForObject("error DeleteAllOf NetworkAttachmentDefinition", instance, err)
	}

	return nil
}
*/
