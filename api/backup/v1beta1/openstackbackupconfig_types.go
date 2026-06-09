/*
Copyright 2026.

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
	"context"

	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// BackupLabelingPolicy controls whether backup labeling is active for a resource type
// +kubebuilder:validation:Enum=enabled;disabled
type BackupLabelingPolicy string

const (
	// BackupLabelingEnabled enables backup labeling for the resource type
	BackupLabelingEnabled BackupLabelingPolicy = "enabled"
	// BackupLabelingDisabled disables backup labeling for the resource type
	BackupLabelingDisabled BackupLabelingPolicy = "disabled"
)

// OpenStackBackupConfigSpec defines the desired state of OpenStackBackupConfig.
type OpenStackBackupConfigSpec struct {
	// DefaultRestoreOrder is the restore order assigned to user-provided resources
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="10"
	DefaultRestoreOrder string `json:"defaultRestoreOrder"`

	// Secrets configuration for backup labeling
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={labeling:enabled}
	Secrets ResourceBackupConfig `json:"secrets"`

	// ConfigMaps configuration for backup labeling
	// Defaults: Excludes kube-root-ca.crt and openshift-service-ca.crt
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={labeling:enabled,excludeNames:{"kube-root-ca.crt","openshift-service-ca.crt"}}
	ConfigMaps ResourceBackupConfig `json:"configMaps"`

	// NetworkAttachmentDefinitions configuration for backup labeling
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={labeling:enabled}
	NetworkAttachmentDefinitions ResourceBackupConfig `json:"networkAttachmentDefinitions"`

}

// ResourceBackupConfig defines backup labeling rules for a resource type
type ResourceBackupConfig struct {
	// Labeling controls whether to label this resource type for backup
	// +kubebuilder:validation:Optional
	Labeling *BackupLabelingPolicy `json:"labeling,omitempty"`

	// RestoreOrder overrides the default restore order for this resource type.
	// If empty, the global DefaultRestoreOrder is used.
	// +kubebuilder:validation:Optional
	RestoreOrder string `json:"restoreOrder,omitempty"`

	// ExcludeLabelKeys is a list of label keys - resources with any of these labels are excluded
	// Example: ["service-cert", "osdp-service"] excludes service-cert and dataplane service secrets
	// +kubebuilder:validation:Optional
	ExcludeLabelKeys []string `json:"excludeLabelKeys,omitempty"`

	// ExcludeNames is a list of resource names to exclude from backup labeling
	// Example: ["kube-root-ca.crt", "openshift-service-ca.crt"] for system ConfigMaps
	// +kubebuilder:validation:Optional
	ExcludeNames []string `json:"excludeNames,omitempty"`

	// IncludeLabelSelector allows filtering resources by label selector
	// Only resources matching this selector will be labeled (in addition to ownerRef check)
	// +kubebuilder:validation:Optional
	IncludeLabelSelector map[string]string `json:"includeLabelSelector,omitempty"`
}

// OpenStackBackupConfigStatus defines the observed state of OpenStackBackupConfig.
type OpenStackBackupConfigStatus struct {
	// LabeledResources tracks how many resources of each type were labeled
	// +kubebuilder:validation:Optional
	LabeledResources ResourceCounts `json:"labeledResources,omitempty"`

	// Conditions represents the latest available observations of the resource's current state
	// +operator-sdk:csv:customresourcedefinitions:type=status
	Conditions condition.Conditions `json:"conditions,omitempty"`
}

// ResourceCounts tracks labeled resource counts by type
type ResourceCounts struct {
	// Secrets is the number of secrets labeled for backup
	// +kubebuilder:validation:Optional
	Secrets int `json:"secrets"`

	// ConfigMaps is the number of configmaps labeled for backup
	// +kubebuilder:validation:Optional
	ConfigMaps int `json:"configMaps"`

	// NetworkAttachmentDefinitions is the number of NADs labeled for backup
	// +kubebuilder:validation:Optional
	NetworkAttachmentDefinitions int `json:"networkAttachmentDefinitions"`

	// CRs is the number of CR instances labeled for backup
	// +kubebuilder:validation:Optional
	CRs int `json:"crs"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=osbkpcfg;osbackupcfg;osbackupconfig
// +kubebuilder:printcolumn:name="Secrets",type="integer",JSONPath=".status.labeledResources.secrets",description="Labeled Secrets"
// +kubebuilder:printcolumn:name="ConfigMaps",type="integer",JSONPath=".status.labeledResources.configMaps",description="Labeled ConfigMaps"
// +kubebuilder:printcolumn:name="NADs",type="integer",JSONPath=".status.labeledResources.networkAttachmentDefinitions",description="Labeled NADs"
// +kubebuilder:printcolumn:name="CRs",type="integer",JSONPath=".status.labeledResources.crs",description="Labeled CR instances"
// +kubebuilder:metadata:labels=backup.openstack.org/restore=true
// +kubebuilder:metadata:labels=backup.openstack.org/category=controlplane
// +kubebuilder:metadata:labels=backup.openstack.org/restore-order=20

// OpenStackBackupConfig is the Schema for the openstackbackupconfigs API.
// It configures automatic backup labeling for user-provided resources (without ownerReferences).
type OpenStackBackupConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenStackBackupConfigSpec   `json:"spec,omitempty"`
	Status OpenStackBackupConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OpenStackBackupConfigList contains a list of OpenStackBackupConfig.
type OpenStackBackupConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenStackBackupConfig `json:"items"`
}

// GetOpenStackBackupConfigs returns the OpenStackBackupConfig resources in the given namespace.
func GetOpenStackBackupConfigs(ctx context.Context, namespace string, c client.Client) (*OpenStackBackupConfigList, error) {
	configList := &OpenStackBackupConfigList{}
	listOpts := []client.ListOption{
		client.InNamespace(namespace),
	}
	if err := c.List(ctx, configList, listOpts...); err != nil {
		return nil, err
	}
	return configList, nil
}

func init() {
	SchemeBuilder.Register(&OpenStackBackupConfig{}, &OpenStackBackupConfigList{})
}
