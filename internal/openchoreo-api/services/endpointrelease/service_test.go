// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointrelease

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/testutil"
)

const (
	testNamespace = "test-ns"
	testProject   = "test-project"
)

func TestListEndpointReleases(t *testing.T) {
	ctx := context.Background()
	svc := NewService(testutil.NewFakeClient(
		testutil.NewEndpointRelease(testNamespace, testProject, "payments-api", "payments-api-aaa"),
		testutil.NewEndpointRelease(testNamespace, testProject, "payments-api", "payments-api-bbb"),
		testutil.NewEndpointRelease(testNamespace, testProject, "orders-api", "orders-api-ccc"),
	), testutil.TestLogger())

	all, err := svc.ListEndpointReleases(ctx, testNamespace, "", services.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, all.Items, 3)

	filtered, err := svc.ListEndpointReleases(ctx, testNamespace, "payments-api", services.ListOptions{})
	require.NoError(t, err)
	require.Len(t, filtered.Items, 2)
	for _, er := range filtered.Items {
		assert.Equal(t, "payments-api", er.Spec.Owner.EndpointName)
		assert.Equal(t, endpointReleaseTypeMeta, er.TypeMeta)
	}
}

func TestGetEndpointRelease(t *testing.T) {
	ctx := context.Background()
	svc := NewService(testutil.NewFakeClient(
		testutil.NewEndpointRelease(testNamespace, testProject, "payments-api", "payments-api-aaa"),
	), testutil.TestLogger())

	er, err := svc.GetEndpointRelease(ctx, testNamespace, "payments-api-aaa")
	require.NoError(t, err)
	assert.Equal(t, endpointReleaseTypeMeta, er.TypeMeta)

	_, err = svc.GetEndpointRelease(ctx, testNamespace, "missing")
	require.ErrorIs(t, err, ErrEndpointReleaseNotFound)
}

func TestDeleteEndpointRelease(t *testing.T) {
	ctx := context.Background()
	newSvc := func() Service {
		ep := testutil.NewEndpoint(testNamespace, testProject, "payments-api")
		ep.Status.LatestRelease = &openchoreov1alpha1.LatestEndpointRelease{Name: "payments-api-latest", Hash: "h"}
		pinning := &openchoreov1alpha1.EndpointReleaseBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "payments-api-production", Namespace: testNamespace},
			Spec: openchoreov1alpha1.EndpointReleaseBindingSpec{
				Owner:           openchoreov1alpha1.EndpointReleaseBindingOwner{ProjectName: testProject, EndpointName: "payments-api"},
				Environment:     "production",
				EndpointRelease: "payments-api-pinned",
			},
		}
		return NewService(testutil.NewFakeClient(
			ep, pinning,
			testutil.NewEndpointRelease(testNamespace, testProject, "payments-api", "payments-api-old"),
			testutil.NewEndpointRelease(testNamespace, testProject, "payments-api", "payments-api-pinned"),
			testutil.NewEndpointRelease(testNamespace, testProject, "payments-api", "payments-api-latest"),
		), testutil.TestLogger())
	}

	t.Run("deletes an unused release", func(t *testing.T) {
		svc := newSvc()
		require.NoError(t, svc.DeleteEndpointRelease(ctx, testNamespace, "payments-api-old"))
		_, err := svc.GetEndpointRelease(ctx, testNamespace, "payments-api-old")
		require.ErrorIs(t, err, ErrEndpointReleaseNotFound)
	})

	t.Run("refuses a release an environment pins", func(t *testing.T) {
		err := newSvc().DeleteEndpointRelease(ctx, testNamespace, "payments-api-pinned")
		require.ErrorIs(t, err, ErrEndpointReleaseInUse)
		assert.Contains(t, err.Error(), "pinned in production")
	})

	t.Run("refuses the latest release", func(t *testing.T) {
		err := newSvc().DeleteEndpointRelease(ctx, testNamespace, "payments-api-latest")
		require.ErrorIs(t, err, ErrEndpointReleaseInUse)
	})

	t.Run("not found", func(t *testing.T) {
		err := newSvc().DeleteEndpointRelease(ctx, testNamespace, "missing")
		require.ErrorIs(t, err, ErrEndpointReleaseNotFound)
	})
}
