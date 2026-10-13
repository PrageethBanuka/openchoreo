// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/openchoreo/openchoreo/internal/openchoreo-api/api/gen"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/models"
	svcpkg "github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/handlerservices"
	k8sresourcessvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/k8sresources"
	k8sresourcesmocks "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/k8sresources/mocks"
)

func newEndpointK8sHandler(svc *k8sresourcesmocks.MockService) *Handler {
	return &Handler{
		services: &handlerservices.Services{K8sResourcesService: svc},
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestGetEndpointReleaseBindingK8sResourceTreeHandler_MapsErrors(t *testing.T) {
	tests := []struct {
		name    string
		svcErr  error
		wantTyp any
	}{
		{"forbidden -> 403", svcpkg.ErrForbidden, gen.GetEndpointReleaseBindingK8sResourceTree403JSONResponse{}},
		{"binding not found -> 404", k8sresourcessvc.ErrEndpointReleaseBindingNotFound, gen.GetEndpointReleaseBindingK8sResourceTree404JSONResponse{}},
		{"rendered release not found -> 404", k8sresourcessvc.ErrRenderedReleaseNotFound, gen.GetEndpointReleaseBindingK8sResourceTree404JSONResponse{}},
		{"environment not found -> 404", k8sresourcessvc.ErrEnvironmentNotFound, gen.GetEndpointReleaseBindingK8sResourceTree404JSONResponse{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := k8sresourcesmocks.NewMockService(t)
			svc.EXPECT().GetEndpointResourceTree(mock.Anything, "test-ns", "erb-1").Return(nil, tt.svcErr)

			resp, err := newEndpointK8sHandler(svc).GetEndpointReleaseBindingK8sResourceTree(testContext(), gen.GetEndpointReleaseBindingK8sResourceTreeRequestObject{
				NamespaceName:              "test-ns",
				EndpointReleaseBindingName: "erb-1",
			})
			require.NoError(t, err)
			assert.IsType(t, tt.wantTyp, resp)
		})
	}
}

func TestGetEndpointReleaseBindingK8sResourceTreeHandler_ConvertsReleases(t *testing.T) {
	svc := k8sresourcesmocks.NewMockService(t)
	svc.EXPECT().GetEndpointResourceTree(mock.Anything, "test-ns", "erb-1").Return(&k8sresourcessvc.K8sResourceTreeResult{
		RenderedReleases: []k8sresourcessvc.ReleaseResourceTree{{
			Name:        "e-rr-1",
			TargetPlane: "dataplane",
			Nodes:       []models.ResourceNode{{Kind: "HTTPRoute", Name: "e-payments-api-dev"}},
		}},
	}, nil)

	resp, err := newEndpointK8sHandler(svc).GetEndpointReleaseBindingK8sResourceTree(testContext(), gen.GetEndpointReleaseBindingK8sResourceTreeRequestObject{
		NamespaceName:              "test-ns",
		EndpointReleaseBindingName: "erb-1",
	})
	require.NoError(t, err)

	ok, isOK := resp.(gen.GetEndpointReleaseBindingK8sResourceTree200JSONResponse)
	require.True(t, isOK)
	require.Len(t, ok.RenderedReleases, 1)
	assert.Equal(t, "e-rr-1", ok.RenderedReleases[0].Name)
	assert.Nil(t, ok.RenderedReleases[0].RenderedRelease)
	require.Len(t, ok.RenderedReleases[0].Nodes, 1)
	assert.Equal(t, "HTTPRoute", ok.RenderedReleases[0].Nodes[0].Kind)
}

func TestGetEndpointReleaseBindingK8sResourceEventsHandler(t *testing.T) {
	req := gen.GetEndpointReleaseBindingK8sResourceEventsRequestObject{
		NamespaceName:              "test-ns",
		EndpointReleaseBindingName: "erb-1",
		Params:                     gen.GetEndpointReleaseBindingK8sResourceEventsParams{Version: "v1", Kind: "HTTPRoute", Name: "route"},
	}

	t.Run("maps errors", func(t *testing.T) {
		tests := []struct {
			name    string
			svcErr  error
			wantTyp any
		}{
			{"forbidden -> 403", svcpkg.ErrForbidden, gen.GetEndpointReleaseBindingK8sResourceEvents403JSONResponse{}},
			{"binding not found -> 404", k8sresourcessvc.ErrEndpointReleaseBindingNotFound, gen.GetEndpointReleaseBindingK8sResourceEvents404JSONResponse{}},
			{"resource not found -> 404", k8sresourcessvc.ErrResourceNotFound, gen.GetEndpointReleaseBindingK8sResourceEvents404JSONResponse{}},
			{"incomplete tree -> 500", k8sresourcessvc.ErrResourceTreeIncomplete, gen.GetEndpointReleaseBindingK8sResourceEvents500JSONResponse{}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				svc := k8sresourcesmocks.NewMockService(t)
				svc.EXPECT().GetEndpointResourceEvents(mock.Anything, "test-ns", "erb-1", "", "v1", "HTTPRoute", "route").Return(nil, tt.svcErr)

				resp, err := newEndpointK8sHandler(svc).GetEndpointReleaseBindingK8sResourceEvents(testContext(), req)
				require.NoError(t, err)
				assert.IsType(t, tt.wantTyp, resp)
			})
		}
	})

	t.Run("returns events", func(t *testing.T) {
		svc := k8sresourcesmocks.NewMockService(t)
		svc.EXPECT().GetEndpointResourceEvents(mock.Anything, "test-ns", "erb-1", "", "v1", "HTTPRoute", "route").
			Return(&models.ResourceEventsResponse{Events: []models.ResourceEvent{{Reason: "Accepted"}}}, nil)

		resp, err := newEndpointK8sHandler(svc).GetEndpointReleaseBindingK8sResourceEvents(testContext(), req)
		require.NoError(t, err)

		ok, isOK := resp.(gen.GetEndpointReleaseBindingK8sResourceEvents200JSONResponse)
		require.True(t, isOK)
		require.Len(t, ok.Events, 1)
		assert.Equal(t, "Accepted", ok.Events[0].Reason)
	})
}
