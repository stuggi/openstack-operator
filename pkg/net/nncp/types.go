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

package nncp

import (
	"time"

	nmstateshared "github.com/nmstate/kubernetes-nmstate/api/shared"
	nmstatev1 "github.com/nmstate/kubernetes-nmstate/api/v1"
)

// NNCP -
type NNCP struct {
	nncpName    string
	nncpSpec    *nmstateshared.NodeNetworkConfigurationPolicySpec
	nncp        *nmstatev1.NodeNetworkConfigurationPolicy
	labels      map[string]string
	annotations map[string]string
	timeout     time.Duration
}
