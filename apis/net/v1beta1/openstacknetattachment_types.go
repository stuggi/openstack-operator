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

package v1beta1

import (
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OpenStackNetAttachmentSpec defines the desired state of OpenStackNetAttachment
type OpenStackNetAttachmentSpec struct {
	// +kubebuilder:validation:Required
	// AttachConfiguration used for NodeNetworkConfigurationPolicy or NodeSriovConfigurationPolicy
	AttachConfiguration NodeConfigurationPolicy `json:"attachConfiguration"`
}

// OpenStackNetAttachmentStatus defines the observed state of OpenStackNetAttachment
type OpenStackNetAttachmentStatus struct {
	// Conditions - conditions to display in the OpenShift GUI, which reflect CurrentState
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`

	// AttachType of the OpenStackNetAttachment
	AttachType AttachType `json:"attachType"`

	// BridgeName of the OpenStackNetAttachment
	BridgeName string `json:"bridgeName"`
}

// IsReady - Is this resource in its fully-configured (quiesced) state?
func (instance *OpenStackNetAttachment) IsReady() bool {
	return true
	//return instance.Status.CurrentState == shared.NetAttachConfigured
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=osnetattachment;osnetsattachment;osnetattach;osnetsattach;osnetatt;osnetsatt
//+operator-sdk:csv:customresourcedefinitions:displayName="OpenStack NetAttachment"
//+kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.currentState`,description="Status"

// OpenStackNetAttachment is the Schema for the openstacknetattachments API
type OpenStackNetAttachment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenStackNetAttachmentSpec   `json:"spec,omitempty"`
	Status OpenStackNetAttachmentStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// OpenStackNetAttachmentList contains a list of OpenStackNetAttachment
type OpenStackNetAttachmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenStackNetAttachment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OpenStackNetAttachment{}, &OpenStackNetAttachmentList{})
}
