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

package operator

import (
	"context"
	"errors"
	"testing"

	. "github.com/onsi/ginkgo/v2" //revive:disable:dot-imports
	. "github.com/onsi/gomega"    //revive:disable:dot-imports

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorv1beta1 "github.com/openstack-k8s-operators/openstack-operator/apis/operator/v1beta1"
)

func TestController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "OpenStack Controller Suite")
}

// MockClient implements client.Client interface for testing
type MockClient struct {
	// Function fields to control behavior
	ListFunc   func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error
	DeleteFunc func(_ context.Context, _ client.Object, _ ...client.DeleteOption) error

	// Call tracking
	ListCalls   []client.ObjectList
	DeleteCalls []client.Object
}

func (m *MockClient) Get(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	return nil
}

func (m *MockClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	m.ListCalls = append(m.ListCalls, list)
	if m.ListFunc != nil {
		return m.ListFunc(ctx, list, opts...)
	}
	return nil
}

func (m *MockClient) Create(_ context.Context, _ client.Object, _ ...client.CreateOption) error {
	return nil
}

func (m *MockClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	m.DeleteCalls = append(m.DeleteCalls, obj)
	if m.DeleteFunc != nil {
		return m.DeleteFunc(ctx, obj, opts...)
	}
	return nil
}

func (m *MockClient) Update(_ context.Context, _ client.Object, _ ...client.UpdateOption) error {
	return nil
}

func (m *MockClient) Patch(_ context.Context, _ client.Object, _ client.Patch, _ ...client.PatchOption) error {
	return nil
}

func (m *MockClient) DeleteAllOf(_ context.Context, _ client.Object, _ ...client.DeleteAllOfOption) error {
	return nil
}

func (m *MockClient) Status() client.StatusWriter {
	return nil
}

func (m *MockClient) Scheme() *runtime.Scheme {
	return nil
}

func (m *MockClient) RESTMapper() meta.RESTMapper {
	return nil
}

func (m *MockClient) SubResource(_ string) client.SubResourceClient {
	return nil
}

func (m *MockClient) GroupVersionKindFor(_ runtime.Object) (schema.GroupVersionKind, error) {
	return schema.GroupVersionKind{}, nil
}

func (m *MockClient) IsObjectNamespaced(_ runtime.Object) (bool, error) {
	return true, nil
}

// createMockOperator creates a mock OLM Operator object
func createMockOperator(name, namespace string, refs []interface{}) *unstructured.Unstructured {
	operator := &unstructured.Unstructured{}
	operator.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "operators.coreos.com",
		Version: "v1",
		Kind:    "Operator",
	})
	operator.SetName(name)
	operator.SetNamespace(namespace)

	if len(refs) > 0 {
		err := unstructured.SetNestedSlice(operator.Object, refs, "status", "components", "refs")
		if err != nil {
			panic(err)
		}
	}

	return operator
}

// createMockReference creates a mock reference object
func createMockReference(apiVersion, kind, name, namespace string) map[string]interface{} {
	ref := map[string]interface{}{
		"apiVersion": apiVersion,
		"kind":       kind,
		"name":       name,
	}
	if namespace != "" {
		ref["namespace"] = namespace
	}
	return ref
}

