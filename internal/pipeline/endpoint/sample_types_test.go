// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointpipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
)

// meshSamplesDir holds the mesh EndpointType samples rendered by these tests.
var meshSamplesDir = filepath.Join("..", "..", "..", "samples", "mesh-endpoint-types")

// loadClusterEndpointType reads a ClusterEndpointType sample.
func loadClusterEndpointType(t *testing.T, file string) *v1alpha1.EndpointTypeSpec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(meshSamplesDir, file))
	require.NoError(t, err)
	var cet v1alpha1.ClusterEndpointType
	require.NoError(t, yaml.UnmarshalStrict(data, &cet))
	spec := v1alpha1.EndpointTypeSpec(cet.Spec)
	return &spec
}

// meshBackends are a stable and a canary backend, plus a weight-0 shadow.
func meshBackends() []Backend {
	ns := "dp-default-shop-dev-a1b2c3d"
	backend := func(name, svc string, weight int32) Backend {
		return Backend{
			Name: name, Kind: "Component", ComponentName: svc, Endpoint: "http", Protocol: "HTTP",
			ServiceName: svc, Namespace: ns, Port: 8080, Weight: weight,
			ServiceURL: "http://" + svc + "." + ns + ".svc.cluster.local:8080",
		}
	}
	return []Backend{backend("stable", "orders", 90), backend("canary", "orders-v2", 10), backend("shadow", "orders-v3", 0)}
}

// meshTakeOver is the orders Service, whose callers the mesh route serves.
func meshTakeOver() *URLContext {
	return &URLContext{
		ComponentName: "orders", Endpoint: "http", BasePath: "/",
		ServiceName: "orders", Namespace: "dp-default-shop-dev-a1b2c3d", Port: 8080,
		ServiceURL: "http://orders.dp-default-shop-dev-a1b2c3d.svc.cluster.local:8080",
	}
}

func meshDataPlane(mesh string) DataPlaneContext {
	return BuildDataPlaneContext(&v1alpha1.DataPlane{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"openchoreo.dev/service-mesh": mesh}},
	})
}

// renderMesh renders a mesh sample with the given traffic parameters.
func renderMesh(t *testing.T, file, mesh string, params map[string]any) (*RenderOutput, error) {
	t.Helper()
	input := &RenderInput{
		EndpointTypeSpec: loadClusterEndpointType(t, file),
		Backends:         meshBackends(),
		TakeOver:         meshTakeOver(),
		Metadata:         fixtureMetadata(),
		DataPlane:        meshDataPlane(mesh),
		Environment:      fixtureGateway(),
	}
	if params != nil {
		input.Parameters = rawExt(t, params)
	}
	return NewPipeline().Render(context.Background(), input)
}

// trafficParams is a header rule, a cookie rule and a 10% mirror.
func trafficParams() map[string]any {
	return map[string]any{
		"rules": []any{
			map[string]any{
				"name":        "testers",
				"matches":     []any{map[string]any{"headers": []any{map[string]any{"name": "x-canary", "value": "true"}}}},
				"backendRefs": []any{map[string]any{"name": "canary"}},
			},
			map[string]any{
				"name":        "arm-b",
				"matches":     []any{map[string]any{"cookies": []any{map[string]any{"name": "ab", "value": "b"}}, "method": "GET"}},
				"backendRefs": []any{map[string]any{"name": "canary"}},
			},
		},
		"mirrors": []any{map[string]any{"backendRef": "shadow", "percent": 10}},
	}
}

func specOf(t *testing.T, out *RenderOutput) map[string]any {
	t.Helper()
	require.Len(t, out.Entries, 1)
	return out.Entries[0].Object["spec"].(map[string]any)
}

