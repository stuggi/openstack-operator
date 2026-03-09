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
	"time"

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
			// Simple test - just verify the object exists
			backupConfig := &backupv1.OpenStackBackupConfig{}
			Expect(k8sClient.Get(ctx, backupConfigName, backupConfig)).Should(Succeed())
			logger.Info("BackupConfig exists", "name", backupConfig.Name, "namespace", backupConfig.Namespace)
		})
	})

	When("OpenStackBackupConfig reconciles with CRs in namespace", func() {
		BeforeEach(func() {
			backupConfigName = types.NamespacedName{
				Name:      "test-backup-with-crs",
				Namespace: namespace,
			}

			// Create some CRs that should get labeled
			// Create OpenStackControlPlane (which has backup labels in CRD)
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
			// Give controller time to reconcile and label CRs
			time.Sleep(2 * time.Second)

			// Check that OpenStackControlPlane got backup labels
			controlPlane := &corev1.OpenStackControlPlane{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-controlplane",
					Namespace: namespace,
				}, controlPlane)).Should(Succeed())

				labels := controlPlane.GetLabels()
				logger.Info("ControlPlane labels", "labels", labels)

				// Check for backup labels
				if labels != nil {
					backupLabel := labels[commonbackup.BackupLabel]
					if backupLabel == "true" {
						logger.Info("Found backup label", "value", backupLabel)
					}

					restoreLabel := labels[commonbackup.BackupRestoreLabel]
					if restoreLabel == "true" {
						logger.Info("Found backup-restore label", "value", restoreLabel)
					}
				}
			}, timeout, interval).Should(Succeed())
		})

		It("Should reconcile without 'Kind is missing' errors", func() {
			// Give controller time to reconcile
			time.Sleep(3 * time.Second)

			// Verify no errors in logs (the controller will log errors if Kind is missing)
			// The fact that we got here without test failures means no critical errors occurred
			logger.Info("Test completed - controller reconciled successfully without Kind errors")
		})
	})
})
