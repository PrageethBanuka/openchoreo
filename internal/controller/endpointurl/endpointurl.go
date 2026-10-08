// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

// Package endpointurl resolves the URLs clients use to reach an endpoint from
// the Gateway API routes rendered for it. It is shared by the ReleaseBinding
// controller (routes rendered by a ComponentType) and the EndpointReleaseBinding
// controller (routes rendered by an EndpointType).
package endpointurl

import (
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/labels"
)

const (
	HTTPRouteKind   = "HTTPRoute"
	GRPCRouteKind   = "GRPCRoute"
	TLSRouteKind    = "TLSRoute"
	GatewayAPIGroup = "gateway.networking.k8s.io"

	// ClusterLocalSuffix is the default Kubernetes cluster domain suffix.
	ClusterLocalSuffix = "svc.cluster.local"

	// SchemeHTTP and the Scheme* constants below are the URL schemes for endpoint
	// protocols. The TLS variants are used when the workload speaks the protocol
	// over TLS (gateway-terminated or passthrough).
	SchemeHTTP  = "http"
	SchemeHTTPS = "https"
	SchemeWS    = "ws"
	SchemeWSS   = "wss"
	SchemeGRPC  = "grpc"
	SchemeGRPCS = "grpcs"
	SchemeTCP   = "tcp"
	SchemeTLS   = "tls"
	SchemeUDP   = "udp"
)

// RouteKindCompat lists which endpoint protocols each Gateway API route kind
// may expose. HTTPRoute/GRPCRoute attach to the http and https gateway listeners
// (the gateway terminates TLS on https); TLSRoute attaches to the tls listener for
// SNI-based passthrough where the application terminates TLS. A given endpoint
// is exposed by at most one route kind per visibility — the TLS trait swaps an
// HTTPRoute/GRPCRoute for a TLSRoute when application-level TLS termination is
// required.
var RouteKindCompat = map[string]map[openchoreov1alpha1.EndpointProtocol]bool{
	HTTPRouteKind: {
		openchoreov1alpha1.EndpointProtocolHTTP:      true,
		openchoreov1alpha1.EndpointProtocolGraphQL:   true,
		openchoreov1alpha1.EndpointProtocolWebsocket: true,
		openchoreov1alpha1.EndpointProtocolGRPC:      true,
	},
	GRPCRouteKind: {
		openchoreov1alpha1.EndpointProtocolGRPC: true,
	},
	TLSRouteKind: {
		openchoreov1alpha1.EndpointProtocolHTTP:      true,
		openchoreov1alpha1.EndpointProtocolGraphQL:   true,
		openchoreov1alpha1.EndpointProtocolWebsocket: true,
		openchoreov1alpha1.EndpointProtocolGRPC:      true,
		openchoreov1alpha1.EndpointProtocolTCP:       true,
	},
}

// IsRoutableProtocol reports whether any route kind can expose this endpoint protocol.
func IsRoutableProtocol(p openchoreov1alpha1.EndpointProtocol) bool {
	for _, compat := range RouteKindCompat {
		if compat[p] {
			return true
		}
	}
	return false
}

// RouteKind returns the Gateway API route kind of obj, or "" when obj is not a
// Gateway API route.
func RouteKind(obj *unstructured.Unstructured) string {
	if obj.GetObjectKind().GroupVersionKind().Group != GatewayAPIGroup {
		return ""
	}
	if _, ok := RouteKindCompat[obj.GetKind()]; !ok {
		return ""
	}
	return obj.GetKind()
}

// ResolveRouteURLs returns the gateway URLs a route exposes for an endpoint of
// the given protocol and visibility. Returns nil when the route has no hostname
// or no gateway endpoint is configured for the visibility.
func ResolveRouteURLs(
	obj *unstructured.Unstructured,
	kind string,
	protocol openchoreov1alpha1.EndpointProtocol,
	visibility openchoreov1alpha1.EndpointVisibility,
	env *openchoreov1alpha1.Environment,
	dp *openchoreov1alpha1.DataPlane,
) *openchoreov1alpha1.EndpointGatewayURLs {
	hostname := FirstHostname(obj)
	gw := GatewayEndpointForVisibility(visibility, env, dp)
	if hostname == "" || gw == nil {
		return nil
	}
	return BuildRouteURLs(kind, protocol, hostname, RoutePath(obj, kind), gw)
}

