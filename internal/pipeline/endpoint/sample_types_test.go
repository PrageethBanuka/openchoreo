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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"github.com/openchoreo/openchoreo/api/v1alpha1"
)

var (
	// meshSamplesDir holds the mesh EndpointType samples rendered by these tests.
	meshSamplesDir = filepath.Join("..", "..", "..", "samples", "mesh-endpoint-types")

	// httpSample is the getting-started http EndpointType.
	httpSample = filepath.Join("..", "..", "..", "samples", "getting-started", "cluster-endpoint-types", "http.yaml")
)

const sampleNS = "dp-default-shop-dev-a1b2c3d"

// loadClusterEndpointType reads a ClusterEndpointType sample.
func loadClusterEndpointType(t *testing.T, path string) *v1alpha1.EndpointTypeSpec {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var cet v1alpha1.ClusterEndpointType
	require.NoError(t, yaml.UnmarshalStrict(data, &cet))
	spec := v1alpha1.EndpointTypeSpec(cet.Spec)
	return &spec
}

// sampleBackends are the trips rewrite at 90/10, plus a weight-0 shadow.
func sampleBackends() []Backend {
	backend := func(name string, weight int32) Backend {
		svc := "e-trips-api-dev-" + name
		return Backend{
			Name: name, Kind: "Component", ComponentName: name, Endpoint: "http", Protocol: "HTTP",
			PodSelector: map[string]string{"openchoreo.dev/component-uid": name + "-uid", "openchoreo.dev/environment-uid": "dev-uid"},
			TargetPort:  8080, ServiceName: svc, Namespace: sampleNS, Port: 80, Weight: weight, BasePath: "/",
			ServiceURL: "http://" + svc + "." + sampleNS + ".svc.cluster.local:80",
		}
	}
	return []Backend{backend("trips-python", 90), backend("trips-go", 10), backend("trips-shadow", 0)}
}

// sampleRules are a header rule, a cookie rule under /beta, and the default
// rule that splits 90/10 and mirrors 10% to the shadow, resolved as the
// binding controller does.
func sampleRules() []Rule {
	b := sampleBackends()
	// Built as the controller does, so the templates get Conditions; the
	// header is written in mixed case and renders lowercase.
	header := NewMatch(nil, []HeaderMatch{{Name: "X-Canary", Value: "true"}}, nil)
	cookie := NewMatch(&PathMatch{Value: "/beta"}, nil, []CookieMatch{{Name: "ab", Value: "b"}})
	single := b[1]
	single.Weight = 1
	return []Rule{
		{Name: "testers", Matches: []Match{header}, Backends: []Backend{single}, Mirrors: []Mirror{}},
		{Name: "arm-b", Matches: []Match{cookie}, Backends: []Backend{single}, Mirrors: []Mirror{}},
		{Name: "default", Matches: []Match{}, Backends: b, Mirrors: []Mirror{{Backend: b[2], Percent: 10}}},
	}
}

func sampleMetadata() MetadataContext {
	return MetadataContext{
		Name:              "e-trips-api-dev",
		Namespace:         sampleNS,
		EndpointNamespace: "default",
		EndpointName:      "trips-api",
		ProjectName:       "rides",
		EnvironmentName:   "development",
		Labels:            map[string]string{"openchoreo.dev/project": "rides"},
	}
}

func sampleDataPlane(annotations map[string]string) DataPlaneContext {
	return BuildDataPlaneContext(&v1alpha1.DataPlane{ObjectMeta: metav1.ObjectMeta{Annotations: annotations}})
}

func sampleEnvironment() EnvironmentContext {
	return BuildEnvironmentContext(nil, &v1alpha1.DataPlane{
		Spec: v1alpha1.DataPlaneSpec{
			Gateway: v1alpha1.GatewaySpec{
				Ingress: &v1alpha1.GatewayNetworkSpec{
					External: &v1alpha1.GatewayEndpointSpec{
						Name: "gateway-default", Namespace: "openchoreo-data-plane",
						HTTP: &v1alpha1.GatewayListenerSpec{Port: 19080, Host: "openchoreoapis.localhost"},
					},
				},
			},
		},
	})
}

