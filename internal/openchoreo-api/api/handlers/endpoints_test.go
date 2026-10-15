// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	authzcore "github.com/openchoreo/openchoreo/internal/authz/core"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/api/gen"
	endpointsvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpoint"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/handlerservices"
)

const (
	testEPNs      = "test-ns"
	testEPProject = "test-project"
)

func newEndpointService(t *testing.T, objects []client.Object, pdp authzcore.PDP) endpointsvc.Service {
	t.Helper()
	// Bootstrap with a Project so project validation passes.
	bootstrap := make([]client.Object, 0, 1+len(objects))
	bootstrap = append(bootstrap, &openchoreov1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: testEPProject, Namespace: testEPNs},
	})
	bootstrap = append(bootstrap, objects...)

	fakeClient := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(bootstrap...).
		Build()
	return endpointsvc.NewServiceWithAuthz(fakeClient, pdp, slog.Default())
}

func newHandlerWithEndpointService(svc endpointsvc.Service) *Handler {
	return &Handler{
		services: &handlerservices.Services{EndpointService: svc},
		logger:   slog.Default(),
	}
}

// testEndpointObj routes to the given components with equal weight.
func testEndpointObj(name string, components ...string) *openchoreov1alpha1.Endpoint {
	refs := make([]openchoreov1alpha1.EndpointBackendRef, 0, len(components))
	for _, c := range components {
		refs = append(refs, openchoreov1alpha1.EndpointBackendRef{ComponentName: c, Endpoint: "http", Weight: ptr.To[int32](1)})
	}
	return &openchoreov1alpha1.Endpoint{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testEPNs},
		Spec: openchoreov1alpha1.EndpointSpec{
			Owner:       openchoreov1alpha1.EndpointOwner{ProjectName: testEPProject},
			Type:        openchoreov1alpha1.EndpointTypeRef{Kind: openchoreov1alpha1.EndpointTypeRefKindClusterEndpointType, Name: "http-weighted"},
			BackendRefs: refs,
		},
	}
}

func newEndpointBody(name string, weights ...int32) gen.Endpoint {
	refs := make([]gen.EndpointBackendRef, 0, len(weights))
	for i, w := range weights {
		refs = append(refs, gen.EndpointBackendRef{ComponentName: []string{"payments-v1", "payments-v2"}[i], Endpoint: "http", Weight: ptr.To(w)})
	}
	body := gen.Endpoint{
		Metadata: gen.ObjectMeta{Name: name},
		Spec: &gen.EndpointSpec{
			Type:        gen.EndpointTypeRef{Kind: ptr.To(gen.EndpointTypeRefKindClusterEndpointType), Name: "http-weighted"},
			BackendRefs: &refs,
		},
	}
	body.Spec.Owner.ProjectName = testEPProject
	return body
}

func TestListEndpointsHandler(t *testing.T) {
	ctx := testContext()
	objects := []client.Object{
		testEndpointObj("payments-api", "payments-v1", "payments-v2"),
		testEndpointObj("orders-api", "orders"),
	}

	t.Run("success - returns items", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, objects, &allowAllPDP{}))
		resp, err := h.ListEndpoints(ctx, gen.ListEndpointsRequestObject{NamespaceName: testEPNs})
		require.NoError(t, err)
		typed, ok := resp.(gen.ListEndpoints200JSONResponse)
		require.True(t, ok, "expected 200 response, got %T", resp)
		assert.Len(t, typed.Items, 2)
	})

	t.Run("filtered by backend component", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, objects, &allowAllPDP{}))
		resp, err := h.ListEndpoints(ctx, gen.ListEndpointsRequestObject{
			NamespaceName: testEPNs,
			Params:        gen.ListEndpointsParams{Component: ptr.To("payments-v2")},
		})
		require.NoError(t, err)
		typed, ok := resp.(gen.ListEndpoints200JSONResponse)
		require.True(t, ok, "expected 200 response, got %T", resp)
		require.Len(t, typed.Items, 1)
		assert.Equal(t, "payments-api", typed.Items[0].Metadata.Name)
	})

	t.Run("unknown project returns 404", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, objects, &allowAllPDP{}))
		resp, err := h.ListEndpoints(ctx, gen.ListEndpointsRequestObject{
			NamespaceName: testEPNs,
			Params:        gen.ListEndpointsParams{Project: ptr.To("missing")},
		})
		require.NoError(t, err)
		assert.IsType(t, gen.ListEndpoints404JSONResponse{}, resp)
	})

	t.Run("unauthorized items filtered out", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, objects, &denyAllPDP{}))
		resp, err := h.ListEndpoints(ctx, gen.ListEndpointsRequestObject{NamespaceName: testEPNs})
		require.NoError(t, err)
		typed, ok := resp.(gen.ListEndpoints200JSONResponse)
		require.True(t, ok, "expected 200 response, got %T", resp)
		assert.Empty(t, typed.Items)
	})
}

