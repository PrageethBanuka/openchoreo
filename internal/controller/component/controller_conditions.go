// Copyright 2025 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package component

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openchoreo/openchoreo/internal/controller"
)

// Constants for condition types

const (
	// ConditionReady indicates that the Component has been successfully reconciled.
	// When autoDeploy is enabled, this means ComponentRelease and ReleaseBinding are created/updated.
	// When autoDeploy is disabled, this means the Component has been validated.
	ConditionReady controller.ConditionType = "Ready"

	// ConditionFinalizing indicates that the Component is being finalized (deleted).
	ConditionFinalizing controller.ConditionType = "Finalizing"

	// ConditionEndpointsExposed reports whether the Workload endpoints that name an
	// EndpointType are served through their Endpoints. Absent when none do.
	ConditionEndpointsExposed controller.ConditionType = "EndpointsExposed"
)

// Constants for condition reasons

const (
	// Success states (Status=True)

	// ReasonReconciled indicates the Component has been successfully validated
	// Used when autoDeploy is disabled - only validation is performed
	ReasonReconciled controller.ConditionReason = "Reconciled"

	// ReasonComponentReleaseReady indicates ComponentRelease and ReleaseBinding are successfully created/updated
	// Used when autoDeploy is enabled
	ReasonComponentReleaseReady controller.ConditionReason = "ComponentReleaseReady"

	// Configuration issues (Status=False)

	// ReasonWorkloadNotFound indicates the referenced Workload doesn't exist
	ReasonWorkloadNotFound controller.ConditionReason = "WorkloadNotFound"
	// ReasonComponentTypeNotFound indicates the referenced ComponentType doesn't exist
	ReasonComponentTypeNotFound controller.ConditionReason = "ComponentTypeNotFound"
	// ReasonTraitNotFound indicates one or more referenced Traits don't exist
	ReasonTraitNotFound controller.ConditionReason = "TraitNotFound"
	// ReasonProjectNotFound indicates the referenced Project doesn't exist
	ReasonProjectNotFound controller.ConditionReason = "ProjectNotFound"
	// ReasonDeploymentPipelineNotFound indicates the deployment pipeline is not found
	ReasonDeploymentPipelineNotFound controller.ConditionReason = "DeploymentPipelineNotFound"
	// ReasonInvalidConfiguration indicates the Component configuration is invalid
	ReasonInvalidConfiguration controller.ConditionReason = "InvalidConfiguration"
	// ReasonWorkflowNotAllowed indicates the referenced Workflow is not in allowedWorkflows
	ReasonWorkflowNotAllowed controller.ConditionReason = "WorkflowNotAllowed"
	// ReasonWorkflowNotFound indicates the referenced Workflow doesn't exist
	ReasonWorkflowNotFound controller.ConditionReason = "WorkflowNotFound"

	// AutoDeploy issues (Status=False)

	// ReasonAutoDeployFailed indicates failure to handle autoDeploy (ComponentRelease/ReleaseBinding creation)
	ReasonAutoDeployFailed controller.ConditionReason = "AutoDeployFailed"

	// ReasonFinalizing indicates the Component is being finalized
	ReasonFinalizing controller.ConditionReason = "Finalizing"

	// ReasonStillRouted indicates deletion is held because an Endpoint still sends
	// traffic to the Component in some environment.
	ReasonStillRouted controller.ConditionReason = "StillRouted"

	// Endpoint exposure (EndpointsExposed condition)

	// ReasonEndpointsExposed indicates every exposed workload endpoint has its Endpoint
	ReasonEndpointsExposed controller.ConditionReason = "EndpointsExposed"
	// ReasonExposeConflict indicates the named Endpoint exists but doesn't route to this component
	ReasonExposeConflict controller.ConditionReason = "ExposeConflict"
	// ReasonEndpointTypeMismatch indicates the Workload names a different type than the Endpoint has
	ReasonEndpointTypeMismatch controller.ConditionReason = "TypeMismatch"
	// ReasonEndpointTypeNotFound indicates the named EndpointType or ClusterEndpointType doesn't exist
	ReasonEndpointTypeNotFound controller.ConditionReason = "EndpointTypeNotFound"
)

// NewComponentStillRoutedCondition creates a condition indicating deletion is
// held until no Endpoint sends traffic to the Component.
func NewComponentStillRoutedCondition(generation int64, message string) metav1.Condition {
	return controller.NewCondition(
		ConditionFinalizing,
		metav1.ConditionTrue,
		ReasonStillRouted,
		message,
		generation,
	)
}

// NewComponentFinalizingCondition creates a condition indicating the Component is being finalized.
func NewComponentFinalizingCondition(generation int64) metav1.Condition {
	return controller.NewCondition(
		ConditionFinalizing,
		metav1.ConditionTrue,
		ReasonFinalizing,
		"Component is finalizing",
		generation,
	)
}