// renderSample renders a sample type for the trips Endpoint.
func renderSample(t *testing.T, path string, visibility []string, annotations map[string]string, rules []Rule) (*RenderOutput, error) {
	t.Helper()
	return NewPipeline().Render(context.Background(), &RenderInput{
		EndpointTypeSpec: loadClusterEndpointType(t, path),
		Visibility:       visibility,
		Backends:         sampleBackends(),
		Rules:            rules,
		Metadata:         sampleMetadata(),
		DataPlane:        sampleDataPlane(annotations),
		Environment:      sampleEnvironment(),
	})
}

// byID indexes rendered entries by ID.
func byID(out *RenderOutput) map[string]map[string]any {
	m := make(map[string]map[string]any, len(out.Entries))
	for _, e := range out.Entries {
		m[e.ID] = e.Object
	}
	return m
}

func specOf(obj map[string]any) map[string]any {
	return obj["spec"].(map[string]any)
}

var (
	external = []string{"external"}
	istio    = map[string]string{"openchoreo.dev/service-mesh": "istio"}
)

// meshRules is the mesh route of the trips Endpoint: the testers' header, the
// cookie arm under /beta, then the 90/10 default rule with a mirror.
const meshRules = `[
	{"matches":[{"headers":[{"type":"Exact","name":"x-canary","value":"true"}]}],
	 "backendRefs":[{"name":"e-trips-api-dev-trips-go","port":80,"weight":1}]},
	{"matches":[{"path":{"type":"PathPrefix","value":"/beta"},"headers":[{"type":"RegularExpression","name":"cookie","value":"(.*;\\s*)?ab=b(;.*)?"}]}],
	 "backendRefs":[{"name":"e-trips-api-dev-trips-go","port":80,"weight":1}]},
	{"filters":[{"type":"RequestMirror","requestMirror":{"backendRef":{"name":"e-trips-api-dev-trips-shadow","port":80},"percent":10}}],
	 "backendRefs":[{"name":"e-trips-api-dev-trips-python","port":80,"weight":90},{"name":"e-trips-api-dev-trips-go","port":80,"weight":10}]}
]`

func TestHTTP_ServicesAndGatewayRouteWithoutMesh(t *testing.T) {
	out, err := renderSample(t, httpSample, external, nil, sampleRules())
	require.NoError(t, err)
	entries := byID(out)
	assert.NotContains(t, entries, "mesh-route", "no mesh on the data plane")

	stable := entries["stable-service-0"]
	require.NotNil(t, stable)
	assert.Equal(t, "trips-api", stable["metadata"].(map[string]any)["name"])
	assert.JSONEq(t, `{"openchoreo.dev/component-uid":"trips-python-uid","openchoreo.dev/environment-uid":"dev-uid"}`,
		jsonOf(t, specOf(stable)["selector"]), "the stable Service selects the primary backend")
	assert.JSONEq(t, `[{"name":"http","port":80,"targetPort":8080,"appProtocol":"http"}]`, jsonOf(t, specOf(stable)["ports"]))

	for i, name := range []string{"trips-python", "trips-go", "trips-shadow"} {
		svc := entries["backend-service-"+string(rune('0'+i))]
		require.NotNil(t, svc, name)
		assert.Equal(t, "e-trips-api-dev-"+name, svc["metadata"].(map[string]any)["name"])
	}

	policy := entries["gateway-ingress-0"]
	require.NotNil(t, policy, "the gateway may reach backends that declare no visibility themselves")
	assert.JSONEq(t, `[{"from":[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"openchoreo-data-plane"}}}],
		"ports":[{"port":8080,"protocol":"TCP"}]}]`, jsonOf(t, specOf(policy)["ingress"]))

	route := entries["gateway-route-0"]
	require.NotNil(t, route)
	assert.Equal(t, "e-trips-api-dev-external", route["metadata"].(map[string]any)["name"])
	assert.JSONEq(t, `["development-default.openchoreoapis.localhost"]`, jsonOf(t, specOf(route)["hostnames"]))
	path := `"path":{"type":"PathPrefix","value":"/trips-api"}`
	rewrite := `{"type":"URLRewrite","urlRewrite":{"path":{"type":"ReplacePrefixMatch","replacePrefixMatch":"/"}}}`
	assert.JSONEq(t, `[
		{"matches":[{`+path+`,"headers":[{"type":"Exact","name":"x-canary","value":"true"}]}],
		 "filters":[`+rewrite+`],
		 "backendRefs":[{"name":"e-trips-api-dev-trips-go","port":80,"weight":1}]},
		{"matches":[{"path":{"type":"PathPrefix","value":"/trips-api/beta"},"headers":[{"type":"RegularExpression","name":"cookie","value":"(.*;\\s*)?ab=b(;.*)?"}]}],
		 "filters":[{"type":"URLRewrite","urlRewrite":{"path":{"type":"ReplacePrefixMatch","replacePrefixMatch":"/beta"}}}],
		 "backendRefs":[{"name":"e-trips-api-dev-trips-go","port":80,"weight":1}]},
		{"matches":[{`+path+`}],
		 "filters":[`+rewrite+`,{"type":"RequestMirror","requestMirror":{"backendRef":{"name":"e-trips-api-dev-trips-shadow","port":80},"percent":10}}],
		 "backendRefs":[{"name":"e-trips-api-dev-trips-python","port":80,"weight":90},{"name":"e-trips-api-dev-trips-go","port":80,"weight":10}]}
	]`, jsonOf(t, specOf(route)["rules"]), "the weight-0 shadow is only mirrored to")
}

