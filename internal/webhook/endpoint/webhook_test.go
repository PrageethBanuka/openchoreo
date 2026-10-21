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

const typeName = "http-weighted"

// endpoint builds an Endpoint at address path in namespace default, with the
// given component backends at weight 100.
func endpoint(name, project, path string, backends ...string) *openchoreodevv1alpha1.Endpoint {
	ep := &openchoreodevv1alpha1.Endpoint{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: openchoreodevv1alpha1.EndpointSpec{
			Owner:   openchoreodevv1alpha1.EndpointOwner{ProjectName: project},
			Type:    openchoreodevv1alpha1.EndpointTypeRef{Kind: openchoreodevv1alpha1.EndpointTypeRefKindClusterEndpointType, Name: typeName},
			Address: openchoreodevv1alpha1.EndpointAddress{Path: path},
		},
	}
	for _, c := range backends {
		ep.Spec.BackendRefs = append(ep.Spec.BackendRefs, openchoreodevv1alpha1.EndpointBackendRef{
			Name: c, Component: &openchoreodevv1alpha1.EndpointComponentRef{Name: c, Endpoint: "http"}, Weight: ptr.To[int32](100),
		})
	}
	return ep
}

// takingOver makes ep take over payments-v1's http URL.
func takingOver(ep *openchoreodevv1alpha1.Endpoint) *openchoreodevv1alpha1.Endpoint {
	ep.Spec.TakeOver = &openchoreodevv1alpha1.EndpointTakeOver{
		Component: openchoreodevv1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"},
	}
	return ep
}

func clusterType(hostnames []string, kinds ...openchoreodevv1alpha1.EndpointBackendKind) *openchoreodevv1alpha1.ClusterEndpointType {
	return &openchoreodevv1alpha1.ClusterEndpointType{
		ObjectMeta: metav1.ObjectMeta{Name: typeName},
		Spec: openchoreodevv1alpha1.ClusterEndpointTypeSpec{
			AllowedHostnames:    hostnames,
			AllowedBackendKinds: kinds,
		},
	}
}

// componentRoute is a component ReleaseBinding whose http endpoint is served
// at path on the environment host.
func componentRoute(component, path string) *openchoreodevv1alpha1.ReleaseBinding {
	return &openchoreodevv1alpha1.ReleaseBinding{
		ObjectMeta: metav1.ObjectMeta{Name: component + "-development", Namespace: "default"},
		Spec: openchoreodevv1alpha1.ReleaseBindingSpec{
			Owner:       openchoreodevv1alpha1.ReleaseBindingOwner{ProjectName: "shop", ComponentName: component},
			Environment: "development",
		},
		Status: openchoreodevv1alpha1.ReleaseBindingStatus{Endpoints: []openchoreodevv1alpha1.EndpointURLStatus{{
			Name: "http",
			ExternalURLs: &openchoreodevv1alpha1.EndpointGatewayURLs{
				HTTP: &openchoreodevv1alpha1.EndpointURL{Host: "development-default.openchoreoapis.localhost", Path: path},
			},
		}}},
	}
}

func validator(t *testing.T, objs ...client.Object) *Validator {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, openchoreodevv1alpha1.AddToScheme(scheme))
	return &Validator{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()}
}

func refused(t *testing.T, err error, contains string) {
	t.Helper()
	require.Error(t, err)
	assert.True(t, apierrors.IsInvalid(err), err.Error())
	assert.Contains(t, err.Error(), contains)
}

func TestValidateAddress(t *testing.T) {
	existing := endpoint("payments-api", "shop", "/payments", "payments-v1")
	otherNS := endpoint("billing-api", "billing", "/billing", "billing-v1")
	otherNS.Namespace = "finance"
	ownHost := endpoint("partner-api", "shop", "/partners", "payments-v1")
	ownHost.Spec.Address.Hostname = "api.example.com"
	ownHost.Namespace = "finance"

	tests := []struct {
		name     string
		ep       *openchoreodevv1alpha1.Endpoint
		contains string // empty means admitted
	}{
		{"same path on the environment host in the namespace (case 1, 2)",
			endpoint("payments-canary", "other-project", "/payments", "payments-v2"), `already served by Endpoint default/payments-api`},
		{"same path in another namespace's environment host",
			endpoint("billing-copy", "shop", "/billing", "payments-v1"), ""},
		{"same own hostname and path in another namespace (case 3)",
			func() *openchoreodevv1alpha1.Endpoint {
				ep := endpoint("partner-copy", "shop", "/partners", "payments-v1")
				ep.Spec.Address.Hostname = "api.example.com"
				return ep
			}(), `already served by Endpoint finance/partner-api`},
		{"different path sharing a backend",
			endpoint("payments-partner", "shop", "/payments-partner", "payments-v1"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := validator(t, clusterType([]string{"*.example.com"}), existing, otherNS, ownHost)
			_, err := v.ValidateCreate(context.Background(), tt.ep)
			if tt.contains == "" {
				require.NoError(t, err)
				return
			}
			refused(t, err, tt.contains)
		})
	}
}

