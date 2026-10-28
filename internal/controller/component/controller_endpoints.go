// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package component

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
)

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
// traffic through an Endpoint that has other backends to move it to, or ""
// when deleting it breaks no route that could be kept.
func (r *Reconciler) stillRouted(ctx context.Context, comp *openchoreov1alpha1.Component) (string, error) {
	usages, err := controller.ComponentEndpointUsage(ctx, r.Client, comp.Namespace, comp.Spec.Owner.ProjectName, comp.Name)
	if err != nil {
		return "", err
	}
	routed := controller.RoutedEndpointUsage(usages)
	if len(routed) == 0 {
		return "", nil
	}
	return "Still " + controller.StillRoutedReason(routed), nil
}

// pruneFromEndpoints removes the component from the backends of every Endpoint
// in its project. It runs during deletion, once stillRouted has confirmed no
// Endpoint still needs the component, so the Endpoints keep serving through
// their other backends. An Endpoint whose only backend was this component is
// deleted, since an Endpoint needs at least one backend.
func (r *Reconciler) pruneFromEndpoints(ctx context.Context, comp *openchoreov1alpha1.Component) error {
	var endpoints openchoreov1alpha1.EndpointList
	if err := r.List(ctx, &endpoints, client.InNamespace(comp.Namespace)); err != nil {
		return fmt.Errorf("list endpoints: %w", err)
	}
	for i := range endpoints.Items {
		ep := &endpoints.Items[i]
		if ep.Spec.Owner.ProjectName != comp.Spec.Owner.ProjectName {
			continue
		}
		kept := make([]openchoreov1alpha1.EndpointBackendRef, 0, len(ep.Spec.BackendRefs))
		for _, ref := range ep.Spec.BackendRefs {
			if name, _, ok := controller.BackendComponent(ref); !ok || name != comp.Name {
				kept = append(kept, ref)
			}
		}
		switch {
		case len(kept) == len(ep.Spec.BackendRefs):
			continue
		case len(kept) == 0:
			if err := r.Delete(ctx, ep); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete endpoint %s left without backends: %w", ep.Name, err)
			}
			log.FromContext(ctx).Info("Deleted Endpoint whose only backend was the deleted component", "endpoint", ep.Name)
		default:
			patch := client.MergeFrom(ep.DeepCopy())
			ep.Spec.BackendRefs = kept
			if err := r.Patch(ctx, ep, patch); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("remove %s from endpoint %s: %w", comp.Name, ep.Name, err)
			}
			log.FromContext(ctx).Info("Removed deleted component from Endpoint", "endpoint", ep.Name)
		}
	}
	return nil
}

// listComponentsForEndpoint maps an Endpoint to the Components it routes to.
func (r *Reconciler) listComponentsForEndpoint(_ context.Context, obj client.Object) []ctrl.Request {
	ep := obj.(*openchoreov1alpha1.Endpoint)
	return componentRequests(ep)
}

// listComponentsForEndpointReleaseBinding maps an EndpointReleaseBinding to the
// Components its Endpoint references, whose reverse index carries its weights.
func (r *Reconciler) listComponentsForEndpointReleaseBinding(ctx context.Context, obj client.Object) []ctrl.Request {
	binding := obj.(*openchoreov1alpha1.EndpointReleaseBinding)
	ep := &openchoreov1alpha1.Endpoint{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Spec.Owner.EndpointName}, ep); err != nil {
		return nil
	}
	return componentRequests(ep)
}

func componentRequests(ep *openchoreov1alpha1.Endpoint) []ctrl.Request {
	names := controller.EndpointComponentNames(ep)
	reqs := make([]ctrl.Request, 0, len(names))
	for _, name := range names {
		reqs = append(reqs, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ep.Namespace, Name: name}})
	}
	return reqs
}
