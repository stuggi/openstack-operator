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
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// log is for logging in this package.
var openstacklog = logf.Log.WithName("openstack-resource")

func (r *OpenStack) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

//+kubebuilder:webhook:path=/mutate-operator-openstack-org-v1beta1-openstack,mutating=true,failurePolicy=fail,sideEffects=None,groups=operator.openstack.org,resources=openstacks,verbs=create;update,versions=v1beta1,name=mopenstack.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &OpenStack{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *OpenStack) Default() {
	openstacklog.Info("default", "name", r.Name)

	r.DefaultOperators()
}

func (r *OpenStack) DefaultOperators() {
	// check if all operators are in the spec.ServiceOperators
	for _, op := range ServiceOperatorNames {
		// validate of nextip is already in a reservation and its not usAdd commentMore actions
		f := func(c OperatorSpec) bool {
			return c.Name == op.Name
		}
		idx := slices.IndexFunc(r.Spec.ServiceOperators, f)
		if idx < 0 {
			// append OperatorSpec for op
			// TODO do we want it to have the same order as ServiceOperatorNames ?
			opSpec := OperatorSpec{
				Name:     op.Name,
				Replicas: ptr.To(ReplicasEnabled),
				ControllerManager: ContainerSpec{
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    DefaultManagerCPULimit,
							corev1.ResourceMemory: DefaultManagerMemoryLimit,
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    DefaultManagerCPURequests,
							corev1.ResourceMemory: DefaultManagerMemoryRequests,
						},
					},
				},
				KubeRbacProxy: ContainerSpec{
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    DefaultRbacProxyCPULimit,
							corev1.ResourceMemory: DefaultRbacProxyMemoryLimit,
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    DefaultRbacProxyCPURequests,
							corev1.ResourceMemory: DefaultRbacProxyMemoryRequests,
						},
					},
				},
			}
			if op.Replicas != nil {
				opSpec.Replicas = op.Replicas
			}
			if op.ControllerManager.Resources.Limits != nil {
				opSpec.ControllerManager.Resources.Limits = op.ControllerManager.Resources.Limits
			}
			if op.ControllerManager.Resources.Requests != nil {
				opSpec.ControllerManager.Resources.Requests = op.ControllerManager.Resources.Requests
			}
			if op.KubeRbacProxy.Resources.Limits != nil {
				opSpec.KubeRbacProxy.Resources.Limits = op.KubeRbacProxy.Resources.Limits
			}
			if op.KubeRbacProxy.Resources.Requests != nil {
				opSpec.KubeRbacProxy.Resources.Requests = op.KubeRbacProxy.Resources.Requests
			}
			r.Spec.ServiceOperators = append(
				r.Spec.ServiceOperators,
				opSpec)
		}
	}
}
