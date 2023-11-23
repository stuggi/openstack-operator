package openstack

import (
	"context"
	"fmt"
	"time"

	routev1 "github.com/openshift/api/route/v1"
	"github.com/openstack-k8s-operators/lib-common/modules/certmanager"
	"github.com/openstack-k8s-operators/lib-common/modules/common"
	"github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/route"
	"github.com/openstack-k8s-operators/lib-common/modules/common/secret"
	"github.com/openstack-k8s-operators/lib-common/modules/common/service"
	"github.com/openstack-k8s-operators/lib-common/modules/common/tls"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	corev1 "github.com/openstack-k8s-operators/openstack-operator/apis/core/v1beta1"

	k8s_corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// EnsureDeleted - Delete the object which in turn will clean the sub resources
func EnsureDeleted(ctx context.Context, helper *helper.Helper, obj client.Object) (ctrl.Result, error) {
	key := client.ObjectKeyFromObject(obj)
	if err := helper.GetClient().Get(ctx, key, obj); err != nil {
		if k8s_errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	// Delete the object
	if obj.GetDeletionTimestamp().IsZero() {
		if err := helper.GetClient().Delete(ctx, obj); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil

}

// AddServiceComponentLabel - adds component label to the service override to be able to query
// the service labels to check for any route creation
func AddServiceComponentLabel(svcOverride service.RoutedOverrideSpec, value string) service.RoutedOverrideSpec {
	if svcOverride.EmbeddedLabelsAnnotations == nil {
		svcOverride.EmbeddedLabelsAnnotations = &service.EmbeddedLabelsAnnotations{}
	}
	svcOverride.EmbeddedLabelsAnnotations.Labels = util.MergeStringMaps(
		svcOverride.EmbeddedLabelsAnnotations.Labels, map[string]string{common.AppSelector: value})

	return svcOverride
}

// EndpointDetails - endpoint details
type EndpointDetails struct {
	Name        string
	Namespace   string
	Type        service.Endpoint
	Annotations map[string]string
	Labels      map[string]string
	Service     ServiceDetails
	Route       RouteDetails
	Hostname    *string
	Proto       service.Protocol
	EndpointURL string
}

// TLSDetails - tls settings for the endpoint
type TLSDetails struct {
	Enabled bool
	tls.GenericService
	tls.Ca
}

// ServiceDetails - service details
type ServiceDetails struct {
	Spec         *k8s_corev1.Service
	OverrideSpec service.RoutedOverrideSpec
	TLS          TLSDetails
}

// RouteDetails - route details
type RouteDetails struct {
	Create       bool
	Route        *routev1.Route
	OverrideSpec route.OverrideSpec
	TLS          TLSDetails
}

// GetRoutesListWithLabel - Get all routes in namespace of the obj matching label selector
func GetRoutesListWithLabel(
	ctx context.Context,
	h *helper.Helper,
	namespace string,
	labelSelectorMap map[string]string,
) (*routev1.RouteList, error) {
	routeList := &routev1.RouteList{}
	listOpts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels(labelSelectorMap),
	}

	if err := h.GetClient().List(ctx, routeList, listOpts...); err != nil {
		err = fmt.Errorf("Error listing routes for %s: %w", labelSelectorMap, err)
		return nil, err
	}

	return routeList, nil
}

// GetEndpointServiceOverrides -
func GetEndpointServiceOverrides(serviceEndpointDetails map[service.Endpoint]EndpointDetails) map[service.Endpoint]service.RoutedOverrideSpec {
	overrides := map[service.Endpoint]service.RoutedOverrideSpec{}
	for endpt, endptCfg := range serviceEndpointDetails {
		overrides[endpt] = *endptCfg.Service.OverrideSpec.DeepCopy()
	}

	return overrides
}

// EnsureEndpointConfig -
func EnsureEndpointConfig(
	ctx context.Context,
	instance *corev1.OpenStackControlPlane,
	helper *helper.Helper,
	owner metav1.Object,
	svcs *k8s_corev1.ServiceList,
	svcOverrides map[service.Endpoint]service.RoutedOverrideSpec,
	publicOverride corev1.Override,
	condType condition.Type,
	serviceTLSDisabled *bool,
) (map[service.Endpoint]EndpointDetails, ctrl.Result, error) {
	serviceEndpointDetails := map[service.Endpoint]EndpointDetails{}
	for _, svc := range svcs.Items {
		ed := EndpointDetails{
			Name:      svc.Name,
			Namespace: svc.Namespace,
			Type:      service.Endpoint(svc.Annotations[service.AnnotationEndpointKey]),
			Service: ServiceDetails{
				Spec: &svc,
			},
		}

		ed.Service.OverrideSpec = svcOverrides[ed.Type]

		// TLS on the pod level is enabled if
		// * TLS is enabled for internal endpoint
		// * the particular service has not TLS.Disabled set to true
		if tlsEndpointConfig := instance.Spec.TLS.Endpoint[service.EndpointInternal]; tlsEndpointConfig.Enabled &&
			(serviceTLSDisabled == nil || (serviceTLSDisabled != nil && !*serviceTLSDisabled)) {
			ed.Service.TLS.Enabled = true
		}

		switch ed.Type {
		case service.EndpointPublic:
			// If the service has the create ingress annotation and its a default ClusterIP service -> create route
			ed.Route.Create = svc.ObjectMeta.Annotations[service.AnnotationIngressCreateKey] == "true" &&
				svc.Spec.Type == k8s_corev1.ServiceTypeClusterIP

			if ed.Route.Create {
				if publicOverride.Route != nil {
					ed.Route.OverrideSpec = *publicOverride.Route
				}
			}

			// if public endpoint TLS is enabled
			if tlsEndpointConfig := instance.Spec.TLS.Endpoint[ed.Type]; tlsEndpointConfig.Enabled {
				// TLS for route enabled if public endpoint TLS is true
				ed.Route.TLS.Enabled = true

				// if a custom cert secret was provided we'll use this for
				// the route, otherwise the issuer is used to request one
				// for the endpoint.
				if publicOverride.TLS != nil && publicOverride.TLS.SecretName != "" {
					ed.Route.TLS.SecretName = ptr.To(publicOverride.TLS.SecretName)
				} else {
					ed.Route.TLS.IssuerName = ptr.To(DefaultCAPrefix + ed.Type.String())
				}

				// For re-encryption the route required the internal CA bundle to validate
				// internal certificate from route -> service -> pod. Add the internal CA bundle when
				// * TLS is enabled for internal endpoint
				// * the particular service has not TLS.Disabled set to true
				if ed.Service.TLS.Enabled {
					ed.Service.TLS.CaBundleSecretName = CombinedCASecret
					// TODO: (mschuppert) - if ed.Route.Create == false and custom cert secret provided
					// for the public endpoint, use this and not the issuer. This is the case when
					// env got deployed with LoadBalancer service instead of a route for the public
					// endpoint.
					ed.Service.TLS.IssuerName = ptr.To(DefaultCAPrefix + ed.Type.String())
				}
			}

			ctrlResult, err := ed.ensureRoute(ctx, instance, helper, &svc, owner, condType)
			if err != nil {
				return serviceEndpointDetails, ctrlResult, err
			} else if (ctrlResult != ctrl.Result{}) {
				return serviceEndpointDetails, ctrlResult, nil
			}

		case service.EndpointInternal:
			if ed.Service.TLS.Enabled {
				helper.GetLogger().Info(fmt.Sprintf("BOOO %+v", CombinedCASecret))
				ed.Service.TLS.CaBundleSecretName = CombinedCASecret
				ed.Service.TLS.IssuerName = ptr.To(DefaultCAPrefix + ed.Type.String())
			}
		}

		// update override for the service with the endpoint url
		if ed.EndpointURL != "" {
			// Any trailing path will be added on the service-operator level.
			ed.Service.OverrideSpec.EndpointURL = &ed.EndpointURL
			instance.Status.Conditions.MarkTrue(condType, corev1.OpenStackControlPlaneExposeServiceReadyMessage, owner.GetName())
		}

		//svcOverrides[ed.Type] = ed.Service.OverrideSpec
		serviceEndpointDetails[ed.Type] = ed
	}

	return serviceEndpointDetails, ctrl.Result{}, nil
}

func (ed *EndpointDetails) ensureRoute(
	ctx context.Context,
	instance *corev1.OpenStackControlPlane,
	helper *helper.Helper,
	svc *k8s_corev1.Service,
	owner metav1.Object,
	condType condition.Type,
) (ctrl.Result, error) {
	// check if there is already a route with common.AppSelector from the service
	if svcLabelVal, ok := svc.Labels[common.AppSelector]; ok {
		routes, err := GetRoutesListWithLabel(
			ctx,
			helper,
			instance.Namespace,
			map[string]string{common.AppSelector: svcLabelVal},
		)
		if err != nil {
			return ctrl.Result{}, err
		}

		// check the routes if name changed where we are the owner
		for _, r := range routes.Items {
			instanceRef := metav1.OwnerReference{
				APIVersion:         instance.APIVersion,
				Kind:               instance.Kind,
				Name:               instance.GetName(),
				UID:                instance.GetUID(),
				BlockOwnerDeletion: ptr.To(true),
				Controller:         ptr.To(true),
			}

			owner := metav1.GetControllerOf(&r.ObjectMeta)

			// Delete the route if the service was changed not to expose a route
			if svc.ObjectMeta.Annotations[service.AnnotationIngressCreateKey] == "false" &&
				r.Spec.To.Name == ed.Name &&
				owner != nil && owner.UID == instance.GetUID() {
				// Delete any other owner refs from ref list to not block deletion until owners are gone
				r.SetOwnerReferences([]metav1.OwnerReference{instanceRef})

				// Delete route
				err := helper.GetClient().Delete(ctx, &r)
				if err != nil && !k8s_errors.IsNotFound(err) {
					err = fmt.Errorf("Error deleting route %s: %w", r.Name, err)
					return ctrl.Result{}, err
				}

				if ed.Service.OverrideSpec.EndpointURL != nil {
					ed.Service.OverrideSpec.EndpointURL = nil
					helper.GetLogger().Info(fmt.Sprintf("Service %s override endpointURL removed", svc.Name))
				}
			}
		}
	}

	if ed.Route.Create {
		if instance.Status.Conditions.Get(condType) == nil {
			instance.Status.Conditions.Set(condition.UnknownCondition(
				condType,
				condition.InitReason,
				corev1.OpenStackControlPlaneExposeServiceReadyInitMessage,
				owner.GetName(),
				svc.Name,
			))
		}
		if ed.Service.OverrideSpec.EmbeddedLabelsAnnotations == nil {
			ed.Service.OverrideSpec.EmbeddedLabelsAnnotations = &service.EmbeddedLabelsAnnotations{}
		}

		if labelVal, ok := ed.Service.OverrideSpec.EmbeddedLabelsAnnotations.Labels[common.AppSelector]; ok {
			ed.Labels = map[string]string{common.AppSelector: labelVal}
		}

		ctrlResult, err := ed.CreateRoute(ctx, helper, owner)
		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(
				condType,
				condition.ErrorReason,
				condition.SeverityWarning,
				corev1.OpenStackControlPlaneExposeServiceReadyErrorMessage,
				owner.GetName(),
				ed.Name,
				err.Error()))

			return ctrlResult, err
		} else if (ctrlResult != ctrl.Result{}) {
			return ctrlResult, nil
		}

		return ctrl.Result{}, nil
	}
	instance.Status.Conditions.Remove(condType)

	return ctrl.Result{}, nil
}

