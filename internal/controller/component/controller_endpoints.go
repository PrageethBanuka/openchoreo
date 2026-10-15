// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package component

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
	ocLabels "github.com/openchoreo/openchoreo/internal/labels"
)

// reconcileExposedEndpoints creates the Endpoint for each Workload endpoint that
// names an EndpointType, if it is missing. An existing Endpoint is never
// changed: once created it has its own lifecycle and is edited by people.
// Problems are reported on the EndpointsExposed condition and never block the
// rest of the Component's reconcile.
func (r *Reconciler) reconcileExposedEndpoints(ctx context.Context, comp *openchoreov1alpha1.Component,
	workload *openchoreov1alpha1.Workload) error {
	keys := make([]string, 0, len(workload.Spec.Endpoints))
	for key, ep := range workload.Spec.Endpoints {
		if ep.EndpointType != nil {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		meta.RemoveStatusCondition(&comp.Status.Conditions, string(ConditionEndpointsExposed))
		return nil
	}
	sort.Strings(keys)

	var problems []string
	reason := ReasonEndpointsExposed
	for _, key := range keys {
		wep := workload.Spec.Endpoints[key]
		name := controller.ExposedEndpointName(comp.Name, key, wep)

		found, err := r.endpointTypeExists(ctx, comp.Namespace, *wep.EndpointType)
		if err != nil {
			return err
		}
		if !found {
			reason = ReasonEndpointTypeNotFound
			problems = append(problems, fmt.Sprintf("%s: %s %q not found", key, endpointTypeKind(*wep.EndpointType), wep.EndpointType.Name))
			continue
		}

		existing := &openchoreov1alpha1.Endpoint{}
		err = r.Get(ctx, types.NamespacedName{Namespace: comp.Namespace, Name: name}, existing)
		switch {
		case apierrors.IsNotFound(err):
			if err := r.createExposedEndpoint(ctx, comp, key, name, wep); err != nil {
				return err
			}
		case err != nil:
			return fmt.Errorf("get endpoint %s: %w", name, err)
		case existing.Spec.Owner.ProjectName != comp.Spec.Owner.ProjectName ||
			!controller.HasEndpointBackend(existing, comp.Name, key):
			reason = ReasonExposeConflict
			problems = append(problems, fmt.Sprintf("%s: Endpoint %q exists and does not route to this component", key, name))
		case !controller.EndpointTypeRefEqual(existing.Spec.Type, *wep.EndpointType):
			reason = ReasonEndpointTypeMismatch
			problems = append(problems, fmt.Sprintf("%s: Endpoint %q has type %s %q; an Endpoint's type cannot change",
				key, name, endpointTypeKind(existing.Spec.Type), existing.Spec.Type.Name))
		}
	}

	if len(problems) > 0 {
		controller.MarkFalseCondition(comp, ConditionEndpointsExposed, reason, strings.Join(problems, "; "))
		return nil
	}
	controller.MarkTrueCondition(comp, ConditionEndpointsExposed, ReasonEndpointsExposed,
		fmt.Sprintf("%d workload endpoint(s) served through Endpoints", len(keys)))
	return nil
}

func (r *Reconciler) createExposedEndpoint(ctx context.Context, comp *openchoreov1alpha1.Component,
	key, name string, wep openchoreov1alpha1.WorkloadEndpoint) error {
	ep := &openchoreov1alpha1.Endpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: comp.Namespace,
			Labels: map[string]string{
				ocLabels.LabelKeyProjectName: comp.Spec.Owner.ProjectName,
			},
			Annotations: map[string]string{
				ocLabels.AnnotationKeyEndpointCreatedFrom: fmt.Sprintf("workload/%s/%s", comp.Name, key),
			},
		},
		Spec: openchoreov1alpha1.EndpointSpec{
			Owner: openchoreov1alpha1.EndpointOwner{ProjectName: comp.Spec.Owner.ProjectName},
			Type:  *wep.EndpointType,
			BackendRefs: []openchoreov1alpha1.EndpointBackendRef{
				{ComponentName: comp.Name, Endpoint: key, Weight: ptr.To[int32](100)},
			},
			ReclaimPolicy: openchoreov1alpha1.EndpointReclaimPolicyDelete,
		},
	}
	// No ownerReference: the Endpoint is not owned by the component.
	if err := r.Create(ctx, ep); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create endpoint %s: %w", name, err)
	}
	log.FromContext(ctx).Info("Created Endpoint for workload endpoint", "endpoint", name, "workloadEndpoint", key)
	return nil
}

