// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	authzcore "github.com/openchoreo/openchoreo/internal/authz/core"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/api/gen"
	clusterendpointtypesvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/clusterendpointtype"
	endpointreleasesvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointrelease"
	endpointtypesvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointtype"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/handlerservices"
)

func endpointRouteTemplate() []openchoreov1alpha1.ResourceTemplate {
	return []openchoreov1alpha1.ResourceTemplate{{
		ID:       "route",
		Template: &runtime.RawExtension{Raw: []byte(`{"apiVersion":"gateway.networking.k8s.io/v1","kind":"HTTPRoute"}`)},
	}}
}

// newEndpointReadHandler wires the read-only Endpoint family services over the
// given objects.
func newEndpointReadHandler(t *testing.T, objects []client.Object, allow bool) *Handler {
	t.Helper()
	fakeClient := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(objects...).Build()
	logger := slog.Default()
	var pdp authzcore.PDP = &denyAllPDP{}
	if allow {
		pdp = &allowAllPDP{}
	}
	return &Handler{services: &handlerservices.Services{
		ClusterEndpointTypeService: clusterendpointtypesvc.NewServiceWithAuthz(fakeClient, pdp, logger),
		EndpointTypeService:        endpointtypesvc.NewServiceWithAuthz(fakeClient, pdp, logger),
		EndpointReleaseService:     endpointreleasesvc.NewServiceWithAuthz(fakeClient, pdp, logger),
	}, logger: logger}
}

func TestClusterEndpointTypeHandlers(t *testing.T) {
	ctx := testContext()
	objects := []client.Object{&openchoreov1alpha1.ClusterEndpointType{
		ObjectMeta: metav1.ObjectMeta{Name: "http-weighted"},
		Spec:       openchoreov1alpha1.ClusterEndpointTypeSpec{Resources: endpointRouteTemplate()},
	}}

	h := newEndpointReadHandler(t, objects, true)
	listResp, err := h.ListClusterEndpointTypes(ctx, gen.ListClusterEndpointTypesRequestObject{})
	require.NoError(t, err)
	list, ok := listResp.(gen.ListClusterEndpointTypes200JSONResponse)
	require.True(t, ok, "expected 200 response, got %T", listResp)
	assert.Len(t, list.Items, 1)

	getResp, err := h.GetClusterEndpointType(ctx, gen.GetClusterEndpointTypeRequestObject{CetName: "http-weighted"})
	require.NoError(t, err)
	got, ok := getResp.(gen.GetClusterEndpointType200JSONResponse)
	require.True(t, ok, "expected 200 response, got %T", getResp)
	require.NotNil(t, got.Spec)
	assert.Equal(t, "route", got.Spec.Resources[0].Id)

	missing, err := h.GetClusterEndpointType(ctx, gen.GetClusterEndpointTypeRequestObject{CetName: "missing"})
	require.NoError(t, err)
	assert.IsType(t, gen.GetClusterEndpointType404JSONResponse{}, missing)

	denied, err := newEndpointReadHandler(t, objects, false).GetClusterEndpointType(ctx, gen.GetClusterEndpointTypeRequestObject{CetName: "http-weighted"})
	require.NoError(t, err)
	assert.IsType(t, gen.GetClusterEndpointType403JSONResponse{}, denied)
}

func TestEndpointTypeHandlers(t *testing.T) {
	ctx := testContext()
	objects := []client.Object{&openchoreov1alpha1.EndpointType{
		ObjectMeta: metav1.ObjectMeta{Name: "http-weighted", Namespace: testEPNs},
		Spec:       openchoreov1alpha1.EndpointTypeSpec{Resources: endpointRouteTemplate()},
	}}

	h := newEndpointReadHandler(t, objects, true)
	listResp, err := h.ListEndpointTypes(ctx, gen.ListEndpointTypesRequestObject{NamespaceName: testEPNs})
	require.NoError(t, err)
	list, ok := listResp.(gen.ListEndpointTypes200JSONResponse)
	require.True(t, ok, "expected 200 response, got %T", listResp)
	assert.Len(t, list.Items, 1)

	getResp, err := h.GetEndpointType(ctx, gen.GetEndpointTypeRequestObject{NamespaceName: testEPNs, EtName: "http-weighted"})
	require.NoError(t, err)
	assert.IsType(t, gen.GetEndpointType200JSONResponse{}, getResp)

	missing, err := h.GetEndpointType(ctx, gen.GetEndpointTypeRequestObject{NamespaceName: testEPNs, EtName: "missing"})
	require.NoError(t, err)
	assert.IsType(t, gen.GetEndpointType404JSONResponse{}, missing)
}

func TestEndpointReleaseHandlers(t *testing.T) {
	ctx := testContext()
	release := func(name, endpoint string) *openchoreov1alpha1.EndpointRelease {
		return &openchoreov1alpha1.EndpointRelease{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testEPNs},
			Spec: openchoreov1alpha1.EndpointReleaseSpec{
				Owner: openchoreov1alpha1.EndpointReleaseOwner{ProjectName: testEPProject, EndpointName: endpoint},
				EndpointType: openchoreov1alpha1.EndpointReleaseEndpointType{
					Kind: openchoreov1alpha1.EndpointTypeRefKindClusterEndpointType,
					Name: "http-weighted",
					Spec: openchoreov1alpha1.EndpointTypeSpec{Resources: endpointRouteTemplate()},
				},
				BackendRefs: []openchoreov1alpha1.EndpointBackendRef{{ComponentName: "payments-v1", Endpoint: "http"}},
			},
		}
	}
	objects := []client.Object{release("payments-api-aaa", "payments-api"), release("orders-api-bbb", "orders-api")}
	endpoint := "payments-api"

	h := newEndpointReadHandler(t, objects, true)
	listResp, err := h.ListEndpointReleases(ctx, gen.ListEndpointReleasesRequestObject{
		NamespaceName: testEPNs,
		Params:        gen.ListEndpointReleasesParams{Endpoint: &endpoint},
	})
	require.NoError(t, err)
	list, ok := listResp.(gen.ListEndpointReleases200JSONResponse)
	require.True(t, ok, "expected 200 response, got %T", listResp)
	require.Len(t, list.Items, 1)
	assert.Equal(t, "payments-api-aaa", list.Items[0].Metadata.Name)

	getResp, err := h.GetEndpointRelease(ctx, gen.GetEndpointReleaseRequestObject{NamespaceName: testEPNs, EndpointReleaseName: "payments-api-aaa"})
	require.NoError(t, err)
	assert.IsType(t, gen.GetEndpointRelease200JSONResponse{}, getResp)

	denied, err := newEndpointReadHandler(t, objects, false).GetEndpointRelease(ctx, gen.GetEndpointReleaseRequestObject{NamespaceName: testEPNs, EndpointReleaseName: "payments-api-aaa"})
	require.NoError(t, err)
	assert.IsType(t, gen.GetEndpointRelease403JSONResponse{}, denied)
}
