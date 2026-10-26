// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// EndpointSpec defines the desired state of Endpoint.
// +kubebuilder:validation:XValidation:rule="!has(self.rules) || self.rules.all(r, r.backends.all(b, self.backendRefs.exists(x, x.name == b.name)))",message="every rule backend must name a backend in spec.backendRefs"
// +kubebuilder:validation:XValidation:rule="!has(self.rules) || self.rules.all(r, !has(r.mirrors) || r.mirrors.all(m, self.backendRefs.exists(x, x.name == m.backend)))",message="every mirror must name a backend in spec.backendRefs"
// +kubebuilder:validation:XValidation:rule="!has(self.rules) || self.rules.all(r, (has(r.matches) && size(r.matches) > 0) || r.name == self.rules[size(self.rules) - 1].name)",message="only the last rule may have no matches; it is the catch-all"
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

	// Visibility lists the scopes this Endpoint exposes its backends to. It is
	// declared rather than inferred from rendered routes, so an EndpointType can
	// render one route per requested visibility and the set of route slots this
	// Endpoint occupies is known before anything is rendered. The project scope
	// is implicit for every workload endpoint and is not a route an Endpoint
	// renders, so it is not accepted here.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	// +kubebuilder:validation:XValidation:rule="self.all(v, v != 'project')",message="project visibility is implicit for every workload endpoint and cannot be requested on an Endpoint"
	Visibility []EndpointVisibility `json:"visibility"`

	// AutoDeploy deploys the Endpoint to the first environment of its project's
	// deployment pipeline: the Endpoint controller creates the EndpointReleaseBinding
	// for that environment and advances it to each new EndpointRelease. Later
	// environments are promoted explicitly, as with Component.spec.autoDeploy.
	// +optional
	AutoDeploy bool `json:"autoDeploy,omitempty"`

	// BackendRefs lists the backends this Endpoint routes traffic to, with
	// relative weights. Like a Service selecting Pods, the Endpoint does not own
	// the referenced components.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=name
	BackendRefs []EndpointBackendRef `json:"backendRefs"`

	// Rules route matching requests to chosen backends, ahead of the default
	// split. They are evaluated in order and the first match wins; a rule with
	// no matches is the catch-all and must come last. When unset, the Endpoint
	// sends all traffic across backendRefs by weight.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	Rules []EndpointRule `json:"rules,omitempty"`

	// Schema is the API definition this Endpoint exposes. When unset it resolves
	// from the backend's Workload endpoint schema at render time rather than
	// being copied here, so a new build cannot leave the Endpoint holding a stale
	// contract. Set it explicitly when backends disagree or the exposed surface
	// differs from the backend's.
	// +optional
	Schema *EndpointSchema `json:"schema,omitempty"`

	// Parameters contains values for the parameter schema declared on the referenced
	// EndpointType (or ClusterEndpointType). Validated against that schema at
	// admission and at render time.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	Parameters *runtime.RawExtension `json:"parameters,omitempty"`
}

// EndpointOwner identifies the project that owns an Endpoint.
type EndpointOwner struct {
	// ProjectName is the name of the Project this Endpoint belongs to.
	// +kubebuilder:validation:MinLength=1
	ProjectName string `json:"projectName"`
}

// EndpointSchema is the API definition an Endpoint exposes.
type EndpointSchema struct {
	// Type is the kind of definition, for example OpenAPI or GraphQL.
	// +optional
	Type string `json:"type,omitempty"`
	// Content is the definition itself.
	// +optional
	Content string `json:"content,omitempty"`
}

// EndpointComponentRef names a workload endpoint of a component in the
// Endpoint's project.
type EndpointComponentRef struct {
	// Name is the name of the Component that serves this backend, in the
	// Endpoint's project.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Endpoint is the name of the endpoint in the component's
	// Workload.spec.endpoints to route to. Determines the backend port and protocol.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`
}

// EndpointBackendKind is the kind of an Endpoint backend.
// +kubebuilder:validation:Enum=Component
type EndpointBackendKind string

const (
	// EndpointBackendKindComponent is a component workload endpoint.
	EndpointBackendKindComponent EndpointBackendKind = "Component"
)

// EndpointBackendRef is one traffic backend of an Endpoint. It is a
// discriminated union on kind; exactly the payload matching kind must be set.
// +kubebuilder:validation:XValidation:rule="self.kind != 'Component' || has(self.component)",message="component must be set when kind is Component"
type EndpointBackendRef struct {
	// Name identifies this backend within the Endpoint. It is the list's map key,
	// the handle status reports resolution against, and what rules, overrides and
	// templates use to single out one backend.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Kind is the kind of backend this entry references.
	// +optional
	// +kubebuilder:default=Component
	Kind EndpointBackendKind `json:"kind,omitempty"`

	// Component references a workload endpoint of a Component in the Endpoint's
	// project. Required when kind is Component.
	// +optional
	Component *EndpointComponentRef `json:"component,omitempty"`

	// Weight is the proportion of traffic sent to this backend relative to the
	// sum of all backend weights. Follows Gateway API backendRef weight semantics:
	// 0 sends no traffic to this backend.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000000
	Weight *int32 `json:"weight,omitempty"`
}

