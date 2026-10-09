// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// EndpointReleaseBindingSpec defines the desired state of EndpointReleaseBinding.
// Pins an EndpointRelease to an Environment and carries per-env overrides.
type EndpointReleaseBindingSpec struct {
	// Owner identifies the endpoint and project this EndpointReleaseBinding belongs to.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.owner is immutable"
	Owner EndpointReleaseBindingOwner `json:"owner"`

	// Environment is the name of the Environment this binding targets.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.environment is immutable"
	Environment string `json:"environment"`

	// EndpointRelease is the name of the EndpointRelease pinned by this binding.
	// Unset before the first EndpointRelease is cut; the controller leaves the
	// binding pending until set.
	// +optional
	EndpointRelease string `json:"endpointRelease,omitempty"`

	// EndpointTypeEnvironmentConfigs provides per-environment values for the schema
	// declared on the referenced EndpointType (or ClusterEndpointType). Validated
	// against EndpointType.spec.environmentConfigs by the binding controller;
	// failures surface via status.conditions.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	EndpointTypeEnvironmentConfigs *runtime.RawExtension `json:"endpointTypeEnvironmentConfigs,omitempty"`
}

// EndpointReleaseBindingOwner identifies the endpoint this EndpointReleaseBinding belongs to.
// Mirrors EndpointReleaseOwner for namespace-disambiguation: a namespace can host
// multiple projects, so a bare endpoint name is ambiguous.
type EndpointReleaseBindingOwner struct {
	// ProjectName is the name of the project that owns this endpoint.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ProjectName string `json:"projectName"`

	// EndpointName is the name of the Endpoint.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	EndpointName string `json:"endpointName"`
}

// EndpointReleaseBindingStatus defines the observed state of EndpointReleaseBinding.
type EndpointReleaseBindingStatus struct {
	// Conditions represent the latest available observations of the binding's state.
	// Includes BackendsResolved and Ready (aggregate). observedGeneration is set
	// per-condition (project convention).
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Backends holds the resolution result of each backendRef for this binding's
	// environment. A backend whose component is not deployed in the environment is
	// reported with resolved=false rather than silently receiving its share of traffic.
	// +optional
	// +listType=map
	// +listMapKey=componentName
	// +listMapKey=endpoint
	Backends []ResolvedEndpointBackend `json:"backends,omitempty"`

	// InvokeURL is the primary resolved URL for this endpoint in the environment,
	// derived from the rendered routing resources.
	// +optional
	InvokeURL string `json:"invokeURL,omitempty"`

	// InternalURLs holds the resolved internal gateway URLs.
	// +optional
	InternalURLs *EndpointGatewayURLs `json:"internalURLs,omitempty"`

	// ExternalURLs holds the resolved external gateway URLs.
	// +optional
	ExternalURLs *EndpointGatewayURLs `json:"externalURLs,omitempty"`

	// Serving records the routing last confirmed accepted on the data plane. It
	// is this binding's claim on its backends' workload endpoints: component
	// ReleaseBindings stop rendering their own gateway routes for the claimed
	// (component, endpoint, visibility) slots. It changes only when a newly
	// rendered route is accepted, so it holds steady while a later render is
	// still progressing or its backends are unresolved.
	// +optional
	Serving *EndpointServingClaim `json:"serving,omitempty"`
}

// EndpointServingClaim is the routing an EndpointReleaseBinding is serving in
// its environment, and therefore the routing slots it claims.
type EndpointServingClaim struct {
	// EndpointRelease is the release whose rendered routes were accepted.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	EndpointRelease string `json:"endpointRelease"`

	// Backends are the component workload endpoints receiving traffic (non-zero
	// weight) through the accepted routes.
	// +optional
	// +listType=map
	// +listMapKey=componentName
	// +listMapKey=endpoint
	Backends []EndpointBackendSlot `json:"backends,omitempty"`

	// Visibilities are the gateway visibilities the accepted routes serve, from
	// their openchoreo.dev/endpoint-visibility label (external by default).
	// Component routes of other visibilities are left in place.
	// +optional
	// +listType=set
	Visibilities []EndpointVisibility `json:"visibilities,omitempty"`

	// Since is when this claim was first established.
	// +kubebuilder:validation:Required
	Since metav1.Time `json:"since"`

	// Releasing is set while the binding is being deleted. Component
	// ReleaseBindings resume rendering their own routes; this binding keeps its
	// route until they are live again, so traffic is never left without a route.
	// +optional
	Releasing bool `json:"releasing,omitempty"`
}

// EndpointBackendSlot identifies a component workload endpoint.
type EndpointBackendSlot struct {
	// ComponentName is the name of the backend Component.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ComponentName string `json:"componentName"`

	// Endpoint is the name of the workload endpoint on the Component.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`
}

// ResolvedEndpointBackend is the resolution of a single EndpointBackendRef in an
// environment, populated by the binding controller.
type ResolvedEndpointBackend struct {
	// ComponentName is the name of the backend Component.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ComponentName string `json:"componentName"`

	// Endpoint is the name of the workload endpoint on the backend Component.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`

	// Weight is the effective weight applied to this backend.
	// +optional
	Weight int32 `json:"weight,omitempty"`

	// ServiceURL is the in-cluster service URL of the backend in this environment.
	// +optional
	ServiceURL *EndpointURL `json:"serviceURL,omitempty"`

	// Resolved indicates whether the backend was found deployed in this environment.
	Resolved bool `json:"resolved"`

	// Reason explains why the backend could not be resolved.
	// +optional
	Reason string `json:"reason,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=eprb;eprbs
// +kubebuilder:printcolumn:name="Project",type=string,JSONPath=`.spec.owner.projectName`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.spec.owner.endpointName`
// +kubebuilder:printcolumn:name="Environment",type=string,JSONPath=`.spec.environment`
// +kubebuilder:printcolumn:name="Release",type=string,JSONPath=`.spec.endpointRelease`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// EndpointReleaseBinding is the Schema for the endpointreleasebindings API.
// Pins an EndpointRelease to an Environment, resolves its backends to the
// environment's component services, and renders the EndpointType's routing
// resources.
type EndpointReleaseBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EndpointReleaseBindingSpec   `json:"spec,omitempty"`
	Status EndpointReleaseBindingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// EndpointReleaseBindingList contains a list of EndpointReleaseBinding.
type EndpointReleaseBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EndpointReleaseBinding `json:"items"`
}

// GetConditions returns the conditions from the status.
func (e *EndpointReleaseBinding) GetConditions() []metav1.Condition {
	return e.Status.Conditions
}

// SetConditions sets the conditions in the status.
func (e *EndpointReleaseBinding) SetConditions(conditions []metav1.Condition) {
	e.Status.Conditions = conditions
}

func init() {
	SchemeBuilder.Register(&EndpointReleaseBinding{}, &EndpointReleaseBindingList{})
}
