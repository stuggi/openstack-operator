package openstack

import (
	"context"

	certmgrv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	certmgrmetav1 "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	"github.com/openstack-k8s-operators/lib-common/modules/certmanager"
	"github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/service"

	corev1 "github.com/openstack-k8s-operators/openstack-operator/apis/core/v1beta1"

	ctrl "sigs.k8s.io/controller-runtime"
)

// ReconcileCAs -
func ReconcileCAs(ctx context.Context, instance *corev1.OpenStackControlPlane, helper *helper.Helper) (ctrl.Result, error) {
	// create selfsigned-issuer
	issuerReq := certmanager.SelfSignedIssuer(
		"selfsigned-issuer",
		instance.GetNamespace(),
		map[string]string{},
	)
	/*
		// Cleanuo?
		if !instance.Spec.TLS.Enabled {
			if err := cert.Delete(ctx, helper); err != nil {
				return ctrl.Result{}, err
			}
			instance.Status.Conditions.Remove(corev1beta1.OpenStackControlPlaneCAsReadyCondition)

			return ctrl.Result{}, nil
		}
	*/

	helper.GetLogger().Info("Reconciling CAs", "Namespace", instance.Namespace, "Name", issuerReq.Name)

	issuer := certmanager.NewIssuer(issuerReq, 5)
	ctrlResult, err := issuer.CreateOrPatch(ctx, helper)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			corev1.OpenStackControlPlaneCAReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			corev1.OpenStackControlPlaneCAReadyErrorMessage,
			issuerReq.Kind,
			issuerReq.GetName(),
			err.Error()))

		return ctrlResult, err
	} else if (ctrlResult != ctrl.Result{}) {
		instance.Status.Conditions.Set(condition.FalseCondition(
			corev1.OpenStackControlPlaneCAReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			corev1.OpenStackControlPlaneCAReadyRunningMessage))

		return ctrlResult, nil
	}

	// create RootCA cert and Issuer that uses the generated CA certificate to issue certs
	for _, endpointType := range []service.Endpoint{service.EndpointPublic, service.EndpointInternal} {
		// create RootCA Certificate used to sign certificates
		caName := "rootca-" + string(endpointType)
		caCertReq := certmanager.Cert(
			caName,
			instance.Namespace,
			map[string]string{},
			certmgrv1.CertificateSpec{
				IsCA:       true,
				CommonName: caName,
				SecretName: caName,
				PrivateKey: &certmgrv1.CertificatePrivateKey{
					Algorithm: "ECDSA",
					Size:      256,
				},
				IssuerRef: certmgrmetav1.ObjectReference{
					Name:  issuerReq.Name,
					Kind:  issuerReq.Kind,
					Group: issuerReq.GroupVersionKind().Group,
				},
			})
		cert := certmanager.NewCertificate(caCertReq, 5)

		ctrlResult, err := cert.CreateOrPatch(ctx, helper)
		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(
				corev1.OpenStackControlPlaneCAReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				corev1.OpenStackControlPlaneCAReadyErrorMessage,
				caCertReq.Kind,
				caCertReq.Name,
				err.Error()))

			return ctrlResult, err
		} else if (ctrlResult != ctrl.Result{}) {
			instance.Status.Conditions.Set(condition.FalseCondition(
				corev1.OpenStackControlPlaneCAReadyCondition,
				condition.RequestedReason,
				condition.SeverityInfo,
				corev1.OpenStackControlPlaneCAReadyRunningMessage))

			return ctrlResult, nil
		}

		// create Issuer that uses the generated CA certificate to issue certs
		issuerReq := certmanager.CAIssuer(
			caCertReq.Name,
			instance.GetNamespace(),
			map[string]string{},
			caCertReq.Name,
		)

		issuer := certmanager.NewIssuer(issuerReq, 5)
		ctrlResult, err = issuer.CreateOrPatch(ctx, helper)
		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(
				corev1.OpenStackControlPlaneCAReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				corev1.OpenStackControlPlaneCAReadyErrorMessage,
				issuerReq.Kind,
				issuerReq.GetName(),
				err.Error()))

			return ctrlResult, err
		} else if (ctrlResult != ctrl.Result{}) {
			instance.Status.Conditions.Set(condition.FalseCondition(
				corev1.OpenStackControlPlaneCAReadyCondition,
				condition.RequestedReason,
				condition.SeverityInfo,
				corev1.OpenStackControlPlaneCAReadyRunningMessage))

			return ctrlResult, nil
		}
		instance.Status.Conditions.MarkTrue(corev1.OpenStackControlPlaneCAReadyCondition, corev1.OpenStackControlPlaneCAReadyMessage)
	}

	return ctrl.Result{}, nil
}
