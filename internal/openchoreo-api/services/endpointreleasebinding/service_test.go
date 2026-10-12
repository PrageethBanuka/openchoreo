// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointreleasebinding

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openchoreo/openchoreo/internal/labels"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/testutil"
)

const (
	testNamespace    = "test-ns"
	testProject      = "test-project"
	testEndpointName = "payments-api"
	testEnvironment  = "development"
	testBindingName  = "payments-api-development"
)

// newService creates a service plus the Endpoint fixture owner validation relies on.
func newService(t *testing.T, extra ...client.Object) Service {
	t.Helper()
	objs := make([]client.Object, 0, 1+len(extra))
	objs = append(objs, testutil.NewEndpoint(testNamespace, testProject, testEndpointName))
	objs = append(objs, extra...)
	return NewService(testutil.NewFakeClient(objs...), testutil.TestLogger())
}

func TestCreateEndpointReleaseBinding(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		svc := newService(t)
		rb := testutil.NewEndpointReleaseBinding(testNamespace, testProject, testEndpointName, testEnvironment, testBindingName)

		result, err := svc.CreateEndpointReleaseBinding(ctx, testNamespace, rb)
		require.NoError(t, err)
		assert.Equal(t, endpointReleaseBindingTypeMeta, result.TypeMeta)
		assert.Equal(t, testNamespace, result.Namespace)
		assert.Equal(t, testProject, result.Labels[labels.LabelKeyProjectName])
		assert.Equal(t, testEndpointName, result.Labels[labels.LabelKeyEndpointResourceName])
	})

	t.Run("nil input", func(t *testing.T) {
		svc := newService(t)
		_, err := svc.CreateEndpointReleaseBinding(ctx, testNamespace, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot be nil")
	})

	t.Run("endpoint not found", func(t *testing.T) {
		svc := newService(t)
		rb := testutil.NewEndpointReleaseBinding(testNamespace, testProject, "missing", testEnvironment, "missing-development")
		_, err := svc.CreateEndpointReleaseBinding(ctx, testNamespace, rb)
		require.ErrorIs(t, err, ErrEndpointNotFound)
	})

	t.Run("project must match the endpoint's project", func(t *testing.T) {
		// Authorization is evaluated against the binding's project, so a binding
		// naming another project must be rejected.
		svc := newService(t)
		rb := testutil.NewEndpointReleaseBinding(testNamespace, "other-project", testEndpointName, testEnvironment, testBindingName)
		_, err := svc.CreateEndpointReleaseBinding(ctx, testNamespace, rb)
		var validationErr *services.ValidationError
		require.ErrorAs(t, err, &validationErr)
		assert.Contains(t, validationErr.Msg, "belongs to project")
	})

	t.Run("already exists", func(t *testing.T) {
		svc := newService(t, testutil.NewEndpointReleaseBinding(testNamespace, testProject, testEndpointName, testEnvironment, testBindingName))
		rb := testutil.NewEndpointReleaseBinding(testNamespace, testProject, testEndpointName, testEnvironment, testBindingName)
		_, err := svc.CreateEndpointReleaseBinding(ctx, testNamespace, rb)
		require.ErrorIs(t, err, ErrEndpointReleaseBindingAlreadyExists)
	})
}

func TestUpdateEndpointReleaseBinding(t *testing.T) {
	ctx := context.Background()

	t.Run("moves the release pin", func(t *testing.T) {
		svc := newService(t, testutil.NewEndpointReleaseBinding(testNamespace, testProject, testEndpointName, testEnvironment, testBindingName))
		update := testutil.NewEndpointReleaseBinding(testNamespace, testProject, testEndpointName, testEnvironment, testBindingName)
		update.Spec.EndpointRelease = "payments-api-5b7fc7b"

		result, err := svc.UpdateEndpointReleaseBinding(ctx, testNamespace, update)
		require.NoError(t, err)
		assert.Equal(t, "payments-api-5b7fc7b", result.Spec.EndpointRelease)
		assert.Equal(t, testEndpointName, result.Labels[labels.LabelKeyEndpointResourceName])
	})

	t.Run("not found", func(t *testing.T) {
		svc := newService(t)
		update := testutil.NewEndpointReleaseBinding(testNamespace, testProject, testEndpointName, testEnvironment, "missing")
		_, err := svc.UpdateEndpointReleaseBinding(ctx, testNamespace, update)
		require.ErrorIs(t, err, ErrEndpointReleaseBindingNotFound)
	})
}

func TestListEndpointReleaseBindings(t *testing.T) {
	ctx := context.Background()
	svc := newService(t,
		testutil.NewEndpointReleaseBinding(testNamespace, testProject, testEndpointName, "development", "payments-api-development"),
		testutil.NewEndpointReleaseBinding(testNamespace, testProject, testEndpointName, "staging", "payments-api-staging"),
		testutil.NewEndpointReleaseBinding(testNamespace, testProject, "orders-api", "development", "orders-api-development"),
	)

	names := func(t *testing.T, endpointName, environment string) []string {
		t.Helper()
		result, err := svc.ListEndpointReleaseBindings(ctx, testNamespace, endpointName, environment, services.ListOptions{})
		require.NoError(t, err)
		out := make([]string, 0, len(result.Items))
		for _, rb := range result.Items {
			assert.Equal(t, endpointReleaseBindingTypeMeta, rb.TypeMeta)
			out = append(out, rb.Name)
		}
		return out
	}

	assert.Len(t, names(t, "", ""), 3)
	assert.ElementsMatch(t, []string{"payments-api-development", "payments-api-staging"}, names(t, testEndpointName, ""))
	assert.ElementsMatch(t, []string{"payments-api-development", "orders-api-development"}, names(t, "", "development"))
	assert.Equal(t, []string{"payments-api-staging"}, names(t, testEndpointName, "staging"))
}

func TestGetAndDeleteEndpointReleaseBinding(t *testing.T) {
	ctx := context.Background()
	svc := newService(t, testutil.NewEndpointReleaseBinding(testNamespace, testProject, testEndpointName, testEnvironment, testBindingName))

	got, err := svc.GetEndpointReleaseBinding(ctx, testNamespace, testBindingName)
	require.NoError(t, err)
	assert.Equal(t, endpointReleaseBindingTypeMeta, got.TypeMeta)

	require.NoError(t, svc.DeleteEndpointReleaseBinding(ctx, testNamespace, testBindingName))
	_, err = svc.GetEndpointReleaseBinding(ctx, testNamespace, testBindingName)
	require.ErrorIs(t, err, ErrEndpointReleaseBindingNotFound)
	require.ErrorIs(t, svc.DeleteEndpointReleaseBinding(ctx, testNamespace, testBindingName), ErrEndpointReleaseBindingNotFound)
}
