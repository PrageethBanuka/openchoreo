// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointreleasebinding

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
	"github.com/openchoreo/openchoreo/internal/controller/renderedrelease"
	dpkubernetes "github.com/openchoreo/openchoreo/internal/dataplane/kubernetes"
	"github.com/openchoreo/openchoreo/internal/labels"
	endpointpipeline "github.com/openchoreo/openchoreo/internal/pipeline/endpoint"
)

const (
	testNS      = "default"
	testProject = "shop"
	testEnvName = "dev"
	testEP      = "payments-api"
)

// dpNamespace is the data-plane namespace components and endpoints of
// (testNS, testProject, testEnvName) render into.
var dpNamespace = dpkubernetes.GenerateK8sNameWithLengthLimit(
	dpkubernetes.MaxNamespaceNameLength, "dp", testNS, testProject, testEnvName)

func mustRaw(v any) *runtime.RawExtension {
	b, err := json.Marshal(v)
	Expect(err).NotTo(HaveOccurred())
	return &runtime.RawExtension{Raw: b}
}

// weightedRouteSpec is an EndpointType spec rendering one weighted HTTPRoute
// on the external gateway.
func weightedRouteSpec() openchoreov1alpha1.EndpointTypeSpec {
	return openchoreov1alpha1.EndpointTypeSpec{
		Resources: []openchoreov1alpha1.ResourceTemplate{{
			ID: "route",
			Template: mustRaw(map[string]any{
				"apiVersion": "gateway.networking.k8s.io/v1",
				"kind":       "HTTPRoute",
				"metadata": map[string]any{
					"name":      "${metadata.name}",
					"namespace": "${metadata.namespace}",
					"labels":    map[string]any{"openchoreo.dev/endpoint-visibility": "external"},
				},
				"spec": map[string]any{
					"parentRefs": []any{map[string]any{"name": "${gateway.ingress.external.name}"}},
					"hostnames":  []any{"payments.shop.example.com"},
					"rules": []any{map[string]any{
						"matches":     []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/pay"}}},
						"backendRefs": `${backends.map(b, {"name": b.serviceName, "port": b.port, "weight": b.weight})}`,
					}},
				},
			}),
		}},
	}
}

// svcName is the Service the Endpoint renders for a backend in testEnvName.
func svcName(backend string) string {
	return dpkubernetes.GenerateK8sName(dpkubernetes.GenerateK8sName("e_"+testEP, testEnvName), backend)
}

// newRelease builds an external release, so the claim tests have a component
// route to suppress.
func newRelease(name string, refs ...openchoreov1alpha1.EndpointBackendRef) *openchoreov1alpha1.EndpointRelease {
	return &openchoreov1alpha1.EndpointRelease{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: openchoreov1alpha1.EndpointReleaseSpec{
			Owner: openchoreov1alpha1.EndpointReleaseOwner{ProjectName: testProject, EndpointName: testEP},
			EndpointType: openchoreov1alpha1.EndpointReleaseEndpointType{
				Kind: openchoreov1alpha1.EndpointTypeRefKindClusterEndpointType,
				Name: "http",
				Spec: weightedRouteSpec(),
			},
			Visibility:  []openchoreov1alpha1.EndpointVisibility{openchoreov1alpha1.EndpointVisibilityExternal},
			BackendRefs: refs,
		},
	}
}

func newBinding(release string) *openchoreov1alpha1.EndpointReleaseBinding {
	return &openchoreov1alpha1.EndpointReleaseBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testEP + "-" + testEnvName,
			Namespace:  testNS,
			Finalizers: []string{EndpointReleaseBindingFinalizer},
		},
		Spec: openchoreov1alpha1.EndpointReleaseBindingSpec{
			Owner:           openchoreov1alpha1.EndpointReleaseBindingOwner{ProjectName: testProject, EndpointName: testEP},
			Environment:     testEnvName,
			EndpointRelease: release,
		},
	}
}

// componentBinding is a deployed component's ReleaseBinding, running its
// release (see componentObjects) with HTTP workload endpoint "api".
func componentBinding(component string) *openchoreov1alpha1.ReleaseBinding {
	return &openchoreov1alpha1.ReleaseBinding{
		ObjectMeta: metav1.ObjectMeta{Name: component + "-" + testEnvName, Namespace: testNS},
		Spec: openchoreov1alpha1.ReleaseBindingSpec{
			Owner:       openchoreov1alpha1.ReleaseBindingOwner{ProjectName: testProject, ComponentName: component},
			Environment: testEnvName,
			ReleaseName: component + "-rel",
		},
		Status: openchoreov1alpha1.ReleaseBindingStatus{
			Endpoints: []openchoreov1alpha1.EndpointURLStatus{{
				Name: "api",
				Type: openchoreov1alpha1.EndpointProtocolHTTP,
				ServiceURL: &openchoreov1alpha1.EndpointURL{
					Scheme: "http",
					Host:   component + "." + dpNamespace + ".svc.cluster.local",
					Port:   8080,
				},
			}},
		},
	}
}

// componentObjects are a component and its release, whose workload endpoint
// "api" listens on 8080 behind port 80.
func componentObjects(component string) []client.Object {
	return []client.Object{
		&openchoreov1alpha1.Component{
			ObjectMeta: metav1.ObjectMeta{Name: component, Namespace: testNS, UID: types.UID(component + "-uid")},
			Spec:       openchoreov1alpha1.ComponentSpec{Owner: openchoreov1alpha1.ComponentOwner{ProjectName: testProject}},
		},
		&openchoreov1alpha1.ComponentRelease{
			ObjectMeta: metav1.ObjectMeta{Name: component + "-rel", Namespace: testNS},
			Spec: openchoreov1alpha1.ComponentReleaseSpec{
				Owner: openchoreov1alpha1.ComponentReleaseOwner{ProjectName: testProject, ComponentName: component},
				Workload: openchoreov1alpha1.WorkloadTemplateSpec{
					Endpoints: map[string]openchoreov1alpha1.WorkloadEndpoint{
						"api": {Type: openchoreov1alpha1.EndpointProtocolHTTP, Port: 80, TargetPort: 8080},
					},
				},
			},
		},
	}
}

