// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointreleasebinding

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
)

// endpointReleaseRefIndex indexes EndpointReleaseBinding by spec.endpointRelease
// so an EndpointRelease event wakes exactly the bindings pinned to it.
const endpointReleaseRefIndex = "spec.endpointRelease"

// indexEndpointReleaseRef extracts the pinned EndpointRelease name from a
// binding. Exposed as a package-level value so tests can pass it to
// fake.NewClientBuilder().WithIndex.
func indexEndpointReleaseRef(obj client.Object) []string {
	binding := obj.(*openchoreov1alpha1.EndpointReleaseBinding)
	if binding.Spec.EndpointRelease == "" {
		return nil
	}
	return []string{binding.Spec.EndpointRelease}
}

// setupEndpointReleaseRefIndex registers the field index used by the
// EndpointRelease watch mapper.
func (r *Reconciler) setupEndpointReleaseRefIndex(ctx context.Context, mgr ctrl.Manager) error {
	return mgr.GetFieldIndexer().IndexField(ctx, &openchoreov1alpha1.EndpointReleaseBinding{},
		endpointReleaseRefIndex, indexEndpointReleaseRef)
}

// listBindingsForEndpointRelease returns reconcile requests for the bindings in
// the release's namespace that pin it.
func (r *Reconciler) listBindingsForEndpointRelease(ctx context.Context, obj client.Object) []reconcile.Request {
	release := obj.(*openchoreov1alpha1.EndpointRelease)

	var bindings openchoreov1alpha1.EndpointReleaseBindingList
	if err := r.List(ctx, &bindings,
		client.InNamespace(release.Namespace),
		client.MatchingFields{endpointReleaseRefIndex: release.Name}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list EndpointReleaseBindings for EndpointRelease",
			"endpointRelease", release.Name)
		return nil
	}
	return toRequests(bindings.Items)
}

// listBindingsForBackendReleaseBinding maps a component's ReleaseBinding to the
// EndpointReleaseBindings, in the same environment, of every Endpoint that
// routes to that component. A backend becoming available (or changing its
// service URL) in an environment then re-resolves the Endpoints there.
func (r *Reconciler) listBindingsForBackendReleaseBinding(ctx context.Context, obj client.Object) []reconcile.Request {
	rb := obj.(*openchoreov1alpha1.ReleaseBinding)
	project := rb.Spec.Owner.ProjectName
	if project == "" || rb.Spec.Owner.ComponentName == "" || rb.Spec.Environment == "" {
		return nil
	}

	var endpoints openchoreov1alpha1.EndpointList
	if err := r.List(ctx, &endpoints,
		client.InNamespace(rb.Namespace),
		client.MatchingFields{controller.IndexKeyEndpointBackendComponent: controller.MakeEndpointBackendComponentKey(
			project, rb.Spec.Owner.ComponentName)}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list Endpoints for backend component",
			"component", rb.Spec.Owner.ComponentName)
		return nil
	}

	var requests []reconcile.Request
	for i := range endpoints.Items {
		var bindings openchoreov1alpha1.EndpointReleaseBindingList
		if err := r.List(ctx, &bindings,
			client.InNamespace(rb.Namespace),
			client.MatchingFields{controller.IndexKeyEndpointReleaseBindingOwnerEnv: controller.MakeEndpointReleaseBindingOwnerEnvKey(
				project, endpoints.Items[i].Name, rb.Spec.Environment)}); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "Failed to list EndpointReleaseBindings for Endpoint",
				"endpoint", endpoints.Items[i].Name)
			continue
		}
		requests = append(requests, toRequests(bindings.Items)...)
	}
	return requests
}

func toRequests(bindings []openchoreov1alpha1.EndpointReleaseBinding) []reconcile.Request {
	requests := make([]reconcile.Request, len(bindings))
	for i := range bindings {
		requests[i] = reconcile.Request{
			NamespacedName: types.NamespacedName{Name: bindings[i].Name, Namespace: bindings[i].Namespace},
		}
	}
	return requests
}
