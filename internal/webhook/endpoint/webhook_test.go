// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openchoreodevv1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

func endpoint(name, project string, weights map[string]int32) *openchoreodevv1alpha1.Endpoint {
	ep := &openchoreodevv1alpha1.Endpoint{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       openchoreodevv1alpha1.EndpointSpec{Owner: openchoreodevv1alpha1.EndpointOwner{ProjectName: project}},
	}
	for _, c := range []string{"payments-v1", "payments-v2"} {
		if w, ok := weights[c]; ok {
			ep.Spec.BackendRefs = append(ep.Spec.BackendRefs,
				openchoreodevv1alpha1.EndpointBackendRef{ComponentName: c, Endpoint: "http", Weight: ptr.To(w)})
		}
	}
	return ep
}

func validator(t *testing.T, objs ...client.Object) *Validator {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, openchoreodevv1alpha1.AddToScheme(scheme))
	return &Validator{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()}
}

func TestValidateServingSlots(t *testing.T) {
	existing := endpoint("payments-v1-http", "shop", map[string]int32{"payments-v1": 100})

	tests := []struct {
		name    string
		ep      *openchoreodevv1alpha1.Endpoint
		wantErr bool
	}{
		{"second Endpoint sending traffic to the same slot", endpoint("payments-api", "shop", map[string]int32{"payments-v1": 50, "payments-v2": 50}), true},
		{"same slot staged at weight 0", endpoint("payments-api", "shop", map[string]int32{"payments-v1": 0, "payments-v2": 100}), false},
		{"different slot", endpoint("payments-api", "shop", map[string]int32{"payments-v2": 100}), false},
		{"other project", endpoint("payments-api", "billing", map[string]int32{"payments-v1": 100}), false},
		{"updating the same Endpoint", endpoint("payments-v1-http", "shop", map[string]int32{"payments-v1": 100}), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := validator(t, existing)
			_, err := v.ValidateCreate(context.Background(), tt.ep)
			if tt.wantErr {
				require.Error(t, err)
				assert.True(t, apierrors.IsInvalid(err))
				assert.Contains(t, err.Error(), `Endpoint "payments-v1-http"`)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateServingSlotsIgnoresDeletingEndpoints(t *testing.T) {
	deleting := endpoint("payments-v1-http", "shop", map[string]int32{"payments-v1": 100})
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"keep"}
	v := validator(t, deleting)

	_, err := v.ValidateCreate(context.Background(), endpoint("payments-api", "shop", map[string]int32{"payments-v1": 100}))
	require.NoError(t, err)
}
