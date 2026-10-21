// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// EndpointSpec defines the desired state of Endpoint.
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.takeOver) || !has(self.takeOver) || self.takeOver == oldSelf.takeOver",message="spec.takeOver can be added or removed but not changed"
// +kubebuilder:validation:XValidation:rule="has(self.address) == has(oldSelf.address)",message="spec.address cannot be added or removed after creation; create a new Endpoint to serve another address"
type EndpointSpec struct {
	// Owner identifies the Project this Endpoint belongs to.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.owner is immutable"
	Owner EndpointOwner `json:"owner"`

	// Type references the EndpointType or ClusterEndpointType template for this Endpoint.
	// Kind defaults to EndpointType (namespaced); ClusterEndpointType is also allowed.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.type cannot be changed after creation"
	Type EndpointTypeRef `json:"type"`

	// Address is the URL this Endpoint serves: a path, on the environment's
	// gateway host or on its own hostname. It belongs to the Endpoint, so it
	// stays the same whatever backends answer it, and it is fixed at creation.
	// Required when the EndpointType's addressKind is Gateway (the default);
	// Service types serve the taken-over component's in-cluster Service
	// instead and must leave it unset.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.address cannot be changed after creation; create a new Endpoint to serve another address"
	Address *EndpointAddress `json:"address,omitempty"`

	// TakeOver optionally makes the Endpoint also serve one component workload
	// endpoint's existing URL, so clients of that URL move to the Endpoint
	// without a gap. The component stops rendering its own route for it once
	// the Endpoint's route is accepted, and renders it again when the take-over
	// is removed. It can be added or removed, but not changed.
	// +optional
	TakeOver *EndpointTakeOver `json:"takeOver,omitempty"`

	// BackendRefs lists what answers the Endpoint's traffic, with relative
	// weights: component workload endpoints of the project, or external
	// services. Like a Service selecting Pods, the Endpoint does not own them.
	// An Endpoint may be left with no backends; see ReclaimPolicy.
	// +optional
	// +listType=map
	// +listMapKey=name
	BackendRefs []EndpointBackendRef `json:"backendRefs,omitempty"`

	// ReclaimPolicy decides what happens when the Endpoint has no backends left,
	// for example after its last backend component was deleted. Delete removes
	// the Endpoint and hands its URL back; Retain keeps it as Orphaned, without
	// routes, until a backend is added.
	// +optional
	// +kubebuilder:default=Delete
	ReclaimPolicy EndpointReclaimPolicy `json:"reclaimPolicy,omitempty"`

	// Parameters contains values for the parameter schema declared on the referenced
	// EndpointType (or ClusterEndpointType). Validated against the schema by the
	// Endpoint controller; failures surface via status.conditions.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	Parameters *runtime.RawExtension `json:"parameters,omitempty"`
}

// EndpointReclaimPolicy decides what happens to an Endpoint with no backends.
// +kubebuilder:validation:Enum=Delete;Retain
type EndpointReclaimPolicy string

const (
	// EndpointReclaimPolicyDelete deletes an Endpoint once it has no backends.
	EndpointReclaimPolicyDelete EndpointReclaimPolicy = "Delete"
	// EndpointReclaimPolicyRetain keeps an Endpoint with no backends as Orphaned.
	EndpointReclaimPolicyRetain EndpointReclaimPolicy = "Retain"
)

// EndpointOwner identifies the project that owns an Endpoint.
type EndpointOwner struct {
	// ProjectName is the name of the Project this Endpoint belongs to.
	// +kubebuilder:validation:MinLength=1
	ProjectName string `json:"projectName"`
}

// EndpointAddress is the URL an Endpoint serves.
type EndpointAddress struct {
	// Path is the path prefix clients call, for example /payments.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=200
	// +kubebuilder:validation:Pattern=`^/[a-z0-9]([-a-z0-9/._]*[a-z0-9])?$`
	Path string `json:"path"`

	// Hostname is the Endpoint's own hostname, for example payments.example.com.
	// It must match the EndpointType's allowedHostnames, and each environment's
	// binding can override it. When unset, the environment's gateway host is
	// used, the same host component URLs use.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+$`
	Hostname string `json:"hostname,omitempty"`
}