// baseObjects are the environment, data plane, project, endpoint and
// components every binding renders against.
func baseObjects() []client.Object {
	objs := []client.Object{
		&openchoreov1alpha1.Environment{
			ObjectMeta: metav1.ObjectMeta{Name: testEnvName, Namespace: testNS, UID: "env-uid"},
			Spec: openchoreov1alpha1.EnvironmentSpec{
				DataPlaneRef: &openchoreov1alpha1.DataPlaneRef{Kind: openchoreov1alpha1.DataPlaneRefKindDataPlane, Name: "default"},
			},
		},
		&openchoreov1alpha1.DataPlane{
			ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: testNS},
			Spec: openchoreov1alpha1.DataPlaneSpec{
				Gateway: openchoreov1alpha1.GatewaySpec{
					Ingress: &openchoreov1alpha1.GatewayNetworkSpec{
						External: &openchoreov1alpha1.GatewayEndpointSpec{
							Name:      "gateway-default",
							Namespace: "openchoreo-data-plane",
							HTTP:      &openchoreov1alpha1.GatewayListenerSpec{Port: 19080, Host: "openchoreoapis.localhost"},
							HTTPS:     &openchoreov1alpha1.GatewayListenerSpec{Port: 19443, Host: "openchoreoapis.localhost"},
						},
					},
				},
			},
		},
		&openchoreov1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: testProject, Namespace: testNS}},
		&openchoreov1alpha1.Endpoint{ObjectMeta: metav1.ObjectMeta{Name: testEP, Namespace: testNS}},
	}
	return slices.Concat(objs, componentObjects("payments-v1"), componentObjects("payments-v2"), componentObjects("payments-v3"))
}

func buildClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(objs...).
		WithStatusSubresource(
			&openchoreov1alpha1.EndpointReleaseBinding{},
			&openchoreov1alpha1.ReleaseBinding{},
			&openchoreov1alpha1.RenderedRelease{},
		).
		WithIndex(&openchoreov1alpha1.ReleaseBinding{}, controller.IndexKeyReleaseBindingOwnerEnv,
			func(obj client.Object) []string {
				rb := obj.(*openchoreov1alpha1.ReleaseBinding)
				return []string{controller.MakeReleaseBindingOwnerEnvKey(
					rb.Spec.Owner.ProjectName, rb.Spec.Owner.ComponentName, rb.Spec.Environment)}
			}).
		WithIndex(&openchoreov1alpha1.Endpoint{}, controller.IndexKeyEndpointBackendComponent,
			controller.IndexEndpointBackendComponent).
		WithIndex(&openchoreov1alpha1.EndpointReleaseBinding{}, controller.IndexKeyEndpointReleaseBindingOwnerEnv,
			controller.IndexEndpointReleaseBindingOwnerEnv).
		WithIndex(&openchoreov1alpha1.EndpointReleaseBinding{}, endpointReleaseRefIndex, indexEndpointReleaseRef).
		WithInterceptorFuncs(renderedReleaseGenerations()).
		Build()
}

// renderedReleaseGenerations makes the fake client track metadata.generation
// for RenderedReleases the way the API server does: 1 on create, +1 on every
// spec change. The fake client leaves it at 0, which would hide whether the
// controller waits for the data plane to apply the generation it just wrote.
func renderedReleaseGenerations() interceptor.Funcs {
	return interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*openchoreov1alpha1.RenderedRelease); ok {
				obj.SetGeneration(1)
			}
			return c.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if rr, ok := obj.(*openchoreov1alpha1.RenderedRelease); ok {
				current := &openchoreov1alpha1.RenderedRelease{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(rr), current); err == nil {
					rr.SetGeneration(current.Generation)
					if !apiequality.Semantic.DeepEqual(current.Spec, rr.Spec) {
						rr.SetGeneration(current.Generation + 1)
					}
				}
			}
			return c.Update(ctx, obj, opts...)
		},
	}
}

