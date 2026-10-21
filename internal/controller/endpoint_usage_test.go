// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

func usageFixture(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, openchoreov1alpha1.AddToScheme(scheme))
	const ns = "default"
	backends := func(v1, v2 int32) []openchoreov1alpha1.EndpointBackendRef {
		return []openchoreov1alpha1.EndpointBackendRef{
			{Name: "payments-v1", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"}, Weight: ptr.To(v1)},
			{Name: "payments-v2", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v2", Endpoint: "http"}, Weight: ptr.To(v2)},
		}
	}
	objs := []client.Object{
		&openchoreov1alpha1.Endpoint{
			ObjectMeta: metav1.ObjectMeta{Name: "payments-api", Namespace: ns},
			Spec: openchoreov1alpha1.EndpointSpec{
				Owner:       openchoreov1alpha1.EndpointOwner{ProjectName: "shop"},
				BackendRefs: backends(0, 100),
			},
		},
		// Same name in another project must be ignored.
		&openchoreov1alpha1.Endpoint{
			ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: ns},
			Spec: openchoreov1alpha1.EndpointSpec{
				Owner:       openchoreov1alpha1.EndpointOwner{ProjectName: "billing"},
				BackendRefs: backends(100, 0),
			},
		},
		// Unbound Endpoint still shows up in the index, without environments.
		&openchoreov1alpha1.Endpoint{
			ObjectMeta: metav1.ObjectMeta{Name: "preview", Namespace: ns},
			Spec: openchoreov1alpha1.EndpointSpec{
				Owner:       openchoreov1alpha1.EndpointOwner{ProjectName: "shop"},
				BackendRefs: []openchoreov1alpha1.EndpointBackendRef{{Name: "payments-v1", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"}}},
			},
		},
		&openchoreov1alpha1.EndpointRelease{
			ObjectMeta: metav1.ObjectMeta{Name: "rel-9010", Namespace: ns},
			Spec:       openchoreov1alpha1.EndpointReleaseSpec{BackendRefs: backends(90, 10)},
		},
		&openchoreov1alpha1.EndpointRelease{
			ObjectMeta: metav1.ObjectMeta{Name: "rel-0100", Namespace: ns},
			Spec:       openchoreov1alpha1.EndpointReleaseSpec{BackendRefs: backends(0, 100)},
		},
		binding(ns, "payments-api-development", "development", "rel-0100"),
		binding(ns, "payments-api-production", "production", "rel-9010"),
		binding(ns, "payments-api-staging", "staging", ""), // not pinned yet
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func binding(ns, name, env, release string) *openchoreov1alpha1.EndpointReleaseBinding {
	return &openchoreov1alpha1.EndpointReleaseBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: openchoreov1alpha1.EndpointReleaseBindingSpec{
			Owner:           openchoreov1alpha1.EndpointReleaseBindingOwner{ProjectName: "shop", EndpointName: "payments-api"},
			Environment:     env,
			EndpointRelease: release,
		},
	}
}

func TestComponentEndpointUsage(t *testing.T) {
	c := usageFixture(t)
	usages, err := ComponentEndpointUsage(context.Background(), c, "default", "shop", "payments-v1")
	require.NoError(t, err)
	assert.Equal(t, []EndpointUsage{
		{Endpoint: "payments-api", WorkloadEndpoint: "http", Environment: "development", Weight: 0, OtherBackends: 1},
		{Endpoint: "payments-api", WorkloadEndpoint: "http", Environment: "production", Weight: 90, OtherBackends: 1},
		{Endpoint: "preview", WorkloadEndpoint: "http"},
	}, usages)

	assert.Equal(t, []EndpointUsage{
		{Endpoint: "payments-api", WorkloadEndpoint: "http", Environment: "production", Weight: 90, OtherBackends: 1},
	}, RoutedEndpointUsage(usages))

	// The only backend of a release doesn't block deletion: there is nowhere to
	// move its traffic, and the Endpoint's reclaimPolicy takes over.
	assert.Empty(t, RoutedEndpointUsage([]EndpointUsage{
		{Endpoint: "solo", WorkloadEndpoint: "http", Environment: "production", Weight: 100},
	}))

	assert.Equal(t, []openchoreov1alpha1.ComponentEndpointRef{
		{Name: "payments-api", Endpoint: "http", Environments: []openchoreov1alpha1.ComponentEndpointEnvironment{
			{Name: "development", Weight: 0}, {Name: "production", Weight: 90},
		}},
		{Name: "preview", Endpoint: "http"},
	}, ComponentEndpointRefs(usages))
}

func TestComponentEndpointUsageNone(t *testing.T) {
	c := usageFixture(t)
	usages, err := ComponentEndpointUsage(context.Background(), c, "default", "shop", "orders")
	require.NoError(t, err)
	assert.Empty(t, usages)
	assert.Empty(t, RoutedEndpointUsage(usages))
}

func TestComponentEndpointUsageServedURL(t *testing.T) {
	c := usageFixture(t)
	ep := &openchoreov1alpha1.Endpoint{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "payments-api"}, ep))
	ep.Spec.TakeOver = &openchoreov1alpha1.EndpointTakeOver{Component: openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"}}
	require.NoError(t, c.Update(context.Background(), ep))

	usages, err := ComponentEndpointUsage(context.Background(), c, "default", "shop", "payments-v1")
	require.NoError(t, err)

	// At weight 0 in development payments-v1 still serves the URL the other
	// backend's traffic comes in on, so deleting it would take that URL down.
	routed := RoutedEndpointUsage(usages)
	assert.Equal(t, []EndpointUsage{
		{Endpoint: "payments-api", WorkloadEndpoint: "http", Environment: "development", Weight: 0, OtherBackends: 1, ServesURL: true},
		{Endpoint: "payments-api", WorkloadEndpoint: "http", Environment: "production", Weight: 90, OtherBackends: 1, ServesURL: true},
	}, routed)
	assert.Equal(t,
		"receives traffic through payments-api in production (weight 90); "+
			"has its URL served by payments-api in development, payments-api in production; "+
			"move its weight to 0 and roll out, and delete the Endpoint serving its URL before deleting",
		StillRoutedReason(routed))

	refs := ComponentEndpointRefs(usages)
	require.NotEmpty(t, refs)
	assert.True(t, refs[0].ServesURL)
}
