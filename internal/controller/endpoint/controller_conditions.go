// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openchoreo/openchoreo/internal/controller"
)

// Constants for condition types

const (
	// ConditionReady indicates the Endpoint has been successfully reconciled.
	// Failure modes are encoded in the condition's Reason; this mirrors the
	// minimal Resource condition set (`internal/controller/resource/controller_conditions.go`).
	ConditionReady controller.ConditionType = "Ready"

	// ConditionFinalizing indicates the Endpoint is being finalized (deleted).
	ConditionFinalizing controller.ConditionType = "Finalizing"

	// ConditionOrphaned is True while the Endpoint has no backends left. What
	// happens next is decided by spec.reclaimPolicy.
	ConditionOrphaned controller.ConditionType = "Orphaned"
)

// Constants for condition reasons

const (
	// ReasonReconciled indicates the Endpoint has been validated and the
	// latest EndpointRelease is in place.
	ReasonReconciled controller.ConditionReason = "Reconciled"

	// ReasonEndpointTypeNotFound indicates the referenced EndpointType or
	// ClusterEndpointType does not exist in the cluster yet.
	ReasonEndpointTypeNotFound controller.ConditionReason = "EndpointTypeNotFound"

	// ReasonBackendNotFound indicates a backendRef names a Component that does
	// not exist in the Endpoint's project.
	ReasonBackendNotFound controller.ConditionReason = "BackendNotFound"

	// ReasonAddressInUse indicates another, older Endpoint already serves this
	// Endpoint's address, so this one routes nothing.
	ReasonAddressInUse controller.ConditionReason = "AddressInUse"

	// ReasonTakeOverInUse indicates another, older Endpoint already takes over
	// the same component URL, so this one routes nothing.
	ReasonTakeOverInUse controller.ConditionReason = "TakeOverInUse"

	// ReasonNoWeightedBackend indicates every backend has weight 0.
	ReasonNoWeightedBackend controller.ConditionReason = "NoWeightedBackend"

	// ReasonNoBackends indicates the Endpoint has no backendRefs left.
	ReasonNoBackends controller.ConditionReason = "NoBackends"

	// ReasonFinalizing indicates the Endpoint is being finalized.
	ReasonFinalizing controller.ConditionReason = "Finalizing"
)

// NewFinalizingCondition returns a Finalizing=True condition observed at the
// given generation, used while the Endpoint is being torn down.
func NewFinalizingCondition(generation int64) metav1.Condition {
	return controller.NewCondition(
		ConditionFinalizing,
		metav1.ConditionTrue,
		ReasonFinalizing,
		"Endpoint is finalizing",
		generation,
	)
}
