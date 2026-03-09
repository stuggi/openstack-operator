/*
Copyright 2024.

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

	"k8s.io/apimachinery/pkg/types"

	commonbackup "github.com/openstack-k8s-operators/lib-common/modules/common/backup"
	backupv1 "github.com/openstack-k8s-operators/openstack-operator/api/backup/v1beta1"
	corev1 "github.com/openstack-k8s-operators/openstack-operator/api/core/v1beta1"
)

var _ = Describe("OpenStackBackupConfig controller", func() {
	var backupConfigName types.NamespacedName

	When("A OpenStackBackupConfig is created", func() {
		BeforeEach(func() {
			backupConfigName = types.NamespacedName{
				Name:      "test-backup-config",
				Namespace: namespace,
			}

			// Create OpenStackBackupConfig
			backupConfig := &backupv1.OpenStackBackupConfig{
				Spec: backupv1.OpenStackBackupConfigSpec{
					TargetNamespace: namespace,
				},
			}
			backupConfig.Name = backupConfigName.Name
			backupConfig.Namespace = backupConfigName.Namespace

			Expect(k8sClient.Create(ctx, backupConfig)).Should(Succeed())
			DeferCleanup(th.DeleteInstance, backupConfig)
		})

		It("Should exist and be retrievable", func() {
			backupConfig := &backupv1.OpenStackBackupConfig{}
			Expect(k8sClient.Get(ctx, backupConfigName, backupConfig)).Should(Succeed())
			Expect(backupConfig.Spec.TargetNamespace).To(Equal(namespace))
		})
	})

	When("OpenStackBackupConfig reconciles with CRs in namespace", func() {
		BeforeEach(func() {
			backupConfigName = types.NamespacedName{
				Name:      "test-backup-with-crs",
				Namespace: namespace,
			}

			// Create OpenStackControlPlane (CRD has backup-restore labels)
			controlPlaneName := types.NamespacedName{
				Name:      "test-controlplane",
				Namespace: namespace,
			}
			spec := GetDefaultOpenStackControlPlaneSpec()
			CreateOpenStackControlPlane(controlPlaneName, spec)
			DeferCleanup(th.DeleteInstance, GetOpenStackControlPlane(controlPlaneName))

			// Create OpenStackBackupConfig after CRs exist
			backupConfig := &backupv1.OpenStackBackupConfig{
				Spec: backupv1.OpenStackBackupConfigSpec{
					TargetNamespace: namespace,
				},
			}
			backupConfig.Name = backupConfigName.Name
			backupConfig.Namespace = backupConfigName.Namespace

			Expect(k8sClient.Create(ctx, backupConfig)).Should(Succeed())
			DeferCleanup(th.DeleteInstance, backupConfig)
		})

		It("Should label CR instances with backup labels", func() {
			controlPlaneName := types.NamespacedName{
				Name:      "test-controlplane",
				Namespace: namespace,
			}

			// The controller should look up the OpenStackControlPlane CRD
			// (which has openstack.org/backup-restore: "true" and
			// openstack.org/backup-restore-order: "30") and apply
			// backup labels to the CR instance.
			Eventually(func(g Gomega) {
				controlPlane := &corev1.OpenStackControlPlane{}
				g.Expect(k8sClient.Get(ctx, controlPlaneName, controlPlane)).Should(Succeed())

				labels := controlPlane.GetLabels()
				g.Expect(labels).NotTo(BeNil(), "ControlPlane should have labels")
				g.Expect(labels[commonbackup.BackupLabel]).To(
					Equal("true"),
					"ControlPlane should have backup label",
				)
				g.Expect(labels[commonbackup.BackupRestoreOrderLabel]).To(
					Equal("30"),
					"ControlPlane should have restore-order label from CRD",
				)
			}, timeout, interval).Should(Succeed())
		})

		It("Should reconcile without apiVersion/Kind errors", func() {
			// Verify the controller reconciles successfully by checking
			// that the BackupConfig itself gets labeled (its CRD also
			// has backup-restore labels with order "20")
			Eventually(func(g Gomega) {
				backupConfig := &backupv1.OpenStackBackupConfig{}
				g.Expect(k8sClient.Get(ctx, backupConfigName, backupConfig)).Should(Succeed())

				labels := backupConfig.GetLabels()
				g.Expect(labels).NotTo(BeNil(), "BackupConfig should have labels")
				g.Expect(labels[commonbackup.BackupLabel]).To(
					Equal("true"),
					"BackupConfig should have backup label (CRD has backup-restore: true)",
				)
			}, timeout, interval).Should(Succeed())
		})
	})
})
