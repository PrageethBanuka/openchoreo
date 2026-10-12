// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	authzcore "github.com/openchoreo/openchoreo/internal/authz/core"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpoint/mocks"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/testutil"
)

const (
	authzNamespace = "ns-a"
	authzProject   = "proj-1"
)

func newAuthzSvc(pdp *testutil.CapturingPDP, internal Service) Service {
	return &endpointServiceWithAuthz{
		internal: internal,
		authz:    testutil.NewTestAuthzChecker(pdp),
	}
}

// projectHierarchy is the hierarchy every Endpoint check uses: Endpoints are
// authorized at the project level.
var projectHierarchy = authzcore.ResourceHierarchy{Namespace: authzNamespace, Project: authzProject}

func TestCreateEndpoint_AuthzCheck(t *testing.T) {
	ep := testutil.NewEndpoint(authzNamespace, authzProject, "my-ep")

	t.Run("allowed", func(t *testing.T) {
		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("CreateEndpoint", mock.Anything, authzNamespace, ep).Return(ep, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		result, err := svc.CreateEndpoint(testutil.AuthzContext(), authzNamespace, ep)
		require.NoError(t, err)
		require.Equal(t, ep, result)
		require.Len(t, pdp.Captured, 1)
		testutil.RequireEvalRequest(t, pdp.Captured[0], "endpoint:create", "endpoint", "my-ep", projectHierarchy)
	})

	t.Run("denied", func(t *testing.T) {
		pdp := testutil.DenyPDP()
		svc := newAuthzSvc(pdp, mocks.NewMockService(t))
		_, err := svc.CreateEndpoint(testutil.AuthzContext(), authzNamespace, ep)
		require.ErrorIs(t, err, services.ErrForbidden)
	})
}

func TestUpdateEndpoint_AuthzCheck(t *testing.T) {
	t.Run("authz uses the stored project, not the body's", func(t *testing.T) {
		existing := testutil.NewEndpoint(authzNamespace, authzProject, "my-ep")
		body := testutil.NewEndpoint(authzNamespace, "different-project", "my-ep")

		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetEndpoint", mock.Anything, authzNamespace, "my-ep").Return(existing, nil)
		mockSvc.On("UpdateEndpoint", mock.Anything, authzNamespace, body).Return(existing, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		_, err := svc.UpdateEndpoint(testutil.AuthzContext(), authzNamespace, body)
		require.NoError(t, err)
		require.Len(t, pdp.Captured, 1)
		testutil.RequireEvalRequest(t, pdp.Captured[0], "endpoint:update", "endpoint", "my-ep", projectHierarchy)
	})

	t.Run("denied", func(t *testing.T) {
		ep := testutil.NewEndpoint(authzNamespace, authzProject, "my-ep")
		pdp := testutil.DenyPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetEndpoint", mock.Anything, authzNamespace, "my-ep").Return(ep, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		_, err := svc.UpdateEndpoint(testutil.AuthzContext(), authzNamespace, ep)
		require.ErrorIs(t, err, services.ErrForbidden)
	})

	t.Run("not found bypasses authz", func(t *testing.T) {
		ep := testutil.NewEndpoint(authzNamespace, authzProject, "my-ep")
		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetEndpoint", mock.Anything, authzNamespace, "my-ep").Return(nil, ErrEndpointNotFound)
		svc := newAuthzSvc(pdp, mockSvc)
		_, err := svc.UpdateEndpoint(testutil.AuthzContext(), authzNamespace, ep)
		require.ErrorIs(t, err, ErrEndpointNotFound)
		require.Empty(t, pdp.Captured)
	})
}

func TestGetEndpoint_AuthzCheck(t *testing.T) {
	ep := testutil.NewEndpoint(authzNamespace, authzProject, "my-ep")

	t.Run("allowed", func(t *testing.T) {
		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetEndpoint", mock.Anything, authzNamespace, "my-ep").Return(ep, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		_, err := svc.GetEndpoint(testutil.AuthzContext(), authzNamespace, "my-ep")
		require.NoError(t, err)
		require.Len(t, pdp.Captured, 1)
		testutil.RequireEvalRequest(t, pdp.Captured[0], "endpoint:view", "endpoint", "my-ep", projectHierarchy)
	})

	t.Run("denied", func(t *testing.T) {
		pdp := testutil.DenyPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetEndpoint", mock.Anything, authzNamespace, "my-ep").Return(ep, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		_, err := svc.GetEndpoint(testutil.AuthzContext(), authzNamespace, "my-ep")
		require.ErrorIs(t, err, services.ErrForbidden)
	})
}

func TestDeleteEndpoint_AuthzCheck(t *testing.T) {
	ep := testutil.NewEndpoint(authzNamespace, authzProject, "my-ep")

	t.Run("allowed", func(t *testing.T) {
		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetEndpoint", mock.Anything, authzNamespace, "my-ep").Return(ep, nil)
		mockSvc.On("DeleteEndpoint", mock.Anything, authzNamespace, "my-ep").Return(nil)
		svc := newAuthzSvc(pdp, mockSvc)
		require.NoError(t, svc.DeleteEndpoint(testutil.AuthzContext(), authzNamespace, "my-ep"))
		require.Len(t, pdp.Captured, 1)
		testutil.RequireEvalRequest(t, pdp.Captured[0], "endpoint:delete", "endpoint", "my-ep", projectHierarchy)
	})

	t.Run("denied", func(t *testing.T) {
		pdp := testutil.DenyPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetEndpoint", mock.Anything, authzNamespace, "my-ep").Return(ep, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		require.ErrorIs(t, svc.DeleteEndpoint(testutil.AuthzContext(), authzNamespace, "my-ep"), services.ErrForbidden)
	})
}

func TestListEndpoints_AuthzCheck(t *testing.T) {
	items := []openchoreov1alpha1.Endpoint{
		*testutil.NewEndpoint(authzNamespace, authzProject, "ep-1"),
		*testutil.NewEndpoint(authzNamespace, authzProject, "ep-2"),
	}

	t.Run("all allowed", func(t *testing.T) {
		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("ListEndpoints", mock.Anything, authzNamespace, "", "payments-v1", mock.Anything).Return(&services.ListResult[openchoreov1alpha1.Endpoint]{Items: items}, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		result, err := svc.ListEndpoints(testutil.AuthzContext(), authzNamespace, "", "payments-v1", services.ListOptions{Limit: 10})
		require.NoError(t, err)
		require.Len(t, result.Items, 2)
		require.Len(t, pdp.Captured, 2)
		testutil.RequireEvalRequest(t, pdp.Captured[0], "endpoint:view", "endpoint", "ep-1", projectHierarchy)
		testutil.RequireEvalRequest(t, pdp.Captured[1], "endpoint:view", "endpoint", "ep-2", projectHierarchy)
	})

	t.Run("all denied — empty result", func(t *testing.T) {
		pdp := testutil.DenyPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("ListEndpoints", mock.Anything, authzNamespace, "", "payments-v1", mock.Anything).Return(&services.ListResult[openchoreov1alpha1.Endpoint]{Items: items}, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		result, err := svc.ListEndpoints(testutil.AuthzContext(), authzNamespace, "", "payments-v1", services.ListOptions{Limit: 10})
		require.NoError(t, err)
		require.Empty(t, result.Items)
	})
}
