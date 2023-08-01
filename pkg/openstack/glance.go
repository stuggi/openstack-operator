package openstack

import (
	"context"
	"fmt"

	"github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/endpoint"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/service"

	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	glancev1 "github.com/openstack-k8s-operators/glance-operator/api/v1beta1"
	corev1beta1 "github.com/openstack-k8s-operators/openstack-operator/apis/core/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
)

// ReconcileGlance -
func ReconcileGlance(ctx context.Context, instance *corev1beta1.OpenStackControlPlane, helper *helper.Helper) (ctrl.Result, error) {
	glance := &glancev1.Glance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "glance",
			Namespace: instance.Namespace,
		},
	}

	if !instance.Spec.Glance.Enabled {
		if res, err := EnsureDeleted(ctx, helper, glance); err != nil {
			return res, err
		}
		instance.Status.Conditions.Remove(corev1beta1.OpenStackControlPlaneGlanceReadyCondition)
		return ctrl.Result{}, nil
	}

	spec := instance.Spec.Glance.DeepCopy()

	// Create service overrides to pass into the service CR
	// and expose the public endpoint using a route per default
	var endpoints = map[service.Endpoint]endpoint.Data{
		service.EndpointPublic:   {},
		service.EndpointInternal: {},
	}

	serviceOverrides := []service.OverrideSpec{}
	routeSD := ServiceDetails{}

	serviceOverrideSpec := []service.OverrideSpec{}
	if spec.Template.GlanceAPIExternal.Override.Service != nil {
		serviceOverrideSpec = append(serviceOverrideSpec, *spec.Template.GlanceAPIExternal.Override.Service)
	}
	if spec.Template.GlanceAPIInternal.Override.Service != nil {
		serviceOverrideSpec = append(serviceOverrideSpec, *spec.Template.GlanceAPIInternal.Override.Service)
	}

	for endpointType, endpt := range endpoints {

		sd := ServiceDetails{
			ServiceName:         glance.Name,
			Namespace:           instance.Namespace,
			Endpoint:            endpointType,
			ExternalEndpoints:   spec.ExternalEndpoints,
			ServiceOverrideSpec: serviceOverrideSpec,
			RouteOverrideSpec:   spec.Override.Route,
		}

		svc, err := sd.CreateEndpointServiceOverride()
		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(
				corev1beta1.OpenStackControlPlaneServiceOverrideReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				corev1beta1.OpenStackControlPlaneServiceOverrideReadyErrorMessage,
				glance.Name,
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

	helper.GetLogger().Info("Reconciling Glance", "Glance.Namespace", instance.Namespace, "Glance.Name", "glance")
	op, err := controllerutil.CreateOrPatch(ctx, helper.GetClient(), glance, func() error {
		instance.Spec.Glance.Template.DeepCopyInto(&glance.Spec)
		glance.Spec.GlanceAPIExternal.Override.Service = service.GetOverrideSpecForEndpoint(serviceOverrides, service.EndpointPublic).DeepCopy()
		glance.Spec.GlanceAPIInternal.Override.Service = service.GetOverrideSpecForEndpoint(serviceOverrides, service.EndpointInternal).DeepCopy()

		if glance.Spec.Secret == "" {
			glance.Spec.Secret = instance.Spec.Secret
		}
		if glance.Spec.DatabaseInstance == "" {
			glance.Spec.DatabaseInstance = "openstack"
		}
		if glance.Spec.StorageClass == "" {
			glance.Spec.StorageClass = instance.Spec.StorageClass
		}
		// if already defined at service level (template section), we don't merge
		// with the global defined extra volumes
		if len(glance.Spec.ExtraMounts) == 0 {

			var glanceVolumes []glancev1.GlanceExtraVolMounts

			for _, ev := range instance.Spec.ExtraMounts {
				glanceVolumes = append(glanceVolumes, glancev1.GlanceExtraVolMounts{
					Name:      ev.Name,
					Region:    ev.Region,
					VolMounts: ev.VolMounts,
				})
			}
			glance.Spec.ExtraMounts = glanceVolumes
		}
		err := controllerutil.SetControllerReference(helper.GetBeforeObject(), glance, helper.GetScheme())
		if err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			corev1beta1.OpenStackControlPlaneGlanceReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			corev1beta1.OpenStackControlPlaneGlanceReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}
	if op != controllerutil.OperationResultNone {
		helper.GetLogger().Info(fmt.Sprintf("glance %s - %s", glance.Name, op))
	}

	if glance.IsReady() {
		instance.Status.Conditions.MarkTrue(corev1beta1.OpenStackControlPlaneGlanceReadyCondition, corev1beta1.OpenStackControlPlaneGlanceReadyMessage)
	} else {
		instance.Status.Conditions.Set(condition.FalseCondition(
			corev1beta1.OpenStackControlPlaneGlanceReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			corev1beta1.OpenStackControlPlaneGlanceReadyRunningMessage))
	}

	if routeSD.route != nil {
		// Add the service CR to the ownerRef list of the route to prevent the route being deleted
		// before the service is deleted. Otherwise this can result cleanup issues which require
		// the endpoint to be reachable.
		// If ALL objects in the list have been deleted, this object will be garbage collected.
		// https://github.com/kubernetes/apimachinery/blob/15d95c0b2af3f4fcf46dce24105e5fbb9379af5a/pkg/apis/meta/v1/types.go#L240-L247
		scheme := runtime.NewScheme()
		gvk := schema.GroupVersionKind{
			Group:   glance.GroupVersionKind().Group,
			Version: glance.GroupVersionKind().Version,
			Kind:    glance.Kind,
		}

		// Add the GVK to the scheme
		scheme.AddKnownTypeWithName(gvk, &glancev1.Glance{})

		err = routeSD.AddOwnerRef(ctx, helper, glance, scheme)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil

}
