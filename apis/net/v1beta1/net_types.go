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
	nmstateapi "github.com/nmstate/kubernetes-nmstate/api/shared"
)

// IPReservation contains an IP, Hostname, and a VIP flag
type IPReservation struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
	VIP      bool   `json:"vip"`
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=false
	ServiceVIP bool `json:"serviceVIP,omitempty"`
	Deleted    bool `json:"deleted"`
}

// RoleReservation defines the observed state of the Role Net reservation
type RoleReservation struct {
	// Reservations IP address reservations
	Reservations []IPReservation `json:"reservations"`
	//AddToPredictableIPs bool            `json:"addToPredictableIPs"`
}

// NodeIPReservation contains an IP and Deleted flag
type NodeIPReservation struct {
	IP      string `json:"ip"`
	Deleted bool   `json:"deleted"`
}

// Route definition
type Route struct {
	// +kubebuilder:validation:Required
	// Destination, network CIDR
	Destination string `json:"destination"`

	// +kubebuilder:validation:Required
	// Nexthop, gateway for the destination
	Nexthop string `json:"nexthop"`
}

// AttachType -
type AttachType string

const (
	// AttachTypeBridge -
	AttachTypeBridge AttachType = "bridge"
	// AttachTypeSriov -
	AttachTypeSriov AttachType = "sriov"
)

// NodeConfigurationPolicy - policy definition to create NodeNetworkConfigurationPolicy or NodeSriovConfigurationPolicy
type NodeConfigurationPolicy struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={}
	NodeNetworkConfigurationPolicy nmstateapi.NodeNetworkConfigurationPolicySpec `json:"nodeNetworkConfigurationPolicy,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default={}
	NodeSriovConfigurationPolicy NodeSriovConfigurationPolicy `json:"nodeSriovConfigurationPolicy,omitempty"`
}

// NodeSriovConfigurationPolicy - Node selector and desired state for SRIOV network
type NodeSriovConfigurationPolicy struct {
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	DesiredState SriovState        `json:"desiredState,omitempty"`
}

// SriovState - SRIOV-specific configuration details for an OSP network
type SriovState struct {
	// +kubebuilder:default=vfio-pci
	DeviceType string `json:"deviceType,omitempty"`
	// +kubebuilder:default=9000
	Mtu        uint32 `json:"mtu,omitempty"`
	NumVfs     uint32 `json:"numVfs"`
	Port       string `json:"port"`
	RootDevice string `json:"rootDevice,omitempty"`
	// +kubebuilder:validation:Enum={"on","off"}
	// +kubebuilder:default=on
	SpoofCheck string `json:"spoofCheck,omitempty"`
	// +kubebuilder:validation:Enum={"on","off"}
	// +kubebuilder:default=off
	Trust string `json:"trust,omitempty"`
}
