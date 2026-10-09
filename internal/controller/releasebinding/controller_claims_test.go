// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package releasebinding

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
)

var externalOnly = []openchoreov1alpha1.EndpointVisibility{openchoreov1alpha1.EndpointVisibilityExternal}

func claimsTestEndpoint(project string, components ...string) *openchoreov1alpha1.Endpoint {
	ep := &openchoreov1alpha1.Endpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-api", Namespace: "default"},
		Spec: openchoreov1alpha1.EndpointSpec{
			Owner: openchoreov1alpha1.EndpointOwner{ProjectName: project},
			Type:  openchoreov1alpha1.EndpointTypeRef{Name: "http-weighted"},
		},
	}
	for _, c := range components {
		ep.Spec.BackendRefs = append(ep.Spec.BackendRefs, openchoreov1alpha1.EndpointBackendRef{ComponentName: c, Endpoint: "http"})
	}
	return ep
}

func claimsTestBinding(env string, serving *openchoreov1alpha1.EndpointServingClaim) *openchoreov1alpha1.EndpointReleaseBinding {
	return &openchoreov1alpha1.EndpointReleaseBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-api-" + env, Namespace: "default"},
		Spec: openchoreov1alpha1.EndpointReleaseBindingSpec{
			Owner:       openchoreov1alpha1.EndpointReleaseBindingOwner{ProjectName: "shop", EndpointName: "payments-api"},
			Environment: env,
		},
		Status: openchoreov1alpha1.EndpointReleaseBindingStatus{Serving: serving},
	}
}

func servingClaim(releasing bool, components ...string) *openchoreov1alpha1.EndpointServingClaim {
	claim := &openchoreov1alpha1.EndpointServingClaim{
		EndpointRelease: "rel-1",
		Visibilities:    externalOnly,
		Since:           metav1.Now(),
		Releasing:       releasing,
	}
	for _, c := range components {
		claim.Backends = append(claim.Backends, openchoreov1alpha1.EndpointBackendSlot{ComponentName: c, Endpoint: "http"})
	}
	return claim
}

func claimsTestReconciler(t *testing.T, objs ...client.Object) *Reconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, openchoreov1alpha1.AddToScheme(scheme))
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&openchoreov1alpha1.EndpointReleaseBinding{}).
		WithIndex(&openchoreov1alpha1.ReleaseBinding{}, controller.IndexKeyReleaseBindingOwnerEnv,
			func(obj client.Object) []string {
				rb := obj.(*openchoreov1alpha1.ReleaseBinding)
				return []string{controller.MakeReleaseBindingOwnerEnvKey(
					rb.Spec.Owner.ProjectName, rb.Spec.Owner.ComponentName, rb.Spec.Environment)}
			}).
		Build()
	return &Reconciler{Client: c, Scheme: scheme}
}

func componentRB(component, env string) *openchoreov1alpha1.ReleaseBinding {
	return &openchoreov1alpha1.ReleaseBinding{
		ObjectMeta: metav1.ObjectMeta{Name: component + "-" + env, Namespace: "default"},
		Spec: openchoreov1alpha1.ReleaseBindingSpec{
			Owner:       openchoreov1alpha1.ReleaseBindingOwner{ProjectName: "shop", ComponentName: component},
			Environment: env,
		},
	}
}

func TestClaimedEndpoints(t *testing.T) {
	ctx := context.Background()
	rb := componentRB("payments-v1", "dev")

	t.Run("serving claim on this component is returned", func(t *testing.T) {
		r := claimsTestReconciler(t,
			claimsTestEndpoint("shop", "payments-v1", "payments-v2"),
			claimsTestBinding("dev", servingClaim(false, "payments-v1", "payments-v2")),
		)
		claims, err := r.claimedEndpoints(ctx, rb)
		require.NoError(t, err)
		require.Len(t, claims["http"], 1)
		assert.Equal(t, "payments-api", claims["http"][0].endpointName)
		assert.Equal(t, externalOnly, claims["http"][0].visibilities)
	})

	cases := []struct {
		name string
		objs []client.Object
	}{
		{
			name: "binding without a serving claim yet",
			objs: []client.Object{
				claimsTestEndpoint("shop", "payments-v1"),
				claimsTestBinding("dev", nil),
			},
		},
		{
			name: "releasing claim hands the slot back",
			objs: []client.Object{
				claimsTestEndpoint("shop", "payments-v1"),
				claimsTestBinding("dev", servingClaim(true, "payments-v1")),
			},
		},
		{
			name: "claim in another environment",
			objs: []client.Object{
				claimsTestEndpoint("shop", "payments-v1"),
				claimsTestBinding("prod", servingClaim(false, "payments-v1")),
			},
		},
		{
			name: "claim that serves only another component",
			objs: []client.Object{
				claimsTestEndpoint("shop", "payments-v1", "payments-v2"),
				claimsTestBinding("dev", servingClaim(false, "payments-v2")),
			},
		},
		{
			name: "Endpoint of another project",
			objs: []client.Object{
				claimsTestEndpoint("other-project", "payments-v1"),
				claimsTestBinding("dev", servingClaim(false, "payments-v1")),
			},
		},
	}
	for _, tc := range cases {
		t.Run("no claim: "+tc.name, func(t *testing.T) {
			r := claimsTestReconciler(t, tc.objs...)
			claims, err := r.claimedEndpoints(ctx, rb)
			require.NoError(t, err)
			assert.Empty(t, claims)
		})
	}
}

