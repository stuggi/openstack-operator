package openstack

import (
	"context"
	"fmt"
	"strings"
	"time"

	routev1 "github.com/openshift/api/route/v1"
	"github.com/openstack-k8s-operators/lib-common/modules/common"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/route"
	"github.com/openstack-k8s-operators/lib-common/modules/common/service"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	corev1 "github.com/openstack-k8s-operators/openstack-operator/apis/core/v1beta1"
	k8s_corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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

// GetExternalEnpointDetailsForEndpoint - returns the MetalLBData for the endpoint, if not specified nil.
func GetExternalEnpointDetailsForEndpoint(
	externalEndpoints []corev1.MetalLBConfig,
	endpt service.Endpoint,
) *corev1.MetalLBConfig {
	for _, metallbcfg := range externalEndpoints {
		if metallbcfg.Endpoint == endpt {
			return metallbcfg.DeepCopy()
		}
	}

	return nil
}

// ServiceDetails - service details to create service.Service with overrides
type ServiceDetails struct {
	ServiceName         string
	Namespace           string
	Endpoint            service.Endpoint
	ExternalEndpoints   []corev1.MetalLBConfig
	ServiceOverrideSpec []service.OverrideSpec
	RouteOverrideSpec   *route.OverrideSpec
	endpointName        string
	endpointURL         string
	baseServiceLabels   map[string]string
	serviceLabels       map[string]string
	hostname            *string
	route               *routev1.Route
}

// GetServiceLabels - returns the labels for the service, used as selector on the route
func (sd *ServiceDetails) GetServiceLabels() map[string]string {
	return sd.serviceLabels
}

// GetEndpointName - returns the name of the endpoint
func (sd *ServiceDetails) GetEndpointName() string {
	return sd.endpointName
}

// GetEndpointURL - returns the URL of the endpoint (proto://hostname/path)
func (sd *ServiceDetails) GetEndpointURL() string {
	return sd.endpointURL
}

// public endpoint
// 1) Default (service override nil, no definition for public endpt in externalEndpoints)
// - create route + pass in service selector
// 2) externalEndpoints != nil for public endpt
// - pass in override for metallb LoadBalancer service
// 3) service override provided == LoadBalancer service type
// - do nothing let the service operator handle it
// 4) service override provided == ClusterIP service type
// - create route , merge override with service selector
//
// internal endpoint
// 1) Default (service override nil, no definition for public endpt in externalEndpoints)
// - service operator will create default service
// 2) externalEndpoints != nil for internal endpt, no service override provided
// - pass in override for metallb LoadBalancer service
// 3) externalEndpoints != nil for internal endpt, service override provided
// - merge override with metallb service and pass in override
// 4) externalEndpoints == nil for internal endpt, service override provided
// - do nothing let the service operator handle it

// CreateEndpointServiceOverride -
func (sd *ServiceDetails) CreateEndpointServiceOverride() (*service.Service, error) {

	// set the endpoint name to service name - endpoint type
	sd.endpointName = sd.ServiceName + "-" + string(sd.Endpoint)

	//  create the service label
	sd.baseServiceLabels = map[string]string{
		common.AppSelector: sd.ServiceName,
	}

	// create the label for the service , used for the route -> service connection
	sd.serviceLabels = util.MergeStringMaps(
		sd.baseServiceLabels,
		map[string]string{
			string(sd.Endpoint): "true",
		},
	)

	// initialize the service override if not specified via the CR
	svcOverride := service.GetOverrideSpecForEndpoint(sd.ServiceOverrideSpec, sd.Endpoint).DeepCopy()
	if svcOverride == nil {
		svcOverride = &service.OverrideSpec{}
		svcOverride.Endpoint = sd.Endpoint
	}

	// get MetalLB config data for the endpoint
	metalLBData := GetExternalEnpointDetailsForEndpoint(sd.ExternalEndpoints, sd.Endpoint)

	// Create generic service definitions for either MetalLB, or generic service
	svcDef := &k8s_corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sd.endpointName,
			Namespace: sd.Namespace,
			Labels:    sd.serviceLabels,
		},
		Spec: k8s_corev1.ServiceSpec{
			Type: k8s_corev1.ServiceTypeClusterIP,
		},
	}

	// if the externalEndpoints is configured for the service and for this endpoint provided,
	// set MetalLB annotations and service type to LoadBalancer
	if metalLBData != nil {
		// create MetalLB service definition
		annotations := map[string]string{
			service.MetalLBAddressPoolAnnotation: metalLBData.IPAddressPool,
		}
		if len(metalLBData.LoadBalancerIPs) > 0 {
			annotations[service.MetalLBLoadBalancerIPs] = strings.Join(metalLBData.LoadBalancerIPs, ",")
		}
		if metalLBData.SharedIP {
			if metalLBData.SharedIPKey == "" {
				annotations[service.MetalLBAllowSharedIPAnnotation] = metalLBData.IPAddressPool
			} else {
				annotations[service.MetalLBAllowSharedIPAnnotation] = metalLBData.SharedIPKey
			}
		}
		svcDef.Annotations = annotations
		svcDef.Spec.Type = k8s_corev1.ServiceTypeLoadBalancer
	}

	// Create the service
	svc, err := service.NewService(
		svcDef,
		time.Duration(5)*time.Second, //not really required as we don't create the service here
		svcOverride,
	)
	if err != nil {
		return nil, err
	}

	// add annotation to register service name in dnsmasq if it is a LoadBalancer service
	if svc.GetServiceType() == k8s_corev1.ServiceTypeLoadBalancer {
		svc.AddAnnotation(map[string]string{
			service.AnnotationHostnameKey: svc.GetServiceHostname(),
		})
	}

	return svc, nil
}

