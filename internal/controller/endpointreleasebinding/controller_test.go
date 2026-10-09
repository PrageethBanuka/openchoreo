// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointreleasebinding

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
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

func newRelease(name string, refs ...openchoreov1alpha1.EndpointBackendRef) *openchoreov1alpha1.EndpointRelease {
	return &openchoreov1alpha1.EndpointRelease{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: openchoreov1alpha1.EndpointReleaseSpec{
			Owner: openchoreov1alpha1.EndpointReleaseOwner{ProjectName: testProject, EndpointName: testEP},
			EndpointType: openchoreov1alpha1.EndpointReleaseEndpointType{
				Kind: openchoreov1alpha1.EndpointTypeRefKindClusterEndpointType,
				Name: "http-weighted",
				Spec: weightedRouteSpec(),
			},
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

// componentBinding is a deployed component's ReleaseBinding exposing an HTTP
// workload endpoint "api" on port 8080.
func componentBinding(component string) *openchoreov1alpha1.ReleaseBinding {
	return &openchoreov1alpha1.ReleaseBinding{
		ObjectMeta: metav1.ObjectMeta{Name: component + "-" + testEnvName, Namespace: testNS},
		Spec: openchoreov1alpha1.ReleaseBindingSpec{
			Owner:       openchoreov1alpha1.ReleaseBindingOwner{ProjectName: testProject, ComponentName: component},
			Environment: testEnvName,
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

// baseObjects are the environment, data plane, project and endpoint every
// binding renders against.
func baseObjects() []client.Object {
	return []client.Object{
		&openchoreov1alpha1.Environment{
			ObjectMeta: metav1.ObjectMeta{Name: testEnvName, Namespace: testNS},
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
		Build()
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
		{ComponentName: "payments-v1", Endpoint: "api", Weight: ptr.To[int32](90)},
		{ComponentName: "payments-v2", Endpoint: "api", Weight: ptr.To[int32](10)},
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
		Expect(routeBackendRefs(rr)).To(MatchJSON(`[
			{"name":"payments-v1","port":8080,"weight":90},
			{"name":"payments-v2","port":8080,"weight":10}
		]`))

		By("reporting each backend's resolution")
		Expect(b.Status.Backends).To(HaveLen(2))
		Expect(b.Status.Backends[0].Resolved).To(BeTrue())
		Expect(b.Status.Backends[0].Weight).To(Equal(int32(90)))
		Expect(b.Status.Backends[0].ServiceURL.Host).To(Equal("payments-v1." + dpNamespace + ".svc.cluster.local"))

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
		rr.Status.Resources = []openchoreov1alpha1.RenderedManifestStatus{{
			ID: "route", Kind: "HTTPRoute", HealthStatus: openchoreov1alpha1.HealthStatusHealthy,
		}}
		Expect(cli.Status().Update(bctx, rr)).To(Succeed())

		b = reconcileBinding()
		Expect(condition(b, string(ConditionRoutesReady)).Status).To(Equal(metav1.ConditionTrue))
		Expect(condition(b, string(ConditionReady)).Status).To(Equal(metav1.ConditionTrue))
	})

	It("reports RoutesDegraded when the data plane marks the route degraded", func() {
		setup(newRelease("rel-1", refs...), newBinding("rel-1"),
			componentBinding("payments-v1"), componentBinding("payments-v2"))

		b := reconcileBinding()
		rr := getRenderedRelease(b)
		rr.Status.Resources = []openchoreov1alpha1.RenderedManifestStatus{{
			ID: "route", Kind: "HTTPRoute", HealthStatus: openchoreov1alpha1.HealthStatusDegraded,
		}}
		Expect(cli.Status().Update(bctx, rr)).To(Succeed())

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
			openchoreov1alpha1.EndpointBackendRef{ComponentName: "payments-v1", Endpoint: "api", Weight: ptr.To[int32](100)},
			openchoreov1alpha1.EndpointBackendRef{ComponentName: "payments-v2", Endpoint: "api", Weight: ptr.To[int32](0)},
		), newBinding("rel-1"), componentBinding("payments-v1"))

		b := reconcileBinding()
		Expect(condition(b, string(ConditionBackendsResolved)).Status).To(Equal(metav1.ConditionTrue))
		Expect(routeBackendRefs(getRenderedRelease(b))).To(MatchJSON(`[{"name":"payments-v1","port":8080,"weight":100}]`))
		Expect(b.Status.Backends[1].Resolved).To(BeFalse())
	})

	It("reports an endpoint that the component does not expose", func() {
		setup(newRelease("rel-1", openchoreov1alpha1.EndpointBackendRef{ComponentName: "payments-v1", Endpoint: "admin"}),
			newBinding("rel-1"), componentBinding("payments-v1"))

		b := reconcileBinding()
		Expect(condition(b, string(ConditionBackendsResolved)).Status).To(Equal(metav1.ConditionFalse))
		Expect(b.Status.Backends[0].Reason).To(ContainSubstring(`endpoint "admin" of component "payments-v1" is not available`))
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
				Type:        openchoreov1alpha1.EndpointTypeRef{Name: "http-weighted"},
				BackendRefs: []openchoreov1alpha1.EndpointBackendRef{{ComponentName: "payments-v1", Endpoint: "api"}},
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