func TestCreateEndpointHandler(t *testing.T) {
	ctx := testContext()

	t.Run("success", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, nil, &allowAllPDP{}))
		body := newEndpointBody("payments-api", 90, 10)
		resp, err := h.CreateEndpoint(ctx, gen.CreateEndpointRequestObject{NamespaceName: testEPNs, Body: &body})
		require.NoError(t, err)
		typed, ok := resp.(gen.CreateEndpoint201JSONResponse)
		require.True(t, ok, "expected 201 response, got %T", resp)
		assert.Equal(t, "payments-api", typed.Metadata.Name)
		require.NotNil(t, typed.Spec)
		require.NotNil(t, typed.Spec.BackendRefs)
		require.Len(t, *typed.Spec.BackendRefs, 2)
		assert.Equal(t, int32(10), *(*typed.Spec.BackendRefs)[1].Weight)
	})

	t.Run("nil body returns 400", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, nil, &allowAllPDP{}))
		resp, err := h.CreateEndpoint(ctx, gen.CreateEndpointRequestObject{NamespaceName: testEPNs})
		require.NoError(t, err)
		assert.IsType(t, gen.CreateEndpoint400JSONResponse{}, resp)
	})

	t.Run("unknown project returns 400", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, nil, &allowAllPDP{}))
		body := newEndpointBody("payments-api", 100)
		body.Spec.Owner.ProjectName = "missing"
		resp, err := h.CreateEndpoint(ctx, gen.CreateEndpointRequestObject{NamespaceName: testEPNs, Body: &body})
		require.NoError(t, err)
		assert.IsType(t, gen.CreateEndpoint400JSONResponse{}, resp)
	})

	t.Run("already exists returns 409", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, []client.Object{testEndpointObj("payments-api", "payments-v1")}, &allowAllPDP{}))
		body := newEndpointBody("payments-api", 100)
		resp, err := h.CreateEndpoint(ctx, gen.CreateEndpointRequestObject{NamespaceName: testEPNs, Body: &body})
		require.NoError(t, err)
		assert.IsType(t, gen.CreateEndpoint409JSONResponse{}, resp)
	})

	t.Run("forbidden returns 403", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, nil, &denyAllPDP{}))
		body := newEndpointBody("payments-api", 100)
		resp, err := h.CreateEndpoint(ctx, gen.CreateEndpointRequestObject{NamespaceName: testEPNs, Body: &body})
		require.NoError(t, err)
		assert.IsType(t, gen.CreateEndpoint403JSONResponse{}, resp)
	})
}

func TestGetEndpointHandler(t *testing.T) {
	ctx := testContext()

	t.Run("success", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, []client.Object{testEndpointObj("payments-api", "payments-v1")}, &allowAllPDP{}))
		resp, err := h.GetEndpoint(ctx, gen.GetEndpointRequestObject{NamespaceName: testEPNs, EndpointName: "payments-api"})
		require.NoError(t, err)
		typed, ok := resp.(gen.GetEndpoint200JSONResponse)
		require.True(t, ok, "expected 200 response, got %T", resp)
		assert.Equal(t, "payments-api", typed.Metadata.Name)
	})

	t.Run("not found returns 404", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, nil, &allowAllPDP{}))
		resp, err := h.GetEndpoint(ctx, gen.GetEndpointRequestObject{NamespaceName: testEPNs, EndpointName: "missing"})
		require.NoError(t, err)
		assert.IsType(t, gen.GetEndpoint404JSONResponse{}, resp)
	})

	t.Run("forbidden returns 403", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, []client.Object{testEndpointObj("payments-api", "payments-v1")}, &denyAllPDP{}))
		resp, err := h.GetEndpoint(ctx, gen.GetEndpointRequestObject{NamespaceName: testEPNs, EndpointName: "payments-api"})
		require.NoError(t, err)
		assert.IsType(t, gen.GetEndpoint403JSONResponse{}, resp)
	})
}

