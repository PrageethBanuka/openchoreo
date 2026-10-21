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
// rehydrates EndpointTypeSpec, BackendRefs-derived Backends and Parameters
// from the immutable EndpointRelease snapshot and supplies EnvironmentConfigs
// from the binding.
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

	// Backends are the resolved traffic backends for this environment, exposed
	// to CEL as ${backends}. Required to be non-empty.
	Backends []Backend

	// Address is the Endpoint's own address in this environment, exposed to
	// CEL as ${address}. Required.
	Address AddressContext

	// TakeOver is the component URL the Endpoint also serves, exposed to CEL
	// as ${takeOver}. Nil when the Endpoint takes nothing over, or when the
	// component is not deployed in this environment.
	TakeOver *URLContext

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
// deployed in the binding's environment, or an external service.
type Backend struct {
	// Name identifies the backend within the Endpoint.
	Name string `json:"name"`

	// Kind is Component or External.
	Kind string `json:"kind"`

	// ComponentName is the backend Component, for a component backend.
	ComponentName string `json:"componentName,omitempty"`

	// Endpoint is the workload endpoint name on the Component, for a component
	// backend.
	Endpoint string `json:"endpoint,omitempty"`

	// External is the service an external backend calls in this environment.
	External *ExternalBackend `json:"external,omitempty"`

	// Protocol is the workload endpoint protocol (HTTP, gRPC, ...).
	Protocol string `json:"protocol"`

	// ServiceName and Namespace locate the backend's Kubernetes Service on the
	// data plane. HTTPRoute backendRefs use {name: serviceName, port: port}.
	ServiceName string `json:"serviceName"`
	Namespace   string `json:"namespace"`
	Port        int32  `json:"port"`

	// Weight is the relative traffic weight from the Endpoint's backendRef.
	Weight int32 `json:"weight"`

	// ServiceURL is the in-cluster URL of the backend, e.g.
	// http://payments-v1.dp-default-shop-dev.svc.cluster.local:8080.
	ServiceURL string `json:"serviceURL"`

	// BasePath is the workload endpoint's base path on the backend ("/" when
	// unset). Templates rewrite inherited path prefixes to it.
	BasePath string `json:"basePath"`

	// ComponentURLs are the addresses the backend component serves today
	// through its own ComponentType routes. When the Endpoint owns this
	// endpoint, those routes are not rendered, so an EndpointType serves these
	// addresses itself to keep clients of the old URLs working.
	ComponentURLs ComponentURLs `json:"componentURLs"`
}

// AddressContext is the Endpoint's own address in one environment.
type AddressContext struct {
	// Host is the hostname clients call: the Endpoint's own hostname, or the
	// environment's external gateway host.
	Host string `json:"host"`

	// Path is the path prefix clients call.
	Path string `json:"path"`

	// OwnHostname is true when Host is the Endpoint's own hostname rather than
	// the environment's gateway host.
	OwnHostname bool `json:"ownHostname"`
}

// URLContext is a component URL an Endpoint takes over: the address that
// component serves today for that workload endpoint, per visibility. A
// visibility the component does not expose is omitted.
type URLContext struct {
	// ComponentName and Endpoint name the workload endpoint whose URL is served.
	ComponentName string `json:"componentName"`
	Endpoint      string `json:"endpoint"`

	// BasePath is that workload endpoint's base path ("/" when unset).
	// Templates rewrite the served path prefix to it.
	BasePath string `json:"basePath"`

	// ServiceName, Namespace and Port locate the component's in-cluster
	// Service for that workload endpoint, and ServiceURL is its full URL: the
	// address in-cluster callers already use. Service-addressed (mesh) types
	// key their routes on it.
	ServiceName string `json:"serviceName,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	Port        int32  `json:"port,omitempty"`
	ServiceURL  string `json:"serviceURL,omitempty"`

	External *Address `json:"external,omitempty"`
	Internal *Address `json:"internal,omitempty"`
}

// ExternalBackend is where an external backend sends traffic.
type ExternalBackend struct {
	// Scheme is http or https.
	Scheme string `json:"scheme"`
	// Host is the service's hostname; templates also send it as the Host header.
	Host string `json:"host"`
	// Port is the service's port, defaulted from the scheme.
	Port int32 `json:"port"`
	// URL is the full URL as configured.
	URL string `json:"url"`
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
	Backends           []Backend          `json:"backends"`
	Address            AddressContext     `json:"address"`
	TakeOver           *URLContext        `json:"takeOver"`
	DataPlane          DataPlaneContext   `json:"dataplane"`
	Environment        EnvironmentContext `json:"environment"`
	// Gateway is the effective gateway exposed at the top level for terseness;
	// omitted when no gateway is configured, so guard with has(environment.gateway).
	Gateway *GatewayData `json:"gateway,omitempty"`
}
