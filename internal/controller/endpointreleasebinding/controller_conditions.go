// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointreleasebinding

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openchoreo/openchoreo/internal/controller"
)

// Constants for condition types

const (
	// ConditionBackendsResolved indicates every backendRef with a non-zero
	// weight resolved to a deployed workload endpoint in this environment.
	ConditionBackendsResolved controller.ConditionType = "BackendsResolved"

	// ConditionSynced indicates the owned RenderedRelease has been
	// created/updated for this binding.
	ConditionSynced controller.ConditionType = "Synced"

	// ConditionRoutesReady indicates every rendered routing resource has been
	// applied to the data plane and reports a healthy status.
	ConditionRoutesReady controller.ConditionType = "RoutesReady"

	// ConditionReady aggregates BackendsResolved, Synced and RoutesReady.
	ConditionReady controller.ConditionType = "Ready"

	// ConditionFinalizing indicates the binding is being finalized (deleted).
	ConditionFinalizing controller.ConditionType = "Finalizing"
)

// Constants for condition reasons

const (
	// ReasonEndpointReleaseNotSet indicates the binding has no spec.endpointRelease pin yet.
	ReasonEndpointReleaseNotSet controller.ConditionReason = "EndpointReleaseNotSet"

	// ReasonEndpointReleaseNotFound indicates the pinned EndpointRelease does not exist.
	ReasonEndpointReleaseNotFound controller.ConditionReason = "EndpointReleaseNotFound"

	// ReasonInvalidReleaseConfiguration indicates the EndpointRelease snapshot
	// disagrees with the binding's owner.
	ReasonInvalidReleaseConfiguration controller.ConditionReason = "InvalidReleaseConfiguration"

	// ReasonEnvironmentNotFound indicates the referenced Environment does not exist.
	ReasonEnvironmentNotFound controller.ConditionReason = "EnvironmentNotFound"

	// ReasonDataPlaneNotFound indicates the Environment's dataPlaneRef does not
	// resolve to an existing DataPlane or ClusterDataPlane.
	ReasonDataPlaneNotFound controller.ConditionReason = "DataPlaneNotFound"

	// ReasonEndpointNotFound indicates the owning Endpoint does not exist.
	ReasonEndpointNotFound controller.ConditionReason = "EndpointNotFound"

	// ReasonProjectNotFound indicates the owning Project does not exist.
	ReasonProjectNotFound controller.ConditionReason = "ProjectNotFound"

	// ReasonBackendsResolved indicates every backend resolved.
	ReasonBackendsResolved controller.ConditionReason = "BackendsResolved"

	// ReasonBackendsUnresolved indicates one or more weighted backends are not
	// deployed in this environment. The previous RenderedRelease, if any, is
	// left in place so traffic is not shifted to a missing backend.
	ReasonBackendsUnresolved controller.ConditionReason = "BackendsUnresolved"

	// ReasonReleaseCreated indicates the RenderedRelease was created or updated.
	ReasonReleaseCreated controller.ConditionReason = "ReleaseCreated"

	// ReasonReleaseSynced indicates the RenderedRelease is up to date.
	ReasonReleaseSynced controller.ConditionReason = "ReleaseSynced"

	// ReasonRenderingFailed indicates the pipeline failed to render the
	// snapshot's templates.
	ReasonRenderingFailed controller.ConditionReason = "RenderingFailed"

	// ReasonReleaseOwnershipConflict indicates a RenderedRelease already exists
	// at the target name but is owned by something else.
	ReasonReleaseOwnershipConflict controller.ConditionReason = "ReleaseOwnershipConflict"

	// ReasonReleaseUpdateFailed indicates a transient failure creating or
	// updating the RenderedRelease.
	ReasonReleaseUpdateFailed controller.ConditionReason = "ReleaseUpdateFailed"

	// ReasonRoutesReady indicates every rendered resource is applied and healthy.
	ReasonRoutesReady controller.ConditionReason = "RoutesReady"

	// ReasonRoutesProgressing indicates one or more rendered resources have no
	// observed healthy status yet.
	ReasonRoutesProgressing controller.ConditionReason = "RoutesProgressing"

	// ReasonRoutesDegraded indicates one or more rendered resources reported
	// HealthStatus=Degraded on the data plane, e.g. the gateway rejected a route
	// or could not resolve one of its backend references.
	ReasonRoutesDegraded controller.ConditionReason = "RoutesDegraded"

	// ReasonResourceApplyFailed indicates the RenderedRelease reported
	// ResourcesApplied=False for its current generation.
	ReasonResourceApplyFailed controller.ConditionReason = "ResourceApplyFailed"

	// ReasonReady indicates the binding is fully ready.
	ReasonReady controller.ConditionReason = "Ready"

	// ReasonFinalizing indicates the binding is being finalized.
	ReasonFinalizing controller.ConditionReason = "Finalizing"
)

// NewFinalizingCondition returns a Finalizing=True condition observed at the
// given generation, used while the binding is being torn down.
func NewFinalizingCondition(generation int64) metav1.Condition {
	return controller.NewCondition(
		ConditionFinalizing,
		metav1.ConditionTrue,
		ReasonFinalizing,
		"EndpointReleaseBinding is finalizing",
		generation,
	)
}
