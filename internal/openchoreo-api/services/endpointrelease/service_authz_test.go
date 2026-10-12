// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointrelease

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	authzcore "github.com/openchoreo/openchoreo/internal/authz/core"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointrelease/mocks"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/testutil"
)

const (
	authzNamespace = "ns-a"
	authzProject   = "proj-1"
)

var projectHierarchy = authzcore.ResourceHierarchy{Namespace: authzNamespace, Project: authzProject}

func newAuthzSvc(pdp *testutil.CapturingPDP, internal Service) Service {
	return &endpointReleaseServiceWithAuthz{
		internal: internal,
		authz:    testutil.NewTestAuthzChecker(pdp),
	}
}

func TestGetEndpointRelease_AuthzCheck(t *testing.T) {
	er := testutil.NewEndpointRelease(authzNamespace, authzProject, "my-ep", "my-ep-aaa")

	t.Run("allowed", func(t *testing.T) {
		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetEndpointRelease", mock.Anything, authzNamespace, "my-ep-aaa").Return(er, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		_, err := svc.GetEndpointRelease(testutil.AuthzContext(), authzNamespace, "my-ep-aaa")
		require.NoError(t, err)
		require.Len(t, pdp.Captured, 1)
		testutil.RequireEvalRequest(t, pdp.Captured[0], "endpointrelease:view", "endpointrelease", "my-ep-aaa", projectHierarchy)
	})

	t.Run("denied", func(t *testing.T) {
		pdp := testutil.DenyPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetEndpointRelease", mock.Anything, authzNamespace, "my-ep-aaa").Return(er, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		_, err := svc.GetEndpointRelease(testutil.AuthzContext(), authzNamespace, "my-ep-aaa")
		require.ErrorIs(t, err, services.ErrForbidden)
	})

	t.Run("not found bypasses authz", func(t *testing.T) {
		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetEndpointRelease", mock.Anything, authzNamespace, "missing").Return(nil, ErrEndpointReleaseNotFound)
		svc := newAuthzSvc(pdp, mockSvc)
		_, err := svc.GetEndpointRelease(testutil.AuthzContext(), authzNamespace, "missing")
		require.ErrorIs(t, err, ErrEndpointReleaseNotFound)
		require.Empty(t, pdp.Captured)
	})
}

func TestListEndpointReleases_AuthzCheck(t *testing.T) {
	items := []openchoreov1alpha1.EndpointRelease{
		*testutil.NewEndpointRelease(authzNamespace, authzProject, "my-ep", "my-ep-aaa"),
		*testutil.NewEndpointRelease(authzNamespace, authzProject, "my-ep", "my-ep-bbb"),
	}

	t.Run("all allowed", func(t *testing.T) {
		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("ListEndpointReleases", mock.Anything, authzNamespace, "my-ep", mock.Anything).Return(&services.ListResult[openchoreov1alpha1.EndpointRelease]{Items: items}, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		result, err := svc.ListEndpointReleases(testutil.AuthzContext(), authzNamespace, "my-ep", services.ListOptions{Limit: 10})
		require.NoError(t, err)
		require.Len(t, result.Items, 2)
		testutil.RequireEvalRequest(t, pdp.Captured[1], "endpointrelease:view", "endpointrelease", "my-ep-bbb", projectHierarchy)
	})

	t.Run("all denied — empty result", func(t *testing.T) {
		pdp := testutil.DenyPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("ListEndpointReleases", mock.Anything, authzNamespace, "my-ep", mock.Anything).Return(&services.ListResult[openchoreov1alpha1.EndpointRelease]{Items: items}, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		result, err := svc.ListEndpointReleases(testutil.AuthzContext(), authzNamespace, "my-ep", services.ListOptions{Limit: 10})
		require.NoError(t, err)
		require.Empty(t, result.Items)
	})
}
