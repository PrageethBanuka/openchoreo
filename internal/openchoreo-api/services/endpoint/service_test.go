// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openchoreo/openchoreo/internal/labels"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	projectsvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/project"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/testutil"
)

const (
	testNamespace = "test-ns"
	testProject   = "test-project"
)

// newService creates a service plus the Project fixture project validation relies on.
func newService(t *testing.T, extra ...client.Object) Service {
	t.Helper()
	objs := make([]client.Object, 0, 1+len(extra))
	objs = append(objs, testutil.NewProject(testNamespace, testProject))
	objs = append(objs, extra...)
	return NewService(testutil.NewFakeClient(objs...), testutil.TestLogger())
}

func TestCreateEndpoint(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		svc := newService(t)
		ep := testutil.NewEndpoint(testNamespace, testProject, "payments-api")

		result, err := svc.CreateEndpoint(ctx, testNamespace, ep)
		require.NoError(t, err)
		assert.Equal(t, endpointTypeMeta, result.TypeMeta)
		assert.Equal(t, testNamespace, result.Namespace)
		assert.Equal(t, testProject, result.Labels[labels.LabelKeyProjectName])
	})

	t.Run("nil input", func(t *testing.T) {
		svc := newService(t)
		_, err := svc.CreateEndpoint(ctx, testNamespace, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot be nil")
	})

	t.Run("project not found", func(t *testing.T) {
		svc := newService(t)
		ep := testutil.NewEndpoint(testNamespace, "missing-project", "payments-api")
		_, err := svc.CreateEndpoint(ctx, testNamespace, ep)
		require.ErrorIs(t, err, projectsvc.ErrProjectNotFound)
	})

	t.Run("already exists", func(t *testing.T) {
		svc := newService(t, testutil.NewEndpoint(testNamespace, testProject, "payments-api"))
		_, err := svc.CreateEndpoint(ctx, testNamespace, testutil.NewEndpoint(testNamespace, testProject, "payments-api"))
		require.ErrorIs(t, err, ErrEndpointAlreadyExists)
	})
}

func TestUpdateEndpoint(t *testing.T) {
	ctx := context.Background()

	t.Run("changes the weights", func(t *testing.T) {
		svc := newService(t, testutil.NewEndpoint(testNamespace, testProject, "payments-api"))
		update := testutil.NewEndpoint(testNamespace, testProject, "payments-api")
		*update.Spec.BackendRefs[0].Weight = 50
		*update.Spec.BackendRefs[1].Weight = 50

		result, err := svc.UpdateEndpoint(ctx, testNamespace, update)
		require.NoError(t, err)
		assert.Equal(t, int32(50), *result.Spec.BackendRefs[0].Weight)
		assert.Equal(t, int32(50), *result.Spec.BackendRefs[1].Weight)
		assert.Equal(t, testProject, result.Labels[labels.LabelKeyProjectName], "project label must survive an update that drops labels")
	})

	t.Run("project is immutable", func(t *testing.T) {
		svc := newService(t, testutil.NewEndpoint(testNamespace, testProject, "payments-api"))
		update := testutil.NewEndpoint(testNamespace, "other-project", "payments-api")

		_, err := svc.UpdateEndpoint(ctx, testNamespace, update)
		var validationErr *services.ValidationError
		require.ErrorAs(t, err, &validationErr)
	})

	t.Run("not found", func(t *testing.T) {
		svc := newService(t)
		_, err := svc.UpdateEndpoint(ctx, testNamespace, testutil.NewEndpoint(testNamespace, testProject, "missing"))
		require.ErrorIs(t, err, ErrEndpointNotFound)
	})
}

func TestListEndpoints(t *testing.T) {
	ctx := context.Background()

	routesV1 := testutil.NewEndpoint(testNamespace, testProject, "payments-api")
	otherProject := testutil.NewEndpoint(testNamespace, "other-project", "orders-api")
	otherProject.Spec.BackendRefs = otherProject.Spec.BackendRefs[1:] // payments-v2 only
	svc := newService(t, routesV1, otherProject, testutil.NewProject(testNamespace, "other-project"))

	names := func(t *testing.T, projectName, componentName string) []string {
		t.Helper()
		result, err := svc.ListEndpoints(ctx, testNamespace, projectName, componentName, services.ListOptions{})
		require.NoError(t, err)
		out := make([]string, 0, len(result.Items))
		for _, ep := range result.Items {
			assert.Equal(t, endpointTypeMeta, ep.TypeMeta)
			out = append(out, ep.Name)
		}
		return out
	}

	assert.ElementsMatch(t, []string{"payments-api", "orders-api"}, names(t, "", ""))
	assert.Equal(t, []string{"payments-api"}, names(t, testProject, ""))
	assert.Equal(t, []string{"payments-api"}, names(t, "", "payments-v1"))
	assert.ElementsMatch(t, []string{"payments-api", "orders-api"}, names(t, "", "payments-v2"))
	assert.Empty(t, names(t, "other-project", "payments-v1"))

	t.Run("unknown project", func(t *testing.T) {
		_, err := svc.ListEndpoints(ctx, testNamespace, "missing", "", services.ListOptions{})
		require.ErrorIs(t, err, projectsvc.ErrProjectNotFound)
	})
}

func TestGetAndDeleteEndpoint(t *testing.T) {
	ctx := context.Background()
	svc := newService(t, testutil.NewEndpoint(testNamespace, testProject, "payments-api"))

	got, err := svc.GetEndpoint(ctx, testNamespace, "payments-api")
	require.NoError(t, err)
	assert.Equal(t, endpointTypeMeta, got.TypeMeta)

	require.NoError(t, svc.DeleteEndpoint(ctx, testNamespace, "payments-api"))
	_, err = svc.GetEndpoint(ctx, testNamespace, "payments-api")
	require.ErrorIs(t, err, ErrEndpointNotFound)
	require.ErrorIs(t, svc.DeleteEndpoint(ctx, testNamespace, "payments-api"), ErrEndpointNotFound)
}
