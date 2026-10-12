// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package clusterendpointtype

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	authzcore "github.com/openchoreo/openchoreo/internal/authz/core"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/clusterendpointtype/mocks"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/testutil"
)

func newAuthzSvc(pdp *testutil.CapturingPDP, internal Service) Service {
	return &clusterEndpointTypeServiceWithAuthz{
		internal: internal,
		authz:    testutil.NewTestAuthzChecker(pdp),
	}
}

func TestGetClusterEndpointType_AuthzCheck(t *testing.T) {
	et := testutil.NewClusterEndpointType("http-weighted")

	t.Run("allowed", func(t *testing.T) {
		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetClusterEndpointType", mock.Anything, "http-weighted").Return(et, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		_, err := svc.GetClusterEndpointType(testutil.AuthzContext(), "http-weighted")
		require.NoError(t, err)
		require.Len(t, pdp.Captured, 1)
		testutil.RequireEvalRequest(t, pdp.Captured[0], "clusterendpointtype:view", "clusterendpointtype", "http-weighted", authzcore.ResourceHierarchy{})
	})

	t.Run("denied", func(t *testing.T) {
		pdp := testutil.DenyPDP()
		svc := newAuthzSvc(pdp, mocks.NewMockService(t))
		_, err := svc.GetClusterEndpointType(testutil.AuthzContext(), "http-weighted")
		require.ErrorIs(t, err, services.ErrForbidden)
	})
}

func TestListClusterEndpointTypes_AuthzCheck(t *testing.T) {
	items := []openchoreov1alpha1.ClusterEndpointType{*testutil.NewClusterEndpointType("a"), *testutil.NewClusterEndpointType("b")}

	t.Run("all allowed", func(t *testing.T) {
		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("ListClusterEndpointTypes", mock.Anything, mock.Anything).Return(&services.ListResult[openchoreov1alpha1.ClusterEndpointType]{Items: items}, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		result, err := svc.ListClusterEndpointTypes(testutil.AuthzContext(), services.ListOptions{Limit: 10})
		require.NoError(t, err)
		require.Len(t, result.Items, 2)
		testutil.RequireEvalRequest(t, pdp.Captured[1], "clusterendpointtype:view", "clusterendpointtype", "b", authzcore.ResourceHierarchy{})
	})

	t.Run("all denied — empty result", func(t *testing.T) {
		pdp := testutil.DenyPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("ListClusterEndpointTypes", mock.Anything, mock.Anything).Return(&services.ListResult[openchoreov1alpha1.ClusterEndpointType]{Items: items}, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		result, err := svc.ListClusterEndpointTypes(testutil.AuthzContext(), services.ListOptions{Limit: 10})
		require.NoError(t, err)
		require.Empty(t, result.Items)
	})
}
