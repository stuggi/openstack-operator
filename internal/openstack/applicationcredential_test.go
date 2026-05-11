package openstack

import (
	"context"
	"testing"

	. "github.com/onsi/gomega" //revive:disable:dot-imports

	keystonev1 "github.com/openstack-k8s-operators/keystone-operator/api/v1beta1"
	"github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	libSecret "github.com/openstack-k8s-operators/lib-common/modules/common/secret"
	corev1beta1 "github.com/openstack-k8s-operators/openstack-operator/api/core/v1beta1"
	dataplanev1 "github.com/openstack-k8s-operators/openstack-operator/api/dataplane/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func init() {
	_ = corev1beta1.AddToScheme(scheme.Scheme)
	_ = keystonev1.AddToScheme(scheme.Scheme)
	_ = dataplanev1.AddToScheme(scheme.Scheme)
}

func acTestHelper(objects ...client.Object) *helper.Helper {
	s := scheme.Scheme

	fakeClient := fakeclient.NewClientBuilder().
		WithScheme(s).
		WithObjects(objects...).
		WithStatusSubresource(
			&keystonev1.KeystoneApplicationCredential{},
			&dataplanev1.OpenStackDataPlaneNodeSet{},
		).
		Build()

	mockObj := &corev1beta1.OpenStackControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cp",
			Namespace: "test-ns",
		},
	}

	h, _ := helper.NewHelper(
		mockObj,
		fakeClient,
		fake.NewSimpleClientset(),
		s,
		ctrl.Log.WithName("test"),
	)
	return h
}

func readyACCR(name, namespace, secretName string, annotations map[string]string) *keystonev1.KeystoneApplicationCredential {
	cr := &keystonev1.KeystoneApplicationCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: annotations,
		},
		Status: keystonev1.KeystoneApplicationCredentialStatus{
			SecretName: secretName,
			Conditions: condition.Conditions{
				{
					Type:   condition.ReadyCondition,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}
	return cr
}

// --- ReconcilePendingEDPMSyncs tests ---

func TestReconcilePendingEDPMSyncs_NoACCRs(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()
	h := acTestHelper()

	result, err := ReconcilePendingEDPMSyncs(ctx, h, "test-ns")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result).To(Equal(ctrl.Result{}), "should return zero result when no AC CRs exist")
}

func TestReconcilePendingEDPMSyncs_DeployedMatchesCurrent(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	novaAC := readyACCR("ac-nova", "test-ns", "ac-nova-abc12-secret", map[string]string{
		EDPMDeployedSecretAnnotation: "ac-nova-abc12-secret",
	})
	ceilAC := readyACCR("ac-ceilometer", "test-ns", "ac-ceilometer-def34-secret", map[string]string{
		EDPMDeployedSecretAnnotation: "ac-ceilometer-def34-secret",
	})

	h := acTestHelper(novaAC, ceilAC)
	novaAC.Status.SecretName = "ac-nova-abc12-secret"
	g.Expect(h.GetClient().Status().Update(ctx, novaAC)).To(Succeed())
	ceilAC.Status.SecretName = "ac-ceilometer-def34-secret"
	g.Expect(h.GetClient().Status().Update(ctx, ceilAC)).To(Succeed())

	result, err := ReconcilePendingEDPMSyncs(ctx, h, "test-ns")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result).To(Equal(ctrl.Result{}), "should return zero result when deployed == current for all services")
}

