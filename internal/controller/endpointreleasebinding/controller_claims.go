// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointreleasebinding

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
	"github.com/openchoreo/openchoreo/internal/controller/endpointurl"
	"github.com/openchoreo/openchoreo/internal/controller/renderedrelease"
	"github.com/openchoreo/openchoreo/internal/labels"
	endpointpipeline "github.com/openchoreo/openchoreo/internal/pipeline/endpoint"
)

// updateServingClaim records, as status.serving, the routing this binding
// serves once the data plane has accepted the current render: the component
// URL it takes over, if it rendered one, and the visibilities of the rendered
// routes. That component reads it to stop rendering its own routes for it; the
// Endpoint's backends keep their own URLs. Without a take-over the claim holds
// no URLs, so no component route is suppressed.
//
// The claim only changes when RoutesReady is True for this render, so it
// holds steady while a later render is progressing or its backends are
// unresolved: the previously accepted route is still the one serving traffic.
func updateServingClaim(
	binding *openchoreov1alpha1.EndpointReleaseBinding,
	release *openchoreov1alpha1.EndpointRelease,
	output *endpointpipeline.RenderOutput,
	tookOver bool,
) {
	if !meta.IsStatusConditionTrue(binding.Status.Conditions, string(ConditionRoutesReady)) {
		return
	}

	next := &openchoreov1alpha1.EndpointServingClaim{
		EndpointRelease: release.Name,
		Visibilities:    routeVisibilities(output),
	}
	if tookOver && release.Spec.TakeOver != nil {
		next.URLs = []openchoreov1alpha1.EndpointBackendSlot{{
			ComponentName: release.Spec.TakeOver.Component.Name,
			Endpoint:      release.Spec.TakeOver.Component.Endpoint,
		}}
	}

	if cur := binding.Status.Serving; cur != nil && !cur.Releasing {
		next.Since = cur.Since
		if sameClaim(cur, next) {
			return
		}
	} else {
		next.Since = metav1.Now()
	}
	binding.Status.Serving = next
}

// routeVisibilities returns the distinct visibilities of the rendered Gateway
// API routes, read from their openchoreo.dev/endpoint-visibility label and
// defaulting to external, sorted for a stable status.
func routeVisibilities(output *endpointpipeline.RenderOutput) []openchoreov1alpha1.EndpointVisibility {
	var visibilities []openchoreov1alpha1.EndpointVisibility
	for i := range output.Entries {
		obj := &unstructured.Unstructured{Object: output.Entries[i].Object}
		if endpointurl.RouteKind(obj) == "" {
			continue
		}
		v := openchoreov1alpha1.EndpointVisibility(obj.GetLabels()[labels.LabelKeyEndpointVisibility])
		if v == "" {
			v = openchoreov1alpha1.EndpointVisibilityExternal
		}
		if !slices.Contains(visibilities, v) {
			visibilities = append(visibilities, v)
		}
	}
	slices.Sort(visibilities)
	return visibilities
}

func sameClaim(a, b *openchoreov1alpha1.EndpointServingClaim) bool {
	return a.EndpointRelease == b.EndpointRelease &&
		slices.Equal(a.URLs, b.URLs) &&
		slices.Equal(a.Visibilities, b.Visibilities)
}

