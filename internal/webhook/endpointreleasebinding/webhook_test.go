// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointreleasebinding

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openchoreodevv1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

func erb(name, endpoint, env string) *openchoreodevv1alpha1.EndpointReleaseBinding {
	return &openchoreodevv1alpha1.EndpointReleaseBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: openchoreodevv1alpha1.EndpointReleaseBindingSpec{
			Owner:       openchoreodevv1alpha1.EndpointReleaseBindingOwner{ProjectName: "shop", EndpointName: endpoint},
			Environment: env,
		},
	}
}

func TestValidateCreateOneBindingPerEnvironment(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openchoreodevv1alpha1.AddToScheme(scheme))
	v := &Validator{Client: fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(erb("payments-api-development", "payments-api", "development")).Build()}

	_, err := v.ValidateCreate(context.Background(), erb("payments-api-dev-2", "payments-api", "development"))
	require.Error(t, err)
	assert.True(t, apierrors.IsInvalid(err))

	_, err = v.ValidateCreate(context.Background(), erb("payments-api-staging", "payments-api", "staging"))
	require.NoError(t, err)

	_, err = v.ValidateCreate(context.Background(), erb("orders-api-development", "orders-api", "development"))
	require.NoError(t, err)
}
