package openstack

import (
	"context"
	"fmt"
	"sort"
	"time"

	keystonev1 "github.com/openstack-k8s-operators/keystone-operator/api/v1beta1"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	object "github.com/openstack-k8s-operators/lib-common/modules/common/object"
	"github.com/openstack-k8s-operators/lib-common/modules/common/secret"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	corev1beta1 "github.com/openstack-k8s-operators/openstack-operator/api/core/v1beta1"
	dataplanev1 "github.com/openstack-k8s-operators/openstack-operator/api/dataplane/v1beta1"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// mergeAppCred returns a new ApplicationCredentialSection by overlaying
// service-specific values on top of the global defaults.
func mergeAppCred(
	global corev1beta1.ApplicationCredentialSection,
	svc *corev1beta1.ServiceAppCredSection,
) corev1beta1.ApplicationCredentialSection {
	out := global
	if svc != nil {
		out.Enabled = svc.Enabled

		// only override expiry/grace if specified
		if svc.ExpirationDays != nil {
			out.ExpirationDays = svc.ExpirationDays
		}
		if svc.GracePeriodDays != nil {
			out.GracePeriodDays = svc.GracePeriodDays
		}

		// only override Roles if user set them
		if len(svc.Roles) > 0 {
			out.Roles = svc.Roles
		}
		// only override Unrestricted if user set it
		if svc.Unrestricted != nil {
			out.Unrestricted = svc.Unrestricted
		}
		// only override AccessRules if user set them
		if len(svc.AccessRules) > 0 {
			out.AccessRules = svc.AccessRules
		}
	}

	return out
}

// isACEnabled checks if AC should be enabled for a given service configuration
func isACEnabled(globalAC corev1beta1.ApplicationCredentialSection, serviceAC *corev1beta1.ServiceAppCredSection) bool {
	// Global AC must be enabled
	if !globalAC.Enabled {
		return false
	}
	// Service AC must be enabled
	return serviceAC != nil && serviceAC.Enabled
}

const (
	// EDPMACConsumerFinalizer blocks keystone-operator from revoking an AC
	// secret while EDPM nodes still reference the old credential.
	EDPMACConsumerFinalizer = "openstack.org/edpm-ac-consumer"

	// EDPMDeployedSecretAnnotation is set on the AC CR to track which AC
	// secret is currently deployed to EDPM nodes. Only that secret gets
	// the EDPM consumer finalizer, avoiding orphaned finalizers on
	// intermediate secrets during rapid rotations (A→B→C).
	EDPMDeployedSecretAnnotation = "openstack.org/edpm-deployed-secret"

	// EDPMSyncedConfigHashAnnotation records the combined config secret hash
	// at the time EDPM was last confirmed synced. Used as a baseline to
	// detect when the service operator has re-rendered the config secret
	// after a rotation.
	EDPMSyncedConfigHashAnnotation = "openstack.org/edpm-synced-config-hash"

	// EDPMSyncFallbackInterval is the safety-net requeue interval while
	// waiting for EDPM to redeploy. The primary trigger is the Watch on
	// OpenStackDataPlaneNodeSet status changes — this fallback only fires
	// if the Watch misses an event.
	EDPMSyncFallbackInterval = 30 * time.Minute
)

// edpmACServiceTypes maps AC service names to the EDPM service type used for
// dynamic DataPlaneService discovery. Only services that actually run on EDPM
// are listed. To add a new EDPM service, add an entry here and pass the
// service type via the edpmServiceType parameter in the service reconciler.
var edpmACServiceTypes = map[string]string{
	"nova":       "nova",
	"ceilometer": "telemetry",
}

// getEDPMConfigSecretNames lists OpenStackDataPlaneService CRs matching the
// given edpmServiceType and returns all secret names from their dataSources.
// This discovers custom services dynamically instead of relying on a hardcoded
// mapping from AC service name to DataPlaneService CR name.
func getEDPMConfigSecretNames(
	ctx context.Context,
	c client.Client,
	namespace string,
	edpmServiceType string,
) ([]string, error) {
	if edpmServiceType == "" {
		return nil, nil
	}

	svcList := &dataplanev1.OpenStackDataPlaneServiceList{}
	if err := c.List(ctx, svcList, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list OpenStackDataPlaneServices: %w", err)
	}

	seen := map[string]bool{}
	var names []string
	for _, svc := range svcList.Items {
		svcType := svc.Spec.EDPMServiceType
		if svcType == "" {
			svcType = svc.Name
		}
		if svcType != edpmServiceType {
			continue
		}
		for _, ds := range svc.Spec.DataSources {
			if ds.SecretRef != nil && !seen[ds.SecretRef.Name] {
				seen[ds.SecretRef.Name] = true
				names = append(names, ds.SecretRef.Name)
			}
		}
	}
	return names, nil
}

// computeCombinedConfigHash fetches each config secret by name, hashes it,
// and returns a single deterministic hash representing the combined state.
func computeCombinedConfigHash(
	ctx context.Context,
	c client.Client,
	namespace string,
	configSecretNames []string,
) (string, error) {
	if len(configSecretNames) == 0 {
		return "", nil
	}

	hashes := make(map[string]string, len(configSecretNames))
	for _, name := range configSecretNames {
		sec := &corev1.Secret{}
		if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, sec); err != nil {
			if k8s_errors.IsNotFound(err) {
				continue
			}
			return "", fmt.Errorf("failed to get config secret %s: %w", name, err)
		}
		h, err := secret.Hash(sec)
		if err != nil {
			return "", fmt.Errorf("failed to hash config secret %s: %w", name, err)
		}
		hashes[name] = h
	}
	if len(hashes) == 0 {
		return "", nil
	}

	keys := make([]string, 0, len(hashes))
	for k := range hashes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	sorted := make([]struct {
		Name string
		Hash string
	}, 0, len(keys))
	for _, k := range keys {
		sorted = append(sorted, struct {
			Name string
			Hash string
		}{k, hashes[k]})
	}
	return util.ObjectHash(sorted)
}

