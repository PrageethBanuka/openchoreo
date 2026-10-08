// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
)

const (
	// EndpointFinalizer ensures owned EndpointReleaseBindings and EndpointReleases
	// are cleaned up before the Endpoint is removed.
	EndpointFinalizer = "openchoreo.dev/endpoint-cleanup"

	// requeueWaitForChildren is how long we wait between reconciles while owned
	// children (bindings or releases) are still being torn down. Matches the
	// Resource controller's 5s convention.
	requeueWaitForChildren = 5 * time.Second
)

// ensureFinalizer adds the endpoint-cleanup finalizer when missing. The first
// return value indicates whether the finalizer was just added (caller should
// requeue to pick up the change).
func (r *Reconciler) ensureFinalizer(ctx context.Context, ep *openchoreov1alpha1.Endpoint) (bool, error) {
	if !ep.DeletionTimestamp.IsZero() {
		return false, nil
	}
	if controllerutil.AddFinalizer(ep, EndpointFinalizer) {
		return true, r.Update(ctx, ep)
	}
	return false, nil
}

// finalize handles the deletion path.
//
//  1. Set the Finalizing condition the first time we observe deletion so users
//     see "deletion in progress" via status.
//  2. Delete EndpointReleaseBindings of the Endpoint and wait for them to go.
//     Their own finalizers tear down the rendered routing resources.
//  3. Once bindings are gone, delete owned EndpointReleases and wait.
//  4. Once releases are gone, remove the finalizer and let K8s GC the Endpoint.
func (r *Reconciler) finalize(ctx context.Context, old, ep *openchoreov1alpha1.Endpoint) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("endpoint", ep.Name)

	if !controllerutil.ContainsFinalizer(ep, EndpointFinalizer) {
		logger.Info("Endpoint is being deleted but endpoint-cleanup finalizer is not present; skipping cleanup")
		return ctrl.Result{}, nil
	}

	cond := meta.FindStatusCondition(ep.Status.Conditions, string(ConditionFinalizing))
	if cond == nil || cond.Status != metav1.ConditionTrue {
		if meta.SetStatusCondition(&ep.Status.Conditions, NewFinalizingCondition(ep.Generation)) {
			return controller.UpdateStatusConditionsAndReturn(ctx, r.Client, old, ep)
		}
	}

	hasBindings, err := r.deleteOwnedEndpointReleaseBindings(ctx, ep)
	if err != nil {
		return ctrl.Result{}, err
	}
	if hasBindings {
		return r.waitForChildren(ctx, old, ep, "Waiting for EndpointReleaseBindings to be deleted")
	}

	hasReleases, err := r.deleteOwnedEndpointReleases(ctx, ep)
	if err != nil {
		return ctrl.Result{}, err
	}
	if hasReleases {
		return r.waitForChildren(ctx, old, ep, "Waiting for EndpointReleases to be deleted")
	}

	if controllerutil.RemoveFinalizer(ep, EndpointFinalizer) {
		if err := r.Update(ctx, ep); err != nil {
			return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
		}
	}

	return ctrl.Result{}, nil
}

// waitForChildren records msg on the Finalizing condition and requeues.
func (r *Reconciler) waitForChildren(ctx context.Context, old, ep *openchoreov1alpha1.Endpoint, msg string) (ctrl.Result, error) {
	log.FromContext(ctx).Info(msg, "endpoint", ep.Name)
	controller.MarkTrueCondition(ep, ConditionFinalizing, ReasonFinalizing, msg)
	if !equality.Semantic.DeepEqual(old.Status.Conditions, ep.Status.Conditions) {
		if err := controller.UpdateStatusConditions(ctx, r.Client, old, ep); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: requeueWaitForChildren}, nil
}

// deleteOwnedEndpointReleaseBindings triggers deletion of every
// EndpointReleaseBinding of the given Endpoint. Returns true if any bindings
// still exist; the caller should requeue to wait for them to be deleted.
func (r *Reconciler) deleteOwnedEndpointReleaseBindings(ctx context.Context, ep *openchoreov1alpha1.Endpoint) (bool, error) {
	bindings := &openchoreov1alpha1.EndpointReleaseBindingList{}
	if err := r.List(ctx, bindings,
		client.InNamespace(ep.Namespace),
		client.MatchingFields{controller.IndexKeyEndpointReleaseBindingOwnerEndpointName: ep.Name}); err != nil {
		return false, fmt.Errorf("list EndpointReleaseBindings: %w", err)
	}
	found := false
	for i := range bindings.Items {
		binding := &bindings.Items[i]
		// A namespace can host several projects; only touch this Endpoint's bindings.
		if binding.Spec.Owner.ProjectName != ep.Spec.Owner.ProjectName {
			continue
		}
		found = true
		if !binding.DeletionTimestamp.IsZero() {
			continue
		}
		if err := client.IgnoreNotFound(r.Delete(ctx, binding)); err != nil {
			return false, fmt.Errorf("delete EndpointReleaseBinding %s: %w", binding.Name, err)
		}
	}
	return found, nil
}

// deleteOwnedEndpointReleases triggers deletion of every EndpointRelease owned
// by the given Endpoint. Returns true if any still exist; the caller should
// requeue to wait for the API server to remove them.
func (r *Reconciler) deleteOwnedEndpointReleases(ctx context.Context, ep *openchoreov1alpha1.Endpoint) (bool, error) {
	releases := &openchoreov1alpha1.EndpointReleaseList{}
	if err := r.List(ctx, releases,
		client.InNamespace(ep.Namespace),
		client.MatchingFields{controller.IndexKeyEndpointReleaseOwnerEndpointName: ep.Name}); err != nil {
		return false, fmt.Errorf("list EndpointReleases: %w", err)
	}
	found := false
	for i := range releases.Items {
		release := &releases.Items[i]
		if release.Spec.Owner.ProjectName != ep.Spec.Owner.ProjectName {
			continue
		}
		found = true
		if err := client.IgnoreNotFound(r.Delete(ctx, release)); err != nil {
			return false, fmt.Errorf("delete EndpointRelease %s: %w", release.Name, err)
		}
	}
	return found, nil
}
