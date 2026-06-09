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

package util //nolint:revive // util is an acceptable package name in this context

import (
	"context"
	"testing"

	. "github.com/onsi/gomega" //revive:disable:dot-imports

	dataplanev1 "github.com/openstack-k8s-operators/openstack-operator/api/dataplane/v1beta1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestParseAnsibleExecutionSummaryFromPod(t *testing.T) {
	g := NewWithT(t)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "runner",
					State: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							Message: `{"totalHosts":3,"failedHosts":1,"unreachableHosts":1,"failurePercent":67,"failedHostList":["host-b"],"unreachableHostList":["host-c"]}`,
						},
					},
				},
			},
		},
	}

	summary, err := ParseAnsibleExecutionSummaryFromPod(pod)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(summary).To(Equal(&dataplanev1.AnsibleExecutionSummary{
		TotalHosts:          ptr.To(3),
		FailedHosts:         ptr.To(1),
		UnreachableHosts:    ptr.To(1),
		FailurePercent:      ptr.To(67),
		FailedHostList:      &[]string{"host-b"},
		UnreachableHostList: &[]string{"host-c"},
	}))
}

func TestGetAnsibleExecutionSummary(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-job",
			Namespace: "test-namespace",
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"batch.kubernetes.io/job-name": "test-job",
			},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "runner",
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							Message: `{"totalHosts":2,"failedHosts":1,"unreachableHosts":0,"failurePercent":50,"failedHostList":["host-b"],"unreachableHostList":[]}`,
						},
					},
				},
			},
		},
	}

	h := setupTestHelper(false, job, pod)

	summary, err := GetAnsibleExecutionSummary(ctx, h, job)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(summary).To(Equal(&dataplanev1.AnsibleExecutionSummary{
		TotalHosts:          ptr.To(2),
		FailedHosts:         ptr.To(1),
		UnreachableHosts:    ptr.To(0),
		FailurePercent:      ptr.To(50),
		FailedHostList:      &[]string{"host-b"},
		UnreachableHostList: &[]string{},
	}))
}