func TestValidateComponentPath(t *testing.T) {
	v := validator(t, clusterType(nil), componentRoute("payments-v1", "/payments-v1-http"))

	_, err := v.ValidateCreate(context.Background(), endpoint("payments-api", "shop", "/payments-v1-http", "payments-v1"))
	refused(t, err, "take it over (spec.takeOver)")

	_, err = v.ValidateCreate(context.Background(),
		takingOver(endpoint("payments-api", "shop", "/payments-v1-http", "payments-v1")))
	require.NoError(t, err, "taking the route over is how its URL moves to the Endpoint (case 5)")
}

func TestValidateTakeOver(t *testing.T) {
	holder := takingOver(endpoint("payments-api", "shop", "/payments", "payments-v1"))
	v := validator(t, clusterType(nil), holder)

	_, err := v.ValidateCreate(context.Background(),
		takingOver(endpoint("payments-next", "shop", "/payments-next", "payments-v1")))
	refused(t, err, `already taken over by Endpoint "payments-api"`)
}

func TestValidateTypeLimits(t *testing.T) {
	external := func() *openchoreodevv1alpha1.Endpoint {
		ep := endpoint("orders-api", "shop", "/orders", "orders-v1")
		ep.Spec.BackendRefs = append(ep.Spec.BackendRefs, openchoreodevv1alpha1.EndpointBackendRef{
			Name: "legacy", External: &openchoreodevv1alpha1.EndpointExternalRef{URL: "https://legacy.example.com"}, Weight: ptr.To[int32](10),
		})
		return ep
	}

	_, err := validator(t, clusterType(nil)).ValidateCreate(context.Background(), external())
	refused(t, err, "does not allow External backends")

	_, err = validator(t, clusterType(nil, openchoreodevv1alpha1.EndpointBackendKindComponent,
		openchoreodevv1alpha1.EndpointBackendKindExternal)).ValidateCreate(context.Background(), external())
	require.NoError(t, err)

	withHost := endpoint("orders-api", "shop", "/orders", "orders-v1")
	withHost.Spec.Address.Hostname = "orders.other.org"
	_, err = validator(t, clusterType([]string{"*.example.com"})).ValidateCreate(context.Background(), withHost)
	refused(t, err, "allowed: *.example.com")

	_, err = validator(t, clusterType(nil)).ValidateCreate(context.Background(), withHost)
	refused(t, err, "leave hostname empty")
}

func TestValidateWeights(t *testing.T) {
	ep := endpoint("orders-api", "shop", "/orders", "orders-v1")
	ep.Spec.BackendRefs[0].Weight = ptr.To[int32](0)
	_, err := validator(t, clusterType(nil)).ValidateCreate(context.Background(), ep)
	refused(t, err, "every backend has weight 0")
}

func TestValidateIgnoresDeletingEndpoints(t *testing.T) {
	deleting := endpoint("payments-api", "shop", "/payments", "payments-v1")
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"keep"}
	v := validator(t, clusterType(nil), deleting)

	_, err := v.ValidateCreate(context.Background(), endpoint("payments-next", "shop", "/payments", "payments-v1"))
	require.NoError(t, err)
}

func TestValidateUpdateChecksOnlyChangedClaims(t *testing.T) {
	holder := endpoint("payments-api", "shop", "/payments", "payments-v1")
	// An Endpoint that got past the webhook (held back by the controller) can
	// still be updated, for example to add its finalizer.
	heldBack := endpoint("payments-canary", "shop", "/payments", "payments-v1")
	v := validator(t, clusterType(nil), holder)

	updated := heldBack.DeepCopy()
	updated.Finalizers = []string{"openchoreo.dev/endpoint-cleanup"}
	_, err := v.ValidateUpdate(context.Background(), heldBack, updated)
	require.NoError(t, err)

	added := endpoint("payments-other", "shop", "/payments-other", "payments-v1")
	withTakeOver := takingOver(added.DeepCopy())
	holderTakeOver := takingOver(endpoint("payments-old", "shop", "/payments-old", "payments-v1"))
	v = validator(t, clusterType(nil), holderTakeOver)
	_, err = v.ValidateUpdate(context.Background(), added, withTakeOver)
	refused(t, err, "already taken over")
}