var _ = Describe("EndpointReleaseBinding Controller", func() {
	bctx := context.Background()

	var (
		cli        client.Client
		reconciler *Reconciler
	)

	setup := func(objs ...client.Object) {
		cli = buildClient(append(baseObjects(), objs...)...)
		reconciler = &Reconciler{Client: cli, Scheme: scheme.Scheme, Pipeline: endpointpipeline.NewPipeline()}
	}

	reconcileBinding := func() *openchoreov1alpha1.EndpointReleaseBinding {
		key := types.NamespacedName{Name: testEP + "-" + testEnvName, Namespace: testNS}
		_, err := reconciler.Reconcile(bctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		out := &openchoreov1alpha1.EndpointReleaseBinding{}
		Expect(cli.Get(bctx, key, out)).To(Succeed())
		return out
	}

	getRenderedRelease := func(b *openchoreov1alpha1.EndpointReleaseBinding) *openchoreov1alpha1.RenderedRelease {
		rr := &openchoreov1alpha1.RenderedRelease{}
		Expect(cli.Get(bctx, types.NamespacedName{Name: makeRenderedReleaseName(b), Namespace: testNS}, rr)).To(Succeed())
		return rr
	}

	// markApplied records what the data-plane RenderedRelease controller writes
	// once it has applied a generation: ResourcesApplied=True for it, plus the
	// given per-resource health.
	markApplied := func(rr *openchoreov1alpha1.RenderedRelease, health openchoreov1alpha1.HealthStatus) {
		rr.Status.Conditions = []metav1.Condition{{
			Type: renderedrelease.ConditionResourcesApplied, Status: metav1.ConditionTrue,
			Reason: "ResourcesApplied", ObservedGeneration: rr.Generation, LastTransitionTime: metav1.Now(),
		}}
		rr.Status.Resources = []openchoreov1alpha1.RenderedManifestStatus{{
			ID: "route", Kind: "HTTPRoute", HealthStatus: health,
		}}
		Expect(cli.Status().Update(bctx, rr)).To(Succeed())
	}

	routeBackendRefs := func(rr *openchoreov1alpha1.RenderedRelease) string {
		Expect(rr.Spec.Resources).To(HaveLen(1))
		var route map[string]any
		Expect(json.Unmarshal(rr.Spec.Resources[0].Object.Raw, &route)).To(Succeed())
		rule := route["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)
		b, err := json.Marshal(rule["backendRefs"])
		Expect(err).NotTo(HaveOccurred())
		return string(b)
	}

	condition := func(b *openchoreov1alpha1.EndpointReleaseBinding, ct string) *metav1.Condition {
		c := meta.FindStatusCondition(b.Status.Conditions, ct)
		Expect(c).NotTo(BeNil(), "expected condition %s", ct)
		return c
	}

	refs := []openchoreov1alpha1.EndpointBackendRef{
		{Name: "payments-v1", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "api"}, Weight: ptr.To[int32](90)},
		{Name: "payments-v2", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v2", Endpoint: "api"}, Weight: ptr.To[int32](10)},
	}

	It("renders a weighted HTTPRoute, reports backends and URLs, and becomes Ready once the route is healthy", func() {
		setup(newRelease("rel-1", refs...), newBinding("rel-1"),
			componentBinding("payments-v1"), componentBinding("payments-v2"))

		b := reconcileBinding()

		By("rendering a RenderedRelease owned by the endpoint")
		rr := getRenderedRelease(b)
		Expect(rr.Spec.Owner).To(Equal(openchoreov1alpha1.RenderedReleaseOwner{ProjectName: testProject, EndpointName: testEP}))
		Expect(rr.Spec.TargetPlane).To(Equal(openchoreov1alpha1.TargetPlaneDataPlane))
		Expect(rr.Labels).To(HaveKeyWithValue(labels.LabelKeyEndpointResourceName, testEP))
		Expect(metav1.IsControlledBy(rr, b)).To(BeTrue())
		Expect(routeBackendRefs(rr)).To(MatchJSON(fmt.Sprintf(`[
			{"name":%q,"port":80,"weight":90},
			{"name":%q,"port":80,"weight":10}
		]`, svcName("payments-v1"), svcName("payments-v2"))))

		By("reporting each backend's resolution")
		Expect(b.Status.Backends).To(HaveLen(2))
		Expect(b.Status.Backends[0].Resolved).To(BeTrue())
		Expect(b.Status.Backends[0].Weight).To(Equal(int32(90)))
		Expect(b.Status.Backends[0].ServiceURL.Host).To(Equal(svcName("payments-v1")+"."+dpNamespace+".svc.cluster.local"),
			"the backend resolves to the Service the Endpoint renders for it, not the component's own")

		By("resolving gateway URLs from the rendered route")
		Expect(b.Status.ExternalURLs).NotTo(BeNil())
		Expect(b.Status.ExternalURLs.HTTPS.Host).To(Equal("payments.shop.example.com"))
		Expect(b.Status.ExternalURLs.HTTPS.Port).To(Equal(int32(19443)))
		Expect(b.Status.InvokeURL).To(Equal("https://payments.shop.example.com:19443/pay"))

		By("waiting for the data plane before reporting Ready")
		Expect(condition(b, string(ConditionBackendsResolved)).Status).To(Equal(metav1.ConditionTrue))
		Expect(condition(b, string(ConditionSynced)).Status).To(Equal(metav1.ConditionTrue))
		Expect(condition(b, string(ConditionRoutesReady)).Reason).To(Equal(string(ReasonRoutesProgressing)))
		Expect(condition(b, string(ConditionReady)).Status).To(Equal(metav1.ConditionFalse))

		By("becoming Ready once the route reports healthy")
		markApplied(rr, openchoreov1alpha1.HealthStatusHealthy)

		b = reconcileBinding()
		Expect(condition(b, string(ConditionRoutesReady)).Status).To(Equal(metav1.ConditionTrue))
		Expect(condition(b, string(ConditionReady)).Status).To(Equal(metav1.ConditionTrue))
	})

	It("passes each backend's pods, ports, own address and base path to the template", func() {
		rel := newRelease("rel-1", refs[0])
		rel.Spec.EndpointType.Spec.Resources = []openchoreov1alpha1.ResourceTemplate{{
			ID: "route",
			Template: mustRaw(map[string]any{
				"apiVersion": "gateway.networking.k8s.io/v1",
				"kind":       "HTTPRoute",
				"metadata":   map[string]any{"name": "${metadata.name}", "namespace": "${metadata.namespace}"},
				"spec": map[string]any{
					"hostnames": []any{"${backends[0].componentURLs.external.host}"},
					"rules": []any{map[string]any{
						"matches": []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "${backends[0].componentURLs.external.path}"}}},
						"filters": []any{map[string]any{"type": "URLRewrite", "urlRewrite": map[string]any{
							"path": map[string]any{"type": "ReplacePrefixMatch", "replacePrefixMatch": "${backends[0].basePath}"}}}},
					}},
				},
			}),
		}, {
			ID: "service",
			Template: mustRaw(map[string]any{
				"apiVersion": "v1",
				"kind":       "Service",
				"metadata":   map[string]any{"name": "${backends[0].serviceName}", "namespace": "${metadata.namespace}"},
				"spec": map[string]any{
					"selector": "${backends[0].podSelector}",
					"ports":    []any{map[string]any{"port": "${backends[0].port}", "targetPort": "${backends[0].targetPort}"}},
				},
			}),
		}}
		comp := componentBinding("payments-v1")
		comp.Status.Endpoints[0].ExternalURLs = &openchoreov1alpha1.EndpointGatewayURLs{
			HTTP: &openchoreov1alpha1.EndpointURL{Scheme: "http", Host: "development-default.openchoreoapis.localhost", Port: 19080, Path: "/payments-v1-http"},
		}
		setup(rel, newBinding("rel-1"), comp)

		b := reconcileBinding()
		var route map[string]any
		Expect(json.Unmarshal(getRenderedRelease(b).Spec.Resources[0].Object.Raw, &route)).To(Succeed())
		spec := route["spec"].(map[string]any)
		Expect(spec["hostnames"]).To(Equal([]any{"development-default.openchoreoapis.localhost"}))
		rule := spec["rules"].([]any)[0].(map[string]any)
		Expect(rule["matches"]).To(Equal([]any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/payments-v1-http"}}}))
		filter := rule["filters"].([]any)[0].(map[string]any)
		Expect(filter["urlRewrite"].(map[string]any)["path"].(map[string]any)["replacePrefixMatch"]).To(Equal("/"),
			"the base path defaults to / when the workload endpoint sets none")

		var svc map[string]any
		Expect(json.Unmarshal(getRenderedRelease(b).Spec.Resources[1].Object.Raw, &svc)).To(Succeed())
		Expect(svc["metadata"].(map[string]any)["name"]).To(Equal(svcName("payments-v1")))
		selector := svc["spec"].(map[string]any)["selector"].(map[string]any)
		Expect(selector).To(HaveKeyWithValue(labels.LabelKeyComponentUID, "payments-v1-uid"))
		Expect(selector).To(HaveKeyWithValue(labels.LabelKeyEnvironmentUID, "env-uid"))
		Expect(svc["spec"].(map[string]any)["ports"]).To(Equal([]any{map[string]any{"port": float64(80), "targetPort": float64(8080)}}))
	})

	It("reports RoutesDegraded when the data plane marks the route degraded", func() {
		setup(newRelease("rel-1", refs...), newBinding("rel-1"),
			componentBinding("payments-v1"), componentBinding("payments-v2"))

		b := reconcileBinding()
		rr := getRenderedRelease(b)
		markApplied(rr, openchoreov1alpha1.HealthStatusDegraded)

		b = reconcileBinding()
		routes := condition(b, string(ConditionRoutesReady))
		Expect(routes.Status).To(Equal(metav1.ConditionFalse))
		Expect(routes.Reason).To(Equal(string(ReasonRoutesDegraded)))
		Expect(routes.Message).To(ContainSubstring(`"route" (HTTPRoute) is degraded`))
		Expect(condition(b, string(ConditionReady)).Reason).To(Equal(string(ReasonRoutesDegraded)))
	})

	It("keeps the previous route when a weighted backend is not deployed in the environment", func() {
		setup(newRelease("rel-1", refs[0]), newRelease("rel-2", refs...), newBinding("rel-1"),
			componentBinding("payments-v1"))

		By("routing 100% to v1 from the first release")
		b := reconcileBinding()
		Expect(condition(b, string(ConditionReady)).Reason).NotTo(Equal(string(ReasonBackendsUnresolved)))
		before := routeBackendRefs(getRenderedRelease(b))

		By("pinning the 90/10 release while v2 is missing from the environment")
		b.Spec.EndpointRelease = "rel-2"
		Expect(cli.Update(bctx, b)).To(Succeed())
		b = reconcileBinding()

		Expect(routeBackendRefs(getRenderedRelease(b))).To(Equal(before), "the existing route must not change")

		cond := condition(b, string(ConditionBackendsResolved))
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(string(ReasonBackendsUnresolved)))
		Expect(cond.Message).To(ContainSubstring("payments-v2/api"))
		Expect(condition(b, string(ConditionReady)).Reason).To(Equal(string(ReasonBackendsUnresolved)))

		Expect(b.Status.Backends).To(HaveLen(2))
		Expect(b.Status.Backends[1].Resolved).To(BeFalse())
		Expect(b.Status.Backends[1].Reason).To(ContainSubstring(`component "payments-v2" is not deployed in environment "dev"`))

		By("rendering the split once v2 is deployed")
		Expect(cli.Create(bctx, componentBinding("payments-v2"))).To(Succeed())
		b = reconcileBinding()
		Expect(condition(b, string(ConditionBackendsResolved)).Status).To(Equal(metav1.ConditionTrue))
		Expect(routeBackendRefs(getRenderedRelease(b))).To(ContainSubstring(`"weight":10`))
	})

	It("does not block on an undeployed backend staged at weight 0", func() {
		setup(newRelease("rel-1",
			openchoreov1alpha1.EndpointBackendRef{Name: "payments-v1", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "api"}, Weight: ptr.To[int32](100)},
			openchoreov1alpha1.EndpointBackendRef{Name: "payments-v2", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v2", Endpoint: "api"}, Weight: ptr.To[int32](0)},
		), newBinding("rel-1"), componentBinding("payments-v1"))

		b := reconcileBinding()
		Expect(condition(b, string(ConditionBackendsResolved)).Status).To(Equal(metav1.ConditionTrue))
		Expect(routeBackendRefs(getRenderedRelease(b))).To(MatchJSON(fmt.Sprintf(`[{"name":%q,"port":80,"weight":100}]`, svcName("payments-v1"))))
		Expect(b.Status.Backends[1].Resolved).To(BeFalse())
	})

	It("reports an endpoint that the component does not expose", func() {
		setup(newRelease("rel-1", openchoreov1alpha1.EndpointBackendRef{Name: "payments-v1", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "admin"}}),
			newBinding("rel-1"), componentBinding("payments-v1"))

		b := reconcileBinding()
		Expect(condition(b, string(ConditionBackendsResolved)).Status).To(Equal(metav1.ConditionFalse))
		Expect(b.Status.Backends[0].Reason).To(ContainSubstring(`component "payments-v1" has no endpoint "admin"`))
	})

	It("waits with Synced=False when no release is pinned", func() {
		setup(newBinding(""))

		b := reconcileBinding()
		Expect(condition(b, string(ConditionSynced)).Reason).To(Equal(string(ReasonEndpointReleaseNotSet)))
		Expect(condition(b, string(ConditionReady)).Reason).To(Equal(string(ReasonEndpointReleaseNotSet)))
	})

	It("refuses a release cut for a different endpoint", func() {
		other := newRelease("rel-other", refs[0])
		other.Spec.Owner.EndpointName = "orders-api"
		setup(other, newBinding("rel-other"), componentBinding("payments-v1"))

		b := reconcileBinding()
		Expect(condition(b, string(ConditionSynced)).Reason).To(Equal(string(ReasonInvalidReleaseConfiguration)))
	})

	It("deletes the RenderedRelease before releasing its finalizer", func() {
		setup(newRelease("rel-1", refs[0]), newBinding("rel-1"), componentBinding("payments-v1"))
		b := reconcileBinding()
		getRenderedRelease(b)

		Expect(cli.Delete(bctx, b)).To(Succeed())
		reconcileBinding() // records Finalizing
		key := types.NamespacedName{Name: b.Name, Namespace: testNS}
		res, err := reconciler.Reconcile(bctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(requeueWaitForChild))

		rrKey := types.NamespacedName{Name: makeRenderedReleaseName(b), Namespace: testNS}
		Expect(apierrors.IsNotFound(cli.Get(bctx, rrKey, &openchoreov1alpha1.RenderedRelease{}))).To(BeTrue())

		_, err = reconciler.Reconcile(bctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(apierrors.IsNotFound(cli.Get(bctx, key, &openchoreov1alpha1.EndpointReleaseBinding{}))).To(BeTrue())
	})

	It("maps a backend component's ReleaseBinding to the bindings of Endpoints routing to it in that environment", func() {
		endpoint := &openchoreov1alpha1.Endpoint{
			ObjectMeta: metav1.ObjectMeta{Name: "checkout-api", Namespace: testNS},
			Spec: openchoreov1alpha1.EndpointSpec{
				Owner:       openchoreov1alpha1.EndpointOwner{ProjectName: testProject},
				Type:        openchoreov1alpha1.EndpointTypeRef{Name: "http"},
				BackendRefs: []openchoreov1alpha1.EndpointBackendRef{{Name: "payments-v1", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "api"}}},
			},
		}
		devBinding := newBinding("")
		devBinding.Name = "checkout-api-dev"
		devBinding.Spec.Owner.EndpointName = "checkout-api"
		prodBinding := devBinding.DeepCopy()
		prodBinding.Name = "checkout-api-prod"
		prodBinding.Spec.Environment = "prod"
		setup(endpoint, devBinding, prodBinding)

		reqs := reconciler.listBindingsForBackendReleaseBinding(bctx, componentBinding("payments-v1"))
		Expect(reqs).To(ConsistOf(reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "checkout-api-dev", Namespace: testNS},
		}))
	})

	Context("progressive delivery", func() {
		It("applies a binding's weight override without a new release", func() {
			binding := newBinding("rel-1")
			binding.Spec.BackendOverrides = []openchoreov1alpha1.EndpointBackendOverride{{Name: "payments-v2", Weight: ptr.To[int32](50)}}
			setup(newRelease("rel-1", refs...), binding, componentBinding("payments-v1"), componentBinding("payments-v2"))

			b := reconcileBinding()
			Expect(routeBackendRefs(getRenderedRelease(b))).To(MatchJSON(fmt.Sprintf(`[
				{"name":%q,"port":80,"weight":90},
				{"name":%q,"port":80,"weight":50}
			]`, svcName("payments-v1"), svcName("payments-v2"))))
			Expect(b.Status.Backends[1].Weight).To(Equal(int32(50)), "status reports the effective weight")
		})

		It("refuses an override that names a backend the release doesn't have", func() {
			binding := newBinding("rel-1")
			binding.Spec.BackendOverrides = []openchoreov1alpha1.EndpointBackendOverride{{Name: "payments-v9", Weight: ptr.To[int32](50)}}
			setup(newRelease("rel-1", refs...), binding, componentBinding("payments-v1"), componentBinding("payments-v2"))

			b := reconcileBinding()
			Expect(condition(b, string(ConditionSynced)).Reason).To(Equal(string(ReasonBackendOverrideInvalid)))
			Expect(condition(b, string(ConditionSynced)).Message).To(ContainSubstring("payments-v9"))
		})

		It("routes nothing when the overrides leave no backend with weight", func() {
			binding := newBinding("rel-1")
			binding.Spec.BackendOverrides = []openchoreov1alpha1.EndpointBackendOverride{
				{Name: "payments-v1", Weight: ptr.To[int32](0)},
				{Name: "payments-v2", Weight: ptr.To[int32](0)},
			}
			setup(newRelease("rel-1", refs...), binding, componentBinding("payments-v1"), componentBinding("payments-v2"))

			b := reconcileBinding()
			Expect(condition(b, string(ConditionBackendsResolved)).Status).To(Equal(metav1.ConditionFalse))
			Expect(condition(b, string(ConditionBackendsResolved)).Message).To(ContainSubstring("backendOverrides"))
		})

		// rulesRelease renders one route rule per context rule, listing its
		// backends by name with their weights, and its mirrors.
		rulesRelease := func(backendRefs ...openchoreov1alpha1.EndpointBackendRef) *openchoreov1alpha1.EndpointRelease {
			rel := newRelease("rel-1", backendRefs...)
			rel.Spec.EndpointType.Spec.Resources = []openchoreov1alpha1.ResourceTemplate{{
				ID: "route",
				Template: mustRaw(map[string]any{
					"apiVersion": "gateway.networking.k8s.io/v1",
					"kind":       "HTTPRoute",
					"metadata":   map[string]any{"name": "${metadata.name}", "namespace": "${metadata.namespace}"},
					"spec": map[string]any{
						"rules": `${rules.map(r, {"name": r.name, "matches": size(r.matches), ` +
							`"backends": r.backends.map(b, b.name + "=" + string(b.weight)), "mirrors": r.mirrors.map(m, m.backend.name + "=" + string(m.percent))})}`,
					},
				}),
			}}
			return rel
		}
		renderedRules := func(b *openchoreov1alpha1.EndpointReleaseBinding) string {
			var route map[string]any
			Expect(json.Unmarshal(getRenderedRelease(b).Spec.Resources[0].Object.Raw, &route)).To(Succeed())
			out, err := json.Marshal(route["spec"].(map[string]any)["rules"])
			Expect(err).NotTo(HaveOccurred())
			return string(out)
		}

		It("renders one default rule that splits the backends by weight", func() {
			binding := newBinding("rel-1")
			binding.Spec.BackendOverrides = []openchoreov1alpha1.EndpointBackendOverride{{Name: "payments-v2", Weight: ptr.To[int32](30)}}
			setup(rulesRelease(refs...), binding, componentBinding("payments-v1"), componentBinding("payments-v2"))
			b := reconcileBinding()
			Expect(renderedRules(b)).To(MatchJSON(`[
				{"name":"default","matches":0,"backends":["payments-v1=90","payments-v2=30"],"mirrors":[]}
			]`))
		})

		// withV2 returns refs with payments-v2 at weight 0, changed by edit.
		withV2 := func(edit func(*openchoreov1alpha1.EndpointBackendRef)) []openchoreov1alpha1.EndpointBackendRef {
			out := []openchoreov1alpha1.EndpointBackendRef{*refs[0].DeepCopy(), *refs[1].DeepCopy()}
			out[1].Weight = ptr.To[int32](0)
			edit(&out[1])
			return out
		}
		preview := withV2(func(r *openchoreov1alpha1.EndpointBackendRef) {
			r.Match = &openchoreov1alpha1.EndpointMatch{
				Headers: []openchoreov1alpha1.EndpointHeaderMatch{{Name: "X-Preview", Value: "v2"}},
			}
		})
		shadow := withV2(func(r *openchoreov1alpha1.EndpointBackendRef) {
			r.Mirror = &openchoreov1alpha1.EndpointMirror{Percent: 100}
		})

		It("sends a backend's matched requests to it alone, ahead of the weighted split", func() {
			binding := newBinding("rel-1")
			binding.Spec.BackendOverrides = []openchoreov1alpha1.EndpointBackendOverride{
				{Name: "payments-v1", Weight: ptr.To[int32](0)},
				{Name: "payments-v2", Weight: ptr.To[int32](100)},
			}
			setup(rulesRelease(preview...), binding, componentBinding("payments-v1"), componentBinding("payments-v2"))
			b := reconcileBinding()
			Expect(renderedRules(b)).To(MatchJSON(`[
				{"name":"payments-v2","matches":1,"backends":["payments-v2=1"],"mirrors":[]},
				{"name":"default","matches":0,"backends":["payments-v1=0","payments-v2=100"],"mirrors":[]}
			]`), "the overrides switch the split; the match keeps sending its requests to v2")
		})

		It("leaves out the match of a backend that is not deployed in the environment", func() {
			setup(rulesRelease(preview...), newBinding("rel-1"), componentBinding("payments-v1"))
			b := reconcileBinding()
			Expect(condition(b, string(ConditionBackendsResolved)).Status).To(Equal(metav1.ConditionTrue))
			Expect(renderedRules(b)).To(MatchJSON(`[
				{"name":"default","matches":0,"backends":["payments-v1=90"],"mirrors":[]}
			]`))
			Expect(b.Status.Backends[1].Resolved).To(BeFalse())
			Expect(b.Status.Backends[1].Reason).To(ContainSubstring("not deployed"))
		})

		It("copies the weighted split to a mirror backend, at the binding's share", func() {
			binding := newBinding("rel-1")
			binding.Spec.BackendOverrides = []openchoreov1alpha1.EndpointBackendOverride{
				{Name: "payments-v2", Mirror: &openchoreov1alpha1.EndpointMirror{Percent: 10}},
			}
			setup(rulesRelease(shadow...), binding, componentBinding("payments-v1"), componentBinding("payments-v2"))
			b := reconcileBinding()
			Expect(renderedRules(b)).To(MatchJSON(`[
				{"name":"default","matches":0,"backends":["payments-v1=90","payments-v2=0"],"mirrors":["payments-v2=10"]}
			]`))
			Expect(b.Status.Backends[1].Mirror).To(Equal(&openchoreov1alpha1.EndpointMirror{Percent: 10}))
		})

		It("refuses a mirror override on a backend that is not a mirror", func() {
			binding := newBinding("rel-1")
			binding.Spec.BackendOverrides = []openchoreov1alpha1.EndpointBackendOverride{
				{Name: "payments-v1", Mirror: &openchoreov1alpha1.EndpointMirror{Percent: 10}},
			}
			setup(rulesRelease(shadow...), binding, componentBinding("payments-v1"), componentBinding("payments-v2"))
			b := reconcileBinding()
			Expect(condition(b, string(ConditionSynced)).Reason).To(Equal(string(ReasonBackendOverrideInvalid)))
			Expect(condition(b, string(ConditionSynced)).Message).To(ContainSubstring("no mirror"))
		})

		It("reports no gateway URL for a mesh route", func() {
			rel := newRelease("rel-1", refs...)
			rel.Spec.EndpointType.Spec.Resources = []openchoreov1alpha1.ResourceTemplate{{
				ID: "route",
				Template: mustRaw(map[string]any{
					"apiVersion": "gateway.networking.k8s.io/v1",
					"kind":       "HTTPRoute",
					"metadata": map[string]any{"name": "${metadata.name}", "namespace": "${metadata.namespace}",
						"labels": map[string]any{"openchoreo.dev/endpoint-visibility": "external"}},
					"spec": map[string]any{
						"parentRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "${metadata.endpointName}", "port": 80}},
						"rules": []any{map[string]any{
							"backendRefs": `${backends.map(b, {"name": b.serviceName, "port": b.port, "weight": b.weight})}`,
						}},
					},
				}),
			}}
			setup(rel, newBinding("rel-1"), componentBinding("payments-v1"), componentBinding("payments-v2"))
			b := reconcileBinding()
			Expect(condition(b, string(ConditionSynced)).Status).To(Equal(metav1.ConditionTrue))
			Expect(b.Status.ExternalURLs).To(BeNil())
			Expect(b.Status.InvokeURL).To(BeEmpty())
		})
	})

	Context("serving claim", func() {
		// v1URL and v2URL are the slots of the releases' component backends.
		v1URL := openchoreov1alpha1.EndpointBackendSlot{ComponentName: "payments-v1", Endpoint: "api"}
		v2URL := openchoreov1alpha1.EndpointBackendSlot{ComponentName: "payments-v2", Endpoint: "api"}

		// claimFor reconciles the binding to an applied, healthy route and returns it.
		claimFor := func() *openchoreov1alpha1.EndpointReleaseBinding {
			b := reconcileBinding()
			markApplied(getRenderedRelease(b), openchoreov1alpha1.HealthStatusHealthy)
			return reconcileBinding()
		}

		// componentLegacyRR is a component's data-plane RenderedRelease carrying its
		// own external route for workload endpoint "api".
		componentLegacyRR := func(component string, health openchoreov1alpha1.HealthStatus) *openchoreov1alpha1.RenderedRelease {
			rr := &openchoreov1alpha1.RenderedRelease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      component + "-" + testEnvName,
					Namespace: testNS,
					Labels: map[string]string{
						labels.LabelKeyProjectName:     testProject,
						labels.LabelKeyComponentName:   component,
						labels.LabelKeyEnvironmentName: testEnvName,
					},
				},
				Spec: openchoreov1alpha1.RenderedReleaseSpec{
					Owner:           openchoreov1alpha1.RenderedReleaseOwner{ProjectName: testProject, ComponentName: component},
					EnvironmentName: testEnvName,
					TargetPlane:     openchoreov1alpha1.TargetPlaneDataPlane,
					Resources: []openchoreov1alpha1.RenderedManifest{{
						ID: "httproute-external",
						Object: mustRaw(map[string]any{
							"apiVersion": "gateway.networking.k8s.io/v1",
							"kind":       "HTTPRoute",
							"metadata": map[string]any{
								"name": component + "-api",
								"labels": map[string]any{
									"openchoreo.dev/endpoint-name":       "api",
									"openchoreo.dev/endpoint-visibility": "external",
								},
							},
						}),
					}},
				},
			}
			Expect(cli.Create(bctx, rr)).To(Succeed())
			rr.Status.Conditions = []metav1.Condition{{
				Type: renderedrelease.ConditionResourcesApplied, Status: metav1.ConditionTrue,
				Reason: "ResourcesApplied", ObservedGeneration: rr.Generation, LastTransitionTime: metav1.Now(),
			}}
			rr.Status.Resources = []openchoreov1alpha1.RenderedManifestStatus{{
				ID: "httproute-external", Kind: "HTTPRoute", HealthStatus: health,
			}}
			Expect(cli.Status().Update(bctx, rr)).To(Succeed())
			return rr
		}

		It("is set only once the data plane has applied and accepted the route", func() {
			setup(newRelease("rel-1", refs...), newBinding("rel-1"),
				componentBinding("payments-v1"), componentBinding("payments-v2"))

			b := reconcileBinding()
			Expect(b.Status.Serving).To(BeNil(), "no claim before the route is applied")

			b = claimFor()
			Expect(b.Status.Serving).NotTo(BeNil())
			Expect(b.Status.Serving.EndpointRelease).To(Equal("rel-1"))
			Expect(b.Status.Serving.URLs).To(Equal([]openchoreov1alpha1.EndpointBackendSlot{v1URL, v2URL}),
				"the claim covers every component backend")
			Expect(b.Status.Serving.Visibilities).To(Equal([]openchoreov1alpha1.EndpointVisibility{openchoreov1alpha1.EndpointVisibilityExternal}))
			Expect(b.Status.Serving.Releasing).To(BeFalse())
		})

		It("holds while a newer release cannot be routed, and moves once it is accepted", func() {
			setup(newRelease("rel-1", refs[0]), newRelease("rel-2", refs...), newBinding("rel-1"),
				componentBinding("payments-v1"))

			b := claimFor()
			Expect(b.Status.Serving.URLs).To(Equal([]openchoreov1alpha1.EndpointBackendSlot{v1URL}))
			since := b.Status.Serving.Since

			By("pinning rel-2 while payments-v2 is missing keeps the rel-1 claim")
			b.Spec.EndpointRelease = "rel-2"
			Expect(cli.Update(bctx, b)).To(Succeed())
			b = reconcileBinding()
			Expect(condition(b, string(ConditionBackendsResolved)).Status).To(Equal(metav1.ConditionFalse))
			Expect(b.Status.Serving.EndpointRelease).To(Equal("rel-1"))

			By("once payments-v2 is deployed the new route renders, but the claim waits for the data plane")
			Expect(cli.Create(bctx, componentBinding("payments-v2"))).To(Succeed())
			b = reconcileBinding()
			Expect(b.Status.Serving.EndpointRelease).To(Equal("rel-1"))

			By("the accepted rel-2 route replaces the claim and keeps its start time")
			markApplied(getRenderedRelease(b), openchoreov1alpha1.HealthStatusHealthy)
			b = reconcileBinding()
			Expect(b.Status.Serving.EndpointRelease).To(Equal("rel-2"))
			Expect(b.Status.Serving.URLs).To(Equal([]openchoreov1alpha1.EndpointBackendSlot{v1URL, v2URL}))
			Expect(b.Status.Serving.Since).To(Equal(since))
		})

		It("hands slots back before removing its route on deletion", func() {
			setup(newRelease("rel-1", refs[0]), newBinding("rel-1"), componentBinding("payments-v1"))
			b := claimFor()
			key := types.NamespacedName{Name: b.Name, Namespace: testNS}
			rrKey := types.NamespacedName{Name: makeRenderedReleaseName(b), Namespace: testNS}

			reconcileOnce := func() {
				_, err := reconciler.Reconcile(bctx, reconcile.Request{NamespacedName: key})
				Expect(err).NotTo(HaveOccurred())
			}

			By("the component still reports the Endpoint as routing its endpoint")
			comp := &openchoreov1alpha1.ReleaseBinding{}
			Expect(cli.Get(bctx, types.NamespacedName{Name: "payments-v1-" + testEnvName, Namespace: testNS}, comp)).To(Succeed())
			comp.Status.Endpoints[0].RoutedBy = []openchoreov1alpha1.EndpointRouteOwner{{EndpointName: testEP}}
			Expect(cli.Status().Update(bctx, comp)).To(Succeed())

			Expect(cli.Delete(bctx, b)).To(Succeed())
			reconcileOnce() // Finalizing
			reconcileOnce() // claim marked releasing

			b = &openchoreov1alpha1.EndpointReleaseBinding{}
			Expect(cli.Get(bctx, key, b)).To(Succeed())
			Expect(b.Status.Serving.Releasing).To(BeTrue())

			By("the route stays while the component has not re-rendered its own")
			reconcileOnce()
			Expect(cli.Get(bctx, rrKey, &openchoreov1alpha1.RenderedRelease{})).To(Succeed())
			Expect(cli.Get(bctx, key, b)).To(Succeed())
			Expect(condition(b, string(ConditionFinalizing)).Reason).To(Equal(string(ReasonReleasingClaims)))

			By("and while the component's own route is not healthy yet")
			Expect(cli.Get(bctx, types.NamespacedName{Name: "payments-v1-" + testEnvName, Namespace: testNS}, comp)).To(Succeed())
			comp.Status.Endpoints[0].RoutedBy = nil
			Expect(cli.Status().Update(bctx, comp)).To(Succeed())
			legacy := componentLegacyRR("payments-v1", openchoreov1alpha1.HealthStatusProgressing)
			reconcileOnce()
			Expect(cli.Get(bctx, rrKey, &openchoreov1alpha1.RenderedRelease{})).To(Succeed())

			By("once it is healthy, the Endpoint route is removed and the binding goes away")
			legacy.Status.Resources[0].HealthStatus = openchoreov1alpha1.HealthStatusHealthy
			Expect(cli.Status().Update(bctx, legacy)).To(Succeed())
			reconcileOnce()
			Expect(apierrors.IsNotFound(cli.Get(bctx, rrKey, &openchoreov1alpha1.RenderedRelease{}))).To(BeTrue())
			reconcileOnce()
			Expect(apierrors.IsNotFound(cli.Get(bctx, key, &openchoreov1alpha1.EndpointReleaseBinding{}))).To(BeTrue())
		})

		It("stops waiting after the release timeout", func() {
			b := newBinding("rel-1")
			b.DeletionTimestamp = &metav1.Time{Time: time.Now().Add(-claimReleaseTimeout - time.Minute)}
			b.Status.Conditions = []metav1.Condition{NewFinalizingCondition(0)}
			b.Status.Serving = &openchoreov1alpha1.EndpointServingClaim{
				EndpointRelease: "rel-1",
				URLs:            []openchoreov1alpha1.EndpointBackendSlot{v1URL},
				Visibilities:    []openchoreov1alpha1.EndpointVisibility{openchoreov1alpha1.EndpointVisibilityExternal},
				Since:           metav1.Now(),
				Releasing:       true,
			}
			comp := componentBinding("payments-v1")
			comp.Status.Endpoints[0].RoutedBy = []openchoreov1alpha1.EndpointRouteOwner{{EndpointName: testEP}}
			rr := &openchoreov1alpha1.RenderedRelease{
				ObjectMeta: metav1.ObjectMeta{Name: makeRenderedReleaseName(b), Namespace: testNS},
				Spec: openchoreov1alpha1.RenderedReleaseSpec{
					Owner:           openchoreov1alpha1.RenderedReleaseOwner{ProjectName: testProject, EndpointName: testEP},
					EnvironmentName: testEnvName,
					TargetPlane:     openchoreov1alpha1.TargetPlaneDataPlane,
				},
			}
			setup(b, comp, rr)

			_, err := reconciler.Reconcile(bctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: b.Name, Namespace: testNS}})
			Expect(err).NotTo(HaveOccurred())
			Expect(apierrors.IsNotFound(cli.Get(bctx, client.ObjectKeyFromObject(rr), &openchoreov1alpha1.RenderedRelease{}))).
				To(BeTrue(), "the Endpoint route is removed even though the component never re-rendered")
		})
	})
})

