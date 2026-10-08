// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointurl

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

func TestString(t *testing.T) {
	cases := []struct {
		name string
		url  *openchoreov1alpha1.EndpointURL
		want string
	}{
		{"nil", nil, ""},
		{"default_https_port_omitted", &openchoreov1alpha1.EndpointURL{Scheme: "https", Host: "api.example.com", Port: 443, Path: "/v1"}, "https://api.example.com/v1"},
		{"non_default_port_kept", &openchoreov1alpha1.EndpointURL{Scheme: "https", Host: "api.example.com", Port: 19443}, "https://api.example.com:19443"},
		{"path_without_slash", &openchoreov1alpha1.EndpointURL{Scheme: "http", Host: "svc.ns.svc.cluster.local", Port: 8080, Path: "api"}, "http://svc.ns.svc.cluster.local:8080/api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, String(tc.url))
		})
	}
}

func TestResolveRouteURLs(t *testing.T) {
	route := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata":   map[string]any{"name": "payments"},
		"spec": map[string]any{
			"hostnames": []any{"payments.example.com"},
			"rules": []any{map[string]any{
				"matches": []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/pay"}}},
			}},
		},
	}}
	dp := &openchoreov1alpha1.DataPlane{Spec: openchoreov1alpha1.DataPlaneSpec{
		Gateway: openchoreov1alpha1.GatewaySpec{Ingress: &openchoreov1alpha1.GatewayNetworkSpec{
			External: &openchoreov1alpha1.GatewayEndpointSpec{
				HTTPS: &openchoreov1alpha1.GatewayListenerSpec{Port: 443},
			},
		}},
	}}

	require.Equal(t, HTTPRouteKind, RouteKind(route))

	urls := ResolveRouteURLs(route, HTTPRouteKind, openchoreov1alpha1.EndpointProtocolHTTP,
		openchoreov1alpha1.EndpointVisibilityExternal, nil, dp)
	require.NotNil(t, urls)
	assert.Nil(t, urls.HTTP, "no http listener configured")
	assert.Equal(t, "https://payments.example.com/pay", String(urls.HTTPS))

	assert.Nil(t, ResolveRouteURLs(route, HTTPRouteKind, openchoreov1alpha1.EndpointProtocolHTTP,
		openchoreov1alpha1.EndpointVisibilityInternal, nil, dp), "no internal gateway configured")
}

func TestRouteKind_IgnoresNonGatewayObjects(t *testing.T) {
	svc := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Service"}}
	assert.Empty(t, RouteKind(svc))
}