func TestHTTP_OneSplitForGatewayAndMesh(t *testing.T) {
	t.Run("gateway outside the mesh: both routes split the same way", func(t *testing.T) {
		out, err := renderSample(t, httpSample, external, istio, sampleRules())
		require.NoError(t, err)
		entries := byID(out)
		mesh := entries["mesh-route"]
		require.NotNil(t, mesh)
		assert.JSONEq(t, `[{"group":"","kind":"Service","name":"trips-api","port":80}]`, jsonOf(t, specOf(mesh)["parentRefs"]))
		assert.JSONEq(t, meshRules, jsonOf(t, specOf(mesh)["rules"]))

		gw := specOf(entries["gateway-route-0"])["rules"].([]any)
		require.Len(t, gw, 3)
		last := gw[2].(map[string]any)
		assert.JSONEq(t, `[{"name":"e-trips-api-dev-trips-python","port":80,"weight":90},{"name":"e-trips-api-dev-trips-go","port":80,"weight":10}]`,
			jsonOf(t, last["backendRefs"]), "the gateway carries the mesh route's weights")
	})

	t.Run("gateway in the mesh: the gateway forwards and only the mesh splits", func(t *testing.T) {
		out, err := renderSample(t, httpSample, external,
			map[string]string{"openchoreo.dev/service-mesh": "istio", "openchoreo.dev/gateway-in-mesh": "true"}, sampleRules())
		require.NoError(t, err)
		entries := byID(out)
		assert.JSONEq(t, `[{
			"matches":[{"path":{"type":"PathPrefix","value":"/trips-api"}}],
			"filters":[{"type":"URLRewrite","urlRewrite":{"path":{"type":"ReplacePrefixMatch","replacePrefixMatch":"/"}}}],
			"backendRefs":[{"name":"trips-api","port":80}]
		}]`, jsonOf(t, specOf(entries["gateway-route-0"])["rules"]))
		assert.JSONEq(t, meshRules, jsonOf(t, specOf(entries["mesh-route"])["rules"]))
	})
}

func TestHTTP_Visibility(t *testing.T) {
	out, err := renderSample(t, httpSample, []string{"namespace"}, istio, sampleRules())
	require.NoError(t, err)
	entries := byID(out)
	assert.NotContains(t, entries, "gateway-route-0", "namespace visibility has no gateway route")
	assert.NotContains(t, entries, "gateway-ingress-0", "nor a gateway NetworkPolicy")
	assert.Contains(t, entries, "mesh-route")

	_, err = renderSample(t, httpSample, []string{"internal"}, nil, sampleRules())
	require.Error(t, err, "the environment has no internal gateway")
	assert.Contains(t, err.Error(), "no gateway for one of the Endpoint's visibilities")
}

func TestHTTP_LinkerdRefusesMirrors(t *testing.T) {
	_, err := renderSample(t, httpSample, external, map[string]string{"openchoreo.dev/service-mesh": "linkerd"}, sampleRules())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Linkerd does not support request mirroring")
}

var namespaceOnly = []string{"namespace"}

