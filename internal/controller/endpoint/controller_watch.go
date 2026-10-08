// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
)

const (
	// endpointTypeRefIndex indexes Endpoint by its (Cluster)EndpointType reference.
	// Index key format: "{kind}:{name}" — e.g. "EndpointType:http-weighted",
	// "ClusterEndpointType:http-weighted". Mirrors Resource's resourceTypeRefIndex.
	endpointTypeRefIndex = "spec.type"
)

// indexEndpointTypeRef extracts the (Cluster)EndpointType reference key from an
// Endpoint. Exposed as a package-level value so tests can pass it to
// fake.NewClientBuilder().WithIndex.
func indexEndpointTypeRef(obj client.Object) []string {
	ep := obj.(*openchoreov1alpha1.Endpoint)
	kind := resolvedKind(ep.Spec.Type.Kind)
	return []string{string(kind) + ":" + ep.Spec.Type.Name}
}

// setupEndpointTypeRefIndex registers the field index used by the watch mappers.
func (r *Reconciler) setupEndpointTypeRefIndex(ctx context.Context, mgr ctrl.Manager) error {
	return mgr.GetFieldIndexer().IndexField(ctx, &openchoreov1alpha1.Endpoint{},
		endpointTypeRefIndex, indexEndpointTypeRef)
}

// listEndpointsForEndpointType returns reconcile requests for Endpoints in the
// same namespace as the given EndpointType that reference it via
// spec.type.{Kind=EndpointType, Name=et.Name}.
func (r *Reconciler) listEndpointsForEndpointType(ctx context.Context, obj client.Object) []reconcile.Request {
	et := obj.(*openchoreov1alpha1.EndpointType)
	indexKey := string(openchoreov1alpha1.EndpointTypeRefKindEndpointType) + ":" + et.Name
	return r.requestsForIndex(ctx, endpointTypeRefIndex, indexKey, client.InNamespace(et.Namespace))
}

// listEndpointsForClusterEndpointType returns reconcile requests for Endpoints
// across all namespaces that reference the given ClusterEndpointType via
// spec.type.{Kind=ClusterEndpointType, Name=cet.Name}.
func (r *Reconciler) listEndpointsForClusterEndpointType(ctx context.Context, obj client.Object) []reconcile.Request {
	cet := obj.(*openchoreov1alpha1.ClusterEndpointType)
	indexKey := string(openchoreov1alpha1.EndpointTypeRefKindClusterEndpointType) + ":" + cet.Name
	return r.requestsForIndex(ctx, endpointTypeRefIndex, indexKey)
}

// listEndpointsForComponent returns reconcile requests for Endpoints in the
// Component's namespace that list it as a backend, so a BackendNotFound
// condition clears once the Component is created.
func (r *Reconciler) listEndpointsForComponent(ctx context.Context, obj client.Object) []reconcile.Request {
	comp := obj.(*openchoreov1alpha1.Component)
	indexKey := controller.MakeEndpointBackendComponentKey(comp.Spec.Owner.ProjectName, comp.Name)
	return r.requestsForIndex(ctx, controller.IndexKeyEndpointBackendComponent, indexKey,
		client.InNamespace(comp.Namespace))
}

// requestsForIndex lists Endpoints by the given field index and converts them
// to reconcile.Requests. Extra ListOptions (e.g. client.InNamespace) scope the
// lookup further.
func (r *Reconciler) requestsForIndex(ctx context.Context, index, indexKey string, opts ...client.ListOption) []reconcile.Request {
	listOpts := append([]client.ListOption{client.MatchingFields{index: indexKey}}, opts...)

	var endpoints openchoreov1alpha1.EndpointList
	if err := r.List(ctx, &endpoints, listOpts...); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to list Endpoints by index", "index", index, "indexKey", indexKey)
		return nil
	}

	requests := make([]reconcile.Request, len(endpoints.Items))
	for i, ep := range endpoints.Items {
		requests[i] = reconcile.Request{
			NamespacedName: types.NamespacedName{Name: ep.Name, Namespace: ep.Namespace},
		}
	}
	return requests
}