// componentRoutesLive reports whether a component's own gateway routes for
// endpointName are live again in the binding's environment, after this
// binding marked its claim as releasing. It is the "make" half of the
// make-before-break hand-back: the binding keeps its own route until this is
// true for every slot it claimed.
//
// Live means the component's ReleaseBinding no longer lists this Endpoint in
// routedBy (it re-rendered without suppressing the slot) and every route it
// now renders for the endpoint, in one of the claimed visibilities, is applied
// and Healthy on the data plane. A component that is not deployed in the
// environment, or whose ComponentType renders no route for the endpoint, has
// nothing to wait for.
func (r *Reconciler) componentRoutesLive(
	ctx context.Context,
	binding *openchoreov1alpha1.EndpointReleaseBinding,
	slot openchoreov1alpha1.EndpointBackendSlot,
	visibilities []openchoreov1alpha1.EndpointVisibility,
) (bool, string, error) {
	project := binding.Spec.Owner.ProjectName
	env := binding.Spec.Environment

	var rbList openchoreov1alpha1.ReleaseBindingList
	if err := r.List(ctx, &rbList, client.InNamespace(binding.Namespace),
		client.MatchingFields{controller.IndexKeyReleaseBindingOwnerEnv: controller.MakeReleaseBindingOwnerEnvKey(
			project, slot.ComponentName, env)}); err != nil {
		return false, "", fmt.Errorf("list ReleaseBindings for component %q: %w", slot.ComponentName, err)
	}
	if len(rbList.Items) != 1 || rbList.Items[0].Spec.State == openchoreov1alpha1.ReleaseStateUndeploy {
		return true, "", nil
	}
	rb := &rbList.Items[0]

	for _, ep := range rb.Status.Endpoints {
		if ep.Name != slot.Endpoint {
			continue
		}
		for _, owner := range ep.RoutedBy {
			if owner.EndpointName == binding.Spec.Owner.EndpointName {
				return false, fmt.Sprintf("component %q has not re-rendered its route for %q yet", slot.ComponentName, slot.Endpoint), nil
			}
		}
	}

	var rrList openchoreov1alpha1.RenderedReleaseList
	if err := r.List(ctx, &rrList, client.InNamespace(binding.Namespace), client.MatchingLabels{
		labels.LabelKeyProjectName:     project,
		labels.LabelKeyComponentName:   slot.ComponentName,
		labels.LabelKeyEnvironmentName: env,
	}); err != nil {
		return false, "", fmt.Errorf("list RenderedReleases for component %q: %w", slot.ComponentName, err)
	}

	for i := range rrList.Items {
		rr := &rrList.Items[i]
		if rr.Spec.TargetPlane != openchoreov1alpha1.TargetPlaneDataPlane {
			continue
		}
		ids := legacyRouteIDs(rr, slot.Endpoint, visibilities)
		if len(ids) == 0 {
			continue
		}
		applied := meta.FindStatusCondition(rr.Status.Conditions, renderedrelease.ConditionResourcesApplied)
		if applied == nil || applied.Status != metav1.ConditionTrue || applied.ObservedGeneration != rr.Generation {
			return false, fmt.Sprintf("component %q route for %q is not applied yet", slot.ComponentName, slot.Endpoint), nil
		}
		for _, id := range ids {
			if !resourceHealthy(rr, id) {
				return false, fmt.Sprintf("component %q route for %q is not healthy yet", slot.ComponentName, slot.Endpoint), nil
			}
		}
	}
	return true, "", nil
}

// legacyRouteIDs returns the IDs of the gateway routes in a component's
// RenderedRelease that serve endpointName in one of the given visibilities.
func legacyRouteIDs(rr *openchoreov1alpha1.RenderedRelease, endpointName string, visibilities []openchoreov1alpha1.EndpointVisibility) []string {
	var ids []string
	for i := range rr.Spec.Resources {
		res := &rr.Spec.Resources[i]
		if res.Object == nil || len(res.Object.Raw) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(res.Object.Raw, &m); err != nil {
			continue
		}
		obj := &unstructured.Unstructured{Object: m}
		if endpointurl.RouteKind(obj) == "" {
			continue
		}
		l := obj.GetLabels()
		if l[labels.LabelKeyEndpointName] != endpointName {
			continue
		}
		if !slices.Contains(visibilities, openchoreov1alpha1.EndpointVisibility(l[labels.LabelKeyEndpointVisibility])) {
			continue
		}
		ids = append(ids, res.ID)
	}
	return ids
}

func resourceHealthy(rr *openchoreov1alpha1.RenderedRelease, id string) bool {
	for i := range rr.Status.Resources {
		if rr.Status.Resources[i].ID == id {
			return rr.Status.Resources[i].HealthStatus == openchoreov1alpha1.HealthStatusHealthy
		}
	}
	return false
}
