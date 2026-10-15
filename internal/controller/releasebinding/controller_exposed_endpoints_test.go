// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package releasebinding

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

func exposingWorkload(endpointName string) *openchoreov1alpha1.Workload {
	w := &openchoreov1alpha1.Workload{}
	w.Spec.Endpoints = map[string]openchoreov1alpha1.WorkloadEndpoint{
		"http": {
			Type: openchoreov1alpha1.EndpointProtocolHTTP, Port: 8080,
			EndpointType: &openchoreov1alpha1.EndpointTypeRef{Name: "http-gateway"},
			EndpointName: endpointName,
		},
		"metrics": {Type: openchoreov1alpha1.EndpointProtocolHTTP, Port: 9090},
	}
	return w
}

func releasedEndpoint(release string) *openchoreov1alpha1.Endpoint {
	ep := claimsTestEndpoint("shop", "payments-v1")
	ep.Spec.BackendRefs[0].Weight = ptr.To[int32](100)
	if release != "" {
		ep.Status.LatestRelease = &openchoreov1alpha1.LatestEndpointRelease{Name: release, Hash: "h"}
	}
	return ep
}

func getERB(t *testing.T, r *Reconciler, name string) (*openchoreov1alpha1.EndpointReleaseBinding, bool) {
	t.Helper()
	erb := &openchoreov1alpha1.EndpointReleaseBinding{}
	err := r.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, erb)
	return erb, err == nil
}

func TestEnsureExposedEndpointBindingsCreatesPinnedBinding(t *testing.T) {
	r := claimsTestReconciler(t, releasedEndpoint("payments-api-abc"))

	require.NoError(t, r.ensureExposedEndpointBindings(context.Background(),
		componentRB("payments-v1", "development"), exposingWorkload("payments-api")))

	erb, ok := getERB(t, r, "payments-api-development")
	require.True(t, ok)
	assert.Equal(t, "payments-api-abc", erb.Spec.EndpointRelease)
	assert.Equal(t, "development", erb.Spec.Environment)
	assert.Equal(t, openchoreov1alpha1.EndpointReleaseBindingOwner{ProjectName: "shop", EndpointName: "payments-api"}, erb.Spec.Owner)
	assert.Empty(t, erb.OwnerReferences, "the binding belongs to the Endpoint's lifecycle")
}

func TestEnsureExposedEndpointBindingsNeverRepins(t *testing.T) {
	existing := claimsTestBinding("development", nil)
	existing.Spec.EndpointRelease = "payments-api-old"
	r := claimsTestReconciler(t, releasedEndpoint("payments-api-new"), existing)

	require.NoError(t, r.ensureExposedEndpointBindings(context.Background(),
		componentRB("payments-v1", "development"), exposingWorkload("payments-api")))

	erb, ok := getERB(t, r, "payments-api-development")
	require.True(t, ok)
	assert.Equal(t, "payments-api-old", erb.Spec.EndpointRelease, "rollouts stay a person's action")
}

func TestEnsureExposedEndpointBindingsSkips(t *testing.T) {
	tests := []struct {
		name     string
		endpoint *openchoreov1alpha1.Endpoint
		rb       *openchoreov1alpha1.ReleaseBinding
	}{
		{"endpoint not created yet", nil, componentRB("payments-v1", "development")},
		{"endpoint has no release yet", releasedEndpoint(""), componentRB("payments-v1", "development")},
		{"endpoint doesn't route to this component", releasedEndpoint("rel"), componentRB("payments-v9", "development")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := claimsTestReconciler(t)
			if tt.endpoint != nil {
				r = claimsTestReconciler(t, tt.endpoint)
			}
			require.NoError(t, r.ensureExposedEndpointBindings(context.Background(), tt.rb, exposingWorkload("payments-api")))
			_, ok := getERB(t, r, "payments-api-development")
			assert.False(t, ok)
		})
	}
}
