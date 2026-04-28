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

package functional_test

import (
	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	common_helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	dataplanev1 "github.com/openstack-k8s-operators/openstack-operator/api/dataplane/v1beta1"
	"github.com/openstack-k8s-operators/openstack-operator/internal/openstack"
)

// newTestHelper creates a Helper for use in tests. The owner object is only
// used by Helper for owner references — the helpers under test only use
// GetClient(), so the owner is irrelevant.
func newTestHelper() *common_helper.Helper {
	kclient, err := kubernetes.NewForConfig(cfg)
	Expect(err).ToNot(HaveOccurred())
	dummy := &dataplanev1.OpenStackDataPlaneDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "dummy", Namespace: namespace},
	}
	h, err := common_helper.NewHelper(dummy, k8sClient, kclient, scheme.Scheme, ctrl.Log)
	Expect(err).ToNot(HaveOccurred())
	return h
}

var _ = Describe("Dataplane deployment helpers", func() {
	var (
		h           *common_helper.Helper
		service     *dataplanev1.OpenStackDataPlaneService
		nodeset     *dataplanev1.OpenStackDataPlaneNodeSet
		nodesetList *dataplanev1.OpenStackDataPlaneNodeSetList
	)

	BeforeEach(func() {
		h = newTestHelper()

		service = &dataplanev1.OpenStackDataPlaneService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ovn",
				Namespace: namespace,
			},
			Spec: dataplanev1.OpenStackDataPlaneServiceSpec{
				EDPMServiceType: "ovn",
				Playbook:        "osp.edpm.ovn",
			},
		}
		Expect(k8sClient.Create(ctx, service)).Should(Succeed())
		DeferCleanup(th.DeleteInstance, service)

		nodeset = &dataplanev1.OpenStackDataPlaneNodeSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-nodeset",
				Namespace: namespace,
			},
			Spec: dataplanev1.OpenStackDataPlaneNodeSetSpec{
				Services: []string{"ovn"},
				Nodes: map[string]dataplanev1.NodeSection{
					"compute-0": {},
				},
				NodeTemplate: dataplanev1.NodeTemplate{
					AnsibleSSHPrivateKeySecret: "ssh-key",
				},
			},
		}
		Expect(k8sClient.Create(ctx, nodeset)).Should(Succeed())
		DeferCleanup(th.DeleteInstance, nodeset)

		nodesetList = &dataplanev1.OpenStackDataPlaneNodeSetList{
			Items: []dataplanev1.OpenStackDataPlaneNodeSet{*nodeset},
		}
	})

	When("no deployments exist", func() {
		It("IsDataplaneDeploymentRunningForServiceType returns false", func() {
			Eventually(func(g Gomega) {
				running, err := openstack.IsDataplaneDeploymentRunningForServiceType(
					ctx, h, namespace, nodesetList, "ovn")
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(running).To(BeFalse())
			}, timeout, interval).Should(Succeed())
		})

		It("IsDataplaneDeploymentCompletedForServiceType returns false", func() {
			Eventually(func(g Gomega) {
				completed, err := openstack.IsDataplaneDeploymentCompletedForServiceType(
					ctx, h, namespace, nodesetList, "ovn", "0.6.39")
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(completed).To(BeFalse())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a running deployment exists with ovn service override", func() {
		BeforeEach(func() {
			deployment := &dataplanev1.OpenStackDataPlaneDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "edpm-ovn-update",
					Namespace: namespace,
				},
				Spec: dataplanev1.OpenStackDataPlaneDeploymentSpec{
					NodeSets:              []string{"test-nodeset"},
					ServicesOverride:      []string{"ovn"},
					DeploymentRequeueTime: 15,
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).Should(Succeed())
			DeferCleanup(th.DeleteInstance, deployment)
		})

		It("IsDataplaneDeploymentRunningForServiceType returns true", func() {
			Eventually(func(g Gomega) {
				running, err := openstack.IsDataplaneDeploymentRunningForServiceType(
					ctx, h, namespace, nodesetList, "ovn")
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(running).To(BeTrue())
			}, timeout, interval).Should(Succeed())
		})

		It("IsDataplaneDeploymentCompletedForServiceType returns false", func() {
			Eventually(func(g Gomega) {
				completed, err := openstack.IsDataplaneDeploymentCompletedForServiceType(
					ctx, h, namespace, nodesetList, "ovn", "0.6.39")
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(completed).To(BeFalse())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a completed deployment exists with matching version", func() {
		BeforeEach(func() {
			deployment := &dataplanev1.OpenStackDataPlaneDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "edpm-ovn-update-done",
					Namespace: namespace,
				},
				Spec: dataplanev1.OpenStackDataPlaneDeploymentSpec{
					NodeSets:              []string{"test-nodeset"},
					ServicesOverride:      []string{"ovn"},
					DeploymentRequeueTime: 15,
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).Should(Succeed())
			DeferCleanup(th.DeleteInstance, deployment)

			// Mark as deployed with target version
			deployment.Status.Deployed = true
			deployment.Status.DeployedVersion = "0.6.39"
			Expect(k8sClient.Status().Update(ctx, deployment)).Should(Succeed())
		})

		It("IsDataplaneDeploymentRunningForServiceType returns false", func() {
			Eventually(func(g Gomega) {
				running, err := openstack.IsDataplaneDeploymentRunningForServiceType(
					ctx, h, namespace, nodesetList, "ovn")
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(running).To(BeFalse())
			}, timeout, interval).Should(Succeed())
		})

		It("IsDataplaneDeploymentCompletedForServiceType returns true", func() {
			Eventually(func(g Gomega) {
				completed, err := openstack.IsDataplaneDeploymentCompletedForServiceType(
					ctx, h, namespace, nodesetList, "ovn", "0.6.39")
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(completed).To(BeTrue())
			}, timeout, interval).Should(Succeed())
		})

		It("IsDataplaneDeploymentCompletedForServiceType returns false for wrong version", func() {
			Eventually(func(g Gomega) {
				completed, err := openstack.IsDataplaneDeploymentCompletedForServiceType(
					ctx, h, namespace, nodesetList, "ovn", "0.6.40")
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(completed).To(BeFalse())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a deployment exists for a different service type", func() {
		BeforeEach(func() {
			otherService := &dataplanev1.OpenStackDataPlaneService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nova",
					Namespace: namespace,
				},
				Spec: dataplanev1.OpenStackDataPlaneServiceSpec{
					EDPMServiceType: "nova",
					Playbook:        "osp.edpm.nova",
				},
			}
			Expect(k8sClient.Create(ctx, otherService)).Should(Succeed())
			DeferCleanup(th.DeleteInstance, otherService)

			deployment := &dataplanev1.OpenStackDataPlaneDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "edpm-nova-update",
					Namespace: namespace,
				},
				Spec: dataplanev1.OpenStackDataPlaneDeploymentSpec{
					NodeSets:              []string{"test-nodeset"},
					ServicesOverride:      []string{"nova"},
					DeploymentRequeueTime: 15,
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).Should(Succeed())
			DeferCleanup(th.DeleteInstance, deployment)
		})

		It("IsDataplaneDeploymentRunningForServiceType returns false for ovn", func() {
			Eventually(func(g Gomega) {
				running, err := openstack.IsDataplaneDeploymentRunningForServiceType(
					ctx, h, namespace, nodesetList, "ovn")
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(running).To(BeFalse())
			}, timeout, interval).Should(Succeed())
		})
	})

	// This tests the fallback path used when reconciles race: no running
	// deployment exists (it already completed), but the saved condition state
	// was lost. The version controller falls back to checking for a completed
	// deployment with the target version.
	When("a deployment completed but no running deployment exists (race fallback)", func() {
		BeforeEach(func() {
			deployment := &dataplanev1.OpenStackDataPlaneDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "edpm-ovn-race",
					Namespace: namespace,
				},
				Spec: dataplanev1.OpenStackDataPlaneDeploymentSpec{
					NodeSets:              []string{"test-nodeset"},
					ServicesOverride:      []string{"ovn"},
					DeploymentRequeueTime: 15,
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).Should(Succeed())
			DeferCleanup(th.DeleteInstance, deployment)

			deployment.Status.Deployed = true
			deployment.Status.DeployedVersion = "0.6.39"
			Expect(k8sClient.Status().Update(ctx, deployment)).Should(Succeed())
		})

		It("should detect no running deployment and find completed one as fallback", func() {
			Eventually(func(g Gomega) {
				running, err := openstack.IsDataplaneDeploymentRunningForServiceType(
					ctx, h, namespace, nodesetList, "ovn")
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(running).To(BeFalse())

				completed, err := openstack.IsDataplaneDeploymentCompletedForServiceType(
					ctx, h, namespace, nodesetList, "ovn", "0.6.39")
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(completed).To(BeTrue())
			}, timeout, interval).Should(Succeed())
		})
	})

	When("a deployment references a nodeset with no nodes", func() {
		BeforeEach(func() {
			emptyNodeset := &dataplanev1.OpenStackDataPlaneNodeSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "empty-nodeset",
					Namespace: namespace,
				},
				Spec: dataplanev1.OpenStackDataPlaneNodeSetSpec{
					Services: []string{"ovn"},
					Nodes:    map[string]dataplanev1.NodeSection{},
					NodeTemplate: dataplanev1.NodeTemplate{
						AnsibleSSHPrivateKeySecret: "ssh-key",
					},
				},
			}
			Expect(k8sClient.Create(ctx, emptyNodeset)).Should(Succeed())
			DeferCleanup(th.DeleteInstance, emptyNodeset)

			deployment := &dataplanev1.OpenStackDataPlaneDeployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "edpm-empty-nodeset",
					Namespace: namespace,
				},
				Spec: dataplanev1.OpenStackDataPlaneDeploymentSpec{
					NodeSets:              []string{"empty-nodeset"},
					ServicesOverride:      []string{"ovn"},
					DeploymentRequeueTime: 15,
				},
			}
			Expect(k8sClient.Create(ctx, deployment)).Should(Succeed())
			DeferCleanup(th.DeleteInstance, deployment)

			nodesetList = &dataplanev1.OpenStackDataPlaneNodeSetList{
				Items: []dataplanev1.OpenStackDataPlaneNodeSet{*emptyNodeset},
			}
		})

		It("IsDataplaneDeploymentRunningForServiceType returns false", func() {
			Eventually(func(g Gomega) {
				running, err := openstack.IsDataplaneDeploymentRunningForServiceType(
					ctx, h, namespace, nodesetList, "ovn")
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(running).To(BeFalse())
			}, timeout, interval).Should(Succeed())
		})
	})
})
