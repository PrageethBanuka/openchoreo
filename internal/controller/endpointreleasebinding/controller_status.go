// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointreleasebinding

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/log"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
	"github.com/openchoreo/openchoreo/internal/controller/endpointurl"
	"github.com/openchoreo/openchoreo/internal/controller/renderedrelease"
	"github.com/openchoreo/openchoreo/internal/labels"
	endpointpipeline "github.com/openchoreo/openchoreo/internal/pipeline/endpoint"
)

// resolveURLs derives status.externalURLs, status.internalURLs and
// status.invokeURL from the rendered Gateway API routes. A route's visibility
// comes from its openchoreo.dev/endpoint-visibility label, defaulting to
// external; the first route per visibility wins. The URL scheme follows the
// backends' protocol.
func resolveURLs(
	ctx context.Context,
	binding *openchoreov1alpha1.EndpointReleaseBinding,
	in *reconcileInputs,
	output *endpointpipeline.RenderOutput,
	backends []endpointpipeline.Backend,
) {
	logger := log.FromContext(ctx)
	protocol := openchoreov1alpha1.EndpointProtocol(backends[0].Protocol)

	var external, internal *openchoreov1alpha1.EndpointGatewayURLs
	for i := range output.Entries {
		obj := &unstructured.Unstructured{Object: output.Entries[i].Object}
		kind := endpointurl.RouteKind(obj)
		if kind == "" || !endpointurl.RouteKindCompat[kind][protocol] {
			continue
		}
		// A mesh route serves in-cluster callers of the Endpoint's Service; it
		// has no gateway URL.
		if isMeshRoute(obj) {
			continue
		}

		visibility := openchoreov1alpha1.EndpointVisibility(obj.GetLabels()[labels.LabelKeyEndpointVisibility])
		if visibility == "" {
			visibility = openchoreov1alpha1.EndpointVisibilityExternal
		}

		urls := endpointurl.ResolveRouteURLs(obj, kind, protocol, visibility, in.environment, in.dataPlane)
		if urls == nil {
			logger.Info("No gateway URL for rendered route",
				"route", obj.GetName(), "kind", kind, "visibility", visibility)
			continue
		}
		if visibility == openchoreov1alpha1.EndpointVisibilityExternal {
			if external == nil {
				external = urls
			}
		} else if internal == nil {
			internal = urls
		}
	}

	binding.Status.ExternalURLs = external
	binding.Status.InternalURLs = internal
	binding.Status.InvokeURL = preferredURL(external)
	if binding.Status.InvokeURL == "" {
		binding.Status.InvokeURL = preferredURL(internal)
	}
}

// isMeshRoute reports whether every parentRef of a Gateway API route is a
// core Service, which makes it a GAMMA mesh route.
func isMeshRoute(obj *unstructured.Unstructured) bool {
	parents, found, err := unstructured.NestedSlice(obj.Object, "spec", "parentRefs")
	if err != nil || !found || len(parents) == 0 {
		return false
	}
	for _, p := range parents {
		ref, ok := p.(map[string]any)
		if !ok {
			return false
		}
		kind, _ := ref["kind"].(string)
		group, _ := ref["group"].(string)
		if kind != "Service" || (group != "" && group != "core") {
			return false
		}
	}
	return true
}

// preferredURL returns the most secure URL a client should call: https, then
// http, then tls.
func preferredURL(urls *openchoreov1alpha1.EndpointGatewayURLs) string {
	if urls == nil {
		return ""
	}
	for _, u := range []*openchoreov1alpha1.EndpointURL{urls.HTTPS, urls.HTTP, urls.TLS} {
		if s := endpointurl.String(u); s != "" {
			return s
		}
	}
	return ""
}

