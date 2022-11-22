/*
Copyright 2022 Red Hat

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

	netv1 "github.com/openstack-k8s-operators/openstack-operator/apis/net/v1beta1"

	v1 "k8s.io/api/apps/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

//
// GetOpenStackNetAttachmentWithLabel - Return OpenStackNet with labels
//
func GetOpenStackNetAttachmentWithLabel(
	ctx context.Context,
	c client.Client,
	namespace string,
	labelSelector map[string]string,
) (*netv1.OpenStackNetAttachment, error) {
	osNetAttachList, err := GetOpenStackNetAttachmentsWithLabel(
		ctx,
		c,
		namespace,
		labelSelector,
	)
	if err != nil {
		return nil, err
	}
	if len(osNetAttachList.Items) == 0 {
		return nil, k8s_errors.NewNotFound(v1.Resource("openstacknetattachment"), fmt.Sprint(labelSelector))
	} else if len(osNetAttachList.Items) > 1 {
		return nil, fmt.Errorf("multiple OpenStackNetAttachments with label %v not found", labelSelector)
	}
	return &osNetAttachList.Items[0], nil
}

// GetOpenStackNetAttachmentsWithLabel - Return a list of all OpenStackNetAttachmentss in the namespace that have (optional) labels
func GetOpenStackNetAttachmentsWithLabel(
	ctx context.Context,
	c client.Client,
	namespace string,
	labelSelector map[string]string,
) (*netv1.OpenStackNetAttachmentList, error) {
	osNetAttachList := &netv1.OpenStackNetAttachmentList{}

	listOpts := []client.ListOption{
		client.InNamespace(namespace),
	}

	if len(labelSelector) > 0 {
		labels := client.MatchingLabels(labelSelector)
		listOpts = append(listOpts, labels)
	}

	if err := c.List(ctx, osNetAttachList, listOpts...); err != nil {
		return nil, err
	}

	return osNetAttachList, nil
}

// GetOpenStackNetAttachmentWithAttachReference - Return OpenStackNetAttachment for the reference name use in the osnet config
func GetOpenStackNetAttachmentWithAttachReference(
	ctx context.Context,
	c client.Client,
	namespace string,
	attachReference string,
) (*netv1.OpenStackNetAttachment, error) {
	osNetAttach, err := GetOpenStackNetAttachmentWithLabel(
		ctx,
		c,
		namespace,
		map[string]string{
			AttachReference: attachReference,
		},
	)
	if err != nil {
		return nil, err
	}

	return osNetAttach, nil
}

// GetOpenStackNetAttachmentType - Return type of OpenStackNetAttachment, either bridge or sriov
func GetOpenStackNetAttachmentType(
	ctx context.Context,
	c client.Client,
	namespace string,
	attachReference string,
) (*netv1.AttachType, error) {

	osNetAttach, err := GetOpenStackNetAttachmentWithAttachReference(
		ctx,
		c,
		namespace,
		attachReference,
	)
	if err != nil {
		return nil, err
	}

	return &osNetAttach.Status.AttachType, nil
}

// GetOpenStackNetAttachmentBridgeName - Return name of the Bridge configured by the OpenStackNetAttachment
func GetOpenStackNetAttachmentBridgeName(
	ctx context.Context,
	c client.Client,
	namespace string,
	attachReference string,
) (string, error) {

	osNetAttach, err := GetOpenStackNetAttachmentWithAttachReference(
		ctx,
		c,
		namespace,
		attachReference,
	)
	if err != nil {
		return "", err
	}

	return osNetAttach.Status.BridgeName, nil
}
