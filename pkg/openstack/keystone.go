package openstack

import (
	"context"
	"fmt"

	"github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"

	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	keystonev1 "github.com/openstack-k8s-operators/keystone-operator/api/v1beta1"
	corev1beta1 "github.com/openstack-k8s-operators/openstack-operator/apis/core/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/openstack-k8s-operators/lib-common/modules/common/endpoint"
	"github.com/openstack-k8s-operators/lib-common/modules/common/service"
)

// ReconcileKeystoneAPI -
func ReconcileKeystoneAPI(ctx context.Context, instance *corev1beta1.OpenStackControlPlane, helper *helper.Helper) (ctrl.Result, error) {

	keystoneAPI := &keystonev1.KeystoneAPI{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "keystone", //FIXME (keystone doesn't seem to work unless named "keystone")
			Namespace: instance.Namespace,
		},
	}

	if !instance.Spec.Keystone.Enabled {
		if res, err := EnsureDeleted(ctx, helper, keystoneAPI); err != nil {
			return res, err
		}
		instance.Status.Conditions.Remove(corev1beta1.OpenStackControlPlaneKeystoneAPIReadyCondition)
		return ctrl.Result{}, nil
	}

	spec := instance.Spec.Keystone.DeepCopy()

	// Create service overrides to pass into the service CR
	// and expose the public endpoint using a route per default
	var endpoints = map[service.Endpoint]endpoint.Data{
		service.EndpointPublic:   {},
		service.EndpointInternal: {},
	}
	serviceOverrides := []service.OverrideSpec{}
	routeSD := ServiceDetails{}
	for endpointType, endpt := range endpoints {

		sd := ServiceDetails{
			ServiceName:         keystoneAPI.Name,
			Namespace:           instance.Namespace,
			Endpoint:            endpointType,
			ExternalEndpoints:   spec.ExternalEndpoints,
			ServiceOverrideSpec: spec.Template.Override.Service,
			RouteOverrideSpec:   spec.Override.Route,
		}

		svc, err := sd.CreateEndpointServiceOverride()
		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(
				corev1beta1.OpenStackControlPlaneServiceOverrideReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				corev1beta1.OpenStackControlPlaneServiceOverrideReadyErrorMessage,
				keystoneAPI.Name,
				string(endpointType),
				err.Error()))

			return ctrl.Result{}, err
		}

		// Create the route if it is public endpoint and the service type is ClusterIP
		if svc != nil && sd.Endpoint == service.EndpointPublic &&
			svc.GetServiceType() == corev1.ServiceTypeClusterIP {
			//TODO create TLS cert
			var ctrlResult reconcile.Result
			ctrlResult, err = sd.CreateRoute(ctx, helper, *svc, endpt.Path)
			if err != nil {
				instance.Status.Conditions.Set(condition.FalseCondition(
					condition.ExposeServiceReadyCondition,
					condition.ErrorReason,
					condition.SeverityWarning,
					condition.ExposeServiceReadyErrorMessage,
					err.Error()))
				return ctrlResult, err
			} else if (ctrlResult != ctrl.Result{}) {
				return ctrlResult, nil
			}
			routeSD = sd

			instance.Status.Conditions.MarkTrue(condition.ExposeServiceReadyCondition, condition.ExposeServiceReadyMessage)
		}

		svcOverride := service.OverrideSpec{
			Endpoint: sd.Endpoint,
			EmbeddedLabelsAnnotations: &service.EmbeddedLabelsAnnotations{
				Annotations: svc.GetAnnotations(),
				Labels:      svc.GetLabels(),
			},
			Spec: svc.GetSpec(),
		}

		if sd.GetEndpointURL() != "" {
			svcOverride.EndpointURL = pointer.String(sd.GetEndpointURL())
		}

		serviceOverrides = append(serviceOverrides, svcOverride)
		instance.Status.Conditions.MarkTrue(corev1beta1.OpenStackControlPlaneServiceOverrideReadyCondition, corev1beta1.OpenStackControlPlaneServiceOverrideReadyMessage)
	}

	helper.GetLogger().Info("Reconciling KeystoneAPI", "KeystoneAPI.Namespace", instance.Namespace, "KeystoneAPI.Name", "keystone")
	op, err := controllerutil.CreateOrPatch(ctx, helper.GetClient(), keystoneAPI, func() error {
		instance.Spec.Keystone.Template.DeepCopyInto(&keystoneAPI.Spec)
		keystoneAPI.Spec.Override.Service = serviceOverrides
		if keystoneAPI.Spec.Secret == "" {
			keystoneAPI.Spec.Secret = instance.Spec.Secret
		}
		if keystoneAPI.Spec.NodeSelector == nil && instance.Spec.NodeSelector != nil {
			keystoneAPI.Spec.NodeSelector = instance.Spec.NodeSelector
		}
		if keystoneAPI.Spec.DatabaseInstance == "" {
			//keystoneAPI.Spec.DatabaseInstance = instance.Name // name of MariaDB we create here
			keystoneAPI.Spec.DatabaseInstance = "openstack" //FIXME: see above
		}
		err := controllerutil.SetControllerReference(helper.GetBeforeObject(), keystoneAPI, helper.GetScheme())
		if err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			corev1beta1.OpenStackControlPlaneKeystoneAPIReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			corev1beta1.OpenStackControlPlaneKeystoneAPIReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}
	if op != controllerutil.OperationResultNone {
		helper.GetLogger().Info(fmt.Sprintf("KeystoneAPI %s - %s", keystoneAPI.Name, op))
	}

	if keystoneAPI.IsReady() {
		instance.Status.Conditions.MarkTrue(corev1beta1.OpenStackControlPlaneKeystoneAPIReadyCondition, corev1beta1.OpenStackControlPlaneKeystoneAPIReadyMessage)
	} else {
		instance.Status.Conditions.Set(condition.FalseCondition(
			corev1beta1.OpenStackControlPlaneKeystoneAPIReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			corev1beta1.OpenStackControlPlaneKeystoneAPIReadyRunningMessage))
	}

	if routeSD.route != nil {
		// Add the service CR to the ownerRef list of the route to prevent the route being deleted
		// before the service is deleted. Otherwise this can result cleanup issues which require
		// the endpoint to be reachable.
		// If ALL objects in the list have been deleted, this object will be garbage collected.
		// https://github.com/kubernetes/apimachinery/blob/15d95c0b2af3f4fcf46dce24105e5fbb9379af5a/pkg/apis/meta/v1/types.go#L240-L247
		scheme := runtime.NewScheme()
		gvk := schema.GroupVersionKind{
			Group:   keystoneAPI.GroupVersionKind().Group,
			Version: keystoneAPI.GroupVersionKind().Version,
			Kind:    keystoneAPI.Kind,
		}

		// Add the GVK to the scheme
		scheme.AddKnownTypeWithName(gvk, &keystonev1.KeystoneAPI{})

		err = routeSD.AddOwnerRef(ctx, helper, keystoneAPI, scheme)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil

}
