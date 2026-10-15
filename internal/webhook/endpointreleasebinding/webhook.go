// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointreleasebinding

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	openchoreodevv1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

// SetupEndpointReleaseBindingWebhookWithManager registers the validating webhook
// for EndpointReleaseBinding in the manager.
func SetupEndpointReleaseBindingWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &openchoreodevv1alpha1.EndpointReleaseBinding{}).
		WithCustomValidator(&Validator{Client: mgr.GetClient()}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-openchoreo-dev-v1alpha1-endpointreleasebinding,mutating=false,failurePolicy=fail,sideEffects=None,groups=openchoreo.dev,resources=endpointreleasebindings,verbs=create,versions=v1alpha1,name=vendpointreleasebinding-v1alpha1.kb.io,admissionReviewVersions=v1

// Validator refuses a second EndpointReleaseBinding for the same Endpoint and
// environment: an Endpoint runs one release per environment. Owner and
// environment are immutable, so only creates are checked.
type Validator struct {
	Client client.Reader
}

var _ webhook.CustomValidator = &Validator{}

// ValidateCreate implements webhook.CustomValidator.
func (v *Validator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	erb, ok := obj.(*openchoreodevv1alpha1.EndpointReleaseBinding)
	if !ok {
		return nil, fmt.Errorf("expected an EndpointReleaseBinding object but got %T", obj)
	}

	var existing openchoreodevv1alpha1.EndpointReleaseBindingList
	if err := v.Client.List(ctx, &existing, client.InNamespace(erb.Namespace)); err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("list endpoint release bindings: %w", err))
	}
	for i := range existing.Items {
		other := &existing.Items[i]
		if other.Name == erb.Name ||
			other.Spec.Owner != erb.Spec.Owner ||
			other.Spec.Environment != erb.Spec.Environment {
			continue
		}
		return nil, apierrors.NewInvalid(
			openchoreodevv1alpha1.GroupVersion.WithKind("EndpointReleaseBinding").GroupKind(), erb.Name,
			field.ErrorList{field.Duplicate(field.NewPath("spec", "environment"),
				fmt.Sprintf("Endpoint %q is already bound in %q by %q", erb.Spec.Owner.EndpointName, erb.Spec.Environment, other.Name))})
	}
	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator.
func (v *Validator) ValidateUpdate(context.Context, runtime.Object, runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator.
func (v *Validator) ValidateDelete(context.Context, runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
