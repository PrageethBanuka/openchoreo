// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package clusterendpointtype

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/testutil"
)

func TestListAndGetClusterEndpointType(t *testing.T) {
	ctx := context.Background()
	svc := NewService(testutil.NewFakeClient(testutil.NewClusterEndpointType("http-weighted")), testutil.TestLogger())

	list, err := svc.ListClusterEndpointTypes(ctx, services.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, clusterEndpointTypeTypeMeta, list.Items[0].TypeMeta)

	got, err := svc.GetClusterEndpointType(ctx, "http-weighted")
	require.NoError(t, err)
	assert.Equal(t, clusterEndpointTypeTypeMeta, got.TypeMeta)
	assert.Len(t, got.Spec.Resources, 1)

	_, err = svc.GetClusterEndpointType(ctx, "missing")
	require.ErrorIs(t, err, ErrClusterEndpointTypeNotFound)
}