func TestMeshGamma_WeightedOnly(t *testing.T) {
	out, err := renderMesh(t, "mesh-gamma.yaml", "istio", nil)
	require.NoError(t, err)

	obj := out.Entries[0].Object
	assert.Equal(t, "HTTPRoute", obj["kind"])
	labels := obj["metadata"].(map[string]any)["labels"].(map[string]any)
	assert.Equal(t, "project", labels["openchoreo.dev/endpoint-visibility"])

	spec := specOf(t, out)
	assert.JSONEq(t, `[{"group":"","kind":"Service","name":"orders","port":8080}]`, jsonOf(t, spec["parentRefs"]))
	assert.JSONEq(t, `[{"backendRefs":[
		{"name":"orders","port":8080,"weight":90},
		{"name":"orders-v2","port":8080,"weight":10}
	]}]`, jsonOf(t, spec["rules"]), "the weight-0 shadow is left out of the default split")
}

func TestMeshGamma_RulesAndMirror(t *testing.T) {
	out, err := renderMesh(t, "mesh-gamma.yaml", "cilium", trafficParams())
	require.NoError(t, err)

	assert.JSONEq(t, `[
		{"matches":[{"headers":[{"type":"Exact","name":"x-canary","value":"true"}]}],
		 "backendRefs":[{"name":"orders-v2","port":8080,"weight":1}]},
		{"matches":[{"headers":[{"type":"RegularExpression","name":"cookie","value":"(.*;\\s*)?ab=b(;.*)?"}],"method":"GET"}],
		 "backendRefs":[{"name":"orders-v2","port":8080,"weight":1}]},
		{"backendRefs":[{"name":"orders","port":8080,"weight":90},{"name":"orders-v2","port":8080,"weight":10}],
		 "filters":[{"type":"RequestMirror","requestMirror":{"backendRef":{"name":"orders-v3","port":8080},"percent":10}}]}
	]`, jsonOf(t, specOf(t, out)["rules"]))
}

func TestMeshGamma_Refusals(t *testing.T) {
	t.Run("no mesh on the data plane", func(t *testing.T) {
		_, err := renderMesh(t, "mesh-gamma.yaml", "", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "openchoreo.dev/service-mesh")
	})
	t.Run("rule targets an unknown backend", func(t *testing.T) {
		params := trafficParams()
		params["rules"].([]any)[0].(map[string]any)["backendRefs"] = []any{map[string]any{"name": "missing"}}
		_, err := renderMesh(t, "mesh-gamma.yaml", "istio", params)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "a rule targets a backend")
	})
	t.Run("mirroring on Linkerd", func(t *testing.T) {
		_, err := renderMesh(t, "mesh-gamma.yaml", "linkerd", trafficParams())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Linkerd does not support request mirroring")
	})
	t.Run("parameters outside the schema", func(t *testing.T) {
		_, err := renderMesh(t, "mesh-gamma.yaml", "istio", map[string]any{
			"mirrors": []any{map[string]any{"backendRef": "shadow", "percent": 150}},
		})
		require.Error(t, err)
	})
}

// TestMeshSamples_ShareTrafficSchema keeps each type's copy of the traffic
// schema identical to traffic-schema.yaml.
func TestMeshSamples_ShareTrafficSchema(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(meshSamplesDir, "traffic-schema.yaml"))
	require.NoError(t, err)
	var shared v1alpha1.SchemaSection
	require.NoError(t, yaml.UnmarshalStrict(data, &shared))

	for _, file := range meshSampleFiles {
		t.Run(file, func(t *testing.T) {
			spec := loadClusterEndpointType(t, file)
			require.NotNil(t, spec.Parameters)
			assert.JSONEq(t, canonicalJSON(t, shared.OpenAPIV3Schema.Raw), canonicalJSON(t, spec.Parameters.OpenAPIV3Schema.Raw))
		})
	}
}

// meshSampleFiles are the EndpointTypes that carry the shared traffic schema.
var meshSampleFiles = []string{"mesh-gamma.yaml", "istio-virtualservice.yaml", "cilium-cec.yaml"}

func canonicalJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal(raw, &v))
	return jsonOf(t, v)
}

