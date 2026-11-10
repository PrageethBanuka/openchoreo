// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointpipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
)

// progressiveTypesDir holds the EndpointTypes the progressive delivery samples
// apply; rendering them here keeps them in step with ${rules}.
var progressiveTypesDir = filepath.Join("..", "..", "..", "samples", "progressive-delivery", "endpoint-types")

func loadProgressiveType(t *testing.T, file string) *v1alpha1.EndpointTypeSpec {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(progressiveTypesDir, file))
	require.NoError(t, err)
	var cet v1alpha1.ClusterEndpointType
	require.NoError(t, yaml.UnmarshalStrict(raw, &cet))
	spec := v1alpha1.EndpointTypeSpec(cet.Spec)
	return &spec
}

// progressiveInput is the stable backend at 90 and the canary at 10, with
// testers reaching the canary by header, a preview of it under /next, and a
// 10% mirror to a weight-0 shadow, built as the binding controller does.
func progressiveInput(t *testing.T, file string, visibility v1alpha1.EndpointVisibility) *RenderInput {
	backends := fixtureBackends()
	shadow := backends[1]
	shadow.Name, shadow.ServiceName, shadow.Weight = "shadow", "greeter-v3-dev-5e6f", 0
	backends = append(backends, shadow)
	canary := backends[1]
	canary.Weight = 1
	return &RenderInput{
		EndpointTypeSpec: loadProgressiveType(t, file),
		Parameters:       rawExt(t, map[string]any{"pathPrefix": "/greeter"}),
		Visibility:       []v1alpha1.EndpointVisibility{visibility},
		Backends:         backends,
		Rules: []Rule{
			{Name: "testers", Backends: []Backend{canary}, Mirrors: []Mirror{},
				Matches: []Match{NewMatch(nil, []HeaderMatch{{Name: "X-Canary", Value: "true"}}, nil)}},
			{Name: "preview", Backends: []Backend{canary}, Mirrors: []Mirror{},
				Matches: []Match{NewMatch(&PathMatch{Value: "/next"}, nil, nil)}},
			{Name: "default", Matches: []Match{}, Backends: backends,
				Mirrors: []Mirror{{Backend: shadow, Percent: 10}}},
		},
		Metadata:    fixtureMetadata(),
		Environment: fixtureEnvironment(),
	}
}

func TestSampleType_HTTPProgressiveRendersRules(t *testing.T) {
	out, err := NewPipeline().Render(context.Background(), progressiveInput(t, "http.yaml", v1alpha1.EndpointVisibilityExternal))
	require.NoError(t, err)
	require.Len(t, out.Entries, 1)

	rules := out.Entries[0].Object["spec"].(map[string]any)["rules"].([]any)
	require.Len(t, rules, 3, "two match rules, then the default")

	testers := rules[0].(map[string]any)
	assert.Equal(t, []any{map[string]any{
		"path":    map[string]any{"type": "PathPrefix", "value": "/greeter"},
		"headers": []any{map[string]any{"type": "Exact", "name": "x-canary", "value": "true"}},
	}}, testers["matches"])
	assert.Equal(t, []any{map[string]any{"name": "greeter-v2-dev-3c4d", "port": float64(8080), "weight": float64(1)}},
		testers["backendRefs"])

	preview := rules[1].(map[string]any)
	assert.Equal(t, map[string]any{"type": "PathPrefix", "value": "/greeter/next"},
		preview["matches"].([]any)[0].(map[string]any)["path"])

	def := rules[2].(map[string]any)
	assert.Equal(t, []any{
		map[string]any{"name": "greeter-dev-1a2b", "port": float64(8080), "weight": float64(90)},
		map[string]any{"name": "greeter-v2-dev-3c4d", "port": float64(8080), "weight": float64(10)},
	}, def["backendRefs"], "the weight-0 shadow is not routed to")
	assert.Contains(t, def["filters"], map[string]any{"type": "RequestMirror", "requestMirror": map[string]any{
		"backendRef": map[string]any{"name": "greeter-v3-dev-5e6f", "port": float64(8080)}, "percent": float64(10)}})
}

