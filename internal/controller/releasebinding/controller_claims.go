// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package releasebinding

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
	"github.com/openchoreo/openchoreo/internal/controller/endpointurl"
	"github.com/openchoreo/openchoreo/internal/labels"
)

// endpointClaim is an Endpoint's claim on one of this component's workload
// endpoints in the binding's environment: the Endpoint's own route serves the
// listed visibilities, so the component must not render routes for them.
type endpointClaim struct {
	endpointName string
	visibilities []openchoreov1alpha1.EndpointVisibility
}

// claimedEndpoints returns, per workload endpoint name, the Endpoint claims
// on this component in the binding's environment: the Endpoints routing to
// it. A claim is the
// status.serving record of an EndpointReleaseBinding: present once the
// Endpoint's route has been accepted, absent or marked releasing while the
// binding hands traffic back. Endpoints and their bindings are read from the
// binding's namespace and filtered here, which is a cache read in the manager.
func (r *Reconciler) claimedEndpoints(
	ctx context.Context,
	rb *openchoreov1alpha1.ReleaseBinding,
) (map[string][]endpointClaim, error) {
	project := rb.Spec.Owner.ProjectName
	component := rb.Spec.Owner.ComponentName
	env := rb.Spec.Environment

	var endpoints openchoreov1alpha1.EndpointList
	if err := r.List(ctx, &endpoints, client.InNamespace(rb.Namespace)); err != nil {
		return nil, fmt.Errorf("list Endpoints: %w", err)
	}
	routing := make(map[string]bool)
	for i := range endpoints.Items {
		ep := &endpoints.Items[i]
		if ep.Spec.Owner.ProjectName != project {
			continue
		}
		for _, name := range controller.EndpointComponentNames(ep) {
			if name == component {
				routing[ep.Name] = true
			}
		}
	}
	if len(routing) == 0 {
		return nil, nil
	}

	var bindings openchoreov1alpha1.EndpointReleaseBindingList
	if err := r.List(ctx, &bindings, client.InNamespace(rb.Namespace)); err != nil {
		return nil, fmt.Errorf("list EndpointReleaseBindings: %w", err)
	}

	claims := make(map[string][]endpointClaim)
	for i := range bindings.Items {
		erb := &bindings.Items[i]
		serving := erb.Status.Serving
		if erb.Spec.Owner.ProjectName != project || erb.Spec.Environment != env ||
			!routing[erb.Spec.Owner.EndpointName] || serving == nil || serving.Releasing {
			continue
		}
		for _, slot := range serving.URLs {
			if slot.ComponentName != component {
				continue
			}
			claims[slot.Endpoint] = append(claims[slot.Endpoint], endpointClaim{
				endpointName: erb.Spec.Owner.EndpointName,
				visibilities: serving.Visibilities,
			})
		}
	}
	for name := range claims {
		sort.Slice(claims[name], func(i, j int) bool {
			return claims[name][i].endpointName < claims[name][j].endpointName
		})
	}
	return claims, nil
}

// dropClaimedRoutes removes the component's Gateway API routes for claimed
// (workload endpoint, visibility) slots. Routes are matched by their
// openchoreo.dev/endpoint-name and openchoreo.dev/endpoint-visibility labels,
// which every ComponentType route carries for URL resolution. Routes of other
// visibilities, and every other resource, are kept.
func dropClaimedRoutes(resources []map[string]any, claims map[string][]endpointClaim) []map[string]any {
	if len(claims) == 0 {
		return resources
	}
	kept := resources[:0:0]
	for _, res := range resources {
		if isClaimedRoute(res, claims) {
			continue
		}
		kept = append(kept, res)
	}
	return kept
}

