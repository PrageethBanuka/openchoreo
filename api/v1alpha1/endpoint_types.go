// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// EndpointSpec defines the desired state of Endpoint.
// +kubebuilder:validation:XValidation:rule="!has(self.rollout) || self.backendRefs.exists(b, b.name == self.rollout.stable)",message="rollout.stable must name a backend in backendRefs"
// +kubebuilder:validation:XValidation:rule="!has(self.rollout) || self.rollout.candidates.all(c, self.backendRefs.exists(b, b.name == c.name))",message="every rollout candidate must name a backend in backendRefs"
// +kubebuilder:validation:XValidation:rule="!has(self.rollout) || self.rollout.candidates.all(c, self.backendRefs.all(b, b.name != c.name || has(b.mirror) == (self.rollout.strategy == 'mirror')))",message="a mirror rollout needs a mirror on every candidate, and only a mirror rollout can have a candidate with a mirror"
// +kubebuilder:validation:XValidation:rule="!has(self.rollout) || self.rollout.strategy != 'abTest' || self.rollout.candidates.all(c, self.backendRefs.all(b, b.name != c.name || (has(b.match) && has(b.match.cookies))))",message="an abTest rollout needs a cookie match on every candidate"
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
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	// +kubebuilder:validation:XValidation:rule="self.all(v, v != 'project')",message="project visibility is implicit for every workload endpoint and cannot be requested on an Endpoint"
	Visibility []EndpointVisibility `json:"visibility"`

	// BackendRefs lists the backends this Endpoint routes traffic to, with
	// relative weights. Like a Service selecting Pods, the Endpoint does not own
	// the referenced components.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=name
	BackendRefs []EndpointBackendRef `json:"backendRefs"`

	// Schema is the API definition this Endpoint exposes. When unset it resolves
	// from the backend's Workload endpoint schema at render time rather than
	// being copied here, so a new build cannot leave the Endpoint holding a stale
	// contract. Set it explicitly when backends disagree or the exposed surface
	// differs from the backend's.
	// +optional
	Schema *Schema `json:"schema,omitempty"`

	// Parameters contains values for the parameter schema declared on the referenced
	// EndpointType (or ClusterEndpointType). Validated against the schema by the
	// Endpoint controller; failures surface via status.conditions.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	Parameters *runtime.RawExtension `json:"parameters,omitempty"`

	// Rollout is the plan for moving traffic from one backend to others. The
	// portal, occ and openchoreo-api read it to turn each step into the weights
	// on an environment's EndpointReleaseBinding. The controller doesn't read
	// it, and it isn't part of a release, so editing the plan never cuts one.
	// +optional
	Rollout *EndpointRollout `json:"rollout,omitempty"`
}

// EndpointRolloutStrategy is how a rollout moves traffic.
// +kubebuilder:validation:Enum=canary;blueGreen;abTest;mirror
type EndpointRolloutStrategy string

const (
	// EndpointRolloutCanary shifts a growing share of traffic to the candidates.
	EndpointRolloutCanary EndpointRolloutStrategy = "canary"
	// EndpointRolloutBlueGreen previews one candidate by its match, then switches all traffic to it.
	EndpointRolloutBlueGreen EndpointRolloutStrategy = "blueGreen"
	// EndpointRolloutABTest sends each candidate the requests that carry its cookie.
	EndpointRolloutABTest EndpointRolloutStrategy = "abTest"
	// EndpointRolloutMirror copies a growing share of requests to the candidates.
	EndpointRolloutMirror EndpointRolloutStrategy = "mirror"
)

