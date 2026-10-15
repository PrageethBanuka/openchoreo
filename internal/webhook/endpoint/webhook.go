// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"context"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	openchoreodevv1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
)

// SetupEndpointWebhookWithManager registers the validating webhook for Endpoint in the manager.
func SetupEndpointWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &openchoreodevv1alpha1.Endpoint{}).
		WithCustomValidator(&Validator{Client: mgr.GetClient()}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-openchoreo-dev-v1alpha1-endpoint,mutating=false,failurePolicy=fail,sideEffects=None,groups=openchoreo.dev,resources=endpoints,verbs=create;update,versions=v1alpha1,name=vendpoint-v1alpha1.kb.io,admissionReviewVersions=v1

// Validator refuses an Endpoint that would serve a URL another Endpoint
// already serves. A backend with weight above 0 claims its component's
// workload endpoint URL, so two Endpoints in a project may not both send
// traffic to the same {component, endpoint}: one URL, one route.
type Validator struct {
	Client client.Reader
}

var _ webhook.CustomValidator = &Validator{}

// ValidateCreate implements webhook.CustomValidator.
func (v *Validator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	ep, ok := obj.(*openchoreodevv1alpha1.Endpoint)
	if !ok {
		return nil, fmt.Errorf("expected an Endpoint object but got %T", obj)
	}
	return nil, v.validateServingSlots(ctx, ep)
}

// ValidateUpdate implements webhook.CustomValidator.
func (v *Validator) ValidateUpdate(ctx context.Context, _, newObj runtime.Object) (admission.Warnings, error) {
	ep, ok := newObj.(*openchoreodevv1alpha1.Endpoint)
	if !ok {
		return nil, fmt.Errorf("expected an Endpoint object but got %T", newObj)
	}
	if !ep.DeletionTimestamp.IsZero() {
		return nil, nil
	}
	return nil, v.validateServingSlots(ctx, ep)
}

// ValidateDelete implements webhook.CustomValidator.
func (v *Validator) ValidateDelete(context.Context, runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (v *Validator) validateServingSlots(ctx context.Context, ep *openchoreodevv1alpha1.Endpoint) error {
	slots := servingSlots(ep)
	if len(slots) == 0 {
		return nil
	}

	var others openchoreodevv1alpha1.EndpointList
	if err := v.Client.List(ctx, &others, client.InNamespace(ep.Namespace)); err != nil {
		return apierrors.NewInternalError(fmt.Errorf("list endpoints: %w", err))
	}

	var allErrs field.ErrorList
	backendsPath := field.NewPath("spec", "backendRefs")
	for i := range others.Items {
		other := &others.Items[i]
		if other.Name == ep.Name || other.Spec.Owner.ProjectName != ep.Spec.Owner.ProjectName ||
			!other.DeletionTimestamp.IsZero() {
			continue
		}
		taken := servingSlots(other)
		for idx, ref := range ep.Spec.BackendRefs {
			slot := slotKey(ref)
			if slots[slot] && taken[slot] {
				allErrs = append(allErrs, field.Forbidden(backendsPath.Index(idx),
					fmt.Sprintf("%s/%s already receives traffic through Endpoint %q; one URL is served by one Endpoint. "+
						"Add the backend at weight 0, or move its traffic off %q first",
						ref.ComponentName, ref.Endpoint, other.Name, other.Name)))
			}
		}
	}
	if len(allErrs) == 0 {
		return nil
	}
	sort.Slice(allErrs, func(i, j int) bool { return allErrs[i].Field < allErrs[j].Field })
	return apierrors.NewInvalid(openchoreodevv1alpha1.GroupVersion.WithKind("Endpoint").GroupKind(), ep.Name, allErrs)
}

// servingSlots returns the {component, endpoint} slots the Endpoint sends
// traffic to, that is its backends with weight above 0.
func servingSlots(ep *openchoreodevv1alpha1.Endpoint) map[string]bool {
	slots := map[string]bool{}
	for _, ref := range ep.Spec.BackendRefs {
		if controller.EndpointBackendWeight(ref) > 0 {
			slots[slotKey(ref)] = true
		}
	}
	return slots
}

func slotKey(ref openchoreodevv1alpha1.EndpointBackendRef) string {
	return ref.ComponentName + "/" + ref.Endpoint
}
