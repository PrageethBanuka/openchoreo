// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package releasebinding

import (
	"context"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
)

// findReleaseBindingsForEndpoint maps an Endpoint to the ReleaseBindings of the
// components it routes to, so their claims are recomputed when the Endpoint's
// backends change.
func (r *Reconciler) findReleaseBindingsForEndpoint(ctx context.Context, obj client.Object) []reconcile.Request {
	ep := obj.(*openchoreov1alpha1.Endpoint)
	var reqs []reconcile.Request
	for _, name := range controller.EndpointComponentNames(ep) {
		var rbs openchoreov1alpha1.ReleaseBindingList
		if err := r.List(ctx, &rbs, client.InNamespace(ep.Namespace),
			client.MatchingFields{controller.IndexKeyReleaseBindingOwnerComponentName: name}); err != nil {
			log.FromContext(ctx).Error(err, "Failed to list ReleaseBindings for Endpoint component",
				"endpoint", ep.Name, "component", name)
			continue
		}
		for i := range rbs.Items {
			if rbs.Items[i].Spec.Owner.ProjectName != ep.Spec.Owner.ProjectName {
				continue
			}
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&rbs.Items[i])})
		}
	}
	return reqs
}

// endpointLatestReleaseChangedPredicate passes Endpoint creates, deletes and
// changes to status.latestRelease or backendRefs: the inputs that decide which
// Endpoints claim this component's routes.
func endpointLatestReleaseChangedPredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldEp, okOld := e.ObjectOld.(*openchoreov1alpha1.Endpoint)
			newEp, okNew := e.ObjectNew.(*openchoreov1alpha1.Endpoint)
			if !okOld || !okNew {
				return false
			}
			return !apiequality.Semantic.DeepEqual(oldEp.Status.LatestRelease, newEp.Status.LatestRelease) ||
				!apiequality.Semantic.DeepEqual(oldEp.Spec.BackendRefs, newEp.Spec.BackendRefs)
		},
	}
}