// CreateRoute -
func (ed *EndpointDetails) CreateRoute(
	ctx context.Context,
	helper *helper.Helper,
	owner metav1.Object,
) (ctrl.Result, error) {
	// initialize the route with any custom provided route override
	enptRoute, err := route.NewRoute(
		route.GenericRoute(&route.GenericRouteDetails{
			Name:           ed.Name,
			Namespace:      ed.Namespace,
			Labels:         ed.Labels,
			ServiceName:    ed.Service.Spec.Name,
			TargetPortName: ed.Service.Spec.Name,
		}),
		time.Duration(5)*time.Second,
		[]route.OverrideSpec{ed.Route.OverrideSpec},
	)
	if err != nil {
		return ctrl.Result{}, err
	}
	enptRoute.OwnerReferences = append(enptRoute.OwnerReferences, owner)

	// if route TLS is disabled -> create the route
	// if TLS is enabled and the route does not yet exist -> create the route
	// to get the hostname for creating the cert
	serviceRoute := &routev1.Route{}
	err = helper.GetClient().Get(ctx, types.NamespacedName{Name: ed.Name, Namespace: ed.Namespace}, serviceRoute)
	if !ed.Route.TLS.Enabled || (ed.Route.TLS.Enabled && err != nil && k8s_errors.IsNotFound(err)) {
		ctrlResult, err := enptRoute.CreateOrPatch(ctx, helper)
		if err != nil {
			return ctrlResult, err
		} else if (ctrlResult != ctrl.Result{}) {
			return ctrlResult, nil
		}

		ed.Hostname = ptr.To(enptRoute.GetHostname())
	} else if err != nil {
		return ctrl.Result{}, err
	} else {
		ed.Hostname = &serviceRoute.Spec.Host
	}

	// if TLS is enabled for the route
	if ed.Route.TLS.Enabled {
		var ctrlResult reconcile.Result

		certSecret := &k8s_corev1.Secret{}

		// if a custom cert secret was provided, check if it exist
		// and has the required cert, key and cacert
		// Right now there is no check if certificate is valid for
		// the hostname of the route. If the referenced secret is
		// there and has the required files it is just being used.
		if ed.Route.TLS.SecretName != nil {
			certSecret, _, err = secret.GetSecret(ctx, helper, *ed.Route.TLS.SecretName, ed.Namespace)
			if err != nil {
				if k8s_errors.IsNotFound(err) {
					return ctrl.Result{}, fmt.Errorf("certificate secret %s not found: %w", *ed.Route.TLS.SecretName, err)
				}

				return ctrl.Result{}, err
			}

			// check if secret has the expected entries tls.crt, tls.key and ca.crt
			if certSecret != nil {
				for _, key := range []string{"tls.crt", "tls.key", "ca.crt"} {
					if _, exist := certSecret.Data[key]; !exist {
						return ctrl.Result{}, fmt.Errorf("certificate secret %s does not provide %s", *ed.Route.TLS.SecretName, key)
					}
				}
			}
		} else {
			certRequest := certmanager.CertificateRequest{
				IssuerName:  *ed.Route.TLS.IssuerName,
				CertName:    fmt.Sprintf("%s-route", ed.Name),
				Duration:    nil,
				Hostnames:   []string{*ed.Hostname},
				Ips:         nil,
				Annotations: ed.Annotations,
				Labels:      ed.Labels,
				Usages:      nil,
			}
			//create the cert using our issuer for the endpoint
			certSecret, ctrlResult, err = certmanager.EnsureCert(
				ctx,
				helper,
				certRequest)
			if err != nil {
				return ctrlResult, err
			} else if (ctrlResult != ctrl.Result{}) {
				return ctrlResult, nil
			}
		}

		// create default TLS route override
		tlsConfig := &routev1.TLSConfig{
			Termination:                   routev1.TLSTerminationEdge,
			Certificate:                   string(certSecret.Data[tls.CertKey]),
			Key:                           string(certSecret.Data[tls.PrivateKey]),
			CACertificate:                 string(certSecret.Data[tls.CAKey]),
			InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect,
		}

		// for internal TLS (TLSE) use routev1.TLSTerminationReencrypt
		if ed.Service.TLS.Enabled {
			// get the TLSInternalCABundleFile to add it to the route
			// to be able to validate public/internal service endpoints
			tlsConfig.DestinationCACertificate, ctrlResult, err = secret.GetDataFromSecret(
				ctx, helper, ed.Service.TLS.CaBundleSecretName, 5, TLSInternalCABundleFile)
			if err != nil {
				return ctrlResult, err
			} else if (ctrlResult != ctrl.Result{}) {
				return ctrlResult, nil
			}

			tlsConfig.Termination = routev1.TLSTerminationReencrypt
		}

		enptRoute, err = route.NewRoute(
			enptRoute.GetRoute(),
			time.Duration(5)*time.Second,
			[]route.OverrideSpec{
				{
					Spec: &route.Spec{
						TLS: tlsConfig,
					},
				},
				ed.Route.OverrideSpec,
			},
		)
		if err != nil {
			return ctrl.Result{}, err
		}

		ctrlResult, err = enptRoute.CreateOrPatch(ctx, helper)
		if err != nil {
			return ctrlResult, err
		} else if (ctrlResult != ctrl.Result{}) {
			return ctrlResult, nil
		}

		ed.Proto = service.ProtocolHTTPS
	} else {
		ed.Proto = service.ProtocolHTTP
	}

	ed.EndpointURL = ed.Proto.String() + "://" + *ed.Hostname

	return ctrl.Result{}, nil
}