func TestIstioVirtualService_RulesAndMirror(t *testing.T) {
	out, err := renderMesh(t, "istio-virtualservice.yaml", "istio", trafficParams())
	require.NoError(t, err)

	obj := out.Entries[0].Object
	assert.Equal(t, "VirtualService", obj["kind"])
	spec := specOf(t, out)
	assert.JSONEq(t, `["orders.dp-default-shop-dev-a1b2c3d.svc.cluster.local"]`, jsonOf(t, spec["hosts"]))

	host := func(svc string) string { return svc + ".dp-default-shop-dev-a1b2c3d.svc.cluster.local" }
	assert.JSONEq(t, `[
		{"name":"testers","match":[{"headers":{"x-canary":{"exact":"true"}}}],
		 "route":[{"destination":{"host":"`+host("orders-v2")+`","port":{"number":8080}},"weight":1}]},
		{"name":"arm-b","match":[{"headers":{"cookie":{"regex":"(.*;\\s*)?ab=b(;.*)?"}},"method":{"exact":"GET"}}],
		 "route":[{"destination":{"host":"`+host("orders-v2")+`","port":{"number":8080}},"weight":1}]},
		{"name":"default",
		 "route":[
		   {"destination":{"host":"`+host("orders")+`","port":{"number":8080}},"weight":90},
		   {"destination":{"host":"`+host("orders-v2")+`","port":{"number":8080}},"weight":10}],
		 "mirrors":[{"destination":{"host":"`+host("orders-v3")+`","port":{"number":8080}},"percentage":{"value":10}}]}
	]`, jsonOf(t, spec["http"]))
}

func TestIstioVirtualService_NeedsIstio(t *testing.T) {
	_, err := renderMesh(t, "istio-virtualservice.yaml", "cilium", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openchoreo.dev/service-mesh=istio")
}

func TestCiliumEnvoyConfig_RulesAndMirror(t *testing.T) {
	out, err := renderMesh(t, "cilium-cec.yaml", "cilium", trafficParams())
	require.NoError(t, err)

	obj := out.Entries[0].Object
	assert.Equal(t, "CiliumEnvoyConfig", obj["kind"])
	annotations := obj["metadata"].(map[string]any)["annotations"].(map[string]any)
	assert.Equal(t, "false", annotations["cec.cilium.io/use-original-source-address"])

	spec := specOf(t, out)
	ns := "dp-default-shop-dev-a1b2c3d"
	assert.JSONEq(t, `[{"name":"orders","namespace":"`+ns+`"}]`, jsonOf(t, spec["services"]))
	assert.JSONEq(t, `[{"name":"orders-v2","namespace":"`+ns+`"},{"name":"orders-v3","namespace":"`+ns+`"}]`,
		jsonOf(t, spec["backendServices"]), "the taken-over Service is redirected, not just synced")

	resources := spec["resources"].([]any)
	require.Len(t, resources, 5, "a listener, a route configuration and one cluster per backend")
	routes := resources[1].(map[string]any)["virtual_hosts"].([]any)[0].(map[string]any)["routes"]
	assert.JSONEq(t, `[
		{"match":{"prefix":"/","headers":[{"name":"x-canary","string_match":{"exact":"true"}}]},
		 "route":{"weighted_clusters":{"clusters":[{"name":"`+ns+`/orders-v2","weight":1}]}}},
		{"match":{"prefix":"/","headers":[
		   {"name":"cookie","string_match":{"safe_regex":{"regex":"(.*;\\s*)?ab=b(;.*)?"}}},
		   {"name":":method","string_match":{"exact":"GET"}}]},
		 "route":{"weighted_clusters":{"clusters":[{"name":"`+ns+`/orders-v2","weight":1}]}}},
		{"match":{"prefix":"/"},
		 "route":{
		   "weighted_clusters":{"clusters":[{"name":"`+ns+`/orders","weight":90},{"name":"`+ns+`/orders-v2","weight":10}]},
		   "request_mirror_policies":[{"cluster":"`+ns+`/orders-v3","runtime_fraction":{"default_value":{"numerator":10,"denominator":"HUNDRED"}}}]}}
	]`, jsonOf(t, routes))
	assert.JSONEq(t, `{"@type":"type.googleapis.com/envoy.config.cluster.v3.Cluster","name":"`+ns+`/orders","type":"EDS","connect_timeout":"5s"}`,
		jsonOf(t, resources[2]))
}