// EndpointRollout is the plan for rolling out candidate backends in place of
// a stable one.
// +structType=atomic
// +kubebuilder:validation:XValidation:rule="!self.candidates.exists(c, c.name == self.stable)",message="a rollout candidate can't also be stable"
// +kubebuilder:validation:XValidation:rule="has(self.canary) == (self.strategy == 'canary')",message="set canary for a canary rollout only"
// +kubebuilder:validation:XValidation:rule="has(self.mirror) == (self.strategy == 'mirror')",message="set mirror for a mirror rollout only"
// +kubebuilder:validation:XValidation:rule="(!has(self.canary) || self.canary.steps.all(s, s.backends.all(b, self.candidates.exists(c, c.name == b.name)))) && (!has(self.mirror) || self.mirror.steps.all(s, s.backends.all(b, self.candidates.exists(c, c.name == b.name))))",message="a rollout step can only name candidates"
type EndpointRollout struct {
	// Strategy is how traffic moves. It selects which of canary and mirror is set.
	// +kubebuilder:validation:Required
	Strategy EndpointRolloutStrategy `json:"strategy"`

	// Stable is the backend serving now. It keeps the rest of its share at
	// every step, and aborting the rollout returns its traffic to it.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Stable string `json:"stable"`

	// Candidates is the backend being rolled out. It is a list so a rollout
	// can later take several, such as an A/B test with more than two groups.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=1
	// +listType=map
	// +listMapKey=name
	Candidates []EndpointRolloutCandidate `json:"candidates"`

	// Canary lists the steps of a canary rollout.
	// +optional
	Canary *EndpointRolloutSteps `json:"canary,omitempty"`

	// Mirror lists the steps of a mirror rollout.
	// +optional
	Mirror *EndpointRolloutSteps `json:"mirror,omitempty"`
}

// EndpointRolloutCandidate is a backend being rolled out.
type EndpointRolloutCandidate struct {
	// Name of the backend in backendRefs.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
}

// EndpointRolloutSteps is the ordered list of steps of a rollout.
type EndpointRolloutSteps struct {
	// Steps in the order they are taken.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	Steps []EndpointRolloutStep `json:"steps"`
}

// EndpointRolloutStep is the share each candidate gets at one step.
// +kubebuilder:validation:XValidation:rule="self.backends.map(b, b.percent).sum() <= 100",message="a rollout step's shares add up to at most 100"
type EndpointRolloutStep struct {
	// Backends lists each candidate's share at this step.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	// +listType=map
	// +listMapKey=name
	Backends []EndpointRolloutStepBackend `json:"backends"`
}

// EndpointRolloutStepBackend is one candidate's share at a step.
type EndpointRolloutStepBackend struct {
	// Name of the candidate.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// Percent is the candidate's share of the traffic stable had when the
	// rollout started, from 0 to 100; for a mirror rollout, the share of
	// requests copied to it. Backends outside the rollout keep their weights.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Percent int32 `json:"percent"`
}

// EndpointOwner identifies the project that owns an Endpoint.
type EndpointOwner struct {
	// ProjectName is the name of the Project this Endpoint belongs to.
	// +kubebuilder:validation:MinLength=1
	ProjectName string `json:"projectName"`
}

// EndpointBackendKind identifies what an EndpointBackendRef points at.
// +kubebuilder:validation:Enum=Component
type EndpointBackendKind string

const (
	// EndpointBackendKindComponent references a workload endpoint of a Component.
	EndpointBackendKindComponent EndpointBackendKind = "Component"
)

// EndpointBackendRef is one traffic backend of an Endpoint. It is a
// discriminated union on kind; exactly the payload matching kind must be set.
// +kubebuilder:validation:XValidation:rule="self.kind != 'Component' || has(self.component)",message="component must be set when kind is Component"
// +kubebuilder:validation:XValidation:rule="!has(self.mirror) || (has(self.weight) && self.weight == 0 && !has(self.match))",message="a backend with mirror only receives copies; set its weight to 0 and remove match"
// +kubebuilder:validation:XValidation:rule="!has(self.match) || self.name != 'default'",message="default names the rule that splits traffic by weight; give a backend with a match another name"
type EndpointBackendRef struct {
	// Name identifies this backend within the Endpoint. It is the list's map key,
	// the handle status reports resolution against, and what a template uses to
	// single out one backend.
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
	Component *EndpointComponentBackend `json:"component,omitempty"`

	// Weight is the proportion of traffic sent to this backend relative to the
	// sum of all backend weights. Follows Gateway API backendRef weight semantics:
	// 0 sends no traffic to this backend.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000000
	Weight *int32 `json:"weight,omitempty"`

	// Match sends every request that meets all of its conditions to this
	// backend, whatever its weight. Other requests are split by weight, so a
	// backend at weight 0 with a match receives only the requests it matches:
	// testers, a preview path, or an A/B cookie group. When several backends
	// match a request, the first one in the list gets it.
	// +optional
	Match *EndpointMatch `json:"match,omitempty"`

	// Mirror sends this backend a copy of a share of the requests the Endpoint
	// splits by weight, and discards its responses. Only a backend at weight 0
	// without a match can be a mirror, so everything it receives is a copy.
	// +optional
	Mirror *EndpointMirror `json:"mirror,omitempty"`
}