func isClaimedRoute(res map[string]any, claims map[string][]endpointClaim) bool {
	apiVersion, _ := res["apiVersion"].(string)
	kind, _ := res["kind"].(string)
	if _, isRoute := endpointurl.RouteKindCompat[kind]; !isRoute || !strings.HasPrefix(apiVersion, endpointurl.GatewayAPIGroup+"/") {
		return false
	}
	metadata, _ := res["metadata"].(map[string]any)
	resLabels, _ := metadata["labels"].(map[string]any)
	endpointName, _ := resLabels[labels.LabelKeyEndpointName].(string)
	visibility, _ := resLabels[labels.LabelKeyEndpointVisibility].(string)
	for _, c := range claims[endpointName] {
		if slices.Contains(c.visibilities, openchoreov1alpha1.EndpointVisibility(visibility)) {
			return true
		}
	}
	return false
}

// applyClaimsToStatus records, on each workload endpoint's status, the
// Endpoints that own its routing in which visibilities.
func applyClaimsToStatus(
	statuses []openchoreov1alpha1.EndpointURLStatus,
	claims map[string][]endpointClaim,
) []openchoreov1alpha1.EndpointURLStatus {
	for i := range statuses {
		st := &statuses[i]
		st.RoutedBy = nil
		for _, c := range claims[st.Name] {
			st.RoutedBy = append(st.RoutedBy, openchoreov1alpha1.EndpointRouteOwner{
				EndpointName: c.endpointName,
				Visibilities: c.visibilities,
			})
		}
	}
	return statuses
}

// endpointClaimChangedPredicate passes EndpointReleaseBinding events that can
// change which component routes are suppressed: a new claim, a changed claim
// (backends, visibilities or releasing), or deletion.
func endpointClaimChangedPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			erb, ok := e.Object.(*openchoreov1alpha1.EndpointReleaseBinding)
			return ok && erb.Status.Serving != nil
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldERB, ok := e.ObjectOld.(*openchoreov1alpha1.EndpointReleaseBinding)
			if !ok {
				return false
			}
			newERB, ok := e.ObjectNew.(*openchoreov1alpha1.EndpointReleaseBinding)
			if !ok {
				return false
			}
			return !apiequality.Semantic.DeepEqual(oldERB.Status.Serving, newERB.Status.Serving)
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			return true
		},
		GenericFunc: func(_ event.GenericEvent) bool {
			return false
		},
	}
}

// endpointClaimHandler enqueues the component ReleaseBindings named by an
// EndpointReleaseBinding's claim. On update it uses both the old and the new
// claim, so a backend that leaves the claim gets its own routes back promptly.
func (r *Reconciler) endpointClaimHandler() handler.EventHandler {
	enqueue := func(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request], objs ...client.Object) {
		seen := make(map[reconcile.Request]bool)
		for _, obj := range objs {
			for _, req := range r.releaseBindingsForEndpointClaim(ctx, obj) {
				if !seen[req] {
					seen[req] = true
					q.Add(req)
				}
			}
		}
	}
	return handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, q, e.Object)
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, q, e.ObjectOld, e.ObjectNew)
		},
		DeleteFunc: func(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueue(ctx, q, e.Object)
		},
	}
}

// releaseBindingsForEndpointClaim maps an EndpointReleaseBinding to the
// component ReleaseBindings in its environment for each claimed backend.
func (r *Reconciler) releaseBindingsForEndpointClaim(ctx context.Context, obj client.Object) []reconcile.Request {
	erb, ok := obj.(*openchoreov1alpha1.EndpointReleaseBinding)
	if !ok || erb.Status.Serving == nil {
		return nil
	}
	var requests []reconcile.Request
	for _, slot := range erb.Status.Serving.URLs {
		var rbList openchoreov1alpha1.ReleaseBindingList
		if err := r.List(ctx, &rbList, client.InNamespace(erb.Namespace),
			client.MatchingFields{controller.IndexKeyReleaseBindingOwnerEnv: controller.MakeReleaseBindingOwnerEnvKey(
				erb.Spec.Owner.ProjectName, slot.ComponentName, erb.Spec.Environment)}); err != nil {
			log.FromContext(ctx).Error(err, "Failed to list ReleaseBindings for endpoint claim",
				"endpointReleaseBinding", erb.Name, "component", slot.ComponentName)
			continue
		}
		for i := range rbList.Items {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: rbList.Items[i].Name, Namespace: rbList.Items[i].Namespace},
			})
		}
	}
	return requests
}
