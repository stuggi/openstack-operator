/*
Copyright 2022.

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

package openstack

import (
	"context"
	"fmt"

	"github.com/openstack-k8s-operators/lib-common/modules/certmanager"
	"github.com/openstack-k8s-operators/lib-common/modules/common"
	"github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/service"
	"github.com/openstack-k8s-operators/lib-common/modules/common/tls"

	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	novav1 "github.com/openstack-k8s-operators/nova-operator/api/v1beta1"
	corev1beta1 "github.com/openstack-k8s-operators/openstack-operator/apis/core/v1beta1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
)

// ReconcileNova -
func ReconcileNova(ctx context.Context, instance *corev1beta1.OpenStackControlPlane, helper *helper.Helper) (ctrl.Result, error) {
	nova := &novav1.Nova{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nova",
			Namespace: instance.Namespace,
		},
	}
	Log := GetLogger(ctx)

	if !instance.Spec.Nova.Enabled {
		if res, err := EnsureDeleted(ctx, helper, nova); err != nil {
			return res, err
		}
		instance.Status.Conditions.Remove(corev1beta1.OpenStackControlPlaneNovaReadyCondition)
		instance.Status.Conditions.Remove(corev1beta1.OpenStackControlPlaneExposeNovaReadyCondition)
		return ctrl.Result{}, nil
	}

	// NovaAPI
	// add selector to service overrides
	for _, endpointType := range []service.Endpoint{service.EndpointPublic, service.EndpointInternal} {
		// NovaAPI
		if instance.Spec.Nova.Template.APIServiceTemplate.Override.Service == nil {
			instance.Spec.Nova.Template.APIServiceTemplate.Override.Service = map[service.Endpoint]service.RoutedOverrideSpec{}
		}
		instance.Spec.Nova.Template.APIServiceTemplate.Override.Service[endpointType] =
			AddServiceComponentLabel(
				instance.Spec.Nova.Template.APIServiceTemplate.Override.Service[endpointType],
				nova.Name+"-api")
	}
	// NovaMetadata
	if centralMetadataEnabled(instance.Spec.Nova.Template.MetadataServiceTemplate) {
		if instance.Spec.Nova.Template.MetadataServiceTemplate.Override.Service == nil {
			instance.Spec.Nova.Template.MetadataServiceTemplate.Override.Service = &service.OverrideSpec{}
		}
		instance.Spec.Nova.Template.MetadataServiceTemplate.Override.Service.AddLabel(centralMetadataLabelMap(nova.Name))
	}

	// Cells
	for cellName, cellTemplate := range instance.Spec.Nova.Template.CellTemplates {
		// add override where novncproxy enabled is not specified or explicitely set to true
		if cellNoVNCProxEnabled(cellTemplate.NoVNCProxyServiceTemplate) {
			if cellTemplate.NoVNCProxyServiceTemplate.Override.Service == nil {
				cellTemplate.NoVNCProxyServiceTemplate.Override.Service = &service.RoutedOverrideSpec{}
			}

			*cellTemplate.NoVNCProxyServiceTemplate.Override.Service =
				AddServiceComponentLabel(
					*cellTemplate.NoVNCProxyServiceTemplate.Override.Service,
					getNoVNCProxyServiceLabel(nova.Name, cellName))
		}

		// add override where metadata enabled is set to true
		if cellMetadataEnabled(cellTemplate.MetadataServiceTemplate) {
			if cellTemplate.MetadataServiceTemplate.Override.Service == nil {
				cellTemplate.MetadataServiceTemplate.Override.Service = &service.OverrideSpec{}
			}
			cellTemplate.MetadataServiceTemplate.Override.Service.AddLabel(cellMetadataLabelMap(nova.Name, cellName))
		}

		instance.Spec.Nova.Template.CellTemplates[cellName] = cellTemplate
	}

	// When component services got created check if there is the need to create a route
	if err := helper.GetClient().Get(ctx, types.NamespacedName{Name: "nova", Namespace: instance.Namespace}, nova); err != nil {
		if !k8s_errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	// Nova API
	var apiServiceEndpointDetails = Endpoints{}
	if nova.Status.Conditions.IsTrue(novav1.NovaAPIReadyCondition) {
		svcs, err := service.GetServicesListWithLabel(
			ctx,
			helper,
			instance.Namespace,
			map[string]string{common.AppSelector: nova.Name + "-api"},
		)
		if err != nil {
			return ctrl.Result{}, err
		}

		var ctrlResult reconcile.Result
		apiServiceEndpointDetails, ctrlResult, err = EnsureEndpointConfig(
			ctx,
			instance,
			helper,
			nova,
			svcs,
			instance.Spec.Nova.Template.APIServiceTemplate.Override.Service,
			instance.Spec.Nova.APIOverride,
			corev1beta1.OpenStackControlPlaneExposeNovaReadyCondition,
			ptr.To(false), // TODO (mschuppert) could be removed when all integrated service support TLS
		)
		if err != nil {
			return ctrlResult, err
		} else if (ctrlResult != ctrl.Result{}) {
			return ctrlResult, nil
		}

		instance.Spec.Nova.Template.APIServiceTemplate.Override.Service = apiServiceEndpointDetails.GetEndpointServiceOverrides()
	}

	// update TLS settings with cert secret and CABundle
	tlsSpec := nova.Spec.APIServiceTemplate.TLS
	tlsSpec.CaBundleSecretName = instance.Status.TLS.CaBundleSecretName
	for endpt, endptCfg := range apiServiceEndpointDetails.EndpointDetails {
		if endptCfg.Service.TLS.Enabled {
			switch endpt {
			case service.EndpointPublic:
				tlsSpec.API.Public.SecretName = endptCfg.Service.TLS.SecretName
			case service.EndpointInternal:
				tlsSpec.API.Internal.SecretName = endptCfg.Service.TLS.SecretName
			}
		}
	}
	instance.Spec.Nova.Template.APIServiceTemplate.TLS = tlsSpec
	if centralMetadataEnabled(instance.Spec.Nova.Template.MetadataServiceTemplate) {
		instance.Spec.Nova.Template.MetadataServiceTemplate.TLS.CaBundleSecretName = instance.Status.TLS.CaBundleSecretName
	}

	if nova.Status.Conditions.IsTrue(novav1.NovaAllCellsReadyCondition) {
		// create certificate for central Metadata agent if internal TLS and Metadata are enabled
		if instance.Spec.TLS.Enabled(service.EndpointInternal) &&
			centralMetadataEnabled(instance.Spec.Nova.Template.MetadataServiceTemplate) {
			t, ctrlResult, err := getMetadataCertificate(ctx,
				helper,
				nova.Name,
				nova.Namespace,
				instance.Spec.Nova.Template.MetadataServiceTemplate.Override.Service)
			if err != nil {
				return ctrlResult, err
			} else if (ctrlResult != ctrl.Result{}) {
				return ctrlResult, nil
			}

			instance.Spec.Nova.Template.MetadataServiceTemplate.TLS = t
		}

		// cell Metadata and NoVNCProxy
		for cellName, cellTemplate := range instance.Spec.Nova.Template.CellTemplates {
			// create certificate for Metadata agend if internal TLS and Metadata per cell is enabled
			if instance.Spec.TLS.Enabled(service.EndpointInternal) &&
				cellMetadataEnabled(cellTemplate.MetadataServiceTemplate) {

				t, ctrlResult, err := getMetadataCertificate(ctx,
					helper,
					nova.Name,
					nova.Namespace,
					cellTemplate.MetadataServiceTemplate.Override.Service)
				if err != nil {
					return ctrlResult, err
				} else if (ctrlResult != ctrl.Result{}) {
					return ctrlResult, nil
				}

				instance.Spec.Nova.Template.MetadataServiceTemplate.TLS = t
			}

			// skip checking for/creating route if service is not enabled
			if !cellNoVNCProxEnabled(cellTemplate.NoVNCProxyServiceTemplate) {
				continue
			}

			if cellTemplate.NoVNCProxyServiceTemplate.Override.Service == nil {
				cellTemplate.NoVNCProxyServiceTemplate.Override.Service = &service.RoutedOverrideSpec{}
			}

			svcs, err := service.GetServicesListWithLabel(
				ctx,
				helper,
				instance.Namespace,
				map[string]string{
					common.AppSelector: getNoVNCProxyServiceLabel(nova.Name, cellName),
				},
			)
			if err != nil {
				return ctrl.Result{}, err
			}

			var ctrlResult reconcile.Result
			var cellServiceEndpointDetails = Endpoints{}
			cellServiceEndpointDetails, ctrlResult, err = EnsureEndpointConfig(
				ctx,
				instance,
				helper,
				nova,
				svcs,
				map[service.Endpoint]service.RoutedOverrideSpec{
					service.EndpointPublic: *cellTemplate.NoVNCProxyServiceTemplate.Override.Service,
				},
				instance.Spec.Nova.CellOverride[cellName].NoVNCProxy,
				corev1beta1.OpenStackControlPlaneExposeNovaReadyCondition,
				ptr.To(true), // TODO (mschuppert) could be removed when all integrated service support TLS
			)
			if err != nil {
				return ctrlResult, err
			} else if (ctrlResult != ctrl.Result{}) {
				return ctrlResult, nil
			}

			routedOverrideSpec := cellServiceEndpointDetails.GetEndpointServiceOverrides()
			cellTemplate.NoVNCProxyServiceTemplate.Override.Service = ptr.To(routedOverrideSpec[service.EndpointPublic])

			/*
				TODO
					// update TLS settings with cert secret and CABundle
					tlsSpec := cellTemplate.NoVNCProxyServiceTemplate.TLS
					tlsSpec.CaBundleSecretName = instance.Status.TLS.CaBundleSecretName
					for endpt, endptCfg := range apiServiceEndpointDetails.EndpointDetails {
						if endptCfg.Service.TLS.Enabled {
							switch endpt {
							case service.EndpointPublic:
								tlsSpec.API.Public.SecretName = endptCfg.Service.TLS.SecretName
							case service.EndpointInternal:
								tlsSpec.API.Internal.SecretName = endptCfg.Service.TLS.SecretName
							}
						}
					}
					instance.Spec.Nova.Template.APIServiceTemplate.TLS = tlsSpec
			*/

			instance.Spec.Nova.Template.CellTemplates[cellName] = cellTemplate
		}
	}

	Log.Info("Reconciling Nova", "Nova.Namespace", instance.Namespace, "Nova.Name", nova.Name)
	op, err := controllerutil.CreateOrPatch(ctx, helper.GetClient(), nova, func() error {
		// 1)
		// Nova.Spec.APIDatabaseInstance and each NovaCell.CellDatabaseInstance
		// are defaulted to "openstack" in nova-operator and the MariaDB created
		// by openstack-operator is also named "openstack". This works but
		// in production we might want to have separate DB service instances
		// per cell.
		//
		// 2)
		// Each NovaCell.CellMessageBusInstance in defaulted to "rabbitmq" by
		// nova-operator and openstack-operator creates RabbitMQCluster named
		// "rabbitmq" as well. This will not work as sharing rabbitmq
		// between cells will prevent the nova-computes to register itself
		// for the proper cell. Basically each cell will be merged to one,
		// cell0 but cell0 should not have compute nodes registered. Eventually
		// we need to support either rabbitmq vhosts or deploy a separate
		// RabbitMQCluster per nova cell.
		instance.Spec.Nova.Template.DeepCopyInto(&nova.Spec)

		err := controllerutil.SetControllerReference(helper.GetBeforeObject(), nova, helper.GetScheme())
		if err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			corev1beta1.OpenStackControlPlaneNovaReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			corev1beta1.OpenStackControlPlaneNovaReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}
	if op != controllerutil.OperationResultNone {
		Log.Info(fmt.Sprintf("Nova %s - %s", nova.Name, op))
	}

	if nova.IsReady() {
		instance.Status.Conditions.MarkTrue(corev1beta1.OpenStackControlPlaneNovaReadyCondition, corev1beta1.OpenStackControlPlaneNovaReadyMessage)
	} else {
		instance.Status.Conditions.Set(condition.FalseCondition(
			corev1beta1.OpenStackControlPlaneNovaReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			corev1beta1.OpenStackControlPlaneNovaReadyRunningMessage))
	}

	return ctrl.Result{}, nil
}