// evaluateRoutesReady computes RoutesReady from the RenderedRelease status. A
// current-generation ResourcesApplied=False overrides per-resource health;
// otherwise every rendered resource must report Healthy (or Suspended).
func evaluateRoutesReady(binding *openchoreov1alpha1.EndpointReleaseBinding, rr *openchoreov1alpha1.RenderedRelease) {
	if applyCond := meta.FindStatusCondition(rr.Status.Conditions, renderedrelease.ConditionResourcesApplied); applyCond != nil &&
		applyCond.Status == metav1.ConditionFalse &&
		applyCond.ObservedGeneration == rr.Generation {
		controller.MarkFalseCondition(binding, ConditionRoutesReady, ReasonResourceApplyFailed, applyCond.Message)
		return
	}

	// Per-resource health describes whatever the data plane last applied. Until
	// the RenderedRelease reports this generation applied, that is the previous
	// render, and must not count as this render being ready.
	if applyCond := meta.FindStatusCondition(rr.Status.Conditions, renderedrelease.ConditionResourcesApplied); applyCond == nil ||
		applyCond.Status != metav1.ConditionTrue ||
		applyCond.ObservedGeneration != rr.Generation {
		controller.MarkFalseCondition(binding, ConditionRoutesReady, ReasonRoutesProgressing,
			fmt.Sprintf("Waiting for the data plane to apply RenderedRelease generation %d", rr.Generation))
		return
	}

	statusByID := make(map[string]*openchoreov1alpha1.RenderedManifestStatus, len(rr.Status.Resources))
	for i := range rr.Status.Resources {
		statusByID[rr.Status.Resources[i].ID] = &rr.Status.Resources[i]
	}

	for i := range rr.Spec.Resources {
		id := rr.Spec.Resources[i].ID
		st, found := statusByID[id]
		if !found {
			controller.MarkFalseCondition(binding, ConditionRoutesReady, ReasonRoutesProgressing,
				fmt.Sprintf("Resource %q has no observed status yet", id))
			return
		}
		switch st.HealthStatus {
		case openchoreov1alpha1.HealthStatusHealthy, openchoreov1alpha1.HealthStatusSuspended:
		case openchoreov1alpha1.HealthStatusDegraded:
			controller.MarkFalseCondition(binding, ConditionRoutesReady, ReasonRoutesDegraded,
				fmt.Sprintf("Resource %q (%s) is degraded", id, st.Kind))
			return
		default:
			controller.MarkFalseCondition(binding, ConditionRoutesReady, ReasonRoutesProgressing,
				fmt.Sprintf("Resource %q (%s) is %s", id, st.Kind, st.HealthStatus))
			return
		}
	}

	controller.MarkTrueCondition(binding, ConditionRoutesReady, ReasonRoutesReady,
		fmt.Sprintf("All %d resource(s) ready", len(rr.Spec.Resources)))
}

// setReadyCondition aggregates BackendsResolved, Synced and RoutesReady into
// Ready. Ready=True only when all three are True; otherwise Ready=False
// inherits the first failing condition's reason and message. A condition that
// was never set (an earlier step returned) counts as not ready, but an explicit
// failure is always reported in preference to it.
func setReadyCondition(binding *openchoreov1alpha1.EndpointReleaseBinding) {
	subConditions := []controller.ConditionType{ConditionSynced, ConditionBackendsResolved, ConditionRoutesReady}

	for _, ct := range subConditions {
		if cond := meta.FindStatusCondition(binding.Status.Conditions, string(ct)); cond != nil && cond.Status != metav1.ConditionTrue {
			controller.MarkFalseCondition(binding, ConditionReady, controller.ConditionReason(cond.Reason), cond.Message)
			return
		}
	}
	for _, ct := range subConditions {
		if meta.FindStatusCondition(binding.Status.Conditions, string(ct)) == nil {
			controller.MarkFalseCondition(binding, ConditionReady, controller.ConditionReason(string(ct)+"Pending"),
				fmt.Sprintf("%s has not been evaluated yet", ct))
			return
		}
	}
	controller.MarkTrueCondition(binding, ConditionReady, ReasonReady, "Endpoint is routing traffic in this environment")
}
