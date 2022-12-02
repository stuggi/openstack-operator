/*
Copyright 2021 Red Hat

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
	"time"

	netv1 "github.com/openstack-k8s-operators/openstack-operator/apis/net/v1beta1"
)

// OpenStackNetworkAttachment -
type OpenStackNetworkAttachment struct {
	osNetAttName string
	osNetAttSpec *netv1.NodeConfigurationPolicy
	osNetAtt     *netv1.OpenStackNetAttachment
	labels       map[string]string
	annotations  map[string]string
	timeout      time.Duration
}
