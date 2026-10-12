// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointrelease

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