func TestSampleType_IstioRendersRules(t *testing.T) {
	out, err := NewPipeline().Render(context.Background(), progressiveInput(t, "istio-virtualservice.yaml", v1alpha1.EndpointVisibilityNamespace))
	require.NoError(t, err)
	require.Len(t, out.Entries, 1)

	spec := out.Entries[0].Object["spec"].(map[string]any)
	assert.Len(t, spec["hosts"], 3, "callers of any backend's Service get the Endpoint's routing")
	http := spec["http"].([]any)
	require.Len(t, http, 3)
	assert.Equal(t, map[string]any{"x-canary": map[string]any{"exact": "true"}},
		http[0].(map[string]any)["match"].([]any)[0].(map[string]any)["headers"])
	assert.Equal(t, map[string]any{"regex": `/next(/.*)?`},
		http[1].(map[string]any)["match"].([]any)[0].(map[string]any)["uri"])
	mirrors := http[2].(map[string]any)["mirrors"].([]any)
	require.Len(t, mirrors, 1)
	assert.Equal(t, map[string]any{"value": float64(10)}, mirrors[0].(map[string]any)["percentage"])
}

func TestSampleType_GAMMARendersRules(t *testing.T) {
	out, err := NewPipeline().Render(context.Background(), progressiveInput(t, "gamma-httproute.yaml", v1alpha1.EndpointVisibilityNamespace))
	require.NoError(t, err)
	require.Len(t, out.Entries, 1)

	spec := out.Entries[0].Object["spec"].(map[string]any)
	parents := spec["parentRefs"].([]any)
	require.Len(t, parents, 3, "callers of any backend's Service get the Endpoint's routing")
	assert.Equal(t, map[string]any{"group": "", "kind": "Service", "name": "greeter-dev-1a2b", "port": float64(8080)}, parents[0])

	rules := spec["rules"].([]any)
	require.Len(t, rules, 3)
	assert.Equal(t, []any{map[string]any{
		"headers": []any{map[string]any{"type": "Exact", "name": "x-canary", "value": "true"}},
	}}, rules[0].(map[string]any)["matches"])
	assert.NotContains(t, rules[2].(map[string]any), "matches", "the default rule matches everything")
	assert.Len(t, rules[2].(map[string]any)["backendRefs"], 2)
}

func TestSampleTypes_DeclareCapabilities(t *testing.T) {
	all := []v1alpha1.EndpointCapability{
		v1alpha1.EndpointCapabilityMatch, v1alpha1.EndpointCapabilityRegularExpression,
		v1alpha1.EndpointCapabilityMirror, v1alpha1.EndpointCapabilityMirrorPercent,
	}
	assert.ElementsMatch(t, all, loadProgressiveType(t, "http.yaml").Capabilities)
	assert.ElementsMatch(t, all, loadProgressiveType(t, "istio-virtualservice.yaml").Capabilities)
	assert.ElementsMatch(t, all, loadProgressiveType(t, "istio-http.yaml").Capabilities)
	assert.ElementsMatch(t, all, loadProgressiveType(t, "cilium-http.yaml").Capabilities)
	assert.Equal(t, []v1alpha1.EndpointCapability{v1alpha1.EndpointCapabilityMatch},
		loadProgressiveType(t, "gamma-httproute.yaml").Capabilities)
}

// renderKinds renders a sample type for the given visibilities and returns the
// rendered objects by kind.
func renderKinds(t *testing.T, file string, visibility ...v1alpha1.EndpointVisibility) map[string]map[string]any {
	t.Helper()
	input := progressiveInput(t, file, visibility[0])
	input.Visibility = visibility
	out, err := NewPipeline().Render(context.Background(), input)
	require.NoError(t, err)
	kinds := map[string]map[string]any{}
	for _, e := range out.Entries {
		kinds[e.Object["kind"].(string)] = e.Object
	}
	return kinds
}

