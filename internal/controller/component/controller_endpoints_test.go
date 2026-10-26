// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package component

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

const testNS = "default"

func newEndpointsReconciler(t *testing.T, objs ...client.Object) *Reconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, openchoreov1alpha1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &Reconciler{Client: c, Scheme: scheme}
}

func testComponent() *openchoreov1alpha1.Component {
	const name = "payments-v1"
	return &openchoreov1alpha1.Component{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS, Generation: 1},
		Spec: openchoreov1alpha1.ComponentSpec{
			Owner: openchoreov1alpha1.ComponentOwner{ProjectName: "shop"},
		},
	}
}

func TestStillRoutedAndIndex(t *testing.T) {
	ep := &openchoreov1alpha1.Endpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-api", Namespace: testNS},
		Spec: openchoreov1alpha1.EndpointSpec{
			Owner: openchoreov1alpha1.EndpointOwner{ProjectName: "shop"},
			BackendRefs: []openchoreov1alpha1.EndpointBackendRef{
				{Name: "payments-v1", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"}, Weight: ptr.To[int32](0)},
				{Name: "payments-v2", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v2", Endpoint: "http"}, Weight: ptr.To[int32](100)},
			},
		},
	}
	release := func(name string, v1 int32) *openchoreov1alpha1.EndpointRelease {
		return &openchoreov1alpha1.EndpointRelease{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
			Spec: openchoreov1alpha1.EndpointReleaseSpec{BackendRefs: []openchoreov1alpha1.EndpointBackendRef{
				{Name: "payments-v1", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"}, Weight: ptr.To(v1)},
				{Name: "payments-v2", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v2", Endpoint: "http"}, Weight: ptr.To(100 - v1)},
			}},
		}
	}
	binding := &openchoreov1alpha1.EndpointReleaseBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-api-production", Namespace: testNS},
		Spec: openchoreov1alpha1.EndpointReleaseBindingSpec{
			Owner:           openchoreov1alpha1.EndpointReleaseBindingOwner{ProjectName: "shop", EndpointName: "payments-api"},
			Environment:     "production",
			EndpointRelease: "rel-10",
		},
	}
	r := newEndpointsReconciler(t, ep, release("rel-10", 10), release("rel-0", 0), binding)
	comp := testComponent()

	msg, err := r.stillRouted(context.Background(), comp)
	require.NoError(t, err)
	assert.Contains(t, msg, "payments-api in production (weight 10)")

	require.NoError(t, r.updateEndpointIndex(context.Background(), comp))
	assert.Equal(t, []openchoreov1alpha1.ComponentEndpointRef{{
		Name: "payments-api", Endpoint: "http",
		Environments: []openchoreov1alpha1.ComponentEndpointEnvironment{{Name: "production", Weight: 10}},
	}}, comp.Status.Endpoints)

	// Rolled out to 0: deletion may proceed.
	binding.Spec.EndpointRelease = "rel-0"
	require.NoError(t, r.Update(context.Background(), binding))
	msg, err = r.stillRouted(context.Background(), comp)
	require.NoError(t, err)
	assert.Empty(t, msg)
}

func TestFinalizeHoldsWhileStillRouted(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, openchoreov1alpha1.AddToScheme(scheme))
	now := metav1.Now()
	comp := testComponent()
	comp.Finalizers = []string{ComponentFinalizer}
	comp.DeletionTimestamp = &now
	comp.Status.Conditions = []metav1.Condition{NewComponentFinalizingCondition(comp.Generation)}
	ep := &openchoreov1alpha1.Endpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-api", Namespace: testNS},
		Spec: openchoreov1alpha1.EndpointSpec{
			Owner:       openchoreov1alpha1.EndpointOwner{ProjectName: "shop"},
			BackendRefs: []openchoreov1alpha1.EndpointBackendRef{{Name: "payments-v1", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"}}},
		},
	}
	rel := &openchoreov1alpha1.EndpointRelease{
		ObjectMeta: metav1.ObjectMeta{Name: "rel", Namespace: testNS},
		Spec: openchoreov1alpha1.EndpointReleaseSpec{BackendRefs: []openchoreov1alpha1.EndpointBackendRef{
			{Name: "payments-v1", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"}, Weight: ptr.To[int32](100)},
			{Name: "payments-v2", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v2", Endpoint: "http"}, Weight: ptr.To[int32](0)},
		}},
	}
	binding := &openchoreov1alpha1.EndpointReleaseBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-api-development", Namespace: testNS},
		Spec: openchoreov1alpha1.EndpointReleaseBindingSpec{
			Owner:           openchoreov1alpha1.EndpointReleaseBindingOwner{ProjectName: "shop", EndpointName: "payments-api"},
			Environment:     "development",
			EndpointRelease: "rel",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(comp, ep, rel, binding).
		WithStatusSubresource(&openchoreov1alpha1.Component{}).
		Build()
	r := &Reconciler{Client: c, Scheme: scheme}

	cur := &openchoreov1alpha1.Component{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(comp), cur))
	res, err := r.finalize(context.Background(), cur.DeepCopy(), cur)
	require.NoError(t, err)
	assert.Equal(t, stillRoutedRequeue, res.RequeueAfter)

	got := &openchoreov1alpha1.Component{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(comp), got))
	assert.Contains(t, got.Finalizers, ComponentFinalizer, "deletion must be held")
	cond := meta.FindStatusCondition(got.Status.Conditions, string(ConditionFinalizing))
	require.NotNil(t, cond)
	assert.Equal(t, string(ReasonStillRouted), cond.Reason)
	assert.Contains(t, cond.Message, "payments-api in development (weight 100)")
}

func TestPruneFromEndpoints(t *testing.T) {
	shared := &openchoreov1alpha1.Endpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-api", Namespace: testNS},
		Spec: openchoreov1alpha1.EndpointSpec{
			Owner: openchoreov1alpha1.EndpointOwner{ProjectName: "shop"},
			BackendRefs: []openchoreov1alpha1.EndpointBackendRef{
				{Name: "payments-v1", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"}, Weight: ptr.To[int32](0)},
				{Name: "payments-v2", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v2", Endpoint: "http"}, Weight: ptr.To[int32](100)},
			},
		},
	}
	otherProject := &openchoreov1alpha1.Endpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "billing-api", Namespace: testNS},
		Spec: openchoreov1alpha1.EndpointSpec{
			Owner:       openchoreov1alpha1.EndpointOwner{ProjectName: "billing"},
			BackendRefs: []openchoreov1alpha1.EndpointBackendRef{{Name: "payments-v1", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"}}},
		},
	}
	r := newEndpointsReconciler(t, shared, otherProject)
	require.NoError(t, r.pruneFromEndpoints(context.Background(), testComponent()))

	got := &openchoreov1alpha1.Endpoint{}
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(shared), got))
	assert.Equal(t, []openchoreov1alpha1.EndpointBackendRef{
		{Name: "payments-v2", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v2", Endpoint: "http"}, Weight: ptr.To[int32](100)},
	}, got.Spec.BackendRefs)

	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(otherProject), got))
	assert.Len(t, got.Spec.BackendRefs, 1, "Endpoints of other projects are untouched")
}

func TestPruneFromEndpointsDropsRuleBackends(t *testing.T) {
	ep := &openchoreov1alpha1.Endpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: testNS},
		Spec: openchoreov1alpha1.EndpointSpec{
			Owner: openchoreov1alpha1.EndpointOwner{ProjectName: "shop"},
			BackendRefs: []openchoreov1alpha1.EndpointBackendRef{
				{Name: "payments-v1", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"}, Weight: ptr.To[int32](100)},
				{Name: "payments-v2", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v2", Endpoint: "http"}, Weight: ptr.To[int32](0)},
			},
			Rules: []openchoreov1alpha1.EndpointRule{
				{
					Name:     "preview-v1",
					Matches:  []openchoreov1alpha1.EndpointMatch{{Headers: []openchoreov1alpha1.EndpointHeaderMatch{{Name: "x-preview", Value: "v1"}}}},
					Backends: []openchoreov1alpha1.EndpointRuleBackend{{Name: "payments-v1"}},
				},
				{
					Name:     "default",
					Backends: []openchoreov1alpha1.EndpointRuleBackend{{Name: "payments-v1", Weight: ptr.To[int32](90)}, {Name: "payments-v2", Weight: ptr.To[int32](10)}},
					Mirrors:  []openchoreov1alpha1.EndpointMirror{{Backend: "payments-v1", Percent: 10}},
				},
			},
		},
	}
	r := newEndpointsReconciler(t, ep)
	require.NoError(t, r.pruneFromEndpoints(context.Background(), testComponent()))

	got := &openchoreov1alpha1.Endpoint{}
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(ep), got))
	assert.Len(t, got.Spec.BackendRefs, 1)
	require.Len(t, got.Spec.Rules, 1, "a rule left without backends is dropped")
	assert.Equal(t, "default", got.Spec.Rules[0].Name)
	assert.Equal(t, []openchoreov1alpha1.EndpointRuleBackend{{Name: "payments-v2", Weight: ptr.To[int32](10)}}, got.Spec.Rules[0].Backends)
	assert.Empty(t, got.Spec.Rules[0].Mirrors)
}

func TestPruneFromEndpointsDeletesWhenNoBackendIsLeft(t *testing.T) {
	solo := &openchoreov1alpha1.Endpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-api", Namespace: testNS},
		Spec: openchoreov1alpha1.EndpointSpec{
			Owner:       openchoreov1alpha1.EndpointOwner{ProjectName: "shop"},
			BackendRefs: []openchoreov1alpha1.EndpointBackendRef{{Name: "payments-v1", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"}}},
		},
	}
	r := newEndpointsReconciler(t, solo)
	require.NoError(t, r.pruneFromEndpoints(context.Background(), testComponent()))
	err := r.Get(context.Background(), client.ObjectKeyFromObject(solo), &openchoreov1alpha1.Endpoint{})
	assert.True(t, apierrors.IsNotFound(err), "an Endpoint whose only backend was the component is deleted")
}