func TestReconcilePendingEDPMSyncs_DeployedDiffersFromCurrent(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	acCR := readyACCR("ac-nova", "test-ns", "ac-nova-NEW-secret", map[string]string{
		EDPMDeployedSecretAnnotation:    "ac-nova-OLD-secret",
		EDPMSyncedConfigHashAnnotation: "old-hash",
	})
	oldSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ac-nova-OLD-secret", Namespace: "test-ns"},
		Data:       map[string][]byte{"credential": []byte("old")},
	}
	configSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "nova-config", Namespace: "test-ns"},
		Data:       map[string][]byte{"config": []byte("new-config")},
	}
	dpSvc := &dataplanev1.OpenStackDataPlaneService{
		ObjectMeta: metav1.ObjectMeta{Name: "nova", Namespace: "test-ns"},
		Spec: dataplanev1.OpenStackDataPlaneServiceSpec{
			EDPMServiceType: "nova",
			DataSources: []dataplanev1.DataSource{
				{SecretRef: &dataplanev1.SecretEnvSource{LocalObjectReference: dataplanev1.LocalObjectReference{Name: "nova-config"}}},
			},
		},
	}

	ns := &dataplanev1.OpenStackDataPlaneNodeSet{
		ObjectMeta: metav1.ObjectMeta{Name: "compute-0", Namespace: "test-ns"},
		Spec: dataplanev1.OpenStackDataPlaneNodeSetSpec{
			PreProvisioned: true,
			Services:       []string{"nova"},
			Nodes:          map[string]dataplanev1.NodeSection{},
			NodeTemplate:   dataplanev1.NodeTemplate{},
		},
	}

	h := acTestHelper(acCR, oldSecret, configSecret, dpSvc, ns)
	acCR.Status.SecretName = "ac-nova-NEW-secret"
	g.Expect(h.GetClient().Status().Update(ctx, acCR)).To(Succeed())
	ns.Status.SecretHashes = map[string]string{"nova-config": "stale-hash"}
	g.Expect(h.GetClient().Status().Update(ctx, ns)).To(Succeed())

	result, err := ReconcilePendingEDPMSyncs(ctx, h, "test-ns")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(EDPMSyncFallbackInterval),
		"should return RequeueAfter when deployed != current and NodeSets not synced")
}

func TestReconcilePendingEDPMSyncs_NoAnnotationMeansNoSync(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	novaAC := readyACCR("ac-nova", "test-ns", "ac-nova-abc12-secret", nil)

	h := acTestHelper(novaAC)
	novaAC.Status.SecretName = "ac-nova-abc12-secret"
	g.Expect(h.GetClient().Status().Update(ctx, novaAC)).To(Succeed())

	result, err := ReconcilePendingEDPMSyncs(ctx, h, "test-ns")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result).To(Equal(ctrl.Result{}),
		"should return zero result when no EDPM annotations are set")
}

// --- getEDPMConfigSecretNames tests ---

func TestGetEDPMConfigSecretNames_EmptyServiceType(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()
	h := acTestHelper()

	names, err := getEDPMConfigSecretNames(ctx, h.GetClient(), "test-ns", "")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(names).To(BeNil(), "should return nil for empty service type")
}

func TestGetEDPMConfigSecretNames_DynamicDiscovery(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	svc := &dataplanev1.OpenStackDataPlaneService{
		ObjectMeta: metav1.ObjectMeta{Name: "nova", Namespace: "test-ns"},
		Spec: dataplanev1.OpenStackDataPlaneServiceSpec{
			EDPMServiceType: "nova",
			DataSources: []dataplanev1.DataSource{
				{SecretRef: &dataplanev1.SecretEnvSource{LocalObjectReference: dataplanev1.LocalObjectReference{Name: "nova-cell1-config"}}},
				{SecretRef: &dataplanev1.SecretEnvSource{LocalObjectReference: dataplanev1.LocalObjectReference{Name: "nova-compute-config"}}},
			},
		},
	}

	h := acTestHelper(svc)
	names, err := getEDPMConfigSecretNames(ctx, h.GetClient(), "test-ns", "nova")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(names).To(ConsistOf("nova-cell1-config", "nova-compute-config"))
}

func TestGetEDPMConfigSecretNames_CustomService(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	// Custom service with a different name but same EDPMServiceType
	customSvc := &dataplanev1.OpenStackDataPlaneService{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-nova-compute", Namespace: "test-ns"},
		Spec: dataplanev1.OpenStackDataPlaneServiceSpec{
			EDPMServiceType: "nova",
			DataSources: []dataplanev1.DataSource{
				{SecretRef: &dataplanev1.SecretEnvSource{LocalObjectReference: dataplanev1.LocalObjectReference{Name: "custom-nova-config"}}},
			},
		},
	}

	h := acTestHelper(customSvc)
	names, err := getEDPMConfigSecretNames(ctx, h.GetClient(), "test-ns", "nova")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(names).To(ConsistOf("custom-nova-config"),
		"should discover custom services by EDPMServiceType")
}