func TestMeshGamma_RulesAndMirror(t *testing.T) {
	out, err := renderSample(t, filepath.Join(meshSamplesDir, "mesh-gamma.yaml"), namespaceOnly,
		map[string]string{"openchoreo.dev/service-mesh": "cilium"}, sampleRules())
	require.NoError(t, err)
	route := byID(out)["mesh-route"]
	assert.JSONEq(t, `[{"group":"","kind":"Service","name":"trips-api","port":80}]`, jsonOf(t, specOf(route)["parentRefs"]))
	assert.JSONEq(t, meshRules, jsonOf(t, specOf(route)["rules"]))
	assert.Contains(t, byID(out), "stable-service-0")
}

func TestMeshSamples_Refusals(t *testing.T) {
	for _, file := range []string{"mesh-gamma.yaml", "istio-virtualservice.yaml", "cilium-cec.yaml"} {
		t.Run(file+": no mesh", func(t *testing.T) {
			_, err := renderSample(t, filepath.Join(meshSamplesDir, file), namespaceOnly, nil, sampleRules())
			if file == "istio-virtualservice.yaml" {
				// Installing the type is the opt-in; it reads no annotation.
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "openchoreo.dev/service-mesh")
		})
		t.Run(file+": gateway visibility", func(t *testing.T) {
			mesh := map[string]string{"mesh-gamma.yaml": "istio", "istio-virtualservice.yaml": "istio", "cilium-cec.yaml": "cilium"}[file]
			_, err := renderSample(t, filepath.Join(meshSamplesDir, file), external,
				map[string]string{"openchoreo.dev/service-mesh": mesh}, sampleRules())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "serves in-cluster callers only")
		})
	}
	_, err := renderSample(t, filepath.Join(meshSamplesDir, "mesh-gamma.yaml"), namespaceOnly,
		map[string]string{"openchoreo.dev/service-mesh": "linkerd"}, sampleRules())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Linkerd does not support request mirroring")
}

func TestIstioVirtualService_RulesAndMirror(t *testing.T) {
	out, err := renderSample(t, filepath.Join(meshSamplesDir, "istio-virtualservice.yaml"), namespaceOnly, istio, sampleRules())
	require.NoError(t, err)
	vs := byID(out)["virtual-service"]
	assert.Equal(t, "VirtualService", vs["kind"])
	assert.JSONEq(t, `["trips-api.`+sampleNS+`.svc.cluster.local"]`, jsonOf(t, specOf(vs)["hosts"]))

	dest := func(name string) string {
		return `{"host":"e-trips-api-dev-` + name + `.` + sampleNS + `.svc.cluster.local","port":{"number":80}}`
	}
	assert.JSONEq(t, `[
		{"name":"testers","match":[{"headers":{"x-canary":{"exact":"true"}}}],
		 "route":[{"destination":`+dest("trips-go")+`,"weight":1}]},
		{"name":"arm-b","match":[{"uri":{"regex":"/beta(/.*)?"},"headers":{"cookie":{"regex":"(.*;\\s*)?ab=b(;.*)?"}}}],
		 "route":[{"destination":`+dest("trips-go")+`,"weight":1}]},
		{"name":"default",
		 "route":[{"destination":`+dest("trips-python")+`,"weight":90},{"destination":`+dest("trips-go")+`,"weight":10}],
		 "mirrors":[{"destination":`+dest("trips-shadow")+`,"percentage":{"value":10}}]}
	]`, jsonOf(t, specOf(vs)["http"]))
}

func TestCiliumEnvoyConfig_RulesAndMirror(t *testing.T) {
	out, err := renderSample(t, filepath.Join(meshSamplesDir, "cilium-cec.yaml"), namespaceOnly,
		map[string]string{"openchoreo.dev/service-mesh": "cilium"}, sampleRules())
	require.NoError(t, err)
	cec := byID(out)["envoy-config"]
	assert.Equal(t, "CiliumEnvoyConfig", cec["kind"])
	annotations := cec["metadata"].(map[string]any)["annotations"].(map[string]any)
	assert.Equal(t, "false", annotations["cec.cilium.io/use-original-source-address"])

	spec := specOf(cec)
	assert.JSONEq(t, `[{"name":"trips-api","namespace":"`+sampleNS+`"}]`, jsonOf(t, spec["services"]),
		"the stable Service is redirected to Envoy")
	resources := spec["resources"].([]any)
	require.Len(t, resources, 5, "a listener, a route configuration and one cluster per backend")
	cluster := func(name string) string { return sampleNS + "/e-trips-api-dev-" + name }
	routes := resources[1].(map[string]any)["virtual_hosts"].([]any)[0].(map[string]any)["routes"]
	assert.JSONEq(t, `[
		{"match":{"prefix":"/","headers":[{"name":"x-canary","string_match":{"exact":"true"}}]},
		 "route":{"weighted_clusters":{"clusters":[{"name":"`+cluster("trips-go")+`","weight":1}]}}},
		{"match":{"safe_regex":{"regex":"/beta(/.*)?"},"headers":[{"name":"cookie","string_match":{"safe_regex":{"regex":"(.*;\\s*)?ab=b(;.*)?"}}}]},
		 "route":{"weighted_clusters":{"clusters":[{"name":"`+cluster("trips-go")+`","weight":1}]}}},
		{"match":{"prefix":"/"},
		 "route":{
		   "weighted_clusters":{"clusters":[{"name":"`+cluster("trips-python")+`","weight":90},{"name":"`+cluster("trips-go")+`","weight":10}]},
		   "request_mirror_policies":[{"cluster":"`+cluster("trips-shadow")+`","runtime_fraction":{"default_value":{"numerator":10,"denominator":"HUNDRED"}}}]}}
	]`, jsonOf(t, routes))
}