func TestUpdateEndpointHandler(t *testing.T) {
	ctx := testContext()

	t.Run("success - changes the split", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, []client.Object{testEndpointObj("payments-api", "payments-v1", "payments-v2")}, &allowAllPDP{}))
		body := newEndpointBody("ignored", 50, 50)
		resp, err := h.UpdateEndpoint(ctx, gen.UpdateEndpointRequestObject{NamespaceName: testEPNs, EndpointName: "payments-api", Body: &body})
		require.NoError(t, err)
		typed, ok := resp.(gen.UpdateEndpoint200JSONResponse)
		require.True(t, ok, "expected 200 response, got %T", resp)
		assert.Equal(t, "payments-api", typed.Metadata.Name, "the path name wins over the body's")
		require.NotNil(t, typed.Spec.BackendRefs)
		assert.Equal(t, int32(50), *(*typed.Spec.BackendRefs)[0].Weight)
	})

	t.Run("project change returns 400", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, []client.Object{testEndpointObj("payments-api", "payments-v1")}, &allowAllPDP{}))
		body := newEndpointBody("payments-api", 100)
		body.Spec.Owner.ProjectName = "other-project"
		resp, err := h.UpdateEndpoint(ctx, gen.UpdateEndpointRequestObject{NamespaceName: testEPNs, EndpointName: "payments-api", Body: &body})
		require.NoError(t, err)
		assert.IsType(t, gen.UpdateEndpoint400JSONResponse{}, resp)
	})

	t.Run("not found returns 404", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, nil, &allowAllPDP{}))
		body := newEndpointBody("missing", 100)
		resp, err := h.UpdateEndpoint(ctx, gen.UpdateEndpointRequestObject{NamespaceName: testEPNs, EndpointName: "missing", Body: &body})
		require.NoError(t, err)
		assert.IsType(t, gen.UpdateEndpoint404JSONResponse{}, resp)
	})

	t.Run("forbidden returns 403", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, []client.Object{testEndpointObj("payments-api", "payments-v1")}, &denyAllPDP{}))
		body := newEndpointBody("payments-api", 100)
		resp, err := h.UpdateEndpoint(ctx, gen.UpdateEndpointRequestObject{NamespaceName: testEPNs, EndpointName: "payments-api", Body: &body})
		require.NoError(t, err)
		assert.IsType(t, gen.UpdateEndpoint403JSONResponse{}, resp)
	})
}

func TestDeleteEndpointHandler(t *testing.T) {
	ctx := testContext()

	t.Run("success", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, []client.Object{testEndpointObj("payments-api", "payments-v1")}, &allowAllPDP{}))
		resp, err := h.DeleteEndpoint(ctx, gen.DeleteEndpointRequestObject{NamespaceName: testEPNs, EndpointName: "payments-api"})
		require.NoError(t, err)
		assert.IsType(t, gen.DeleteEndpoint204Response{}, resp)
	})

	t.Run("not found returns 404", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, nil, &allowAllPDP{}))
		resp, err := h.DeleteEndpoint(ctx, gen.DeleteEndpointRequestObject{NamespaceName: testEPNs, EndpointName: "missing"})
		require.NoError(t, err)
		assert.IsType(t, gen.DeleteEndpoint404JSONResponse{}, resp)
	})

	t.Run("forbidden returns 403", func(t *testing.T) {
		h := newHandlerWithEndpointService(newEndpointService(t, []client.Object{testEndpointObj("payments-api", "payments-v1")}, &denyAllPDP{}))
		resp, err := h.DeleteEndpoint(ctx, gen.DeleteEndpointRequestObject{NamespaceName: testEPNs, EndpointName: "payments-api"})
		require.NoError(t, err)
		assert.IsType(t, gen.DeleteEndpoint403JSONResponse{}, resp)
	})
}