// EndpointRule sends requests that match it to chosen backends.
type EndpointRule struct {
	// Name identifies the rule, so automation and GitOps can own it.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Matches select the requests this rule applies to: a request matches when
	// any entry matches, and an entry matches when all of its conditions hold.
	// A rule without matches applies to every request; only the last rule may
	// omit them.
	// +optional
	// +kubebuilder:validation:MaxItems=8
	Matches []EndpointMatch `json:"matches,omitempty"`

	// Backends are the backends matching requests go to, by name, with relative
	// weights.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=name
	Backends []EndpointRuleBackend `json:"backends"`

	// Mirrors copy a share of this rule's requests to other backends and
	// discard their responses.
	// +optional
	// +kubebuilder:validation:MaxItems=4
	Mirrors []EndpointMirror `json:"mirrors,omitempty"`
}

// EndpointMatch is one set of conditions a request must all meet.
type EndpointMatch struct {
	// Headers the request must carry.
	// +optional
	// +kubebuilder:validation:MaxItems=8
	Headers []EndpointHeaderMatch `json:"headers,omitempty"`

	// Cookies the request must carry, by exact value. EndpointTypes render a
	// cookie as a match on the Cookie header.
	// +optional
	// +kubebuilder:validation:MaxItems=1
	Cookies []EndpointCookieMatch `json:"cookies,omitempty"`

	// QueryParams the request URL must carry, by exact value.
	// +optional
	// +kubebuilder:validation:MaxItems=8
	QueryParams []EndpointQueryParamMatch `json:"queryParams,omitempty"`
}

// EndpointHeaderMatchType is how a header value is compared.
// +kubebuilder:validation:Enum=Exact;RegularExpression
type EndpointHeaderMatchType string

const (
	// EndpointHeaderMatchExact compares the header value exactly.
	EndpointHeaderMatchExact EndpointHeaderMatchType = "Exact"
	// EndpointHeaderMatchRegularExpression matches the value against a regular expression.
	EndpointHeaderMatchRegularExpression EndpointHeaderMatchType = "RegularExpression"
)

// EndpointHeaderMatch is a header condition.
type EndpointHeaderMatch struct {
	// Name is the header name, case-insensitive.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9!#$%&'*+\-.^_\x60|~]+$`
	Name string `json:"name"`

	// Type is how the value is compared.
	// +optional
	// +kubebuilder:default=Exact
	Type EndpointHeaderMatchType `json:"type,omitempty"`

	// Value is the expected value, or the regular expression.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=4096
	Value string `json:"value"`
}

// EndpointCookieMatch is a cookie condition.
type EndpointCookieMatch struct {
	// Name is the cookie name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_-]{1,64}$`
	Name string `json:"name"`

	// Value is the exact cookie value. Kept to safe characters, because types
	// embed it in a regular expression on the Cookie header.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_.-]{1,128}$`
	Value string `json:"value"`
}

// EndpointQueryParamMatch is a query parameter condition.
type EndpointQueryParamMatch struct {
	// Name is the query parameter name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Name string `json:"name"`

	// Value is the exact expected value.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=1024
	Value string `json:"value"`
}

// EndpointRuleBackend is a backend a rule sends traffic to.
type EndpointRuleBackend struct {
	// Name is a backend in spec.backendRefs.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Weight is this backend's share of the rule's traffic, relative to the
	// rule's other backends; 0 sends none.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000000
	Weight *int32 `json:"weight,omitempty"`
}

// EndpointMirror copies a share of a rule's requests to a backend.
type EndpointMirror struct {
	// Backend is a backend in spec.backendRefs, usually one at weight 0.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Backend string `json:"backend"`

	// Percent of the rule's requests to copy.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Percent int32 `json:"percent"`
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
// +kubebuilder:printcolumn:name="Release",type=string,JSONPath=`.status.latestRelease.name`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Endpoint is the Schema for the endpoints API.
// An Endpoint routes traffic across one or more backends using weights and
// rules, rendered through an EndpointType or ClusterEndpointType. It is
// concerned with traffic routing only; the compute side (ports, network
// policies) stays on Workload.spec.endpoints.
// The plural "endpoints" and the short name "ep" are taken by core v1 Endpoints
// in kubectl, so this kind has no short name; use "endpoints.openchoreo.dev".
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