// EndpointMirror copies a share of requests to a backend.
type EndpointMirror struct {
	// Percent of the requests to copy, from 0 to 100.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Percent int32 `json:"percent"`
}

// EndpointMatch is the set of conditions a request must all meet.
// +kubebuilder:validation:XValidation:rule="has(self.path) || has(self.headers) || has(self.cookies)",message="set a path, headers or cookies, or remove match"
type EndpointMatch struct {
	// Path the request must be for, relative to where the Endpoint is served:
	// a path of /next on an Endpoint served at /checkout matches /checkout/next.
	// +optional
	Path *EndpointPathMatch `json:"path,omitempty"`

	// Headers the request must carry.
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	Headers []EndpointHeaderMatch `json:"headers,omitempty"`

	// Cookies the request must carry, by exact value. EndpointTypes render a
	// cookie as a match on the Cookie header.
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=1
	Cookies []EndpointCookieMatch `json:"cookies,omitempty"`
}

// EndpointPathMatchType is how a request path is compared.
// +kubebuilder:validation:Enum=PathPrefix;Exact
type EndpointPathMatchType string

const (
	// EndpointPathMatchPathPrefix matches the path and every path below it.
	EndpointPathMatchPathPrefix EndpointPathMatchType = "PathPrefix"
	// EndpointPathMatchExact matches the path only.
	EndpointPathMatchExact EndpointPathMatchType = "Exact"
)

// EndpointPathMatch is a path condition.
type EndpointPathMatch struct {
	// Type is how the path is compared.
	// +optional
	// +kubebuilder:default=PathPrefix
	Type EndpointPathMatchType `json:"type,omitempty"`

	// Value is the path, relative to where the Endpoint is served.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:Pattern=`^/[^?#]*$`
	Value string `json:"value"`
}

// EndpointHeaderMatchType is how a header value is compared.
// +kubebuilder:validation:Enum=Exact;RegularExpression
type EndpointHeaderMatchType string

const (
	// EndpointHeaderMatchExact compares the header value exactly.
	EndpointHeaderMatchExact EndpointHeaderMatchType = "Exact"
	// EndpointHeaderMatchRegularExpression matches the value against an RE2 regular expression.
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

// EndpointComponentBackend references a named workload endpoint of a Component.
type EndpointComponentBackend struct {
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
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 63",message="metadata.name must be at most 63 characters"
// +kubebuilder:printcolumn:name="Project",type=string,JSONPath=`.spec.owner.projectName`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type.name`
// +kubebuilder:printcolumn:name="Strategy",type=string,JSONPath=`.spec.rollout.strategy`
// +kubebuilder:printcolumn:name="Release",type=string,JSONPath=`.status.latestRelease.name`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Endpoint is the Schema for the endpoints API.
// An Endpoint routes traffic across one or more backends using weights,
// rendered through an EndpointType or ClusterEndpointType. It is concerned with
// traffic routing only; the compute side (ports, network policies) stays on
// Workload.spec.endpoints.
// The plural "endpoints" and the short name "ep" are taken by core v1 Endpoints
// in kubectl, so this kind has no short name; use "endpoints.openchoreo.dev".
// The name is at most 63 characters because it is carried as a label value on
// what the Endpoint renders.
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