var _ = Describe("OpenStackReconciler.postCleanupObsoleteResources", func() {
	var (
		reconciler *OpenStackReconciler
		mockClient *MockClient
		ctx        context.Context
		instance   *operatorv1beta1.OpenStack
	)

	BeforeEach(func() {
		mockClient = &MockClient{}
		reconciler = &OpenStackReconciler{
			Client: mockClient,
		}
		ctx = context.Background()
		instance = &operatorv1beta1.OpenStack{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-openstack",
				Namespace: "test-namespace",
			},
		}
	})

	Context("when no operators exist", func() {
		It("should return without error", func() {
			// Setup mock to return empty operator list
			mockClient.ListFunc = func(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
				// Keep the list empty
				return nil
			}

			err := reconciler.postCleanupObsoleteResources(ctx, instance)
			Expect(err).ToNot(HaveOccurred())

			// Verify List was called
			Expect(mockClient.ListCalls).To(HaveLen(1))
		})
	})

	Context("when operators.coreos.com API is not available", func() {
		It("should return the error from List operation", func() {
			expectedErr := errors.New("operators.coreos.com API not available")
			mockClient.ListFunc = func(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
				return expectedErr
			}

			err := reconciler.postCleanupObsoleteResources(ctx, instance)
			Expect(err).To(MatchError(expectedErr))
		})
	})

	Context("when non-service operators exist", func() {
		It("should skip non-service operators", func() {
			// Create a non-service operator
			nonServiceOperator := createMockOperator("cert-manager.cert-manager", "test-namespace", nil)

			mockClient.ListFunc = func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				ul := list.(*unstructured.UnstructuredList)
				ul.Items = []unstructured.Unstructured{*nonServiceOperator}
				return nil
			}

			// Should not call Delete since it's not a service operator
			err := reconciler.postCleanupObsoleteResources(ctx, instance)
			Expect(err).ToNot(HaveOccurred())

			// Verify no Delete calls were made
			Expect(mockClient.DeleteCalls).To(HaveLen(0))
		})
	})

	Context("when service operators exist without references", func() {
		It("should delete the operator successfully", func() {
			// Create a service operator without references
			serviceOperator := createMockOperator("keystone-operator.openstack-operators", "test-namespace", nil)

			mockClient.ListFunc = func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				ul := list.(*unstructured.UnstructuredList)
				ul.Items = []unstructured.Unstructured{*serviceOperator}
				return nil
			}

			err := reconciler.postCleanupObsoleteResources(ctx, instance)
			Expect(err).ToNot(HaveOccurred())

			// Verify Delete was called on the operator
			Expect(mockClient.DeleteCalls).To(HaveLen(1))
		})
	})

	Context("when service operators exist with references", func() {
		It("should delete references and requeue", func() {
			// Create references that should be deleted (mix of core and non-core resources)
			refs := []interface{}{
				createMockReference("v1", "ConfigMap", "test-config", "test-namespace"),
				createMockReference("apps/v1", "Deployment", "test-deployment", "test-namespace"),
			}

			serviceOperator := createMockOperator("nova-operator.openstack-operators", "test-namespace", refs)

			mockClient.ListFunc = func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				ul := list.(*unstructured.UnstructuredList)
				ul.Items = []unstructured.Unstructured{*serviceOperator}
				return nil
			}

			err := reconciler.postCleanupObsoleteResources(ctx, instance)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Requeuing/Found references for operator name: nova-operator.openstack-operators"))

			// Verify Delete was called for each reference (2 times)
			Expect(mockClient.DeleteCalls).To(HaveLen(2))
		})

		It("should skip CRD references", func() {
			// Create references including a CRD that should be skipped
			refs := []interface{}{
				createMockReference("v1", "ConfigMap", "test-config", "test-namespace"),
				createMockReference("apiextensions.k8s.io/v1", "CustomResourceDefinition", "test-crd", ""), // No namespace for CRDs
			}

			serviceOperator := createMockOperator("glance-operator.openstack-operators", "test-namespace", refs)

			mockClient.ListFunc = func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				ul := list.(*unstructured.UnstructuredList)
				ul.Items = []unstructured.Unstructured{*serviceOperator}
				return nil
			}

			err := reconciler.postCleanupObsoleteResources(ctx, instance)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Requeuing/Found references"))

			// Expect Delete to be called only for the ConfigMap (1 time), not the CRD
			Expect(mockClient.DeleteCalls).To(HaveLen(1))
		})

		It("should handle NotFound errors gracefully", func() {
			refs := []interface{}{
				createMockReference("v1", "ConfigMap", "missing-config", "test-namespace"),
			}

			serviceOperator := createMockOperator("cinder-operator.openstack-operators", "test-namespace", refs)

			mockClient.ListFunc = func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				ul := list.(*unstructured.UnstructuredList)
				ul.Items = []unstructured.Unstructured{*serviceOperator}
				return nil
			}

			// Return NotFound error for the delete operation
			notFoundErr := apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "configmaps"}, "missing-config")
			mockClient.DeleteFunc = func(_ context.Context, _ client.Object, _ ...client.DeleteOption) error {
				return notFoundErr
			}

			err := reconciler.postCleanupObsoleteResources(ctx, instance)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Requeuing/Found references"))

			// Verify Delete was called once
			Expect(mockClient.DeleteCalls).To(HaveLen(1))
		})

		It("should return other delete errors", func() {
			refs := []interface{}{
				createMockReference("v1", "ConfigMap", "test-config", "test-namespace"),
			}

			serviceOperator := createMockOperator("neutron-operator.openstack-operators", "test-namespace", refs)

			mockClient.ListFunc = func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				ul := list.(*unstructured.UnstructuredList)
				ul.Items = []unstructured.Unstructured{*serviceOperator}
				return nil
			}

			// Return a non-NotFound error
			deleteErr := errors.New("permission denied")
			mockClient.DeleteFunc = func(_ context.Context, _ client.Object, _ ...client.DeleteOption) error {
				return deleteErr
			}

			err := reconciler.postCleanupObsoleteResources(ctx, instance)
			Expect(err).To(MatchError(deleteErr))
		})

		It("should handle different apiVersion formats correctly", func() {
			// Test both core resources (v1) and non-core resources (group/version)
			refs := []interface{}{
				createMockReference("v1", "ConfigMap", "core-resource", "test-namespace"),
				createMockReference("apps/v1", "Deployment", "apps-resource", "test-namespace"),
				createMockReference("networking.k8s.io/v1", "NetworkPolicy", "networking-resource", "test-namespace"),
			}

			serviceOperator := createMockOperator("test-operator.openstack-operators", "test-namespace", refs)

			mockClient.ListFunc = func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				ul := list.(*unstructured.UnstructuredList)
				ul.Items = []unstructured.Unstructured{*serviceOperator}
				return nil
			}

			err := reconciler.postCleanupObsoleteResources(ctx, instance)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Requeuing/Found references"))

			// Verify Delete was called for all three references
			Expect(mockClient.DeleteCalls).To(HaveLen(3))
		})
	})

	Context("when operator deletion fails", func() {
		It("should return the deletion error", func() {
			// Create a service operator without references
			serviceOperator := createMockOperator("horizon-operator.openstack-operators", "test-namespace", nil)

			mockClient.ListFunc = func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				ul := list.(*unstructured.UnstructuredList)
				ul.Items = []unstructured.Unstructured{*serviceOperator}
				return nil
			}

			// Return an error when trying to delete the operator
			deleteErr := errors.New("failed to delete operator")
			mockClient.DeleteFunc = func(_ context.Context, _ client.Object, _ ...client.DeleteOption) error {
				return deleteErr
			}

			err := reconciler.postCleanupObsoleteResources(ctx, instance)
			Expect(err).To(MatchError(deleteErr))
		})
	})
})