// CreateRoute -
func (sd *ServiceDetails) CreateRoute(
	ctx context.Context,
	helper *helper.Helper,
	svc service.Service,
	path string,
) (ctrl.Result, error) {
	// TODO TLS
	route, err := route.NewRoute(
		route.GenericRoute(&route.GenericRouteDetails{
			Name:           sd.ServiceName,
			Namespace:      sd.Namespace,
			Labels:         sd.baseServiceLabels,
			ServiceName:    sd.endpointName,
			TargetPortName: sd.endpointName,
		}),
		time.Duration(5)*time.Second,
		sd.RouteOverrideSpec,
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	ctrlResult, err := route.CreateOrPatch(ctx, helper)
	if err != nil {
		return ctrlResult, err
	} else if (ctrlResult != ctrl.Result{}) {
		return ctrlResult, nil
	}

	sd.hostname = pointer.String(route.GetHostname())
	// TODO TLS endpointURL
	if path != "" {
		sd.endpointURL = "http://" + *sd.hostname + "/" + path
	} else {
		sd.endpointURL = "http://" + *sd.hostname
	}

	sd.route = route.GetRoute()

	return ctrl.Result{}, nil
}

// AddOwnerRef - adds owner to the OwnerReference list of the route
// Add the service CR to the ownerRef list of the route to prevent the route being deleted
// before the service is deleted. Otherwise this can result cleanup issues which require
// the endpoint to be reachable.
// If ALL objects in the list have been deleted, this object will be garbage collected.
// https://github.com/kubernetes/apimachinery/blob/15d95c0b2af3f4fcf46dce24105e5fbb9379af5a/pkg/apis/meta/v1/types.go#L240-L247
func (sd *ServiceDetails) AddOwnerRef(
	ctx context.Context,
	helper *helper.Helper,
	owner metav1.Object,
	scheme *runtime.Scheme,
) error {
	op, err := controllerutil.CreateOrPatch(ctx, helper.GetClient(), sd.route, func() error {
		err := controllerutil.SetOwnerReference(owner, sd.route, scheme)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if op != controllerutil.OperationResultNone {
		helper.GetLogger().Info(fmt.Sprintf("%s route %s - %s", sd.ServiceName, sd.route.Name, op))
	}

	return nil
}