func route(kind, endpointName, visibility string) map[string]any {
	return map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       kind,
		"metadata": map[string]any{
			"name": endpointName + "-" + visibility,
			"labels": map[string]any{
				"openchoreo.dev/endpoint-name":       endpointName,
				"openchoreo.dev/endpoint-visibility": visibility,
			},
		},
	}
}

func TestDropClaimedRoutes(t *testing.T) {
	service := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "payments-v1"}}
	resources := []map[string]any{
		service,
		route("HTTPRoute", "http", "external"),
		route("HTTPRoute", "http", "internal"),
		route("HTTPRoute", "admin", "external"),
	}
	claims := map[string][]endpointClaim{
		"http": {{endpointName: "payments-api", visibilities: externalOnly}},
	}

	kept := dropClaimedRoutes(resources, claims)

	names := make([]string, 0, len(kept))
	for _, res := range kept {
		names = append(names, res["metadata"].(map[string]any)["name"].(string))
	}
	assert.Equal(t, []string{"payments-v1", "http-internal", "admin-external"}, names,
		"only the claimed endpoint's external route is dropped")
	assert.Len(t, resources, 4, "input is not modified")

	assert.Equal(t, resources, dropClaimedRoutes(resources, nil), "no claims keeps everything")
}

func TestApplyClaimsToStatus(t *testing.T) {
	ownExternal := &openchoreov1alpha1.EndpointGatewayURLs{
		HTTP: &openchoreov1alpha1.EndpointURL{Scheme: "http", Host: "development-default.example.com", Port: 19080, Path: "/payments-v1-http"},
	}
	statuses := []openchoreov1alpha1.EndpointURLStatus{
		{Name: "http", ExternalURLs: ownExternal, ServiceURL: &openchoreov1alpha1.EndpointURL{Host: "payments-v1.ns.svc.cluster.local", Port: 8080}},
		{Name: "admin", RoutedBy: []openchoreov1alpha1.EndpointRouteOwner{{EndpointName: "stale"}}},
	}
	claims := map[string][]endpointClaim{
		"http": {{endpointName: "payments-api", visibilities: externalOnly}},
	}

	got := applyClaimsToStatus(statuses, claims)

	assert.Equal(t, []openchoreov1alpha1.EndpointRouteOwner{{EndpointName: "payments-api", Visibilities: externalOnly}}, got[0].RoutedBy)
	assert.Equal(t, ownExternal, got[0].ExternalURLs,
		"the component keeps reporting its own address; the Endpoint serves that same address")
	assert.NotNil(t, got[0].ServiceURL, "serviceURL is untouched")
	assert.Nil(t, got[1].RoutedBy, "a slot no longer claimed loses routedBy")
}

func TestReleaseBindingsForEndpointClaim(t *testing.T) {
	r := claimsTestReconciler(t, componentRB("payments-v1", "dev"), componentRB("payments-v1", "prod"), componentRB("payments-v2", "dev"))
	erb := claimsTestBinding("dev", servingClaim(false, "payments-v1"))

	reqs := r.releaseBindingsForEndpointClaim(context.Background(), erb)
	require.Len(t, reqs, 1)
	assert.Equal(t, "payments-v1-dev", reqs[0].Name)

	assert.Empty(t, r.releaseBindingsForEndpointClaim(context.Background(), claimsTestBinding("dev", nil)))
}
