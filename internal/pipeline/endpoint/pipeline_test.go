// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointpipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
)

// rawExt marshals v and wraps it in a RawExtension.
func rawExt(t *testing.T, v any) *runtime.RawExtension {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return &runtime.RawExtension{Raw: b}
}

func fixtureMetadata() MetadataContext {
	return MetadataContext{
		Name:              "payments-api-dev-a1b2c3d4",
		Namespace:         "dp-default-shop-dev-a1b2c3d",
		EndpointNamespace: "default",
		EndpointName:      "payments-api",
		ProjectName:       "shop",
		EnvironmentName:   "dev",
		Labels:            map[string]string{"openchoreo.dev/project": "shop"},
	}
}

func fixtureBackends() []Backend {
	return []Backend{
		{
			ComponentName: "payments-v1", Endpoint: "api", Protocol: "HTTP",
			ServiceName: "payments-v1", Namespace: "dp-default-shop-dev-a1b2c3d", Port: 8080, Weight: 90,
			ServiceURL: "http://payments-v1.dp-default-shop-dev-a1b2c3d.svc.cluster.local:8080",
		},
		{
			ComponentName: "payments-v2", Endpoint: "api", Protocol: "HTTP",
			ServiceName: "payments-v2", Namespace: "dp-default-shop-dev-a1b2c3d", Port: 8080, Weight: 10,
			ServiceURL: "http://payments-v2.dp-default-shop-dev-a1b2c3d.svc.cluster.local:8080",
		},
	}
}

// fixtureAddress is the fixture Endpoint's own address.
func fixtureAddress() AddressContext {
	return AddressContext{Host: "development-default.openchoreoapis.localhost", Path: "/payments"}
}

// fixtureTakeOver is payments-v1's api URL, taken over by the fixture Endpoint.
func fixtureTakeOver() *URLContext {
	return &URLContext{
		ComponentName: "payments-v1", Endpoint: "api", BasePath: "/",
		External: &Address{Scheme: "http", Host: "development-default.openchoreoapis.localhost", Port: 19080, Path: "/payments-v1-api"},
	}
}

// weightedRouteTemplate is the HTTPRoute shape from the Endpoint design: one
// rule whose backendRefs are mapped from ${backends}.
func weightedRouteTemplate(t *testing.T) v1alpha1.ResourceTemplate {
	return v1alpha1.ResourceTemplate{
		ID: "route",
		Template: rawExt(t, map[string]any{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "HTTPRoute",
			"metadata": map[string]any{
				"name":      "${metadata.name}",
				"namespace": "${metadata.namespace}",
				"labels": map[string]any{
					"openchoreo.dev/endpoint-visibility": "external",
				},
			},
			"spec": map[string]any{
				"parentRefs": []any{map[string]any{
					"name":      "${gateway.ingress.external.name}",
					"namespace": "${gateway.ingress.external.namespace}",
				}},
				"hostnames": []any{"${parameters.hostname}"},
				"rules": []any{map[string]any{
					"matches": []any{map[string]any{
						"path": map[string]any{"type": "PathPrefix", "value": "${parameters.pathPrefix}"},
					}},
					"backendRefs": `${backends.map(b, {"name": b.serviceName, "port": b.port, "weight": b.weight})}`,
				}},
			},
		}),
	}
}

func fixtureGateway() EnvironmentContext {
	return BuildEnvironmentContext(nil, &v1alpha1.DataPlane{
		Spec: v1alpha1.DataPlaneSpec{
			Gateway: v1alpha1.GatewaySpec{
				Ingress: &v1alpha1.GatewayNetworkSpec{
					External: &v1alpha1.GatewayEndpointSpec{Name: "gateway-default", Namespace: "openchoreo-data-plane"},
				},
			},
		},
	})
}