// RoutePath returns the URL base path that clients use to reach the endpoint via
// the gateway. A route may advertise this explicitly through the
// openchoreo.dev/endpoint-base-path annotation; this is required for routes that
// render one match per API resource (e.g. an HTTPRoute with an Exact match per
// OpenAPI path), where the first match is a specific operation rather than the
// endpoint base. When the annotation is absent, fall back to the first HTTPRoute
// match path — the prefix-routing convention where the single match value is the
// exposed base. GRPCRoute (method-based) and TLSRoute (SNI-based) carry no path.
func RoutePath(obj *unstructured.Unstructured, kind string) string {
	if base := obj.GetAnnotations()[labels.AnnotationKeyEndpointBasePath]; base != "" {
		return base
	}
	if kind == HTTPRouteKind {
		return FirstPathValue(obj)
	}
	return ""
}

// BuildRouteURLs constructs an EndpointGatewayURLs by writing into the listener
// fields that the given route kind targets. HTTPRoute and GRPCRoute populate
// urls.HTTP (cleartext) and urls.HTTPS (gateway-terminated TLS); TLSRoute
// populates urls.TLS (SNI passthrough, application terminates TLS). The URL
// scheme inside each entry is derived from the endpoint protocol — the field
// name reflects the gateway listener, not the scheme.
func BuildRouteURLs(
	routeKind string,
	protocol openchoreov1alpha1.EndpointProtocol,
	hostname, path string,
	ep *openchoreov1alpha1.GatewayEndpointSpec,
) *openchoreov1alpha1.EndpointGatewayURLs {
	if ep == nil {
		return nil
	}
	urls := &openchoreov1alpha1.EndpointGatewayURLs{}
	switch routeKind {
	case HTTPRouteKind, GRPCRouteKind:
		if ep.HTTP != nil {
			urls.HTTP = newURL(GatewayScheme(protocol, false), hostname, path, ep.HTTP.Port)
		}
		if ep.HTTPS != nil {
			urls.HTTPS = newURL(GatewayScheme(protocol, true), hostname, path, ep.HTTPS.Port)
		}
	case TLSRouteKind:
		if ep.TLS != nil {
			urls.TLS = newURL(GatewayScheme(protocol, true), hostname, path, ep.TLS.Port)
		}
	}
	if urls.HTTP == nil && urls.HTTPS == nil && urls.TLS == nil {
		return nil
	}
	return urls
}

// GatewayScheme returns the URL scheme used by clients of the given protocol
// through a gateway listener. The tls flag selects between the cleartext form
// (over the http listener) and the TLS form (over the https or tls listener).
// Returns "" for protocols that don't have a meaningful URL scheme for the
// requested TLS variant (e.g. TCP without TLS).
func GatewayScheme(p openchoreov1alpha1.EndpointProtocol, tls bool) string {
	switch p {
	case openchoreov1alpha1.EndpointProtocolHTTP, openchoreov1alpha1.EndpointProtocolGraphQL:
		if tls {
			return SchemeHTTPS
		}
		return SchemeHTTP
	case openchoreov1alpha1.EndpointProtocolWebsocket:
		if tls {
			return SchemeWSS
		}
		return SchemeWS
	case openchoreov1alpha1.EndpointProtocolGRPC:
		if tls {
			return SchemeGRPCS
		}
		return SchemeGRPC
	case openchoreov1alpha1.EndpointProtocolTCP:
		if tls {
			return SchemeTLS
		}
	}
	return ""
}

// ServiceScheme returns the URL scheme for reaching an endpoint of the given
// protocol directly through its in-cluster Service.
func ServiceScheme(p openchoreov1alpha1.EndpointProtocol) string {
	switch p {
	case openchoreov1alpha1.EndpointProtocolHTTP,
		openchoreov1alpha1.EndpointProtocolGraphQL:
		return SchemeHTTP
	case openchoreov1alpha1.EndpointProtocolWebsocket:
		return SchemeWS
	case openchoreov1alpha1.EndpointProtocolGRPC:
		return SchemeGRPC
	case openchoreov1alpha1.EndpointProtocolTCP:
		return SchemeTCP
	case openchoreov1alpha1.EndpointProtocolUDP:
		return SchemeUDP
	default:
		return SchemeHTTP
	}
}