// allNodeSetsSynced returns true when every OpenStackDataPlaneNodeSet has
// SecretHashes matching the live config secrets. Returns true when there
// are no config secrets or no NodeSets.
func allNodeSetsSynced(
	ctx context.Context,
	c client.Client,
	namespace string,
	configSecretNames []string,
) (bool, error) {
	if len(configSecretNames) == 0 {
		return true, nil
	}

	nodeSets := &dataplanev1.OpenStackDataPlaneNodeSetList{}
	if err := c.List(ctx, nodeSets, client.InNamespace(namespace)); err != nil {
		return false, fmt.Errorf("failed to list OpenStackDataPlaneNodeSets: %w", err)
	}
	if len(nodeSets.Items) == 0 {
		return true, nil
	}

	for _, secretName := range configSecretNames {
		sec := &corev1.Secret{}
		err := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, sec)
		if err != nil {
			if k8s_errors.IsNotFound(err) {
				continue
			}
			return false, fmt.Errorf("failed to get config secret %s: %w", secretName, err)
		}
		liveHash, err := secret.Hash(sec)
		if err != nil {
			return false, fmt.Errorf("failed to hash config secret %s: %w", secretName, err)
		}

		for _, ns := range nodeSets.Items {
			deployedHash, exists := ns.Status.SecretHashes[secretName]
			if !exists || deployedHash != liveHash {
				return false, nil
			}
		}
	}

	return true, nil
}

// setEDPMTrackingAnnotations patches the AC CR with the EDPM tracking annotations.
func setEDPMTrackingAnnotations(
	ctx context.Context,
	h *helper.Helper,
	acCR *keystonev1.KeystoneApplicationCredential,
	deployedSecret string,
	configHash string,
) error {
	before := acCR.DeepCopy()
	if acCR.Annotations == nil {
		acCR.Annotations = make(map[string]string)
	}
	acCR.Annotations[EDPMDeployedSecretAnnotation] = deployedSecret
	acCR.Annotations[EDPMSyncedConfigHashAnnotation] = configHash
	patch := client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})
	return h.GetClient().Patch(ctx, acCR, patch)
}

// clearEDPMTrackingAnnotations removes the EDPM tracking annotations from the AC CR
func clearEDPMTrackingAnnotations(
	ctx context.Context,
	h *helper.Helper,
	acCR *keystonev1.KeystoneApplicationCredential,
) error {
	if acCR.Annotations == nil {
		return nil
	}
	_, hasDeployed := acCR.Annotations[EDPMDeployedSecretAnnotation]
	_, hasHash := acCR.Annotations[EDPMSyncedConfigHashAnnotation]
	if !hasDeployed && !hasHash {
		return nil
	}
	before := acCR.DeepCopy()
	delete(acCR.Annotations, EDPMDeployedSecretAnnotation)
	delete(acCR.Annotations, EDPMSyncedConfigHashAnnotation)
	patch := client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})
	return h.GetClient().Patch(ctx, acCR, patch)
}

