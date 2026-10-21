// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package component

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	ocLabels "github.com/openchoreo/openchoreo/internal/labels"
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

func testWorkload(endpoints map[string]openchoreov1alpha1.WorkloadEndpoint) *openchoreov1alpha1.Workload {
	w := &openchoreov1alpha1.Workload{}
	w.Spec.Endpoints = endpoints
	return w
}

var gatewayType = &openchoreov1alpha1.ClusterEndpointType{ObjectMeta: metav1.ObjectMeta{Name: "http-gateway"}}

func exposed(name string) openchoreov1alpha1.WorkloadEndpoint {
	return openchoreov1alpha1.WorkloadEndpoint{
		Type: openchoreov1alpha1.EndpointProtocolHTTP, Port: 8080,
		EndpointType: &openchoreov1alpha1.EndpointTypeRef{
			Kind: openchoreov1alpha1.EndpointTypeRefKindClusterEndpointType, Name: "http-gateway",
		},
		EndpointName: name,
	}
}

func TestReconcileExposedEndpointsCreatesMissing(t *testing.T) {
	r := newEndpointsReconciler(t, gatewayType)
	comp := testComponent()
	wl := testWorkload(map[string]openchoreov1alpha1.WorkloadEndpoint{
		"http":    exposed(""),
		"metrics": {Type: openchoreov1alpha1.EndpointProtocolHTTP, Port: 9090},
	})

	require.NoError(t, r.reconcileExposedEndpoints(context.Background(), comp, wl))

	ep := &openchoreov1alpha1.Endpoint{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: "payments-v1-http"}, ep))
	assert.Equal(t, "shop", ep.Spec.Owner.ProjectName)
	assert.Equal(t, openchoreov1alpha1.EndpointAddress{Path: "/payments-v1-http"}, ep.Spec.Address,
		"the Endpoint's address is the URL path the component already serves")
	require.NotNil(t, ep.Spec.TakeOver)
	assert.Equal(t, openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"}, ep.Spec.TakeOver.Component,
		"and it takes that URL over, so clients keep calling it (case 5)")
	assert.Equal(t, []openchoreov1alpha1.EndpointBackendRef{
		{Name: "payments-v1", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"}, Weight: ptr.To[int32](100)},
	}, ep.Spec.BackendRefs)
	assert.Equal(t, openchoreov1alpha1.EndpointReclaimPolicyDelete, ep.Spec.ReclaimPolicy)
	assert.Equal(t, "workload/payments-v1/http", ep.Annotations[ocLabels.AnnotationKeyEndpointCreatedFrom])
	assert.Empty(t, ep.OwnerReferences, "the component must not own the Endpoint")

	cond := meta.FindStatusCondition(comp.Status.Conditions, string(ConditionEndpointsExposed))
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)

	// Not exposing anything removes the condition.
	require.NoError(t, r.reconcileExposedEndpoints(context.Background(), comp, testWorkload(nil)))
	assert.Nil(t, meta.FindStatusCondition(comp.Status.Conditions, string(ConditionEndpointsExposed)))
}

func TestReconcileExposedEndpointsLeavesExistingAlone(t *testing.T) {
	existing := &openchoreov1alpha1.Endpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-api", Namespace: testNS},
		Spec: openchoreov1alpha1.EndpointSpec{
			Owner:    openchoreov1alpha1.EndpointOwner{ProjectName: "shop"},
			Type:     openchoreov1alpha1.EndpointTypeRef{Kind: openchoreov1alpha1.EndpointTypeRefKindClusterEndpointType, Name: "http-gateway"},
			TakeOver: &openchoreov1alpha1.EndpointTakeOver{Component: openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"}},
			BackendRefs: []openchoreov1alpha1.EndpointBackendRef{
				{Name: "payments-v1", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"}, Weight: ptr.To[int32](90)},
				{Name: "payments-v2", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v2", Endpoint: "http"}, Weight: ptr.To[int32](10)},
			},
		},
	}
	r := newEndpointsReconciler(t, gatewayType, existing)
	comp := testComponent()

	require.NoError(t, r.reconcileExposedEndpoints(context.Background(), comp,
		testWorkload(map[string]openchoreov1alpha1.WorkloadEndpoint{"http": exposed("payments-api")})))

	got := &openchoreov1alpha1.Endpoint{}
	require.NoError(t, r.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: "payments-api"}, got))
	assert.Equal(t, existing.Spec.BackendRefs, got.Spec.BackendRefs, "weights are a person's to change")
	cond := meta.FindStatusCondition(comp.Status.Conditions, string(ConditionEndpointsExposed))
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
}