// newURL constructs a single EndpointURL from the given components.
func newURL(scheme, hostname, path string, port int32) *openchoreov1alpha1.EndpointURL {
	return &openchoreov1alpha1.EndpointURL{
		Scheme: scheme,
		Host:   hostname,
		Port:   port,
		Path:   path,
	}
}

// FirstHostname returns the first entry of spec.hostnames[], or "" if absent.
func FirstHostname(obj *unstructured.Unstructured) string {
	hostnames, found, err := unstructured.NestedStringSlice(obj.Object, "spec", "hostnames")
	if err != nil || !found || len(hostnames) == 0 {
		return ""
	}
	return hostnames[0]
}

// FirstPathValue walks spec.rules[0].matches[0].path.value and returns the path string.
// Returns "" when the HTTPRoute has no path match rule (e.g. root-path webapps).
func FirstPathValue(obj *unstructured.Unstructured) string {
	rules, found, err := unstructured.NestedSlice(obj.Object, "spec", "rules")
	if err != nil || !found || len(rules) == 0 {
		return ""
	}

	firstRule, ok := rules[0].(map[string]interface{})
	if !ok {
		return ""
	}

	matches, found, err := unstructured.NestedSlice(firstRule, "matches")
	if err != nil || !found || len(matches) == 0 {
		return ""
	}

	firstMatch, ok := matches[0].(map[string]interface{})
	if !ok {
		return ""
	}

	value, _, _ := unstructured.NestedString(firstMatch, "path", "value")
	return value
}

// GatewayEndpointForVisibility returns the GatewayEndpointSpec for the ingress gateway
// based on endpoint visibility. External visibility maps to the external gateway endpoint;
// all other visibilities (internal, namespace, project) map to the internal gateway endpoint.
// Environment-level gateway configuration takes precedence over dataplane-level configuration.
// Returns nil if no gateway endpoint is configured for the given visibility.
func GatewayEndpointForVisibility(
	visibility openchoreov1alpha1.EndpointVisibility,
	env *openchoreov1alpha1.Environment,
	dp *openchoreov1alpha1.DataPlane,
) *openchoreov1alpha1.GatewayEndpointSpec {
	if dp == nil {
		return nil
	}

	// Use environment-level gateway config if present, otherwise fall back to dataplane.
	spec := dp.Spec.Gateway
	if env != nil && env.Spec.Gateway.Ingress != nil {
		spec = env.Spec.Gateway
	}

	// If no ingress configured, we can't resolve an endpoint as we don't support egress gateways.
	// TODO: (lahirude@wso2.com) Add support for egress gateway endpoints and update this logic accordingly.
	if spec.Ingress == nil {
		return nil
	}

	if visibility == openchoreov1alpha1.EndpointVisibilityExternal {
		return spec.Ingress.External
	}
	return spec.Ingress.Internal
}

// String formats an EndpointURL as scheme://host[:port][/path]. The port is
// omitted when it is the default for http (80) or https (443).
func String(u *openchoreov1alpha1.EndpointURL) string {
	if u == nil || u.Host == "" {
		return ""
	}
	var sb strings.Builder
	if u.Scheme != "" {
		sb.WriteString(u.Scheme)
		sb.WriteString("://")
	}
	sb.WriteString(u.Host)
	if u.Port != 0 && !((u.Scheme == SchemeHTTP && u.Port == 80) || (u.Scheme == SchemeHTTPS && u.Port == 443)) {
		sb.WriteString(":")
		sb.WriteString(strconv.Itoa(int(u.Port)))
	}
	if u.Path != "" {
		if !strings.HasPrefix(u.Path, "/") {
			sb.WriteString("/")
		}
		sb.WriteString(u.Path)
	}
	return sb.String()
}
