// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointreleasebinding

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
)

const (
	// EndpointReleaseBindingFinalizer ensures the owned RenderedRelease — and
	// with it the routing resources on the data plane — is removed before the
	// binding goes away.
	EndpointReleaseBindingFinalizer = "openchoreo.dev/endpointreleasebinding-cleanup"

	// requeueWaitForChild is how long we wait between reconciles while the
	// owned RenderedRelease is being torn down.
	requeueWaitForChild = 5 * time.Second

	// requeueWaitForClaimRelease is how often a deleting binding re-checks
	// whether the component routes it handed back are live.
	requeueWaitForClaimRelease = 3 * time.Second

	// claimReleaseTimeout bounds the make-before-break wait, measured from the
	// start of deletion, so a binding can always be deleted.
	claimReleaseTimeout = 2 * time.Minute
)

// releaseClaim hands the binding's routing slots back to the component routes
// before the binding's own route is deleted (make-before-break). It marks the
// claim as releasing, so component ReleaseBindings render their own routes
// again, then waits until those are live. It never blocks deletion for longer
// than claimReleaseTimeout after the deletion started. done reports whether the
// caller may go on to delete the RenderedRelease.
func (r *Reconciler) releaseClaim(
	ctx context.Context,
	old, binding *openchoreov1alpha1.EndpointReleaseBinding,
) (done bool, result ctrl.Result, err error) {
	serving := binding.Status.Serving
	if serving == nil {
		return true, ctrl.Result{}, nil
	}

	if !serving.Releasing {
		serving.Releasing = true
		controller.MarkTrueCondition(binding, ConditionFinalizing, ReasonReleasingClaims,
			"Handing traffic back to component routes before removing this binding's route")
		if err := r.Status().Update(ctx, binding); err != nil {
			return false, ctrl.Result{}, fmt.Errorf("mark claim releasing: %w", err)
		}
		return false, ctrl.Result{RequeueAfter: requeueWaitForClaimRelease}, nil
	}

	if time.Since(binding.DeletionTimestamp.Time) > claimReleaseTimeout {
		log.FromContext(ctx).Info("Timed out waiting for component routes; removing the Endpoint route anyway",
			"binding", binding.Name, "timeout", claimReleaseTimeout)
		if controller.MarkTrueCondition(binding, ConditionFinalizing, ReasonReleaseTimedOut,
			fmt.Sprintf("Component routes were not live within %s; removing the Endpoint route anyway", claimReleaseTimeout)) {
			if err := controller.UpdateStatusConditions(ctx, r.Client, old, binding); err != nil {
				return false, ctrl.Result{}, err
			}
		}
		return true, ctrl.Result{}, nil
	}

	for _, slot := range serving.URLs {
		live, waitingFor, err := r.componentRoutesLive(ctx, binding, slot, serving.Visibilities)
		if err != nil {
			return false, ctrl.Result{}, err
		}
		if !live {
			if controller.MarkTrueCondition(binding, ConditionFinalizing, ReasonReleasingClaims,
				fmt.Sprintf("Waiting before removing this binding's route: %s", waitingFor)) {
				if err := controller.UpdateStatusConditions(ctx, r.Client, old, binding); err != nil {
					return false, ctrl.Result{}, err
				}
			}
			return false, ctrl.Result{RequeueAfter: requeueWaitForClaimRelease}, nil
		}
	}
	return true, ctrl.Result{}, nil
}

// ensureFinalizer adds the cleanup finalizer when missing. The first return
// value indicates whether the finalizer was just added.
func (r *Reconciler) ensureFinalizer(ctx context.Context, binding *openchoreov1alpha1.EndpointReleaseBinding) (bool, error) {
	if !binding.DeletionTimestamp.IsZero() {
		return false, nil
	}
	if controllerutil.AddFinalizer(binding, EndpointReleaseBindingFinalizer) {
		return true, r.Update(ctx, binding)
	}
	return false, nil
}

// finalize handles the deletion path:
//
//  1. Set the Finalizing condition the first time deletion is observed.
//  2. Delete the owned RenderedRelease and requeue while it still exists.
//  3. Once it is gone, remove the finalizer.
func (r *Reconciler) finalize(ctx context.Context, old, binding *openchoreov1alpha1.EndpointReleaseBinding) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("binding", binding.Name)

	if !controllerutil.ContainsFinalizer(binding, EndpointReleaseBindingFinalizer) {
		logger.Info("Binding is being deleted but endpointreleasebinding-cleanup finalizer is not present; skipping cleanup")
		return ctrl.Result{}, nil
	}

	// Set Finalizing once. Later steps refine its reason (ReleasingClaims, …), so
	// this must not reset it on every pass.
	if cond := meta.FindStatusCondition(binding.Status.Conditions, string(ConditionFinalizing)); cond == nil || cond.Status != metav1.ConditionTrue {
		meta.SetStatusCondition(&binding.Status.Conditions, NewFinalizingCondition(binding.Generation))
		return controller.UpdateStatusConditionsAndReturn(ctx, r.Client, old, binding)
	}

	if done, result, err := r.releaseClaim(ctx, old, binding); !done || err != nil {
		return result, err
	}

	rrName := makeRenderedReleaseName(binding)
	rr := &openchoreov1alpha1.RenderedRelease{}
	err := r.Get(ctx, types.NamespacedName{Name: rrName, Namespace: binding.Namespace}, rr)
	switch {
	case err == nil:
		if rr.DeletionTimestamp.IsZero() {
			if delErr := r.Delete(ctx, rr); delErr != nil && !apierrors.IsNotFound(delErr) {
				return ctrl.Result{}, fmt.Errorf("delete RenderedRelease %q: %w", rrName, delErr)
			}
			logger.Info("Cascaded RenderedRelease delete", "renderedRelease", rrName)
		}
		return ctrl.Result{RequeueAfter: requeueWaitForChild}, nil
	case apierrors.IsNotFound(err):
		// fall through: clear finalizer
	default:
		return ctrl.Result{}, fmt.Errorf("get RenderedRelease %q: %w", rrName, err)
	}

	if controllerutil.RemoveFinalizer(binding, EndpointReleaseBindingFinalizer) {
		if err := r.Update(ctx, binding); err != nil {
			return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
		}
	}
	return ctrl.Result{}, nil
}
