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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	BarbicanOperatorName           = "barbican"
	CinderOperatorName             = "cinder"
	DesignateOperatorName          = "designate"
	GlanceOperatorName             = "glance"
	HeatOperatorName               = "heat"
	HorizonOperatorName            = "horizon"
	InfraOperatorName              = "infra"
	IronicOperatorName             = "ironic"
	KeystoneOperatorName           = "keystone"
	ManilaOperatorName             = "manila"
	MariaDBOperatorName            = "mariadb"
	NeutronOperatorName            = "neutron"
	NovaOperatorName               = "nova"
	OctaviaOperatorName            = "octavia"
	OpenStackBaremetalOperatorName = "openstack-baremetal"
	OvnOperatorName                = "ovn"
	PlacementOperatorName          = "placement"
	RabbitMQOperatorName           = "rabbitmq-cluster"
	SwiftOperatorName              = "swift"
	TelemetryOperatorName          = "telemetry"
	TestOperatorName               = "test"
	OkrOperatorName                = "okr"
)

// NOTE: test-operator was deployed as a independant package so it may or may not be installed
// NOTE: depending on how watcher-operator is released for FR2 and then in FR3 it may need to be
// added into this list in the future
// IMPORTANT: have this list in synce with the kubebuilder annotations of the ServiceOperators parameter
var (
	ServiceOperatorNames []string = []string{
		BarbicanOperatorName,
		CinderOperatorName,
		DesignateOperatorName,
		GlanceOperatorName,
		HeatOperatorName,
		HorizonOperatorName,
		InfraOperatorName,
		IronicOperatorName,
		KeystoneOperatorName,
		ManilaOperatorName,
		MariaDBOperatorName,
		NeutronOperatorName,
		NovaOperatorName,
		OctaviaOperatorName,
		OpenStackBaremetalOperatorName,
		OvnOperatorName,
		PlacementOperatorName,
		RabbitMQOperatorName,
		SwiftOperatorName,
		TelemetryOperatorName,
		TestOperatorName,
		OkrOperatorName,
	}
)

// OpenStackSpec defines the desired state of OpenStack
type OpenStackSpec struct {
	// +kubebuilder:validation:Optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:XValidation:rule="self.all(x, self.filter(y, y.name == x.name).size() == 1)",message="names in ServiceOperators must be unique"
	// +kubebuilder:default={{name: barbican}, {name: cinder}, {name: designate}, {name: glance}, {name: heat}, {name: horizon}, {name: infra}, {name: keystone}, {name: manila}, {name: mariadb}, {name: neutron}, {name: nova}, {name: octavia}, {name: openstack-baremetal}, {name: ovn}, {name: placement}, {name: rabbitmq-cluster}, {name: swift}, {name: telemetry}, {name: test}, {name: okr, replicas: 0}}
	// ServiceOperators - list of service operators to deploy with tunings
	// NOTE: test-operator was deployed as a independant package so it may or may not be installed
	// NOTE: depending on how watcher-operator is released for FR2 and then in FR3 it may need to be
	// added into this list in the future
	ServiceOperators []OperatorSpec `json:"serviceOperators"`
}

// OperatorSpec - customization for the operator deployment
type OperatorSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// Name of the service operators.
	Name string `json:"name"`
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Maximum=1
	// +kubebuilder:validation:Minimum=0
	// Replicas of the operator deployment
	Replicas *int32 `json:"replicas"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default={limits: {cpu: "500m", memory: "128Mi"},requests: {cpu: "10m", memory: "256Mi"}}
	// ControllerManager - tunings for the controller manager container
	ControllerManager ContainerSpec `json:"controllerManager"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default={limits: {cpu: "500m", memory: "128Mi"},requests: {cpu: "5m", memory: "64Mi"}}
	// ControllerManager - tunings for the kube-rbac-proxy container
	KubeRbacProxy ContainerSpec `json:"kubeRbacProxy"`
}

// ContainerSpec - customizion for the container spec
type ContainerSpec struct {
	// +kubebuilder:validation:Optional
	// Resources - Compute Resources for the service operator controller manager
	// https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// OpenStackStatus defines the observed state of OpenStack
type OpenStackStatus struct {

	// +operator-sdk:csv:customresourcedefinitions:type=status,xDescriptors={"urn:alm:descriptor:io.kubernetes.conditions"}
	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`

	// DeployedOperatorCount - the number of operators deployed
	DeployedOperatorCount *int `json:"deployedOperatorCount,omitempty"`

	// ObservedGeneration - the most recent generation observed for this object.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"` // no spec yet so maybe we don't need this

	// ContainerImage - the container image that has been successfully deployed
	ContainerImage *string `json:"containerImage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +operator-sdk:csv:customresourcedefinitions:displayName="OpenStack"
// +kubebuilder:printcolumn:name="Deployed Operator Count",type=integer,JSONPath=`.status.deployedOperatorCount`
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[0].status",description="Status"
// OpenStack is the Schema for the openstacks API
type OpenStack struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenStackSpec   `json:"spec,omitempty"`
	Status OpenStackStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// OpenStackList contains a list of OpenStack
type OpenStackList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenStack `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OpenStack{}, &OpenStackList{})
}