// jsonOf marshals v so number types from the CEL round-trip compare by value.
func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func TestRender_WeightedHTTPRoute(t *testing.T) {
	p := NewPipeline()

	out, err := p.Render(context.Background(), &RenderInput{
		EndpointTypeSpec: &v1alpha1.EndpointTypeSpec{
			Parameters: &v1alpha1.SchemaSection{OpenAPIV3Schema: rawExt(t, map[string]any{
				"type": "object",
				"properties": map[string]any{
					"hostname":   map[string]any{"type": "string"},
					"pathPrefix": map[string]any{"type": "string", "default": "/"},
				},
			})},
			Resources: []v1alpha1.ResourceTemplate{weightedRouteTemplate(t)},
		},
		Parameters:  rawExt(t, map[string]any{"hostname": "payments.shop.example.com"}),
		Backends:    fixtureBackends(),
		Address:     fixtureAddress(),
		Metadata:    fixtureMetadata(),
		Environment: fixtureGateway(),
	})
	require.NoError(t, err)
	require.Len(t, out.Entries, 1)

	entry := out.Entries[0]
	assert.Equal(t, "route", entry.ID)

	md := entry.Object["metadata"].(map[string]any)
	assert.Equal(t, "payments-api-dev-a1b2c3d4", md["name"])
	assert.Equal(t, "dp-default-shop-dev-a1b2c3d", md["namespace"])

	spec := entry.Object["spec"].(map[string]any)
	assert.JSONEq(t, `["payments.shop.example.com"]`, jsonOf(t, spec["hostnames"]))
	assert.JSONEq(t, `[{"name":"gateway-default","namespace":"openchoreo-data-plane"}]`, jsonOf(t, spec["parentRefs"]))

	rule := spec["rules"].([]any)[0].(map[string]any)
	assert.JSONEq(t, `[{"path":{"type":"PathPrefix","value":"/"}}]`, jsonOf(t, rule["matches"]),
		"pathPrefix should fall back to its schema default")
	assert.JSONEq(t, `[
		{"name":"payments-v1","port":8080,"weight":90},
		{"name":"payments-v2","port":8080,"weight":10}
	]`, jsonOf(t, rule["backendRefs"]))
}

func TestRender_ForEachOverBackends(t *testing.T) {
	p := NewPipeline()

	out, err := p.Render(context.Background(), &RenderInput{
		EndpointTypeSpec: &v1alpha1.EndpointTypeSpec{
			Resources: []v1alpha1.ResourceTemplate{{
				ID:      "backend-config",
				ForEach: "${backends}",
				Var:     "backend",
				Template: rawExt(t, map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":      "${metadata.name + '-' + backend.componentName}",
						"namespace": "${metadata.namespace}",
					},
					"data": map[string]any{"url": "${backend.serviceURL}"},
				}),
			}},
		},
		Backends: fixtureBackends(),
		Address:  fixtureAddress(),
		Metadata: fixtureMetadata(),
	})
	require.NoError(t, err)
	require.Len(t, out.Entries, 2)
	assert.Equal(t, "backend-config-0", out.Entries[0].ID)
	assert.Equal(t, "backend-config-1", out.Entries[1].ID)
	assert.Equal(t, "payments-api-dev-a1b2c3d4-payments-v2",
		out.Entries[1].Object["metadata"].(map[string]any)["name"])
	assert.Equal(t, "http://payments-v2.dp-default-shop-dev-a1b2c3d.svc.cluster.local:8080",
		out.Entries[1].Object["data"].(map[string]any)["url"])
}

func TestRender_IncludeWhenSkipsEntry(t *testing.T) {
	p := NewPipeline()

	route := weightedRouteTemplate(t)
	route.IncludeWhen = "${size(backends) > 5}"

	out, err := p.Render(context.Background(), &RenderInput{
		EndpointTypeSpec: &v1alpha1.EndpointTypeSpec{Resources: []v1alpha1.ResourceTemplate{route}},
		Backends:         fixtureBackends(),
		Address:          fixtureAddress(),
		Metadata:         fixtureMetadata(),
		Environment:      fixtureGateway(),
	})
	require.NoError(t, err)
	assert.Empty(t, out.Entries)
}

