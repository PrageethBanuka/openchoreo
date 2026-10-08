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
