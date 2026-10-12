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
	endpointreleasebindingsvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointreleasebinding"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/handlerservices"
)

const testERBEndpoint = "payments-api"

func newEndpointReleaseBindingService(t *testing.T, objects []client.Object, pdp authzcore.PDP) endpointreleasebindingsvc.Service {
	t.Helper()
	// Bootstrap with an Endpoint so create-time owner validation passes.
	bootstrap := make([]client.Object, 0, 1+len(objects))
	bootstrap = append(bootstrap, testEndpointObj(testERBEndpoint, "payments-v1"))
	bootstrap = append(bootstrap, objects...)

	fakeClient := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(bootstrap...).
		Build()
	return endpointreleasebindingsvc.NewServiceWithAuthz(fakeClient, pdp, slog.Default())
}

func newHandlerWithEndpointReleaseBindingService(svc endpointreleasebindingsvc.Service) *Handler {
	return &Handler{
		services: &handlerservices.Services{EndpointReleaseBindingService: svc},
		logger:   slog.Default(),
	}
}

func testEndpointReleaseBindingObj(name, environment string) *openchoreov1alpha1.EndpointReleaseBinding {
	return &openchoreov1alpha1.EndpointReleaseBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testEPNs},
		Spec: openchoreov1alpha1.EndpointReleaseBindingSpec{
			Owner:       openchoreov1alpha1.EndpointReleaseBindingOwner{ProjectName: testEPProject, EndpointName: testERBEndpoint},
			Environment: environment,
		},
	}
}

func newERBBody(name, project string) gen.EndpointReleaseBinding {
	return gen.EndpointReleaseBinding{
		Metadata: gen.ObjectMeta{Name: name},
		Spec: &gen.EndpointReleaseBindingSpec{
			Owner:           gen.EndpointReleaseOwner{ProjectName: project, EndpointName: testERBEndpoint},
			Environment:     "development",
			EndpointRelease: ptr.To("payments-api-5b7fc7b"),
		},
	}
}

func TestListEndpointReleaseBindingsHandler(t *testing.T) {
	ctx := testContext()
	objects := []client.Object{
		testEndpointReleaseBindingObj("payments-api-development", "development"),
		testEndpointReleaseBindingObj("payments-api-staging", "staging"),
	}

	t.Run("filtered by endpoint and environment", func(t *testing.T) {
		h := newHandlerWithEndpointReleaseBindingService(newEndpointReleaseBindingService(t, objects, &allowAllPDP{}))
		resp, err := h.ListEndpointReleaseBindings(ctx, gen.ListEndpointReleaseBindingsRequestObject{
			NamespaceName: testEPNs,
			Params:        gen.ListEndpointReleaseBindingsParams{Endpoint: ptr.To(testERBEndpoint), Environment: ptr.To("staging")},
		})
		require.NoError(t, err)
		typed, ok := resp.(gen.ListEndpointReleaseBindings200JSONResponse)
		require.True(t, ok, "expected 200 response, got %T", resp)
		require.Len(t, typed.Items, 1)
		assert.Equal(t, "payments-api-staging", typed.Items[0].Metadata.Name)
	})

	t.Run("unauthorized items filtered out", func(t *testing.T) {
		h := newHandlerWithEndpointReleaseBindingService(newEndpointReleaseBindingService(t, objects, &denyAllPDP{}))
		resp, err := h.ListEndpointReleaseBindings(ctx, gen.ListEndpointReleaseBindingsRequestObject{NamespaceName: testEPNs})
		require.NoError(t, err)
		typed, ok := resp.(gen.ListEndpointReleaseBindings200JSONResponse)
		require.True(t, ok, "expected 200 response, got %T", resp)
		assert.Empty(t, typed.Items)
	})
}

func TestCreateEndpointReleaseBindingHandler(t *testing.T) {
	ctx := testContext()

	t.Run("success", func(t *testing.T) {
		h := newHandlerWithEndpointReleaseBindingService(newEndpointReleaseBindingService(t, nil, &allowAllPDP{}))
		body := newERBBody("payments-api-development", testEPProject)
		resp, err := h.CreateEndpointReleaseBinding(ctx, gen.CreateEndpointReleaseBindingRequestObject{NamespaceName: testEPNs, Body: &body})
		require.NoError(t, err)
		typed, ok := resp.(gen.CreateEndpointReleaseBinding201JSONResponse)
		require.True(t, ok, "expected 201 response, got %T", resp)
		assert.Equal(t, "payments-api-5b7fc7b", *typed.Spec.EndpointRelease)
	})

	t.Run("project that is not the endpoint's returns 400", func(t *testing.T) {
		h := newHandlerWithEndpointReleaseBindingService(newEndpointReleaseBindingService(t, nil, &allowAllPDP{}))
		body := newERBBody("payments-api-development", "other-project")
		resp, err := h.CreateEndpointReleaseBinding(ctx, gen.CreateEndpointReleaseBindingRequestObject{NamespaceName: testEPNs, Body: &body})
		require.NoError(t, err)
		assert.IsType(t, gen.CreateEndpointReleaseBinding400JSONResponse{}, resp)
	})

	t.Run("already exists returns 409", func(t *testing.T) {
		h := newHandlerWithEndpointReleaseBindingService(newEndpointReleaseBindingService(t, []client.Object{testEndpointReleaseBindingObj("payments-api-development", "development")}, &allowAllPDP{}))
		body := newERBBody("payments-api-development", testEPProject)
		resp, err := h.CreateEndpointReleaseBinding(ctx, gen.CreateEndpointReleaseBindingRequestObject{NamespaceName: testEPNs, Body: &body})
		require.NoError(t, err)
		assert.IsType(t, gen.CreateEndpointReleaseBinding409JSONResponse{}, resp)
	})

	t.Run("forbidden returns 403", func(t *testing.T) {
		h := newHandlerWithEndpointReleaseBindingService(newEndpointReleaseBindingService(t, nil, &denyAllPDP{}))
		body := newERBBody("payments-api-development", testEPProject)
		resp, err := h.CreateEndpointReleaseBinding(ctx, gen.CreateEndpointReleaseBindingRequestObject{NamespaceName: testEPNs, Body: &body})
		require.NoError(t, err)
		assert.IsType(t, gen.CreateEndpointReleaseBinding403JSONResponse{}, resp)
	})
}