func TestReconcileExposedEndpointsProblems(t *testing.T) {
	other := &openchoreov1alpha1.Endpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-api", Namespace: testNS},
		Spec: openchoreov1alpha1.EndpointSpec{
			Owner:       openchoreov1alpha1.EndpointOwner{ProjectName: "shop"},
			Type:        openchoreov1alpha1.EndpointTypeRef{Kind: openchoreov1alpha1.EndpointTypeRefKindClusterEndpointType, Name: "http-gateway"},
			TakeOver:    &openchoreov1alpha1.EndpointTakeOver{Component: openchoreov1alpha1.EndpointComponentRef{Name: "orders", Endpoint: "http"}},
			BackendRefs: []openchoreov1alpha1.EndpointBackendRef{{Name: "orders", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "orders", Endpoint: "http"}}},
		},
	}
	mine := &openchoreov1alpha1.Endpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-v1-http", Namespace: testNS},
		Spec: openchoreov1alpha1.EndpointSpec{
			Owner:       openchoreov1alpha1.EndpointOwner{ProjectName: "shop"},
			Type:        openchoreov1alpha1.EndpointTypeRef{Name: "legacy"},
			TakeOver:    &openchoreov1alpha1.EndpointTakeOver{Component: openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"}},
			BackendRefs: []openchoreov1alpha1.EndpointBackendRef{{Name: "payments-v1", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"}}},
		},
	}

	tests := []struct {
		name   string
		wep    openchoreov1alpha1.WorkloadEndpoint
		reason string
	}{
		{"conflict", exposed("orders-api"), string(ReasonExposeConflict)},
		{"type mismatch", exposed(""), string(ReasonEndpointTypeMismatch)},
		{"type not found", openchoreov1alpha1.WorkloadEndpoint{
			Type: openchoreov1alpha1.EndpointProtocolHTTP, Port: 8080,
			EndpointType: &openchoreov1alpha1.EndpointTypeRef{Name: "missing"},
		}, string(ReasonEndpointTypeNotFound)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newEndpointsReconciler(t, gatewayType, other, mine)
			comp := testComponent()
			require.NoError(t, r.reconcileExposedEndpoints(context.Background(), comp,
				testWorkload(map[string]openchoreov1alpha1.WorkloadEndpoint{"http": tt.wep})))
			cond := meta.FindStatusCondition(comp.Status.Conditions, string(ConditionEndpointsExposed))
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, tt.reason, cond.Reason)
		})
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
			Owner:    openchoreov1alpha1.EndpointOwner{ProjectName: "shop"},
			TakeOver: &openchoreov1alpha1.EndpointTakeOver{Component: openchoreov1alpha1.EndpointComponentRef{Name: "payments-v2", Endpoint: "http"}},
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

func TestPruneFromEndpointsDropsTheTakeOver(t *testing.T) {
	takesOver := &openchoreov1alpha1.Endpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-api", Namespace: testNS},
		Spec: openchoreov1alpha1.EndpointSpec{
			Owner:    openchoreov1alpha1.EndpointOwner{ProjectName: "shop"},
			Address:  openchoreov1alpha1.EndpointAddress{Path: "/payments"},
			TakeOver: &openchoreov1alpha1.EndpointTakeOver{Component: openchoreov1alpha1.EndpointComponentRef{Name: "payments-v1", Endpoint: "http"}},
			BackendRefs: []openchoreov1alpha1.EndpointBackendRef{
				{Name: "payments-v2", Component: &openchoreov1alpha1.EndpointComponentRef{Name: "payments-v2", Endpoint: "http"}, Weight: ptr.To[int32](100)},
			},
			ReclaimPolicy: openchoreov1alpha1.EndpointReclaimPolicyDelete,
		},
	}
	r := newEndpointsReconciler(t, takesOver)
	require.NoError(t, r.pruneFromEndpoints(context.Background(), testComponent()))

	got := &openchoreov1alpha1.Endpoint{}
	require.NoError(t, r.Get(context.Background(), client.ObjectKeyFromObject(takesOver), got),
		"the Endpoint owns its address, so it survives the component whose URL it took over (case 10)")
	assert.Nil(t, got.Spec.TakeOver)
	assert.Equal(t, "/payments", got.Spec.Address.Path)
	assert.Len(t, got.Spec.BackendRefs, 1)
}