func TestRender_ValidationFailureAborts(t *testing.T) {
	p := NewPipeline()

	_, err := p.Render(context.Background(), &RenderInput{
		EndpointTypeSpec: &v1alpha1.EndpointTypeSpec{
			Validations: []v1alpha1.ValidationRule{{
				Rule:    "${backends.all(b, b.protocol == 'HTTP')}",
				Message: "http-weighted only routes HTTP endpoints",
			}},
			Resources: []v1alpha1.ResourceTemplate{weightedRouteTemplate(t)},
		},
		Backends: []Backend{{ComponentName: "c", Endpoint: "rpc", Protocol: "gRPC", ServiceName: "c", Port: 9090, Weight: 1}},
		Address:  fixtureAddress(),
		Metadata: fixtureMetadata(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http-weighted only routes HTTP endpoints")
}

func TestRender_RejectsNonDataPlaneTarget(t *testing.T) {
	p := NewPipeline()

	route := weightedRouteTemplate(t)
	route.TargetPlane = v1alpha1.TargetPlaneObservabilityPlane

	_, err := p.Render(context.Background(), &RenderInput{
		EndpointTypeSpec: &v1alpha1.EndpointTypeSpec{Resources: []v1alpha1.ResourceTemplate{route}},
		Backends:         fixtureBackends(),
		Address:          fixtureAddress(),
		Metadata:         fixtureMetadata(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "targetPlane")
}

func TestRender_RejectsMissingInputs(t *testing.T) {
	p := NewPipeline()
	spec := &v1alpha1.EndpointTypeSpec{Resources: []v1alpha1.ResourceTemplate{weightedRouteTemplate(t)}}

	_, err := p.Render(context.Background(), &RenderInput{EndpointTypeSpec: spec, Metadata: fixtureMetadata()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Backends is empty")

	md := fixtureMetadata()
	md.Namespace = ""
	_, err = p.Render(context.Background(), &RenderInput{EndpointTypeSpec: spec, Backends: fixtureBackends(), Metadata: md})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Namespace is empty")
}

// addressRouteTemplate is the shape the shipped http-weighted type follows:
// one route for the Endpoint's own address, a second for the taken-over URL
// when there is one, both splitting across every backend by weight.
func addressRouteTemplates(t *testing.T) []v1alpha1.ResourceTemplate {
	route := func(id, host, path, include string) v1alpha1.ResourceTemplate {
		tmpl := v1alpha1.ResourceTemplate{
			ID: id,
			Template: rawExt(t, map[string]any{
				"apiVersion": "gateway.networking.k8s.io/v1",
				"kind":       "HTTPRoute",
				"metadata":   map[string]any{"name": "${metadata.name}-" + id, "namespace": "${metadata.namespace}"},
				"spec": map[string]any{
					"parentRefs": []any{map[string]any{"name": "${gateway.ingress.external.name}"}},
					"hostnames":  []any{host},
					"rules": []any{map[string]any{
						"matches":     []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": path}}},
						"backendRefs": `${backends.map(b, {"name": b.serviceName, "port": b.port, "weight": b.weight})}`,
					}},
				},
			}),
		}
		if include != "" {
			tmpl.IncludeWhen = include
		}
		return tmpl
	}
	return []v1alpha1.ResourceTemplate{
		route("route", "${address.host}", "${address.path}", ""),
		route("takeover", "${takeOver.external.host}", "${takeOver.external.path}", "${takeOver != null}"),
	}
}

func TestRender_ServesTheEndpointAddressAndTakeOver(t *testing.T) {
	p := NewPipeline()

	backends := fixtureBackends()
	backends = append(backends, Backend{
		Name: "legacy", Kind: "External", Protocol: "HTTP", ServiceName: "e-payments-legacy", Port: 443, Weight: 0,
		External: &ExternalBackend{Scheme: "https", Host: "legacy.example.com", Port: 443, URL: "https://legacy.example.com"},
	})

	render := func(takeOver *URLContext) []RenderedEntry {
		out, err := p.Render(context.Background(), &RenderInput{
			EndpointTypeSpec: &v1alpha1.EndpointTypeSpec{Resources: addressRouteTemplates(t)},
			Backends:         backends,
			Address:          fixtureAddress(),
			TakeOver:         takeOver,
			Metadata:         fixtureMetadata(),
			Environment:      fixtureGateway(),
		})
		require.NoError(t, err)
		return out.Entries
	}

	entries := render(nil)
	require.Len(t, entries, 1, "without a take-over only the Endpoint's own address is routed")
	spec := entries[0].Object["spec"].(map[string]any)
	assert.JSONEq(t, `["development-default.openchoreoapis.localhost"]`, jsonOf(t, spec["hostnames"]))
	split := `[{"name":"payments-v1","port":8080,"weight":90},{"name":"payments-v2","port":8080,"weight":10},{"name":"e-payments-legacy","port":443,"weight":0}]`
	assert.JSONEq(t, `[{"matches":[{"path":{"type":"PathPrefix","value":"/payments"}}],"backendRefs":`+split+`}]`,
		jsonOf(t, spec["rules"]), "external backends join the same split")

	entries = render(fixtureTakeOver())
	require.Len(t, entries, 2)
	takeOverSpec := entries[1].Object["spec"].(map[string]any)
	assert.JSONEq(t, `[{"matches":[{"path":{"type":"PathPrefix","value":"/payments-v1-api"}}],"backendRefs":`+split+`}]`,
		jsonOf(t, takeOverSpec["rules"]), "the taken-over URL carries the same split")
}

func TestRender_RequiresAddress(t *testing.T) {
	p := NewPipeline()
	_, err := p.Render(context.Background(), &RenderInput{
		EndpointTypeSpec: &v1alpha1.EndpointTypeSpec{Resources: addressRouteTemplates(t)},
		Backends:         fixtureBackends(),
		Metadata:         fixtureMetadata(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "input.Address is unset")
}