// addEDPMFinalizerToACSecret adds the EDPM consumer finalizer to the named AC
// secret so keystone-operator will not revoke it while EDPM is out of sync.
func addEDPMFinalizerToACSecret(
	ctx context.Context,
	h *helper.Helper,
	namespace string,
	acSecretName string,
) error {
	if acSecretName == "" {
		return nil
	}
	sec := &corev1.Secret{}
	key := types.NamespacedName{Name: acSecretName, Namespace: namespace}
	if err := h.GetClient().Get(ctx, key, sec); err != nil {
		return fmt.Errorf("failed to get AC secret %s for EDPM finalizer: %w", acSecretName, err)
	}
	return object.AddConsumerFinalizer(ctx, h, sec, EDPMACConsumerFinalizer)
}

// removeEDPMFinalizerFromACSecret removes the EDPM consumer finalizer from
// the named AC secret.
func removeEDPMFinalizerFromACSecret(
	ctx context.Context,
	h *helper.Helper,
	namespace string,
	acSecretName string,
) error {
	if acSecretName == "" {
		return nil
	}
	sec := &corev1.Secret{}
	key := types.NamespacedName{Name: acSecretName, Namespace: namespace}
	if err := h.GetClient().Get(ctx, key, sec); err != nil {
		if k8s_errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get AC secret %s for EDPM finalizer removal: %w", acSecretName, err)
	}
	if !controllerutil.ContainsFinalizer(sec, EDPMACConsumerFinalizer) {
		return nil
	}
	before := sec.DeepCopy()
	controllerutil.RemoveFinalizer(sec, EDPMACConsumerFinalizer)
	patch := client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})
	if err := h.GetClient().Patch(ctx, sec, patch); err != nil {
		return fmt.Errorf("failed to remove EDPM finalizer from AC secret %s: %w", acSecretName, err)
	}
	return nil
}

// reconcileEDPMSync manages the EDPM consumer finalizer for an AC service.
//
// Tracking is done via three annotations on the AC CR:
//   - EDPMDeployedSecretAnnotation: the AC secret currently deployed to EDPM
//   - EDPMSyncedConfigHashAnnotation: the config hash when EDPM last synced
//   - EDPMServiceTypeAnnotation: the EDPM service type for dynamic discovery
//
// Only the secret recorded in EDPMDeployedSecretAnnotation gets the EDPM
// consumer finalizer. Intermediate secrets from rapid rotations on ctlplane
// never receive a finalizer because they were never deployed to EDPM.
//
// Two-phase sync detection:
//
//	Phase 1: config hash differs from synced baseline — service operator
//	         has re-rendered the config with new credentials.
//	Phase 2: all NodeSet SecretHashes match live config — EDPM has been
//	         fully redeployed.
func reconcileEDPMSync(
	ctx context.Context,
	h *helper.Helper,
	namespace string,
	acCR *keystonev1.KeystoneApplicationCredential,
	edpmServiceType string,
) (bool, error) {
	Log := GetLogger(ctx)
	currentSecret := acCR.Status.SecretName

	deployedSecret := ""
	syncedHash := ""
	if acCR.Annotations != nil {
		deployedSecret = acCR.Annotations[EDPMDeployedSecretAnnotation]
		syncedHash = acCR.Annotations[EDPMSyncedConfigHashAnnotation]
	}

	configSecretNames, err := getEDPMConfigSecretNames(ctx, h.GetClient(), namespace, edpmServiceType)
	if err != nil {
		return false, err
	}
	if len(configSecretNames) == 0 {
		return false, nil
	}

	currentConfigHash, err := computeCombinedConfigHash(ctx, h.GetClient(), namespace, configSecretNames)
	if err != nil {
		return false, err
	}
	if currentConfigHash == "" {
		return false, nil
	}

	// Initialize tracking: first time we see this AC with EDPM deployed.
	if deployedSecret == "" {
		synced, err := allNodeSetsSynced(ctx, h.GetClient(), namespace, configSecretNames)
		if err != nil {
			return false, err
		}
		if synced {
			if err := setEDPMTrackingAnnotations(ctx, h, acCR, currentSecret, currentConfigHash); err != nil {
				return false, fmt.Errorf("failed to initialize EDPM tracking on AC CR %s: %w", acCR.Name, err)
			}
			Log.Info("Initialized EDPM tracking on AC CR",
				"edpmServiceType", edpmServiceType, "deployedSecret", currentSecret)
		}
		return false, nil
	}

	// Tracking is in sync — nothing to do.
	if deployedSecret == currentSecret {
		return false, nil
	}

	// Rotation detected: deployedSecret is the old AC still on EDPM.
	// Ensure the deployed (old) secret has the EDPM finalizer.
	deployedSec := &corev1.Secret{}
	if err := h.GetClient().Get(ctx, types.NamespacedName{Name: deployedSecret, Namespace: namespace}, deployedSec); err != nil {
		if k8s_errors.IsNotFound(err) {
			Log.Info("EDPM-deployed AC secret no longer exists, resetting tracking",
				"edpmServiceType", edpmServiceType, "lostSecret", deployedSecret)
			if err := setEDPMTrackingAnnotations(ctx, h, acCR, currentSecret, currentConfigHash); err != nil {
				return false, err
			}
			return false, nil
		}
		return false, err
	}

	if !controllerutil.ContainsFinalizer(deployedSec, EDPMACConsumerFinalizer) {
		if err := addEDPMFinalizerToACSecret(ctx, h, namespace, deployedSecret); err != nil {
			return false, err
		}
		Log.Info("Added EDPM finalizer to deployed AC secret",
			"edpmServiceType", edpmServiceType, "deployedSecret", deployedSecret)
	}

	// Phase 1: has the config been updated by the service operator?
	if currentConfigHash == syncedHash {
		Log.Info("EDPM config not yet updated by service operator",
			"edpmServiceType", edpmServiceType, "deployedSecret", deployedSecret)
		return true, nil
	}

	// Phase 2: have all NodeSets been redeployed with the updated config?
	synced, err := allNodeSetsSynced(ctx, h.GetClient(), namespace, configSecretNames)
	if err != nil {
		return false, err
	}
	if !synced {
		Log.Info("EDPM config updated but NodeSets not yet redeployed",
			"edpmServiceType", edpmServiceType, "deployedSecret", deployedSecret)
		return true, nil
	}

	// Fully synced: remove finalizer from old secret, update tracking to current.
	if err := removeEDPMFinalizerFromACSecret(ctx, h, namespace, deployedSecret); err != nil {
		return false, err
	}
	if err := setEDPMTrackingAnnotations(ctx, h, acCR, currentSecret, currentConfigHash); err != nil {
		return false, err
	}
	Log.Info("EDPM synced, released old AC secret and updated tracking",
		"edpmServiceType", edpmServiceType, "releasedSecret", deployedSecret, "newDeployedSecret", currentSecret)
	return false, nil
}

