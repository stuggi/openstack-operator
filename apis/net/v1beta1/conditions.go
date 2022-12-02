/*

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
)

//
// OpenStackNetAttachment Condition Types used by API objects.
//
const (

	// OpenStackNetAttachmentReadyCondition Status=True condition when OpenStackNetAttachment created/patched ok
	OpenStackNetAttachmentReadyCondition condition.Type = "OpenStackNetAttachmentReady"

	// NNCPReadyCondition Status=True condition when nncp created/patched ok
	NNCPReadyCondition condition.Type = "NNCPReady"

	// SrIOVReadyCondition Status=True condition when sriov created/patched ok
	SrIOVReadyCondition condition.Type = "SrIOVReady"
)

//
// OpenStackNetAttachment Reasons used by API objects.
//
const ()

//
// Common Messages used by API objects.
//
const (
	//
	// OpenStackNetAttachmentReady condition messages
	//

	// OpenStackNetAttachmentReadyInitMessage string
	OpenStackNetAttachmentReadyInitMessage = "OpenStackNetAttachment not started"

	// OpenStackNetAttachmentReadyMessage string
	OpenStackNetAttachmentReadyMessage = "OpenStackNetAttachment completed"

	// OpenStackNetAttachmentReadyRunningMessage string
	OpenStackNetAttachmentReadyRunningMessage = "OpenStackNetAttachment in progress"

	// OpenStackNetAttachmentReadyErrorMessage error string
	OpenStackNetAttachmentReadyErrorMessage = "OpenStackNetAttachment error occured %s"

	//
	// NNCPReady condition messages
	//

	// NNCPReadyInitMessage string
	NNCPReadyInitMessage = "Deployment not started"

	// NNCPReadyMessage string
	NNCPReadyMessage = "Deployment completed"

	// NNCPReadyRunningMessage string
	NNCPReadyRunningMessage = "NNCP in progress"

	// NNCPReadyErrorMessage error string
	NNCPReadyErrorMessage = "NNCP error occured %s"
)
