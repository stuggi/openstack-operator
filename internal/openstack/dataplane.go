package openstack

import (
	"context"

	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	corev1beta1 "github.com/openstack-k8s-operators/openstack-operator/api/core/v1beta1"

	dataplanev1 "github.com/openstack-k8s-operators/openstack-operator/api/dataplane/v1beta1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetDataplaneNodesets - returns the dataplanenodesets in the namespace of the controlplane
func GetDataplaneNodesets(ctx context.Context, instance *corev1beta1.OpenStackControlPlane, helper *helper.Helper) (*dataplanev1.OpenStackDataPlaneNodeSetList, error) {
	// Get the dataplane nodesets
	dataplaneNodesets := &dataplanev1.OpenStackDataPlaneNodeSetList{}
	opts := []client.ListOption{
		client.InNamespace(instance.Namespace),
	}
	err := helper.GetClient().List(ctx, dataplaneNodesets, opts...)
	if err != nil {
		return nil, err
	}
	return dataplaneNodesets, nil
}

// DataplaneNodesetsDeployedVersionIsSet checks if deployed version is set for all dataplane nodesets
func DataplaneNodesetsDeployedVersionIsSet(dataplaneNodesets *dataplanev1.OpenStackDataPlaneNodeSetList) bool {
	for _, nodeset := range dataplaneNodesets.Items {
		// FIXME: DeployedVersion on the DataplaneNodeset should be a string pointer to match how Controlplane implements this
		if nodeset.Status.DeployedVersion == "" {
			return false
		}
	}
	return true
}

// DataplaneNodesetsOVNControllerImagesMatch returns true if OVNControllers are deployed on all nodesets
func DataplaneNodesetsOVNControllerImagesMatch(version *corev1beta1.OpenStackVersion, dataplaneNodesets *dataplanev1.OpenStackDataPlaneNodeSetList) bool {
	for _, nodeset := range dataplaneNodesets.Items {
		if nodeset.Generation != nodeset.Status.ObservedGeneration {
			return false
		}
		// After considering generation (to make sure reconciliation has quiesced for
		// the current nodeset spec), we only check nodesets if they have any nodes
		// and have deployed OVN
		if len(nodeset.Spec.Nodes) > 0 && nodeset.Status.ContainerImages["OvnControllerImage"] != "" {
			// Check if OVN controller image matches the target version.
			// Note: We don't check nodeset.IsReady() here because this is an intermediate
			// step in the minor update workflow. The nodeset might be not-Ready due to
			// subsequent deployments running (e.g. edpm-update), but if the OVN image matches,
			// it means the OVN update deployment already completed.
			if nodeset.Status.ContainerImages["OvnControllerImage"] != *version.Status.ContainerImages.OvnControllerImage {
				return false
			}
		}
	}
	return true
}

// deploymentFilter is a predicate that selects which deployments to consider.
type deploymentFilter func(*dataplanev1.OpenStackDataPlaneDeployment) bool

// findDataplaneDeploymentForServiceType checks if any deployment matching the
// filter includes the given service type for at least one nodeset with nodes.
func findDataplaneDeploymentForServiceType(
	ctx context.Context,
	h *helper.Helper,
	namespace string,
	dataplaneNodesets *dataplanev1.OpenStackDataPlaneNodeSetList,
	serviceType string,
	filter deploymentFilter,
) (bool, error) {
	// List all deployments in the namespace
	deployments := &dataplanev1.OpenStackDataPlaneDeploymentList{}
	opts := []client.ListOption{
		client.InNamespace(namespace),
	}
	err := h.GetClient().List(ctx, deployments, opts...)
	if err != nil {
		return false, err
	}

	// Build a map of nodeset name -> nodeset for quick lookup
	nodesetMap := make(map[string]*dataplanev1.OpenStackDataPlaneNodeSet, len(dataplaneNodesets.Items))
	for i := range dataplaneNodesets.Items {
		nodesetMap[dataplaneNodesets.Items[i].Name] = &dataplaneNodesets.Items[i]
	}

	// Cache service lookups to avoid repeated API calls
	serviceCache := make(map[string]*dataplanev1.OpenStackDataPlaneService)

	for _, deployment := range deployments.Items {
		if !filter(&deployment) {
			continue
		}

		// Determine which services this deployment runs for each of its nodesets
		for _, nodesetName := range deployment.Spec.NodeSets {
			nodeset, exists := nodesetMap[nodesetName]
			if !exists || len(nodeset.Spec.Nodes) == 0 {
				continue
			}

			var services []string
			if len(deployment.Spec.ServicesOverride) != 0 {
				services = deployment.Spec.ServicesOverride
			} else {
				services = nodeset.Spec.Services
			}

			for _, serviceName := range services {
				svc, cached := serviceCache[serviceName]
				if !cached {
					foundService := &dataplanev1.OpenStackDataPlaneService{}
					err := h.GetClient().Get(ctx, types.NamespacedName{
						Name:      serviceName,
						Namespace: namespace,
					}, foundService)
					if err != nil {
						if k8s_errors.IsNotFound(err) {
							continue
						}
						return false, err
					}
					svc = foundService
					serviceCache[serviceName] = svc
				}

				if svc.Spec.EDPMServiceType == serviceType {
					return true, nil
				}
			}
		}
	}

	return false, nil
}

// IsDataplaneDeploymentRunningForServiceType checks if a non-completed
// deployment exists that includes the given service type.
func IsDataplaneDeploymentRunningForServiceType(
	ctx context.Context,
	h *helper.Helper,
	namespace string,
	dataplaneNodesets *dataplanev1.OpenStackDataPlaneNodeSetList,
	serviceType string,
) (bool, error) {
	return findDataplaneDeploymentForServiceType(ctx, h, namespace, dataplaneNodesets, serviceType,
		func(d *dataplanev1.OpenStackDataPlaneDeployment) bool {
			return !d.Status.Deployed
		})
}

// IsDataplaneDeploymentCompletedForServiceType checks if a completed deployment
// exists for the given service type with the target version. This is used during
// minor updates when the OVN image is unchanged between versions — the image
// match alone is not sufficient, so we check for a completed deployment instead.
func IsDataplaneDeploymentCompletedForServiceType(
	ctx context.Context,
	h *helper.Helper,
	namespace string,
	dataplaneNodesets *dataplanev1.OpenStackDataPlaneNodeSetList,
	serviceType string,
	targetVersion string,
) (bool, error) {
	return findDataplaneDeploymentForServiceType(ctx, h, namespace, dataplaneNodesets, serviceType,
		func(d *dataplanev1.OpenStackDataPlaneDeployment) bool {
			return d.Status.Deployed && d.Status.DeployedVersion == targetVersion
		})
}

// DataplaneNodesetsDeployed returns true if all nodesets are deployed with the latest version
func DataplaneNodesetsDeployed(version *corev1beta1.OpenStackVersion, dataplaneNodesets *dataplanev1.OpenStackDataPlaneNodeSetList) bool {
	for _, nodeset := range dataplaneNodesets.Items {
		if nodeset.Generation != nodeset.Status.ObservedGeneration {
			return false
		}
		// After considering generation (to make sure reconciliation has quiesced for
		// the current nodeset spec), we only care about deployed status if the nodeset
		// has nodes
		if len(nodeset.Spec.Nodes) == 0 {
			continue
		}
		if !nodeset.IsReady() {
			return false
		}
		if nodeset.Status.DeployedVersion != version.Spec.TargetVersion {
			return false
		}

	}
	return true
}
