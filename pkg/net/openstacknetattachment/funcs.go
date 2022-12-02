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

package openstacknetattachment

import (
	"context"
	"fmt"
	"time"

	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	util "github.com/openstack-k8s-operators/lib-common/modules/common/util"

	netv1 "github.com/openstack-k8s-operators/openstack-operator/apis/net/v1beta1"

	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

//
// NewOpenStackNetworkAttachment returns an initialized OpenStackNetworkAttachment
//
func NewOpenStackNetworkAttachment(
	name string,
	namespace string,
	spec *netv1.NodeConfigurationPolicy,
	labels map[string]string,
	annotations map[string]string,
	timeout time.Duration,
) *OpenStackNetworkAttachment {
	return &OpenStackNetworkAttachment{
		osNetAttName:      name,
		osNetAttNamespace: namespace,
		osNetAttSpec:      spec,
		osNetAtt:          &netv1.OpenStackNetAttachment{},
		labels:            labels,
		annotations:       annotations,
		timeout:           time.Duration(timeout) * time.Second, // timeout to set in s to reconcile
	}
}

//
// CreateOrPatch - creates or patches a OpenStackNetworkAttachment, reconciles after Xs if object won't exist.
//
func (n *OpenStackNetworkAttachment) CreateOrPatch(
	ctx context.Context,
	h *helper.Helper,
) (ctrl.Result, error) {
	n.osNetAtt.ObjectMeta.Name = n.osNetAttName
	n.osNetAtt.ObjectMeta.Namespace = n.osNetAttNamespace

	op, err := controllerutil.CreateOrPatch(ctx, h.GetClient(), n.osNetAtt, func() error {
		// NNCP NodeSelector is immutable so we set this value only if
		// a new object is going to be created
		//if n.nncp.ObjectMeta.CreationTimestamp.IsZero() {
		//	n.nncp.Spec.NodeSelector = n.nncpSpec.NodeSelector
		//}
		n.osNetAtt.Annotations = util.MergeStringMaps(n.osNetAtt.Annotations, n.annotations)
		n.osNetAtt.Labels = util.MergeStringMaps(n.osNetAtt.Labels, n.labels)
		n.osNetAtt.Spec.AttachConfiguration = *n.osNetAttSpec

		// add finaizer
		controllerutil.AddFinalizer(n.osNetAtt, h.GetFinalizer())

		err := controllerutil.SetControllerReference(h.GetBeforeObject(), n.osNetAtt, h.GetScheme())
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			util.LogForObject(h, fmt.Sprintf("OpenStackNetworkAttachment not found, reconcile in %s", n.timeout), n.osNetAtt)
			return ctrl.Result{RequeueAfter: n.timeout}, nil
		}
		return ctrl.Result{}, err
	}
	if op != controllerutil.OperationResultNone {
		util.LogForObject(h, fmt.Sprintf("OpenStackNetworkAttachment: %s", op), n.osNetAtt)
	}

	return ctrl.Result{}, nil
}

//
// GetOpenStackNetAttachment - get the OpenStackNetAttachment object.
//
func (n *OpenStackNetworkAttachment) GetOpenStackNetAttachment() netv1.OpenStackNetAttachment {
	return *n.osNetAtt
}

func (n *OpenStackNetworkAttachment) getOpenStackNetAttachmentWithName(
	ctx context.Context,
	h *helper.Helper,
) error {
	err := h.GetClient().Get(ctx, types.NamespacedName{Name: n.osNetAttName, Namespace: n.osNetAttNamespace}, n.osNetAtt)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			return util.WrapErrorForObject(
				fmt.Sprintf("Failed to get OpenStackNetAttachment %s ", n.osNetAttName),
				h.GetBeforeObject(),
				err,
			)
		}

		return util.WrapErrorForObject(
			fmt.Sprintf("OpenStackNetAttachment error %s", n.osNetAttName),
			h.GetBeforeObject(),
			err,
		)
	}

	return nil
}

//
// DeleteFinalizer deletes a finalizer by its object
//
func (n *OpenStackNetworkAttachment) DeleteFinalizer(
	ctx context.Context,
	h *helper.Helper,
) error {
	controllerutil.RemoveFinalizer(n.osNetAtt, h.GetFinalizer())
	if err := h.GetClient().Update(ctx, n.osNetAtt); err != nil && !k8s_errors.IsNotFound(err) {
		return err
	}
	return nil
}

//
// GetOpenStackNetAttachmentByName returns a *OpenStackNetworkAttachment object with specified name
//
func GetOpenStackNetworkAttachmentByName(
	ctx context.Context,
	h *helper.Helper,
	name string,
	namespace string,
) (*OpenStackNetworkAttachment, error) {
	// create a OpenStackNetworkAttachment by suppplying a resource name
	osNetAttObj := &OpenStackNetworkAttachment{
		osNetAttName:      name,
		osNetAttNamespace: namespace,
		osNetAtt:          &netv1.OpenStackNetAttachment{},
	}

	if err := osNetAttObj.getOpenStackNetAttachmentWithName(ctx, h); err != nil {
		return osNetAttObj, err
	}

	osNetAttObj.annotations = osNetAttObj.osNetAtt.Annotations
	osNetAttObj.labels = osNetAttObj.osNetAtt.Labels

	return osNetAttObj, nil
}
