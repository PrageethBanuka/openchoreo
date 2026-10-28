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

const typeName = "http"

var (
	ext      = openchoreodevv1alpha1.EndpointVisibilityExternal
	internal = openchoreodevv1alpha1.EndpointVisibilityInternal
)

// endpoint builds an Endpoint in namespace default with one visibility and
// the given component backends at weight 100.
func endpoint(name, project string, visibility openchoreodevv1alpha1.EndpointVisibility, backends ...string) *openchoreodevv1alpha1.Endpoint {
	ep := &openchoreodevv1alpha1.Endpoint{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: openchoreodevv1alpha1.EndpointSpec{
			Owner:      openchoreodevv1alpha1.EndpointOwner{ProjectName: project},
			Type:       openchoreodevv1alpha1.EndpointTypeRef{Kind: openchoreodevv1alpha1.EndpointTypeRefKindClusterEndpointType, Name: typeName},
			Visibility: []openchoreodevv1alpha1.EndpointVisibility{visibility},
		},
	}
	for _, c := range backends {
		ep.Spec.BackendRefs = append(ep.Spec.BackendRefs, openchoreodevv1alpha1.EndpointBackendRef{
			Name: c, Component: &openchoreodevv1alpha1.EndpointComponentRef{Name: c, Endpoint: "http"}, Weight: ptr.To[int32](100),
		})
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

func TestValidateSlots(t *testing.T) {
	existing := endpoint("trips-api", "rides", ext, "trips-python", "trips-go")
	otherNS := endpoint("trips-api", "rides", ext, "trips-python")
	otherNS.Namespace = "finance"

	tests := []struct {
		name     string
		ep       *openchoreodevv1alpha1.Endpoint
		contains string // empty means admitted
	}{
		{"same component in the same visibility",
			endpoint("trips-canary", "rides", ext, "trips-go"), `Endpoint "trips-api" already routes trips-go/http with external visibility`},
		{"same component in another visibility",
			endpoint("trips-partners", "rides", internal, "trips-python"), ""},
		{"same component name in another project",
			endpoint("trips-copy", "billing", ext, "trips-python"), ""},
		{"another component",
			endpoint("checkout", "rides", ext, "checkout-blue"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := validator(t, clusterType(nil), existing, otherNS)
			_, err := v.ValidateCreate(context.Background(), tt.ep)
			if tt.contains == "" {
				require.NoError(t, err)
				return
			}
			refused(t, err, tt.contains)
		})
	}
}

func TestValidateTypeLimits(t *testing.T) {
	withHost := func(host string) *openchoreodevv1alpha1.Endpoint {
		ep := endpoint("orders-api", "shop", ext, "orders-v1")
		ep.Spec.Parameters = &runtime.RawExtension{Raw: []byte(`{"hostname":"` + host + `"}`)}
		return ep
	}
	_, err := validator(t, clusterType([]string{"*.example.com"})).ValidateCreate(context.Background(), withHost("orders.other.org"))
	refused(t, err, "allowed: *.example.com")

	_, err = validator(t, clusterType(nil)).ValidateCreate(context.Background(), withHost("orders.example.com"))
	refused(t, err, "leave hostname empty")

	_, err = validator(t, clusterType([]string{"*.example.com"})).ValidateCreate(context.Background(), withHost("orders.example.com"))
	require.NoError(t, err)

	_, err = validator(t, clusterType(nil, openchoreodevv1alpha1.EndpointBackendKindComponent)).
		ValidateCreate(context.Background(), endpoint("orders-api", "shop", ext, "orders-v1"))
	require.NoError(t, err)
}

func TestValidateWeights(t *testing.T) {
	ep := endpoint("orders-api", "shop", ext, "orders-v1")
	ep.Spec.BackendRefs[0].Weight = ptr.To[int32](0)
	_, err := validator(t, clusterType(nil)).ValidateCreate(context.Background(), ep)
	refused(t, err, "every backend has weight 0")
}

func TestValidateIgnoresDeletingEndpoints(t *testing.T) {
	deleting := endpoint("payments-api", "shop", ext, "payments-v1")
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"keep"}
	v := validator(t, clusterType(nil), deleting)

	_, err := v.ValidateCreate(context.Background(), endpoint("payments-next", "shop", ext, "payments-v1"))
	require.NoError(t, err)
}

func TestValidateUpdateChecksOnlyChangedClaims(t *testing.T) {
	holder := endpoint("payments-api", "shop", ext, "payments-v1")
	// An Endpoint that got past the webhook (held back by the controller) can
	// still be updated, for example to add its finalizer.
	heldBack := endpoint("payments-canary", "shop", ext, "payments-v1")
	v := validator(t, clusterType(nil), holder)

	updated := heldBack.DeepCopy()
	updated.Finalizers = []string{"openchoreo.dev/endpoint-cleanup"}
	_, err := v.ValidateUpdate(context.Background(), heldBack, updated)
	require.NoError(t, err)

	partners := endpoint("payments-partners", "shop", internal, "payments-v1")
	widened := partners.DeepCopy()
	widened.Spec.Visibility = append(widened.Spec.Visibility, ext)
	_, err = v.ValidateUpdate(context.Background(), partners, widened)
	refused(t, err, `Endpoint "payments-api" already routes payments-v1/http`)
}

func TestValidateParameters(t *testing.T) {
	typed := clusterType(nil)
	typed.Spec.Parameters = &openchoreodevv1alpha1.SchemaSection{OpenAPIV3Schema: &runtime.RawExtension{Raw: []byte(`{
		"type": "object",
		"properties": {
			"mirrors": {"type": "array", "items": {"type": "object", "required": ["backendRef", "percent"],
				"properties": {"backendRef": {"type": "string"}, "percent": {"type": "integer", "minimum": 0, "maximum": 100}}}}
		},
		"additionalProperties": false
	}`)}}
	withParams := func(raw string) *openchoreodevv1alpha1.Endpoint {
		ep := endpoint("orders-api", "shop", ext, "orders-v1")
		ep.Spec.Parameters = &runtime.RawExtension{Raw: []byte(raw)}
		return ep
	}

	_, err := validator(t, typed).ValidateCreate(context.Background(), withParams(`{"mirrors":[{"backendRef":"orders-v1","percent":10}]}`))
	require.NoError(t, err)

	_, err = validator(t, typed).ValidateCreate(context.Background(), withParams(`{"mirrors":[{"backendRef":"orders-v1","percent":150}]}`))
	refused(t, err, "parameters do not match the schema")

	_, err = validator(t, typed).ValidateCreate(context.Background(), withParams(`{"rules":[]}`))
	refused(t, err, "parameters do not match the schema")
}

func TestValidateMatches(t *testing.T) {
	withMatch := func(ms ...openchoreodevv1alpha1.EndpointMatch) *openchoreodevv1alpha1.Endpoint {
		ep := endpoint("cart", "shop", ext, "storefront", "storefront-recs", "storefront-beta")
		for i := range ms {
			ep.Spec.BackendRefs[i+1].Weight = ptr.To[int32](0)
			ep.Spec.BackendRefs[i+1].Match = &ms[i]
		}
		return ep
	}
	regex := openchoreodevv1alpha1.EndpointHeaderMatchRegularExpression
	v := validator(t, clusterType(nil))

	_, err := v.ValidateCreate(context.Background(), withMatch(openchoreodevv1alpha1.EndpointMatch{
		Path: &openchoreodevv1alpha1.EndpointPathMatch{Value: "/next"},
		Headers: []openchoreodevv1alpha1.EndpointHeaderMatch{
			{Name: "X-Client", Value: "ios"},
			{Name: "user-agent", Type: regex, Value: ".*Mobi.*"},
		},
		Cookies: []openchoreodevv1alpha1.EndpointCookieMatch{{Name: "exp_cartrecs", Value: "b"}},
	}))
	require.NoError(t, err)

	_, err = v.ValidateCreate(context.Background(), withMatch(openchoreodevv1alpha1.EndpointMatch{
		Headers: []openchoreodevv1alpha1.EndpointHeaderMatch{
			{Name: "X-Client", Value: "ios"},
			{Name: "x-client", Value: "android"},
		},
	}))
	refused(t, err, "Duplicate value")

	_, err = v.ValidateCreate(context.Background(), withMatch(openchoreodevv1alpha1.EndpointMatch{
		Headers: []openchoreodevv1alpha1.EndpointHeaderMatch{{Name: "Cookie", Type: regex, Value: ".*beta.*"}},
		Cookies: []openchoreodevv1alpha1.EndpointCookieMatch{{Name: "exp_cartrecs", Value: "b"}},
	}))
	refused(t, err, "not both")

	_, err = v.ValidateCreate(context.Background(), withMatch(openchoreodevv1alpha1.EndpointMatch{
		Headers: []openchoreodevv1alpha1.EndpointHeaderMatch{{Name: "user-agent", Type: regex, Value: "(?=Mobi)"}},
	}))
	refused(t, err, "RE2")

	for _, p := range []string{"/a/../admin", "/./a", "/a//b"} {
		_, err = v.ValidateCreate(context.Background(), withMatch(openchoreodevv1alpha1.EndpointMatch{
			Path: &openchoreodevv1alpha1.EndpointPathMatch{Value: p},
		}))
		refused(t, err, "match.path.value")
	}

	_, err = v.ValidateCreate(context.Background(), withMatch(
		openchoreodevv1alpha1.EndpointMatch{Headers: []openchoreodevv1alpha1.EndpointHeaderMatch{{Name: "X-Preview", Value: "on"}}},
		openchoreodevv1alpha1.EndpointMatch{Headers: []openchoreodevv1alpha1.EndpointHeaderMatch{{Name: "x-preview", Type: openchoreodevv1alpha1.EndpointHeaderMatchExact, Value: "on"}}},
	))
	refused(t, err, `backend "storefront-recs" has the same match`)
}