// TestIstioVirtualService_PortalRollouts renders the rules the portal writes
// for each strategy: lowercase header keys (Istio requires them), the A/B
// cookie plus an app header in one match, and an escaped cookie value.
func TestIstioVirtualService_PortalRollouts(t *testing.T) {
	rules := sampleRules()
	rules[1].Matches = []Match{NewMatch(
		nil,
		[]HeaderMatch{{Name: "X-Client", Value: "ios"}},
		[]CookieMatch{{Name: "exp_cartrecs", Value: "b.1"}},
	)}
	out, err := renderSample(t, filepath.Join(meshSamplesDir, "istio-virtualservice.yaml"), namespaceOnly, nil, rules)
	require.NoError(t, err)
	http := specOf(byID(out)["virtual-service"])["http"].([]any)
	assert.JSONEq(t, `[{"headers":{
		"x-client":{"exact":"ios"},
		"cookie":{"regex":"(.*;\\s*)?exp_cartrecs=b\\.1(;.*)?"}}}]`,
		jsonOf(t, http[1].(map[string]any)["match"]))
}

func TestHTTP_ExactPathMatch(t *testing.T) {
	rules := sampleRules()
	rules[1].Matches = []Match{NewMatch(&PathMatch{Type: PathMatchExact, Value: "/health"}, nil, nil)}
	out, err := renderSample(t, httpSample, external, nil, rules)
	require.NoError(t, err)
	rule := specOf(byID(out)["gateway-route-0"])["rules"].([]any)[1].(map[string]any)
	assert.JSONEq(t, `[{"path":{"type":"Exact","value":"/trips-api/health"}}]`, jsonOf(t, rule["matches"]))
	assert.JSONEq(t, `[{"type":"URLRewrite","urlRewrite":{"path":{"type":"ReplaceFullPath","replaceFullPath":"/health"}}}]`,
		jsonOf(t, rule["filters"]), "an exact path is rewritten whole, keeping the matched path under the backend's base path")
}

func TestHTTP_RequestTimeoutPerEnvironment(t *testing.T) {
	out, err := NewPipeline().Render(context.Background(), &RenderInput{
		EndpointTypeSpec:   loadClusterEndpointType(t, httpSample),
		Visibility:         external,
		Backends:           sampleBackends(),
		Rules:              sampleRules(),
		EnvironmentConfigs: &runtime.RawExtension{Raw: []byte(`{"requestTimeout":"5s"}`)},
		Metadata:           sampleMetadata(),
		DataPlane:          sampleDataPlane(istio),
		Environment:        sampleEnvironment(),
	})
	require.NoError(t, err)
	entries := byID(out)
	for _, id := range []string{"gateway-route-0", "mesh-route"} {
		for _, r := range specOf(entries[id])["rules"].([]any) {
			assert.JSONEq(t, `{"request":"5s"}`, jsonOf(t, r.(map[string]any)["timeouts"]), id)
		}
	}

	out, err = renderSample(t, httpSample, external, nil, sampleRules())
	require.NoError(t, err)
	for _, r := range specOf(byID(out)["gateway-route-0"])["rules"].([]any) {
		assert.NotContains(t, r.(map[string]any), "timeouts", "unset leaves the gateway default")
	}
}