func TestGetEDPMConfigSecretNames_MultipleServicesUnion(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	svc1 := &dataplanev1.OpenStackDataPlaneService{
		ObjectMeta: metav1.ObjectMeta{Name: "nova", Namespace: "test-ns"},
		Spec: dataplanev1.OpenStackDataPlaneServiceSpec{
			EDPMServiceType: "nova",
			DataSources: []dataplanev1.DataSource{
				{SecretRef: &dataplanev1.SecretEnvSource{LocalObjectReference: dataplanev1.LocalObjectReference{Name: "nova-config"}}},
			},
		},
	}
	svc2 := &dataplanev1.OpenStackDataPlaneService{
		ObjectMeta: metav1.ObjectMeta{Name: "nova-custom", Namespace: "test-ns"},
		Spec: dataplanev1.OpenStackDataPlaneServiceSpec{
			EDPMServiceType: "nova",
			DataSources: []dataplanev1.DataSource{
				{SecretRef: &dataplanev1.SecretEnvSource{LocalObjectReference: dataplanev1.LocalObjectReference{Name: "nova-custom-config"}}},
				{SecretRef: &dataplanev1.SecretEnvSource{LocalObjectReference: dataplanev1.LocalObjectReference{Name: "nova-config"}}}, // duplicate
			},
		},
	}

	h := acTestHelper(svc1, svc2)
	names, err := getEDPMConfigSecretNames(ctx, h.GetClient(), "test-ns", "nova")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(names).To(ConsistOf("nova-config", "nova-custom-config"),
		"should return union of secrets, deduplicated")
}

func TestGetEDPMConfigSecretNames_DefaultsToName(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	// EDPMServiceType not set — defaults to CR name
	svc := &dataplanev1.OpenStackDataPlaneService{
		ObjectMeta: metav1.ObjectMeta{Name: "nova", Namespace: "test-ns"},
		Spec: dataplanev1.OpenStackDataPlaneServiceSpec{
			DataSources: []dataplanev1.DataSource{
				{SecretRef: &dataplanev1.SecretEnvSource{LocalObjectReference: dataplanev1.LocalObjectReference{Name: "nova-config"}}},
			},
		},
	}

	h := acTestHelper(svc)
	names, err := getEDPMConfigSecretNames(ctx, h.GetClient(), "test-ns", "nova")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(names).To(ConsistOf("nova-config"),
		"should match on CR name when EDPMServiceType is empty")
}

// --- allNodeSetsSynced tests ---

func TestAllNodeSetsSynced_NoConfigSecrets(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()
	h := acTestHelper()

	synced, err := allNodeSetsSynced(ctx, h.GetClient(), "test-ns", nil)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(synced).To(BeTrue(), "should return true when no config secrets to check")
}

func TestAllNodeSetsSynced_NoNodeSets(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "nova-config", Namespace: "test-ns"},
		Data:       map[string][]byte{"config": []byte("data")},
	}
	h := acTestHelper(sec)

	synced, err := allNodeSetsSynced(ctx, h.GetClient(), "test-ns", []string{"nova-config"})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(synced).To(BeTrue(), "should return true when no NodeSets exist")
}

func TestAllNodeSetsSynced_AllMatch(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	configSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "nova-config", Namespace: "test-ns"},
		Data:       map[string][]byte{"config": []byte("data-v1")},
	}

	h := acTestHelper(configSecret)
	liveHash := computeSecretHash(t, h.GetClient(), "nova-config", "test-ns")

	ns := &dataplanev1.OpenStackDataPlaneNodeSet{
		ObjectMeta: metav1.ObjectMeta{Name: "compute-0", Namespace: "test-ns"},
		Spec: dataplanev1.OpenStackDataPlaneNodeSetSpec{
			PreProvisioned: true,
			Services:       []string{"nova"},
			Nodes:          map[string]dataplanev1.NodeSection{},
			NodeTemplate:   dataplanev1.NodeTemplate{},
		},
	}
	g.Expect(h.GetClient().Create(ctx, ns)).To(Succeed())
	ns.Status.SecretHashes = map[string]string{"nova-config": liveHash}
	g.Expect(h.GetClient().Status().Update(ctx, ns)).To(Succeed())

	synced, err := allNodeSetsSynced(ctx, h.GetClient(), "test-ns", []string{"nova-config"})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(synced).To(BeTrue(), "should return true when all NodeSet hashes match")
}

