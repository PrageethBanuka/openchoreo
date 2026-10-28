// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointpipeline

import (
	"time"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
	projectpipeline "github.com/openchoreo/openchoreo/internal/pipeline/project"
	"github.com/openchoreo/openchoreo/internal/template"
)

// Pipeline renders the inlined (Cluster)EndpointType.spec.resources templates
// for a single EndpointReleaseBinding. The instance holds a template.Engine
// whose CEL environment and program caches are reused across calls.
type Pipeline struct {
	templateEngine *template.Engine

	// celCostLimit bounds the accumulated cost of a single CEL expression.
	// Zero selects the template engine's built-in default.
	celCostLimit uint64

	// renderTimeout bounds the wall-clock duration of each Render call. Zero or
	// negative means no deadline.
	renderTimeout time.Duration
}

// DataPlaneContext is the dataplane surface exposed to CEL as ${dataplane.*}.
// Shared with the project pipeline so both template families see the same shape.
type DataPlaneContext = projectpipeline.DataPlaneContext

// EnvironmentContext is the per-environment surface exposed to CEL as
// ${environment.*}, carrying the env-or-dataplane merged gateway.
type EnvironmentContext = projectpipeline.EnvironmentContext

// GatewayData provides gateway configuration in templates.
type GatewayData = projectpipeline.GatewayData

// BuildDataPlaneContext extracts the dataplane CEL surface from a DataPlane.
var BuildDataPlaneContext = projectpipeline.BuildDataPlaneContext

// BuildEnvironmentContext extracts the per-environment CEL surface, merging
// environment-level gateway overrides with dataplane-level fallbacks.
var BuildEnvironmentContext = projectpipeline.BuildEnvironmentContext

// RenderInput carries everything Render needs. The binding controller
// rehydrates EndpointTypeSpec, BackendRefs-derived Backends, Rules and
// Parameters from the immutable EndpointRelease snapshot and supplies
// EnvironmentConfigs from the binding.
type RenderInput struct {
	// EndpointTypeSpec is the inlined (Cluster)EndpointType spec snapshot from
	// the EndpointRelease. Required.
	EndpointTypeSpec *v1alpha1.EndpointTypeSpec

	// Parameters is the snapshot of Endpoint.spec.parameters, exposed to CEL as
	// ${parameters.*} with EndpointTypeSpec.Parameters schema defaults applied.
	Parameters *runtime.RawExtension

	// EnvironmentConfigs is EndpointReleaseBinding.spec.endpointTypeEnvironmentConfigs,
	// exposed to CEL as ${environmentConfigs.*} with schema defaults applied.
	EnvironmentConfigs *runtime.RawExtension

	// Visibility is the Endpoint's declared spec.visibility, exposed to CEL as
	// ${visibility}: the gateway scopes to render routes for.
	Visibility []string

	// Backends are the resolved traffic backends for this environment, exposed
	// to CEL as ${backends}. Required to be non-empty.
	Backends []Backend

	// Rules are the Endpoint's rules with their backends resolved, exposed to
	// CEL as ${rules}. The list always ends with a catch-all rule: the
	// Endpoint's own, or the default split of Backends when it has none, so a
	// template renders one route rule per entry. Required to be non-empty.
	Rules []Rule

	// Metadata is the controller-computed metadata surface exposed to CEL as
	// ${metadata.*}.
	Metadata MetadataContext

	// DataPlane is exposed to CEL as ${dataplane.*}.
	DataPlane DataPlaneContext

	// Environment is exposed to CEL as ${environment.*}; its gateway is also
	// aliased at the top level as ${gateway.*}.
	Environment EnvironmentContext
}

// Backend is one resolved traffic backend: a workload endpoint of a component
// deployed in the binding's environment, and the Service the Endpoint renders
// for it.
type Backend struct {
	// Name identifies the backend within the Endpoint.
	Name string `json:"name"`

	// Kind is the backend kind (Component).
	Kind string `json:"kind"`

	// ComponentName is the backend Component.
	ComponentName string `json:"componentName,omitempty"`

	// Endpoint is the workload endpoint name on the Component.
	Endpoint string `json:"endpoint,omitempty"`

	// Protocol is the workload endpoint protocol (HTTP, gRPC, ...).
	Protocol string `json:"protocol"`

	// PodSelector selects the backend's pods in this environment: the labels
	// the component's workload carries. Templates use it to render the
	// backend's Service.
	PodSelector map[string]string `json:"podSelector"`

	// TargetPort is the container port of the workload endpoint.
	TargetPort int32 `json:"targetPort"`

	// ServiceName and Namespace name the Service the Endpoint renders for this
	// backend on the data plane, and Port is its port. Routes use
	// {name: serviceName, port: port}.
	ServiceName string `json:"serviceName"`
	Namespace   string `json:"namespace"`
	Port        int32  `json:"port"`

	// Weight is the backend's relative weight in this environment: the
	// Endpoint's, or the binding's override.
	Weight int32 `json:"weight"`

	// ServiceURL is the in-cluster URL of the backend's Service, e.g.
	// http://trips-api-go-1a2b3c4d.dp-default-rides-dev.svc.cluster.local:8080.
	ServiceURL string `json:"serviceURL"`

	// BasePath is the workload endpoint's base path on the backend ("/" when
	// unset). Templates rewrite inherited path prefixes to it.
	BasePath string `json:"basePath"`

	// ComponentURLs are the addresses the backend component serves today
	// through its own ComponentType routes. When the Endpoint claims this
	// endpoint, those routes are not rendered, so an EndpointType may serve
	// these addresses itself to keep clients of the old URLs working.
	ComponentURLs ComponentURLs `json:"componentURLs"`
}

