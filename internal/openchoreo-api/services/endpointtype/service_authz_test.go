// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointtype

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	authzcore "github.com/openchoreo/openchoreo/internal/authz/core"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointtype/mocks"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/testutil"
)

const authzNamespace = "ns-a"

func newAuthzSvc(pdp *testutil.CapturingPDP, internal Service) Service {
	return &endpointTypeServiceWithAuthz{
		internal: internal,
		authz:    testutil.NewTestAuthzChecker(pdp),
	}
}

func TestGetEndpointType_AuthzCheck(t *testing.T) {
	et := testutil.NewEndpointType(authzNamespace, "http-weighted")

	t.Run("allowed", func(t *testing.T) {
		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetEndpointType", mock.Anything, authzNamespace, "http-weighted").Return(et, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		_, err := svc.GetEndpointType(testutil.AuthzContext(), authzNamespace, "http-weighted")
		require.NoError(t, err)
		require.Len(t, pdp.Captured, 1)
		testutil.RequireEvalRequest(t, pdp.Captured[0], "endpointtype:view", "endpointtype", "http-weighted", authzcore.ResourceHierarchy{Namespace: authzNamespace})
	})

	t.Run("denied", func(t *testing.T) {
		pdp := testutil.DenyPDP()
		svc := newAuthzSvc(pdp, mocks.NewMockService(t))
		_, err := svc.GetEndpointType(testutil.AuthzContext(), authzNamespace, "http-weighted")
		require.ErrorIs(t, err, services.ErrForbidden)
	})
}

func TestListEndpointTypes_AuthzCheck(t *testing.T) {
	items := []openchoreov1alpha1.EndpointType{*testutil.NewEndpointType(authzNamespace, "a"), *testutil.NewEndpointType(authzNamespace, "b")}

	t.Run("all allowed", func(t *testing.T) {
		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("ListEndpointTypes", mock.Anything, authzNamespace, mock.Anything).Return(&services.ListResult[openchoreov1alpha1.EndpointType]{Items: items}, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		result, err := svc.ListEndpointTypes(testutil.AuthzContext(), authzNamespace, services.ListOptions{Limit: 10})
		require.NoError(t, err)
		require.Len(t, result.Items, 2)
		testutil.RequireEvalRequest(t, pdp.Captured[1], "endpointtype:view", "endpointtype", "b", authzcore.ResourceHierarchy{Namespace: authzNamespace})
	})

	t.Run("all denied — empty result", func(t *testing.T) {
		pdp := testutil.DenyPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("ListEndpointTypes", mock.Anything, authzNamespace, mock.Anything).Return(&services.ListResult[openchoreov1alpha1.EndpointType]{Items: items}, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		result, err := svc.ListEndpointTypes(testutil.AuthzContext(), authzNamespace, services.ListOptions{Limit: 10})
		require.NoError(t, err)
		require.Empty(t, result.Items)
	})
}