func TestAllNodeSetsSynced_SingleStale(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	configSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "nova-config", Namespace: "test-ns"},
		Data:       map[string][]byte{"config": []byte("data-v2-new")},
	}

	ns := &dataplanev1.OpenStackDataPlaneNodeSet{
		ObjectMeta: metav1.ObjectMeta{Name: "compute-0", Namespace: "test-ns"},
		Spec: dataplanev1.OpenStackDataPlaneNodeSetSpec{
			PreProvisioned: true,
			Services:       []string{"nova"},
			Nodes:          map[string]dataplanev1.NodeSection{},
			NodeTemplate:   dataplanev1.NodeTemplate{},
		},
	}

	h := acTestHelper(configSecret, ns)
	ns.Status.SecretHashes = map[string]string{"nova-config": "stale-hash-from-old-deploy"}
	g.Expect(h.GetClient().Status().Update(ctx, ns)).To(Succeed())

	synced, err := allNodeSetsSynced(ctx, h.GetClient(), "test-ns", []string{"nova-config"})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(synced).To(BeFalse(), "should return false when a NodeSet has a stale hash")
}

func TestAllNodeSetsSynced_MixedSyncState(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	configSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "nova-config", Namespace: "test-ns"},
		Data:       map[string][]byte{"config": []byte("data-v3")},
	}

	h := acTestHelper(configSecret)
	liveHash := computeSecretHash(t, h.GetClient(), "nova-config", "test-ns")

	ns1 := &dataplanev1.OpenStackDataPlaneNodeSet{
		ObjectMeta: metav1.ObjectMeta{Name: "compute-synced", Namespace: "test-ns"},
		Spec: dataplanev1.OpenStackDataPlaneNodeSetSpec{
			PreProvisioned: true,
			Services:       []string{"nova"},
			Nodes:          map[string]dataplanev1.NodeSection{},
			NodeTemplate:   dataplanev1.NodeTemplate{},
		},
	}
	g.Expect(h.GetClient().Create(ctx, ns1)).To(Succeed())
	ns1.Status.SecretHashes = map[string]string{"nova-config": liveHash}
	g.Expect(h.GetClient().Status().Update(ctx, ns1)).To(Succeed())

	ns2 := &dataplanev1.OpenStackDataPlaneNodeSet{
		ObjectMeta: metav1.ObjectMeta{Name: "compute-stale", Namespace: "test-ns"},
		Spec: dataplanev1.OpenStackDataPlaneNodeSetSpec{
			PreProvisioned: true,
			Services:       []string{"nova"},
			Nodes:          map[string]dataplanev1.NodeSection{},
			NodeTemplate:   dataplanev1.NodeTemplate{},
		},
	}
	g.Expect(h.GetClient().Create(ctx, ns2)).To(Succeed())
	ns2.Status.SecretHashes = map[string]string{"nova-config": "old-hash"}
	g.Expect(h.GetClient().Status().Update(ctx, ns2)).To(Succeed())

	synced, err := allNodeSetsSynced(ctx, h.GetClient(), "test-ns", []string{"nova-config"})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(synced).To(BeFalse(), "should return false when any NodeSet is stale")
}

func TestAllNodeSetsSynced_SecretNotFound(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	ns := &dataplanev1.OpenStackDataPlaneNodeSet{
		ObjectMeta: metav1.ObjectMeta{Name: "compute-0", Namespace: "test-ns"},
		Spec: dataplanev1.OpenStackDataPlaneNodeSetSpec{
			PreProvisioned: true,
			Services:       []string{"nova"},
			Nodes:          map[string]dataplanev1.NodeSection{},
			NodeTemplate:   dataplanev1.NodeTemplate{},
		},
	}

	h := acTestHelper(ns)

	synced, err := allNodeSetsSynced(ctx, h.GetClient(), "test-ns", []string{"nonexistent-secret"})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(synced).To(BeTrue(), "should skip missing secrets without error")
}

// computeSecretHash is a test helper that fetches a secret and computes its hash,
// matching the same algorithm used by allNodeSetsSynced.
func computeSecretHash(t *testing.T, c client.Client, name, namespace string) string {
	t.Helper()
	g := NewWithT(t)

	sec := &corev1.Secret{}
	err := c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: namespace}, sec)
	g.Expect(err).ToNot(HaveOccurred())

	hash, err := libSecret.Hash(sec)
	g.Expect(err).ToNot(HaveOccurred())
	return hash
}