func TestSampleType_MeshTypesServeGatewayAndMesh(t *testing.T) {
	for file, meshKind := range map[string]string{"istio-http.yaml": "VirtualService", "cilium-http.yaml": "CiliumEnvoyConfig"} {
		t.Run(file, func(t *testing.T) {
			both := renderKinds(t, file, v1alpha1.EndpointVisibilityExternal, v1alpha1.EndpointVisibilityNamespace)
			assert.Len(t, both, 2)
			assert.Contains(t, both, "HTTPRoute")
			assert.Contains(t, both, meshKind)

			external := renderKinds(t, file, v1alpha1.EndpointVisibilityExternal)
			assert.Equal(t, []string{"HTTPRoute"}, keys(external), "gateway callers only")

			for _, v := range []v1alpha1.EndpointVisibility{v1alpha1.EndpointVisibilityNamespace, v1alpha1.EndpointVisibilityInternal} {
				assert.Equal(t, []string{meshKind}, keys(renderKinds(t, file, v)), "in-cluster callers of %s", v)
			}
		})
	}
}

func TestSampleType_IstioHTTPSplitsTheSameInBothDirections(t *testing.T) {
	kinds := renderKinds(t, "istio-http.yaml", v1alpha1.EndpointVisibilityExternal, v1alpha1.EndpointVisibilityNamespace)

	rules := kinds["HTTPRoute"]["spec"].(map[string]any)["rules"].([]any)
	require.Len(t, rules, 3)
	assert.Equal(t, []any{
		map[string]any{"name": "greeter-dev-1a2b", "port": float64(8080), "weight": float64(90)},
		map[string]any{"name": "greeter-v2-dev-3c4d", "port": float64(8080), "weight": float64(10)},
	}, rules[2].(map[string]any)["backendRefs"])

	http := kinds["VirtualService"]["spec"].(map[string]any)["http"].([]any)
	require.Len(t, http, 3)
	route := http[2].(map[string]any)["route"].([]any)
	weights := make([]any, 0, len(route))
	for _, d := range route {
		weights = append(weights, d.(map[string]any)["weight"])
	}
	assert.Equal(t, []any{float64(90), float64(10)}, weights)
}

func TestSampleType_CiliumHTTPRendersRules(t *testing.T) {
	cec := renderKinds(t, "cilium-http.yaml", v1alpha1.EndpointVisibilityNamespace)["CiliumEnvoyConfig"]
	spec := cec["spec"].(map[string]any)
	assert.Len(t, spec["services"], 3, "callers of any backend's Service go through Envoy")

	resources := spec["resources"].([]any)
	require.Len(t, resources, 5, "a listener, its routes, and a cluster per backend")
	clusters := map[string]bool{}
	for _, r := range resources[2:] {
		clusters[r.(map[string]any)["name"].(string)] = true
	}

	vhost := resources[1].(map[string]any)["virtual_hosts"].([]any)[0].(map[string]any)
	routes := vhost["routes"].([]any)
	require.Len(t, routes, 3, "two match routes, then the default")

	testers := routes[0].(map[string]any)["match"].(map[string]any)
	assert.Equal(t, "/", testers["prefix"])
	assert.Equal(t, []any{map[string]any{"name": "x-canary", "string_match": map[string]any{"exact": "true"}}},
		testers["headers"])
	preview := routes[1].(map[string]any)["match"].(map[string]any)
	assert.Equal(t, map[string]any{"regex": `/next(/.*)?`}, preview["safe_regex"])
	assert.NotContains(t, preview, "prefix")

	def := routes[2].(map[string]any)["route"].(map[string]any)
	weighted := def["weighted_clusters"].(map[string]any)["clusters"].([]any)
	require.Len(t, weighted, 2, "the weight-0 shadow is not routed to")
	for _, c := range weighted {
		assert.True(t, clusters[c.(map[string]any)["name"].(string)], "routes name clusters the config defines")
	}
	assert.Equal(t, float64(90), weighted[0].(map[string]any)["weight"])
	mirrors := def["request_mirror_policies"].([]any)
	require.Len(t, mirrors, 1)
	assert.Equal(t, map[string]any{"default_value": map[string]any{"numerator": float64(10), "denominator": "HUNDRED"}},
		mirrors[0].(map[string]any)["runtime_fraction"])
	assert.True(t, clusters[mirrors[0].(map[string]any)["cluster"].(string)])
}

func keys(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
