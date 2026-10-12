// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointreleasebinding

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	authzcore "github.com/openchoreo/openchoreo/internal/authz/core"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointreleasebinding/mocks"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/testutil"
)

const (
	authzNamespace = "ns-a"
	authzProject   = "proj-1"
)

// expectedEnvAttr is the Context.Resource.Environment value the authz wrapper
// should pass for a binding whose spec.environment is "dev".
var expectedEnvAttr = services.FormatDualScopedResourceName(authzNamespace, "dev", false)

var projectHierarchy = authzcore.ResourceHierarchy{Namespace: authzNamespace, Project: authzProject}

func newAuthzSvc(pdp *testutil.CapturingPDP, internal Service) Service {
	return &endpointReleaseBindingServiceWithAuthz{
		internal: internal,
		authz:    testutil.NewTestAuthzChecker(pdp),
	}
}

func newBindingFixture(name string) *openchoreov1alpha1.EndpointReleaseBinding {
	return testutil.NewEndpointReleaseBinding(authzNamespace, authzProject, "my-ep", "dev", name)
}

func TestCreateEndpointReleaseBinding_AuthzCheck(t *testing.T) {
	rb := newBindingFixture("my-erb")

	t.Run("allowed", func(t *testing.T) {
		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("CreateEndpointReleaseBinding", mock.Anything, authzNamespace, rb).Return(rb, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		_, err := svc.CreateEndpointReleaseBinding(testutil.AuthzContext(), authzNamespace, rb)
		require.NoError(t, err)
		require.Len(t, pdp.Captured, 1)
		testutil.RequireEvalRequest(t, pdp.Captured[0], "endpointreleasebinding:create", "endpointreleasebinding", "my-erb", projectHierarchy)
		require.Equal(t, expectedEnvAttr, pdp.Captured[0].Context.Resource.Environment, "Create authz should attach the binding's environment to the ABAC context")
	})

	t.Run("denied", func(t *testing.T) {
		pdp := testutil.DenyPDP()
		svc := newAuthzSvc(pdp, mocks.NewMockService(t))
		_, err := svc.CreateEndpointReleaseBinding(testutil.AuthzContext(), authzNamespace, rb)
		require.ErrorIs(t, err, services.ErrForbidden)
	})
}

func TestUpdateEndpointReleaseBinding_AuthzCheck(t *testing.T) {
	t.Run("authz uses the stored owner and environment, not the body's", func(t *testing.T) {
		existing := newBindingFixture("my-erb")
		body := newBindingFixture("my-erb")
		body.Spec.Owner.ProjectName = "different-project"
		body.Spec.Environment = "production"

		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetEndpointReleaseBinding", mock.Anything, authzNamespace, "my-erb").Return(existing, nil)
		mockSvc.On("UpdateEndpointReleaseBinding", mock.Anything, authzNamespace, body).Return(existing, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		_, err := svc.UpdateEndpointReleaseBinding(testutil.AuthzContext(), authzNamespace, body)
		require.NoError(t, err)
		require.Len(t, pdp.Captured, 1)
		testutil.RequireEvalRequest(t, pdp.Captured[0], "endpointreleasebinding:update", "endpointreleasebinding", "my-erb", projectHierarchy)
		require.Equal(t, expectedEnvAttr, pdp.Captured[0].Context.Resource.Environment, "authz must use the stored environment, not the body's claim")
	})

	t.Run("denied", func(t *testing.T) {
		rb := newBindingFixture("my-erb")
		pdp := testutil.DenyPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetEndpointReleaseBinding", mock.Anything, authzNamespace, "my-erb").Return(rb, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		_, err := svc.UpdateEndpointReleaseBinding(testutil.AuthzContext(), authzNamespace, rb)
		require.ErrorIs(t, err, services.ErrForbidden)
	})

	t.Run("not found bypasses authz", func(t *testing.T) {
		rb := newBindingFixture("my-erb")
		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetEndpointReleaseBinding", mock.Anything, authzNamespace, "my-erb").Return(nil, ErrEndpointReleaseBindingNotFound)
		svc := newAuthzSvc(pdp, mockSvc)
		_, err := svc.UpdateEndpointReleaseBinding(testutil.AuthzContext(), authzNamespace, rb)
		require.ErrorIs(t, err, ErrEndpointReleaseBindingNotFound)
		require.Empty(t, pdp.Captured)
	})
}

func TestGetEndpointReleaseBinding_AuthzCheck(t *testing.T) {
	rb := newBindingFixture("my-erb")

	t.Run("allowed", func(t *testing.T) {
		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetEndpointReleaseBinding", mock.Anything, authzNamespace, "my-erb").Return(rb, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		_, err := svc.GetEndpointReleaseBinding(testutil.AuthzContext(), authzNamespace, "my-erb")
		require.NoError(t, err)
		require.Len(t, pdp.Captured, 1)
		testutil.RequireEvalRequest(t, pdp.Captured[0], "endpointreleasebinding:view", "endpointreleasebinding", "my-erb", projectHierarchy)
		require.Equal(t, expectedEnvAttr, pdp.Captured[0].Context.Resource.Environment)
	})

	t.Run("denied", func(t *testing.T) {
		pdp := testutil.DenyPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetEndpointReleaseBinding", mock.Anything, authzNamespace, "my-erb").Return(rb, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		_, err := svc.GetEndpointReleaseBinding(testutil.AuthzContext(), authzNamespace, "my-erb")
		require.ErrorIs(t, err, services.ErrForbidden)
	})
}

func TestDeleteEndpointReleaseBinding_AuthzCheck(t *testing.T) {
	rb := newBindingFixture("my-erb")

	t.Run("allowed", func(t *testing.T) {
		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetEndpointReleaseBinding", mock.Anything, authzNamespace, "my-erb").Return(rb, nil)
		mockSvc.On("DeleteEndpointReleaseBinding", mock.Anything, authzNamespace, "my-erb").Return(nil)
		svc := newAuthzSvc(pdp, mockSvc)
		require.NoError(t, svc.DeleteEndpointReleaseBinding(testutil.AuthzContext(), authzNamespace, "my-erb"))
		require.Len(t, pdp.Captured, 1)
		testutil.RequireEvalRequest(t, pdp.Captured[0], "endpointreleasebinding:delete", "endpointreleasebinding", "my-erb", projectHierarchy)
		require.Equal(t, expectedEnvAttr, pdp.Captured[0].Context.Resource.Environment)
	})

	t.Run("denied", func(t *testing.T) {
		pdp := testutil.DenyPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("GetEndpointReleaseBinding", mock.Anything, authzNamespace, "my-erb").Return(rb, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		require.ErrorIs(t, svc.DeleteEndpointReleaseBinding(testutil.AuthzContext(), authzNamespace, "my-erb"), services.ErrForbidden)
	})
}

func TestListEndpointReleaseBindings_AuthzCheck(t *testing.T) {
	items := []openchoreov1alpha1.EndpointReleaseBinding{*newBindingFixture("erb-1"), *newBindingFixture("erb-2")}

	t.Run("all allowed — per-item environment", func(t *testing.T) {
		pdp := testutil.AllowPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("ListEndpointReleaseBindings", mock.Anything, authzNamespace, "my-ep", "", mock.Anything).Return(&services.ListResult[openchoreov1alpha1.EndpointReleaseBinding]{Items: items}, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		result, err := svc.ListEndpointReleaseBindings(testutil.AuthzContext(), authzNamespace, "my-ep", "", services.ListOptions{Limit: 10})
		require.NoError(t, err)
		require.Len(t, result.Items, 2)
		require.Len(t, pdp.Captured, 2)
		testutil.RequireEvalRequest(t, pdp.Captured[0], "endpointreleasebinding:view", "endpointreleasebinding", "erb-1", projectHierarchy)
		testutil.RequireEvalRequest(t, pdp.Captured[1], "endpointreleasebinding:view", "endpointreleasebinding", "erb-2", projectHierarchy)
		require.Equal(t, expectedEnvAttr, pdp.Captured[1].Context.Resource.Environment)
	})

	t.Run("all denied — empty result", func(t *testing.T) {
		pdp := testutil.DenyPDP()
		mockSvc := mocks.NewMockService(t)
		mockSvc.On("ListEndpointReleaseBindings", mock.Anything, authzNamespace, "my-ep", "", mock.Anything).Return(&services.ListResult[openchoreov1alpha1.EndpointReleaseBinding]{Items: items}, nil)
		svc := newAuthzSvc(pdp, mockSvc)
		result, err := svc.ListEndpointReleaseBindings(testutil.AuthzContext(), authzNamespace, "my-ep", "", services.ListOptions{Limit: 10})
		require.NoError(t, err)
		require.Empty(t, result.Items)
	})
}