func getNoVNCProxyServiceLabel(name string, cellName string) string {
	return name + "-novncproxy-" + cellName
}

func getMetadataLabelMap(name string, instType string) map[string]string {
	return map[string]string{
		common.AppSelector: name + "-metadata",
		"type":             instType,
	}
}

func centralMetadataEnabled(metadata novav1.NovaMetadataTemplate) bool {
	// create certificate for central Metadata agent if internal TLS and Metadata are enabled
	return metadata.Enabled == nil || (metadata.Enabled != nil && *metadata.Enabled == true)
}

func centralMetadataLabelMap(name string) map[string]string {
	return getMetadataLabelMap(name, "central")
}

func cellMetadataEnabled(metadata novav1.NovaMetadataTemplate) bool {
	return metadata.Enabled != nil && *metadata.Enabled == true
}

func cellNoVNCProxEnabled(vncproxy novav1.NovaNoVNCProxyTemplate) bool {
	return vncproxy.Enabled != nil && *vncproxy.Enabled == true
}

func cellMetadataLabelMap(name string, cell string) map[string]string {
	lm := getMetadataLabelMap(name, "cell")
	lm["cell"] = cell
	return lm
}

// TODO move to lib-common/modules/certmanager?
func getMetadataCertificate(
	ctx context.Context,
	helper *helper.Helper,
	name string,
	namespace string,
	serviceOverride *service.OverrideSpec,
) (tls.SimpleService, ctrl.Result, error) {
	t := tls.SimpleService{
		Ca: tls.Ca{
			CaBundleSecretName: CombinedCASecret,
		},
	}

	svcs, err := service.GetServicesListWithLabel(
		ctx,
		helper,
		namespace,
		serviceOverride.Labels,
	)
	if err != nil {
		return t, ctrl.Result{}, err
	}

	for _, svc := range svcs.Items {
		// create cert for the service
		certRequest := certmanager.CertificateRequest{
			IssuerName: DefaultCAPrefix + string(service.EndpointInternal),
			CertName:   fmt.Sprintf("%s-svc", svc.Name),
			Hostnames:  []string{fmt.Sprintf("%s.%s.svc", svc.Name, namespace)},
			Labels:     svc.Labels,
		}
		certSecret, ctrlResult, err := certmanager.EnsureCert(
			ctx,
			helper,
			certRequest)
		if err != nil {
			return t, ctrlResult, err
		} else if (ctrlResult != ctrl.Result{}) {
			return t, ctrlResult, nil
		}

		t.SecretName = &certSecret.Name
		break
	}

	return t, ctrl.Result{}, nil
}