// EndpointTakeOver names the component workload endpoint whose existing URL
// an Endpoint also serves.
type EndpointTakeOver struct {
	// Component is the workload endpoint whose URL is taken over.
	// +kubebuilder:validation:Required
	Component EndpointComponentRef `json:"component"`
}

// EndpointComponentRef names a workload endpoint of a component in the
// Endpoint's project.
type EndpointComponentRef struct {
	// Name is the name of the Component.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Endpoint is the name of the endpoint in the component's
	// Workload.spec.endpoints. Determines the port and protocol.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`
}

// EndpointExternalRef is a service outside OpenChoreo.
type EndpointExternalRef struct {
	// URL is the service's base URL, for example https://legacy.example.com.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://[a-zA-Z0-9]([-a-zA-Z0-9.]*[a-zA-Z0-9])?(:[0-9]{1,5})?(/.*)?$`
	URL string `json:"url"`
}

// EndpointBackendKind is the kind of an Endpoint backend.
// +kubebuilder:validation:Enum=Component;External
type EndpointBackendKind string

const (
	// EndpointBackendKindComponent is a component workload endpoint.
	EndpointBackendKindComponent EndpointBackendKind = "Component"
	// EndpointBackendKindExternal is a service outside OpenChoreo.
	EndpointBackendKindExternal EndpointBackendKind = "External"
)

// EndpointBackendRef is one backend of an Endpoint: a component workload
// endpoint or an external service, with its relative weight.
// +kubebuilder:validation:XValidation:rule="has(self.component) != has(self.external)",message="a backend sets exactly one of component or external"
type EndpointBackendRef struct {
	// Name identifies the backend within the Endpoint, for example payments-v1
	// or legacy. Per-environment overrides refer to it.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Component is a workload endpoint of a component in the Endpoint's project.
	// +optional
	Component *EndpointComponentRef `json:"component,omitempty"`

	// External is a service outside OpenChoreo. Its URL is the default for every
	// environment; a binding can override it.
	// +optional
	External *EndpointExternalRef `json:"external,omitempty"`

	// Weight is the proportion of traffic sent to this backend relative to the
	// sum of all backend weights. Follows Gateway API backendRef weight semantics:
	// 0 sends no traffic to this backend.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000000
	Weight *int32 `json:"weight,omitempty"`
}

// Kind returns whether the backend is a component or an external service.
func (b EndpointBackendRef) Kind() EndpointBackendKind {
	if b.External != nil {
		return EndpointBackendKindExternal
	}
	return EndpointBackendKindComponent
}

// EndpointStatus defines the observed state of Endpoint.
type EndpointStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the Endpoint's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LatestRelease is the most recent EndpointRelease for this Endpoint.
	// +optional
	LatestRelease *LatestEndpointRelease `json:"latestRelease,omitempty"`
}

// LatestEndpointRelease identifies the most recent EndpointRelease for an Endpoint.
type LatestEndpointRelease struct {
	// Name of the EndpointRelease resource.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Hash is the content hash of Endpoint.spec + EndpointType/ClusterEndpointType.spec
	// captured at release time.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Hash string `json:"hash"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Project",type=string,JSONPath=`.spec.owner.projectName`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type.name`
// +kubebuilder:printcolumn:name="Path",type=string,JSONPath=`.spec.address.path`
// +kubebuilder:printcolumn:name="Hostname",type=string,JSONPath=`.spec.address.hostname`,priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Endpoint is the Schema for the endpoints API.
// An Endpoint routes traffic across one or more component endpoints using
// weights, rendered through an EndpointType or ClusterEndpointType. It is
// concerned with L7 traffic routing only; the compute side (ports, network
// policies) stays on Workload.spec.endpoints.
// The plural "endpoints" shadows core v1 Endpoints in kubectl; use the
// fully-qualified "endpoints.openchoreo.dev".
type Endpoint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EndpointSpec   `json:"spec,omitempty"`
	Status EndpointStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// EndpointList contains a list of Endpoint.
type EndpointList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Endpoint `json:"items"`
}

func (e *Endpoint) GetConditions() []metav1.Condition {
	return e.Status.Conditions
}

func (e *Endpoint) SetConditions(conditions []metav1.Condition) {
	e.Status.Conditions = conditions
}

func init() {
	SchemeBuilder.Register(&Endpoint{}, &EndpointList{})
}