// ReconcilePendingEDPMSyncs iterates the edpmACServiceTypes map to check each
// EDPM service's AC CR for pending credential syncs and progresses them.
// DataPlaneService CRs are discovered dynamically by EDPMServiceType, so
// custom services (e.g., HCI nova variants) are handled automatically.
//
// Called at the end of reconcileNormal, after all service reconcilers have run,
// so that all EDPM services update their configs in a single reconcile cycle.
// Returns RequeueAfter only when sync is still pending; the primary trigger
// for re-reconciliation is the Watch on OpenStackDataPlaneNodeSet status.
func ReconcilePendingEDPMSyncs(
	ctx context.Context,
	h *helper.Helper,
	namespace string,
) (ctrl.Result, error) {
	pending := false
	for serviceName, edpmServiceType := range edpmACServiceTypes {
		acName := keystonev1.GetACCRName(serviceName)
		acCR := &keystonev1.KeystoneApplicationCredential{}
		err := h.GetClient().Get(ctx, types.NamespacedName{Name: acName, Namespace: namespace}, acCR)
		if err != nil {
			if k8s_errors.IsNotFound(err) {
				continue
			}
			return ctrl.Result{}, err
		}
		if !acCR.IsReady() {
			continue
		}

		isPending, err := reconcileEDPMSync(ctx, h, namespace, acCR, edpmServiceType)
		if err != nil {
			return ctrl.Result{}, err
		}
		if isPending {
			pending = true
		}
	}

	if pending {
		return ctrl.Result{RequeueAfter: EDPMSyncFallbackInterval}, nil
	}
	return ctrl.Result{}, nil
}