// Rule is one of the Endpoint's rules with its backends resolved.
type Rule struct {
	// Name identifies the rule.
	Name string `json:"name"`

	// Matches select the requests the rule applies to; empty for the catch-all.
	Matches []Match `json:"matches"`

	// Backends are the rule's backends, each with the rule's weight for it
	// (after the binding's overrides). Backends at weight 0 are kept, so a
	// template can filter them.
	Backends []Backend `json:"backends"`

	// Mirrors copy a share of the rule's requests to a backend.
	Mirrors []Mirror `json:"mirrors"`
}

// Match is one set of conditions a request must all meet. Lists are never
// nil, so templates can use them without has().
type Match struct {
	// Path is the path condition, relative to where the Endpoint is served;
	// nil when the match has none. Templates join it to the prefix they serve
	// the Endpoint at.
	Path *PathMatch `json:"path,omitempty"`

	// Conditions are the match's header conditions ready to render on any
	// routing API: header names lowercased, and the cookie as a regular
	// expression on the cookie header with its name and value escaped.
	// Templates should render these rather than Headers and Cookies, so
	// every mesh matches the same requests.
	Conditions []HeaderMatch `json:"conditions"`

	// Headers are the match's header conditions, names lowercased.
	Headers []HeaderMatch `json:"headers"`
	// Cookies are the match's cookie conditions as written; see Conditions.
	Cookies []CookieMatch `json:"cookies"`
}

// PathMatch is a path condition; Type is PathPrefix or Exact.
type PathMatch struct {
	Type  string `json:"type"`
	Value string `json:"value"`

	// Regex is an RE2 pattern for the whole path that matches the same
	// requests, for routing APIs whose prefix match is a plain string prefix
	// (Istio, Envoy) rather than path segments as in Gateway API.
	Regex string `json:"regex"`
}

// HeaderMatch is a header condition; Type is Exact or RegularExpression.
type HeaderMatch struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// CookieMatch is an exact cookie condition.
type CookieMatch struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Mirror copies Percent of a rule's requests to Backend.
type Mirror struct {
	Backend Backend `json:"backend"`
	Percent int32   `json:"percent"`
}

// ComponentURLs are a backend component's own gateway addresses, per
// visibility. A visibility the component does not expose is omitted.
type ComponentURLs struct {
	External *Address `json:"external,omitempty"`
	Internal *Address `json:"internal,omitempty"`
}

// Address is one gateway address: the hostname and path prefix clients use.
type Address struct {
	Scheme string `json:"scheme"`
	Host   string `json:"host"`
	Port   int32  `json:"port"`
	Path   string `json:"path"`
}

// RenderOutput carries the rendered manifests in spec order.
type RenderOutput struct {
	Entries []RenderedEntry
}

// RenderedEntry is one rendered manifest. For non-forEach templates ID equals
// the source ResourceTemplate.ID; forEach iterations are suffixed "-<index>".
type RenderedEntry struct {
	ID     string
	Object map[string]any
}

// MetadataContext is the platform-injected metadata surface exposed to CEL
// templates as ${metadata.*}.
type MetadataContext struct {
	// Name is a platform-computed, collision-safe base name for the rendered
	// routing objects of this (endpoint, environment).
	Name string `json:"name"`

	// Namespace is the dp-{ns}-{project}-{env} data-plane namespace, shared with
	// the backend components so routes can reference their Services directly.
	Namespace string `json:"namespace"`

	// EndpointNamespace is the control-plane namespace of the Endpoint.
	EndpointNamespace string `json:"endpointNamespace"`

	EndpointName    string `json:"endpointName"`
	EndpointUID     string `json:"endpointUID"`
	ProjectName     string `json:"projectName"`
	ProjectUID      string `json:"projectUID"`
	EnvironmentName string `json:"environmentName"`
	EnvironmentUID  string `json:"environmentUID"`
	DataPlaneName   string `json:"dataPlaneName"`
	DataPlaneUID    string `json:"dataPlaneUID"`

	// Labels are platform-injected standard labels.
	Labels map[string]string `json:"labels"`

	// Annotations are propagated from the EndpointReleaseBinding CR.
	Annotations map[string]string `json:"annotations"`
}

// BaseContext is the top-level CEL surface fed into the template engine.
type BaseContext struct {
	Metadata           MetadataContext    `json:"metadata"`
	Parameters         map[string]any     `json:"parameters"`
	EnvironmentConfigs map[string]any     `json:"environmentConfigs"`
	Visibility         []string           `json:"visibility"`
	Backends           []Backend          `json:"backends"`
	Rules              []Rule             `json:"rules"`
	DataPlane          DataPlaneContext   `json:"dataplane"`
	Environment        EnvironmentContext `json:"environment"`
	// Gateway is the effective gateway exposed at the top level for terseness;
	// omitted when no gateway is configured, so guard with has(environment.gateway).
	Gateway *GatewayData `json:"gateway,omitempty"`
}
