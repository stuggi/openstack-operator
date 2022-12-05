/*
Copyright 2020 Red Hat

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

package nncp

import (
	"context"
	"fmt"
	"regexp"
	"time"

	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	util "github.com/openstack-k8s-operators/lib-common/modules/common/util"

	nmstate "github.com/openstack-k8s-operators/openstack-operator/pkg/net/nmstate"

	nmstateshared "github.com/nmstate/kubernetes-nmstate/api/shared"
	nmstatev1 "github.com/nmstate/kubernetes-nmstate/api/v1"

	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	//metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

//
// NewNNCP returns an initialized NNCP
//
func NewNNCP(
	nncpName string,
	nncpSpec *nmstateshared.NodeNetworkConfigurationPolicySpec,
	labels map[string]string,
	annotations map[string]string,
	timeout time.Duration,
) *NNCP {
	return &NNCP{
		nncpName:    nncpName,
		nncpSpec:    nncpSpec,
		nncp:        &nmstatev1.NodeNetworkConfigurationPolicy{},
		labels:      labels,
		annotations: annotations,
		timeout:     time.Duration(timeout) * time.Second, // timeout to set in s to reconcile
	}
}

//
// CreateOrPatch - creates or patches a DaemonSet, reconciles after Xs if object won't exist.
//
func (n *NNCP) CreateOrPatch(
	ctx context.Context,
	h *helper.Helper,
) (ctrl.Result, error) {
	n.nncp.ObjectMeta.Name = n.nncpName

	op, err := controllerutil.CreateOrPatch(ctx, h.GetClient(), n.nncp, func() error {
		// NNCP NodeSelector is immutable so we set this value only if
		// a new object is going to be created
		if n.nncp.ObjectMeta.CreationTimestamp.IsZero() {
			n.nncp.Spec.NodeSelector = n.nncpSpec.NodeSelector
		}
		n.nncp.Annotations = util.MergeStringMaps(n.nncp.Annotations, n.annotations)
		n.nncp.Labels = util.MergeStringMaps(n.nncp.Labels, n.labels)
		n.nncp.Spec = *n.nncpSpec

		// add finaizer
		controllerutil.AddFinalizer(n.nncp, h.GetFinalizer())

		// Note: can not set controller reference as it is not possible to set a reference
		// on a cluster scoped resource with a namespaced owning resource. Thats why we set
		// the owner label information.

		return nil
	})
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			util.LogForObject(h, fmt.Sprintf("NodeNetworkConfigurationPolicy not found, reconcile in %s", n.timeout), n.nncp)
			return ctrl.Result{RequeueAfter: n.timeout}, nil
		}
		return ctrl.Result{}, err
	}
	if op != controllerutil.OperationResultNone {
		util.LogForObject(h, fmt.Sprintf("NodeNetworkConfigurationPolicy: %s", op), n.nncp)
	}

	return ctrl.Result{}, nil
}

//
// Delete - delete a NodeNetworkConfigurationPolicy.
//
func (n *NNCP) Delete(
	ctx context.Context,
	h *helper.Helper,
) (reconcile.Result, error) {

	// if the nncp is already gone, return
	err := n.getNNCPWithName(ctx, h)
	if err != nil {
		if !k8s_errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	//bridgeState, err := nmstate.GetDesiredStateBridgeInterfaceState(n.nncp.Spec.DesiredState.Raw)
	interfaceStates, err := nmstate.GetDesiredStateInterfaceStates(h, n.nncp.Spec.DesiredState.Raw)
	if err != nil {
		msg := fmt.Sprintf("Error getting interface states from from %s networkConfigurationPolicy", n.nncpName)
		//cond.Message = fmt.Sprintf("Error getting interface state for %s from %s networkConfigurationPolicy", instance.Status.BridgeName, networkConfigurationPolicy.Name)
		//cond.Reason = shared.ConditionReason(cond.Message)
		//cond.Type = shared.NetAttachError

		return ctrl.Result{}, util.WrapErrorForObject(msg, n.nncp, err)
	}

	stillUp := false
	for _, ifstate := range interfaceStates {
		if ifstate != "absent" && ifstate != "down" {
			stillUp = true
		}
	}

	if stillUp {
		apply := func() error {
			desiredState, err := nmstate.GetDesiredStateAsString(n.nncp.Spec.DesiredState.Raw)
			if err != nil {
				return err
			}

			//
			// Update nncp desired state to absent of all interfaces from the NNCP to unconfigure the device on the worker nodes
			// https://docs.openshift.com/container-platform/4.9/networking/k8s_nmstate/k8s-nmstate-updating-node-network-config.html
			//
			re := regexp.MustCompile(`"state":"up"`)
			desiredStateAbsent := re.ReplaceAllString(desiredState, `"state":"absent"`)

			n.nncp.Spec.DesiredState = nmstateshared.State{
				Raw: nmstateshared.RawState(desiredStateAbsent),
			}

			return nil
		}

		//
		// 1) Update nncp desired state to down to unconfigure the device on the worker nodes
		//
		op, err := controllerutil.CreateOrPatch(ctx, h.GetClient(), n.nncp, apply)
		if err != nil {
			msg := fmt.Sprintf("Updating %s networkConfigurationPolicy", n.nncpName)
			//cond.Message = fmt.Sprintf("Updating %s networkConfigurationPolicy", instance.Status.BridgeName)
			//cond.Reason = shared.ConditionReason(cond.Message)
			//cond.Type = shared.NetAttachError

			err = util.WrapErrorForObject(msg, n.nncp, err)
			return ctrl.Result{}, err
		}

		if op != controllerutil.OperationResultNone {
			util.LogForObject(h, string(op), n.nncp)
		}

		//
		// 2) Delete nncp that DeletionTimestamp get set
		//
		if err := h.GetClient().Delete(ctx, n.nncp); err != nil && !k8s_errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}

	} else if !stillUp && n.nncp.DeletionTimestamp != nil {
		deletionTime := n.nncp.GetDeletionTimestamp().Time
		condition := nmstate.GetCurrentCondition(n.nncp.Status.Conditions)
		if condition != nil {
			nncpStateChangeTime := condition.LastTransitionTime.Time

			h.GetLogger().Info(fmt.Sprintf("DELETE NNCP %s", n.nncpName))

			//
			// 3) Remove finalizer if nncp update finished
			//
			if nncpStateChangeTime.Sub(deletionTime).Seconds() > 0 &&
				condition.Type == "Available" &&
				condition.Reason == "SuccessfullyConfigured" {

				h.GetLogger().Info(fmt.Sprintf("DELETE NNCP remove finalizer %s", n.nncpName))

				if controllerutil.RemoveFinalizer(n.nncp, h.GetFinalizer()) {
					return ctrl.Result{}, h.GetClient().Update(ctx, n.nncp)
				}
			}
		}
	}
	//
	// RequeueAfter after 20s and get the nncp CR deleted when the device got removed from the worker
	//
	return ctrl.Result{RequeueAfter: time.Second * 20}, nil
}

//
// GetNNCP - get the NodeNetworkConfigurationPolicy object.
//
func (n *NNCP) GetNNCP() nmstatev1.NodeNetworkConfigurationPolicy {
	return *n.nncp
}

func (n *NNCP) getNNCPWithName(
	ctx context.Context,
	h *helper.Helper,
) error {
	err := h.GetClient().Get(ctx, types.NamespacedName{Name: n.nncpName}, n.nncp)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			return util.WrapErrorForObject(
				fmt.Sprintf("Failed to get nncp %s ", n.nncpName),
				h.GetBeforeObject(),
				err,
			)
		}

		return util.WrapErrorForObject(
			fmt.Sprintf("NNCP error %s", n.nncpName),
			h.GetBeforeObject(),
			err,
		)
	}

	return nil
}

//
// DeleteFinalizer deletes a finalizer by its object
//
func (n *NNCP) DeleteFinalizer(
	ctx context.Context,
	h *helper.Helper,
) error {

	if controllerutil.RemoveFinalizer(n.nncp, h.GetFinalizer()) {
		if err := h.GetClient().Update(ctx, n.nncp); err != nil && !k8s_errors.IsNotFound(err) {
			return err
		}
	}

	return nil
}

//
// GetNNCPByName returns a *NNCP object with specified name
//
func GetNNCPByName(
	ctx context.Context,
	h *helper.Helper,
	name string,
) (*NNCP, error) {
	// create a NNCP by suppplying a resource name
	nncpObj := &NNCP{
		nncpName: name,
		nncp:     &nmstatev1.NodeNetworkConfigurationPolicy{},
	}

	if err := nncpObj.getNNCPWithName(ctx, h); err != nil {
		return nncpObj, err
	}

	nncpObj.annotations = nncpObj.nncp.Annotations
	nncpObj.labels = nncpObj.nncp.Labels

	return nncpObj, nil
}