var _ = Describe("RenderedRelease owner validation", func() {
	It("rejects a RenderedRelease owned by both a component and an endpoint", func() {
		rr := &openchoreov1alpha1.RenderedRelease{
			ObjectMeta: metav1.ObjectMeta{Name: "rr-two-owners", Namespace: testNS},
			Spec: openchoreov1alpha1.RenderedReleaseSpec{
				Owner: openchoreov1alpha1.RenderedReleaseOwner{
					ProjectName: testProject, ComponentName: "payments-v1", EndpointName: testEP,
				},
				EnvironmentName: testEnvName,
				TargetPlane:     openchoreov1alpha1.TargetPlaneDataPlane,
			},
		}
		err := k8sClient.Create(ctx, rr)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("at most one of componentName, resourceName or endpointName may be set"))
	})

	It("accepts a RenderedRelease owned by an endpoint", func() {
		rr := &openchoreov1alpha1.RenderedRelease{
			ObjectMeta: metav1.ObjectMeta{Name: "rr-endpoint-owner", Namespace: testNS},
			Spec: openchoreov1alpha1.RenderedReleaseSpec{
				Owner:           openchoreov1alpha1.RenderedReleaseOwner{ProjectName: testProject, EndpointName: testEP},
				EnvironmentName: testEnvName,
				TargetPlane:     openchoreov1alpha1.TargetPlaneDataPlane,
			},
		}
		Expect(k8sClient.Create(ctx, rr)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, rr) })
	})
})