func (r *Reconciler) endpointTypeExists(ctx context.Context, namespace string, ref openchoreov1alpha1.EndpointTypeRef) (bool, error) {
	var err error
	if ref.Kind == openchoreov1alpha1.EndpointTypeRefKindClusterEndpointType {
		err = r.Get(ctx, types.NamespacedName{Name: ref.Name}, &openchoreov1alpha1.ClusterEndpointType{})
	} else {
		err = r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, &openchoreov1alpha1.EndpointType{})
	}
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get %s %s: %w", endpointTypeKind(ref), ref.Name, err)
	}
	return true, nil
}

func endpointTypeKind(ref openchoreov1alpha1.EndpointTypeRef) openchoreov1alpha1.EndpointTypeRefKind {
	if ref.Kind == "" {
		return openchoreov1alpha1.EndpointTypeRefKindEndpointType
	}
	return ref.Kind
}

// updateEndpointIndex refreshes status.endpoints, the reverse index of the
// Endpoints that route to this component.
func (r *Reconciler) updateEndpointIndex(ctx context.Context, comp *openchoreov1alpha1.Component) error {
	usages, err := controller.ComponentEndpointUsage(ctx, r.Client, comp.Namespace, comp.Spec.Owner.ProjectName, comp.Name)
	if err != nil {
		return err
	}
	comp.Status.Endpoints = controller.ComponentEndpointRefs(usages)
	return nil
}

// stillRouted returns a message naming where the component still receives
// traffic through an Endpoint, or "" when it receives none.
func (r *Reconciler) stillRouted(ctx context.Context, comp *openchoreov1alpha1.Component) (string, error) {
	usages, err := controller.ComponentEndpointUsage(ctx, r.Client, comp.Namespace, comp.Spec.Owner.ProjectName, comp.Name)
	if err != nil {
		return "", err
	}
	routed := controller.RoutedEndpointUsage(usages)
	if len(routed) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(routed))
	for _, u := range routed {
		parts = append(parts, fmt.Sprintf("%s in %s (weight %d)", u.Endpoint, u.Environment, u.Weight))
	}
	return "Still receives traffic through " + strings.Join(parts, ", ") +
		"; move its weight to 0 and roll out before deleting", nil
}

// listComponentsForEndpoint maps an Endpoint to the Components in its backendRefs.
func (r *Reconciler) listComponentsForEndpoint(_ context.Context, obj client.Object) []ctrl.Request {
	ep := obj.(*openchoreov1alpha1.Endpoint)
	return componentRequests(ep.Namespace, ep.Spec.BackendRefs)
}

// listComponentsForEndpointReleaseBinding maps an EndpointReleaseBinding to the
// backend Components of its Endpoint, whose reverse index carries its weights.
func (r *Reconciler) listComponentsForEndpointReleaseBinding(ctx context.Context, obj client.Object) []ctrl.Request {
	binding := obj.(*openchoreov1alpha1.EndpointReleaseBinding)
	ep := &openchoreov1alpha1.Endpoint{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Spec.Owner.EndpointName}, ep); err != nil {
		return nil
	}
	return componentRequests(ep.Namespace, ep.Spec.BackendRefs)
}

func componentRequests(namespace string, refs []openchoreov1alpha1.EndpointBackendRef) []ctrl.Request {
	seen := map[string]bool{}
	var reqs []ctrl.Request
	for _, ref := range refs {
		if seen[ref.ComponentName] {
			continue
		}
		seen[ref.ComponentName] = true
		reqs = append(reqs, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: ref.ComponentName}})
	}
	return reqs
}
