// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package releasebinding

import (
	"context"
	"fmt"
	"sort"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
	"github.com/openchoreo/openchoreo/internal/labels"
)

// ensureExposedEndpointBindings routes the Endpoints this component's workload
// endpoints are served through in the binding's environment. Deploying the
// component is what routes them, so the developer's flow stays "deploy first".
//
// For each workload endpoint in the deployed release that names an EndpointType,
// if its Endpoint exists, routes to this component and has a release, and has
// no EndpointReleaseBinding in this environment yet, one is created pinned to
// the Endpoint's latest release. An existing binding is never re-pinned: moving
// traffic stays a person's action. The binding has no ownerReference, because
// it belongs to the Endpoint's lifecycle, not the component's.
func (r *Reconciler) ensureExposedEndpointBindings(ctx context.Context, rb *openchoreov1alpha1.ReleaseBinding,
	workload *openchoreov1alpha1.Workload) error {
	keys := make([]string, 0, len(workload.Spec.Endpoints))
	for key, ep := range workload.Spec.Endpoints {
		if ep.EndpointType != nil {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)

	project, component, env := rb.Spec.Owner.ProjectName, rb.Spec.Owner.ComponentName, rb.Spec.Environment

	var bindings openchoreov1alpha1.EndpointReleaseBindingList
	if err := r.List(ctx, &bindings, client.InNamespace(rb.Namespace)); err != nil {
		return fmt.Errorf("list endpoint release bindings: %w", err)
	}
	bound := map[string]bool{}
	for i := range bindings.Items {
		b := &bindings.Items[i]
		if b.Spec.Owner.ProjectName == project && b.Spec.Environment == env {
			bound[b.Spec.Owner.EndpointName] = true
		}
	}

	for _, key := range keys {
		name := controller.ExposedEndpointName(component, key, workload.Spec.Endpoints[key])
		if bound[name] {
			continue
		}
		ep := &openchoreov1alpha1.Endpoint{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: rb.Namespace, Name: name}, ep); err != nil {
			if apierrors.IsNotFound(err) {
				continue // the Component controller creates it; its watch brings us back
			}
			return fmt.Errorf("get endpoint %s: %w", name, err)
		}
		if ep.Spec.Owner.ProjectName != project || !controller.HasEndpointBackend(ep, component, key) ||
			ep.Status.LatestRelease == nil || !ep.DeletionTimestamp.IsZero() {
			continue
		}

		erb := &openchoreov1alpha1.EndpointReleaseBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%s", name, env),
				Namespace: rb.Namespace,
				Labels: map[string]string{
					labels.LabelKeyProjectName:          project,
					labels.LabelKeyEndpointResourceName: name,
				},
			},
			Spec: openchoreov1alpha1.EndpointReleaseBindingSpec{
				Owner: openchoreov1alpha1.EndpointReleaseBindingOwner{
					ProjectName:  project,
					EndpointName: name,
				},
				Environment:     env,
				EndpointRelease: ep.Status.LatestRelease.Name,
			},
		}
		if err := r.Create(ctx, erb); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create endpoint release binding %s: %w", erb.Name, err)
		}
		log.FromContext(ctx).Info("Routed exposed Endpoint in environment",
			"endpoint", name, "environment", env, "release", ep.Status.LatestRelease.Name)
	}
	return nil
}

// findReleaseBindingsForEndpoint maps an Endpoint to the ReleaseBindings of its
// backend components, so a newly created (or newly released) Endpoint gets its
// bindings in the environments those components are deployed to.
func (r *Reconciler) findReleaseBindingsForEndpoint(ctx context.Context, obj client.Object) []reconcile.Request {
	ep := obj.(*openchoreov1alpha1.Endpoint)
	seen := map[string]bool{}
	var reqs []reconcile.Request
	for _, ref := range ep.Spec.BackendRefs {
		if seen[ref.ComponentName] {
			continue
		}
		seen[ref.ComponentName] = true
		var rbs openchoreov1alpha1.ReleaseBindingList
		if err := r.List(ctx, &rbs, client.InNamespace(ep.Namespace),
			client.MatchingFields{controller.IndexKeyReleaseBindingOwnerComponentName: ref.ComponentName}); err != nil {
			log.FromContext(ctx).Error(err, "Failed to list ReleaseBindings for Endpoint backend",
				"endpoint", ep.Name, "component", ref.ComponentName)
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
// changes to status.latestRelease or backendRefs: the inputs that decide
// whether an exposed Endpoint can be bound.
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