// CleanupApplicationCredentialForService deletes the AC CR for a service if it
// exists. Used when a service or its AC is disabled.
//
// For EDPM services (edpmServiceType != ""), deletion is deferred while nodes
// still reference the old credential. ReconcilePendingEDPMSyncs handles sync
// progression; once synced, the next reconcile proceeds with deletion.
// This guard is skipped when the ControlPlane is being torn down.
func CleanupApplicationCredentialForService(
	ctx context.Context,
	helper *helper.Helper,
	instance *corev1beta1.OpenStackControlPlane,
	serviceName string,
	edpmServiceType string,
) error {
	Log := GetLogger(ctx)
	acName := keystonev1.GetACCRName(serviceName)

	if edpmServiceType != "" && instance.DeletionTimestamp == nil {
		acCR := &keystonev1.KeystoneApplicationCredential{}
		err := helper.GetClient().Get(ctx, types.NamespacedName{Name: acName, Namespace: instance.Namespace}, acCR)
		if err != nil {
			if k8s_errors.IsNotFound(err) {
				return nil
			}
			return err
		}

		deployedSecret := ""
		if acCR.Annotations != nil {
			deployedSecret = acCR.Annotations[EDPMDeployedSecretAnnotation]
		}

		if deployedSecret != "" {
			configSecretNames, err := getEDPMConfigSecretNames(ctx, helper.GetClient(), instance.Namespace, edpmServiceType)
			if err != nil {
				return err
			}

			synced, err := allNodeSetsSynced(ctx, helper.GetClient(), instance.Namespace, configSecretNames)
			if err != nil {
				return err
			}
			if !synced {
				Log.Info("EDPM not yet synced, deferring application credential CR deletion",
					"service", serviceName, "acName", acName, "deployedSecret", deployedSecret)
				return nil
			}

			if err := removeEDPMFinalizerFromACSecret(ctx, helper, instance.Namespace, deployedSecret); err != nil {
				return err
			}
			if err := clearEDPMTrackingAnnotations(ctx, helper, acCR); err != nil {
				return err
			}
		}
	}

	acCR := &keystonev1.KeystoneApplicationCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:      acName,
			Namespace: instance.Namespace,
		},
	}
	err := helper.GetClient().Delete(ctx, acCR)
	if k8s_errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	Log.Info("Service disabled, deleted existing KeystoneApplicationCredential CR", "service", serviceName, "acName", acName)
	return nil
}