func TestUpdateEndpointReleaseBindingHandler(t *testing.T) {
	ctx := testContext()

	t.Run("success - moves the pin", func(t *testing.T) {
		h := newHandlerWithEndpointReleaseBindingService(newEndpointReleaseBindingService(t, []client.Object{testEndpointReleaseBindingObj("payments-api-development", "development")}, &allowAllPDP{}))
		body := newERBBody("ignored", testEPProject)
		resp, err := h.UpdateEndpointReleaseBinding(ctx, gen.UpdateEndpointReleaseBindingRequestObject{NamespaceName: testEPNs, EndpointReleaseBindingName: "payments-api-development", Body: &body})
		require.NoError(t, err)
		typed, ok := resp.(gen.UpdateEndpointReleaseBinding200JSONResponse)
		require.True(t, ok, "expected 200 response, got %T", resp)
		assert.Equal(t, "payments-api-development", typed.Metadata.Name)
		assert.Equal(t, "payments-api-5b7fc7b", *typed.Spec.EndpointRelease)
	})

	t.Run("not found returns 404", func(t *testing.T) {
		h := newHandlerWithEndpointReleaseBindingService(newEndpointReleaseBindingService(t, nil, &allowAllPDP{}))
		body := newERBBody("missing", testEPProject)
		resp, err := h.UpdateEndpointReleaseBinding(ctx, gen.UpdateEndpointReleaseBindingRequestObject{NamespaceName: testEPNs, EndpointReleaseBindingName: "missing", Body: &body})
		require.NoError(t, err)
		assert.IsType(t, gen.UpdateEndpointReleaseBinding404JSONResponse{}, resp)
	})

	t.Run("forbidden returns 403", func(t *testing.T) {
		h := newHandlerWithEndpointReleaseBindingService(newEndpointReleaseBindingService(t, []client.Object{testEndpointReleaseBindingObj("payments-api-development", "development")}, &denyAllPDP{}))
		body := newERBBody("payments-api-development", testEPProject)
		resp, err := h.UpdateEndpointReleaseBinding(ctx, gen.UpdateEndpointReleaseBindingRequestObject{NamespaceName: testEPNs, EndpointReleaseBindingName: "payments-api-development", Body: &body})
		require.NoError(t, err)
		assert.IsType(t, gen.UpdateEndpointReleaseBinding403JSONResponse{}, resp)
	})
}

func TestGetAndDeleteEndpointReleaseBindingHandler(t *testing.T) {
	ctx := testContext()
	objects := []client.Object{testEndpointReleaseBindingObj("payments-api-development", "development")}

	t.Run("get success", func(t *testing.T) {
		h := newHandlerWithEndpointReleaseBindingService(newEndpointReleaseBindingService(t, objects, &allowAllPDP{}))
		resp, err := h.GetEndpointReleaseBinding(ctx, gen.GetEndpointReleaseBindingRequestObject{NamespaceName: testEPNs, EndpointReleaseBindingName: "payments-api-development"})
		require.NoError(t, err)
		assert.IsType(t, gen.GetEndpointReleaseBinding200JSONResponse{}, resp)
	})

	t.Run("get not found returns 404", func(t *testing.T) {
		h := newHandlerWithEndpointReleaseBindingService(newEndpointReleaseBindingService(t, nil, &allowAllPDP{}))
		resp, err := h.GetEndpointReleaseBinding(ctx, gen.GetEndpointReleaseBindingRequestObject{NamespaceName: testEPNs, EndpointReleaseBindingName: "missing"})
		require.NoError(t, err)
		assert.IsType(t, gen.GetEndpointReleaseBinding404JSONResponse{}, resp)
	})

	t.Run("delete success", func(t *testing.T) {
		h := newHandlerWithEndpointReleaseBindingService(newEndpointReleaseBindingService(t, objects, &allowAllPDP{}))
		resp, err := h.DeleteEndpointReleaseBinding(ctx, gen.DeleteEndpointReleaseBindingRequestObject{NamespaceName: testEPNs, EndpointReleaseBindingName: "payments-api-development"})
		require.NoError(t, err)
		assert.IsType(t, gen.DeleteEndpointReleaseBinding204Response{}, resp)
	})

	t.Run("delete forbidden returns 403", func(t *testing.T) {
		h := newHandlerWithEndpointReleaseBindingService(newEndpointReleaseBindingService(t, objects, &denyAllPDP{}))
		resp, err := h.DeleteEndpointReleaseBinding(ctx, gen.DeleteEndpointReleaseBindingRequestObject{NamespaceName: testEPNs, EndpointReleaseBindingName: "payments-api-development"})
		require.NoError(t, err)
		assert.IsType(t, gen.DeleteEndpointReleaseBinding403JSONResponse{}, resp)
	})
}
