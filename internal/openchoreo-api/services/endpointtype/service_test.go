// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointtype

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/testutil"
)

func TestListAndGetEndpointType(t *testing.T) {
	ctx := context.Background()
	svc := NewService(testutil.NewFakeClient(testutil.NewEndpointType(authzNamespace, "http-weighted")), testutil.TestLogger())

	list, err := svc.ListEndpointTypes(ctx, authzNamespace, services.ListOptions{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, endpointTypeTypeMeta, list.Items[0].TypeMeta)

	got, err := svc.GetEndpointType(ctx, authzNamespace, "http-weighted")
	require.NoError(t, err)
	assert.Equal(t, endpointTypeTypeMeta, got.TypeMeta)
	assert.Len(t, got.Spec.Resources, 1)

	_, err = svc.GetEndpointType(ctx, authzNamespace, "missing")
	require.ErrorIs(t, err, ErrEndpointTypeNotFound)
}