// EnsureApplicationCredentialForService handles AC creation for a single service.
// If service is not ready, AC creation is deferred
// If AC already exists and is ready, it's used immediately
// If AC doesn't exist and service is ready, AC is created
//
// For EDPM services (edpmServiceType != ""), initial tracking annotations are
// set when the AC becomes ready. The actual EDPM sync reconciliation is handled
// centrally by ReconcilePendingEDPMSyncs at the end of reconcileNormal.
//
// Returns:
//   - acSecretName: name of the AC secret (from status), empty if not ready
//   - result: ctrl.Result with requeue if AC is being created/not ready
//   - err: any error that occurred
func EnsureApplicationCredentialForService(
	ctx context.Context,
	helper *helper.Helper,
	instance *corev1beta1.OpenStackControlPlane,
	serviceName string,
	serviceReady bool,
	secretName string,
	passwordSelector string,
	serviceUser string,
	acConfig *corev1beta1.ServiceAppCredSection,
	edpmServiceType string,
) (acSecretName string, result ctrl.Result, err error) {
	Log := GetLogger(ctx)

	// Generate AC CR name
	acName := keystonev1.GetACCRName(serviceName)

	// Check if AC CR exists
	acCR := &keystonev1.KeystoneApplicationCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:      acName,
			Namespace: instance.Namespace,
		},
	}
	err = helper.GetClient().Get(ctx, types.NamespacedName{Name: acName, Namespace: instance.Namespace}, acCR)

	if err != nil && !k8s_errors.IsNotFound(err) {
		return "", ctrl.Result{}, err
	}
	acExists := err == nil

	// Check if AC is enabled for this service
	if !isACEnabled(instance.Spec.ApplicationCredential, acConfig) {
		if acExists {
			if err := CleanupApplicationCredentialForService(ctx, helper, instance, serviceName, edpmServiceType); err != nil {
				return "", ctrl.Result{}, err
			}
		}
		return "", ctrl.Result{}, nil
	}

	// Validate required fields are not empty
	if secretName == "" || passwordSelector == "" || serviceUser == "" {
		Log.Info("Skipping Application Credential creation: required fields not yet defaulted",
			"service", serviceName,
			"secretName", secretName,
			"passwordSelector", passwordSelector,
			"serviceUser", serviceUser)
		return "", ctrl.Result{}, nil
	}

	// Merge global and service-specific AC configuration
	merged := mergeAppCred(instance.Spec.ApplicationCredential, acConfig)

	// Check if AC CR exists and is ready
	if acExists {
		err = reconcileApplicationCredential(ctx, helper, instance, acName, serviceUser, secretName, passwordSelector, merged)
		if err != nil {
			return "", ctrl.Result{}, err
		}
		if acCR.IsReady() {
			Log.Info("Application Credential is ready", "service", serviceName, "acName", acName, "secretName", acCR.Status.SecretName)

			// For EDPM services, initialize tracking if not already set.
			// The actual sync reconciliation happens in ReconcilePendingEDPMSyncs.
			if edpmServiceType != "" {
				if acCR.Annotations == nil || acCR.Annotations[EDPMDeployedSecretAnnotation] == "" {
					configSecretNames, err := getEDPMConfigSecretNames(ctx, helper.GetClient(), instance.Namespace, edpmServiceType)
					if err != nil {
						return "", ctrl.Result{}, err
					}
					if len(configSecretNames) > 0 {
						synced, err := allNodeSetsSynced(ctx, helper.GetClient(), instance.Namespace, configSecretNames)
						if err != nil {
							return "", ctrl.Result{}, err
						}
						if synced {
							configHash, err := computeCombinedConfigHash(ctx, helper.GetClient(), instance.Namespace, configSecretNames)
							if err != nil {
								return "", ctrl.Result{}, err
							}
							if configHash != "" {
								if err := setEDPMTrackingAnnotations(ctx, helper, acCR, acCR.Status.SecretName, configHash); err != nil {
									return "", ctrl.Result{}, fmt.Errorf("failed to initialize EDPM tracking on AC CR %s: %w", acCR.Name, err)
								}
								Log.Info("Initialized EDPM tracking on AC CR",
									"edpmServiceType", edpmServiceType, "deployedSecret", acCR.Status.SecretName)
							}
						}
					}
				}
			}

			return acCR.Status.SecretName, ctrl.Result{}, nil
		}
		Log.Info("Application Credential not ready yet, requeuing", "service", serviceName, "acName", acName)
		return "", ctrl.Result{RequeueAfter: time.Second * 10}, nil
	}

	// AC doesn't exist
	if !serviceReady {
		// Service not ready, don't create Application Credential yet
		Log.Info("Service not ready, deferring Application Credential creation", "service", serviceName)
		return "", ctrl.Result{}, nil
	}

	// Service is ready, create Application Credential CR
	Log.Info("Service is ready, creating Application Credential", "service", serviceName, "acName", acName)

	err = reconcileApplicationCredential(ctx, helper, instance, acName, serviceUser, secretName, passwordSelector, merged)
	if err != nil {
		return "", ctrl.Result{}, err
	}

	// AC created, but not ready yet - requeue to check readiness
	return "", ctrl.Result{RequeueAfter: time.Second * 5}, nil
}

// reconcileApplicationCredential creates or updates a single ApplicationCredential CR
func reconcileApplicationCredential(
	ctx context.Context,
	helper *helper.Helper,
	instance *corev1beta1.OpenStackControlPlane,
	acName string,
	userName string,
	secretName string,
	passwordSelector string,
	effective corev1beta1.ApplicationCredentialSection,
) error {
	log := GetLogger(ctx)

	acObj := &keystonev1.KeystoneApplicationCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:      acName,
			Namespace: instance.Namespace,
		},
	}

	op, err := controllerutil.CreateOrPatch(ctx, helper.GetClient(), acObj, func() error {
		acObj.Spec.UserName = userName
		acObj.Spec.ExpirationDays = *effective.ExpirationDays
		acObj.Spec.GracePeriodDays = *effective.GracePeriodDays
		acObj.Spec.Secret = secretName
		acObj.Spec.PasswordSelector = passwordSelector
		acObj.Spec.Roles = effective.Roles
		acObj.Spec.Unrestricted = *effective.Unrestricted

		if len(effective.AccessRules) > 0 {
			kr := make([]keystonev1.ACRule, 0, len(effective.AccessRules))
			for _, r := range effective.AccessRules {
				kr = append(kr, keystonev1.ACRule{
					Service: r.Service,
					Path:    r.Path,
					Method:  r.Method,
				})
			}
			acObj.Spec.AccessRules = kr
		}

		return controllerutil.SetControllerReference(
			helper.GetBeforeObject(), acObj, helper.GetScheme(),
		)
	})
	if err != nil {
		return err
	}
	if op != controllerutil.OperationResultNone {
		log.Info("Reconciled Application Credential", "name", acName, "user", userName, "operation", op)
	}
	return nil
}
