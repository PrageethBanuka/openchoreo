// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointrollout

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	endpointsvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpoint"
	endpointreleasesvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointrelease"
	endpointreleasebindingsvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointreleasebinding"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/testutil"
)

const (
	ns      = "test-ns"
	project = "test-project"
	epName  = "greeter-api"
	release = "greeter-api-1"
)

func candidate() openchoreov1alpha1.EndpointBackendRef {
	return openchoreov1alpha1.EndpointBackendRef{
		Name:      "v2",
		Kind:      openchoreov1alpha1.EndpointBackendKindComponent,
		Component: &openchoreov1alpha1.EndpointComponentBackend{Name: "greeter-v2", Endpoint: "http"},
		Weight:    ptr(int32(0)),
		Match: &openchoreov1alpha1.EndpointMatch{
			Headers: []openchoreov1alpha1.EndpointHeaderMatch{{Name: "x-canary", Value: "true"}},
		},
	}
}

func canaryPlan() openchoreov1alpha1.EndpointRollout {
	return openchoreov1alpha1.EndpointRollout{
		Strategy:   openchoreov1alpha1.EndpointRolloutCanary,
		Stable:     "stable",
		Candidates: []openchoreov1alpha1.EndpointRolloutCandidate{{Name: "v2"}},
		Canary: &openchoreov1alpha1.EndpointRolloutSteps{Steps: []openchoreov1alpha1.EndpointRolloutStep{
			{Backends: []openchoreov1alpha1.EndpointRolloutStepBackend{{Name: "v2", Percent: 5}}},
			{Backends: []openchoreov1alpha1.EndpointRolloutStepBackend{{Name: "v2", Percent: 100}}},
		}},
	}
}

// fixture is an Endpoint with stable and v2, its release, and a dev binding.
func fixture(t *testing.T, withPlan bool) (Service, client.Client) {
	t.Helper()
	return interceptedFixture(t, withPlan, nil)
}

// interceptedFixture is fixture with funcs run on each call the services
// make; they get the client the fixture's objects are stored in.
func interceptedFixture(t *testing.T, withPlan bool, funcs func(store client.WithWatch) interceptor.Funcs) (Service, client.Client) {
	t.Helper()
	ep := testutil.NewEndpoint(ns, project, epName)
	ep.Spec.BackendRefs = append(ep.Spec.BackendRefs, candidate())
	if withPlan {
		plan := canaryPlan()
		ep.Spec.Rollout = &plan
	}
	rel := testutil.NewEndpointRelease(ns, project, epName, release)
	rel.Spec.BackendRefs = ep.Spec.BackendRefs
	b := testutil.NewEndpointReleaseBinding(ns, project, epName, "dev", epName+"-dev")
	b.Spec.EndpointRelease = release
	release2 := rel.DeepCopy()
	release2.Name = release + "-next"
	c := testutil.NewFakeClient(ep, rel, release2, b)
	if funcs != nil {
		store := c.(client.WithWatch)
		c = interceptor.NewClient(store, funcs(store))
	}
	logger := testutil.TestLogger()
	return NewService(endpointsvc.NewService(c, logger), endpointreleasesvc.NewService(c, logger),
		endpointreleasebindingsvc.NewService(c, logger), logger), c
}

func overridesOf(t *testing.T, c client.Client) map[string]int32 {
	t.Helper()
	b := &openchoreov1alpha1.EndpointReleaseBinding{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: epName + "-dev"}, b))
	out := map[string]int32{}
	for _, o := range b.Spec.BackendOverrides {
		out[o.Name] = *o.Weight
	}
	return out
}

func TestStart(t *testing.T) {
	ctx := context.Background()
	svc, c := fixture(t, false)

	status, err := svc.Start(ctx, ns, epName, StartRequest{Rollout: canaryPlan()})
	require.NoError(t, err)
	require.NotNil(t, status.Rollout)
	assert.Len(t, status.Stages, 3)
	require.Len(t, status.Environments, 1)
	assert.Equal(t, 0, *status.Environments[0].Step)

	ep := &openchoreov1alpha1.Endpoint{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: epName}, ep))
	assert.Equal(t, "stable", ep.Spec.Rollout.Stable)

	_, err = svc.Start(ctx, ns, epName, StartRequest{Rollout: canaryPlan()})
	var conflictErr *ConflictError
	assert.ErrorAs(t, err, &conflictErr, "a second plan is refused")
}

func TestStep(t *testing.T) {
	ctx := context.Background()
	svc, c := fixture(t, true)

	status, err := svc.Step(ctx, ns, epName, StepRequest{Environment: "dev", ExpectedStep: ptr(0)})
	require.NoError(t, err)
	assert.Equal(t, map[string]int32{"stable": 95, "v2": 5}, overridesOf(t, c))
	assert.Equal(t, 1, *status.Environments[0].Step)
	assert.Equal(t, "dev", status.Change.Environment)

	_, err = svc.Step(ctx, ns, epName, StepRequest{Environment: "dev", ExpectedStep: ptr(0)})
	var conflictErr *ConflictError
	require.ErrorAs(t, err, &conflictErr, "a stale expected step is refused")
	assert.Contains(t, conflictErr.Msg, "step 1")

	_, err = svc.Step(ctx, ns, epName, StepRequest{Environment: "dev"})
	require.NoError(t, err)
	assert.Equal(t, map[string]int32{"stable": 0, "v2": 100}, overridesOf(t, c))

	_, err = svc.Step(ctx, ns, epName, StepRequest{Environment: "dev"})
	assert.ErrorAs(t, err, &conflictErr, "nothing is past the last step")
}

func TestStep_DryRunWritesNothing(t *testing.T) {
	svc, c := fixture(t, true)
	status, err := svc.Step(context.Background(), ns, epName, StepRequest{Environment: "dev", DryRun: true})
	require.NoError(t, err)
	assert.Len(t, status.Change.After, 2)
	assert.Empty(t, overridesOf(t, c))
}

func TestStep_UnknownEnvironment(t *testing.T) {
	svc, _ := fixture(t, true)
	_, err := svc.Step(context.Background(), ns, epName, StepRequest{Environment: "prod"})
	assert.True(t, errors.Is(err, ErrEnvironmentNotBound))
}

func TestAbort(t *testing.T) {
	ctx := context.Background()
	svc, c := fixture(t, true)
	_, err := svc.Step(ctx, ns, epName, StepRequest{Environment: "dev", To: ptr(2)})
	require.NoError(t, err)

	_, err = svc.Abort(ctx, ns, epName, AbortRequest{Environment: "dev"})
	require.NoError(t, err)
	assert.Equal(t, map[string]int32{"stable": 100, "v2": 0}, overridesOf(t, c))
}

func TestFinish(t *testing.T) {
	ctx := context.Background()
	svc, c := fixture(t, true)

	b := &openchoreov1alpha1.EndpointReleaseBinding{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: epName + "-dev"}, b))
	b.Status.Backends = []openchoreov1alpha1.ResolvedEndpointBackend{{Name: "stable", Weight: 100}, {Name: "v2"}}
	require.NoError(t, c.Update(ctx, b))

	_, err := svc.Finish(ctx, ns, epName, FinishRequest{Keep: "v2"})
	var conflictErr *ConflictError
	require.ErrorAs(t, err, &conflictErr)
	assert.Contains(t, conflictErr.Msg, "stable in dev")

	status, err := svc.Finish(ctx, ns, epName, FinishRequest{Keep: "stable"})
	require.NoError(t, err)
	assert.Nil(t, status.Rollout)

	ep := &openchoreov1alpha1.Endpoint{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: epName}, ep))
	assert.Nil(t, ep.Spec.Rollout)
	require.Len(t, ep.Spec.BackendRefs, 1)
	assert.Equal(t, "stable", ep.Spec.BackendRefs[0].Name)
}

func TestFinishDropsTheLeavingMirror(t *testing.T) {
	ctx := context.Background()
	svc, c := fixture(t, true)

	b := &openchoreov1alpha1.EndpointReleaseBinding{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: epName + "-dev"}, b))
	b.Spec.BackendOverrides = []openchoreov1alpha1.EndpointBackendOverride{
		{Name: "stable", Weight: ptr(int32(100))},
		{Name: "v2", Mirror: &openchoreov1alpha1.EndpointMirror{Percent: 100}},
	}
	require.NoError(t, c.Update(ctx, b))

	_, err := svc.Finish(ctx, ns, epName, FinishRequest{Keep: "stable"})
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: epName + "-dev"}, b))
	assert.Equal(t, []openchoreov1alpha1.EndpointBackendOverride{{Name: "stable", Weight: ptr(int32(100))}},
		b.Spec.BackendOverrides, "the stable weight stays until the release without v2 is deployed")
}

func TestNoRollout(t *testing.T) {
	svc, _ := fixture(t, false)
	_, err := svc.Step(context.Background(), ns, epName, StepRequest{Environment: "dev"})
	assert.True(t, errors.Is(err, ErrNoRollout))
}

// afterFirstBindingList runs change on the stored binding once a call has
// listed the bindings, as autoDeploy or another user would.
func afterFirstBindingList(t *testing.T, change func(*openchoreov1alpha1.EndpointReleaseBinding)) func(client.WithWatch) interceptor.Funcs {
	return func(store client.WithWatch) interceptor.Funcs {
		done := false
		return interceptor.Funcs{
			List: func(ctx context.Context, _ client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if err := store.List(ctx, list, opts...); err != nil {
					return err
				}
				if _, ok := list.(*openchoreov1alpha1.EndpointReleaseBindingList); !ok || done {
					return nil
				}
				done = true
				b := &openchoreov1alpha1.EndpointReleaseBinding{}
				require.NoError(t, store.Get(ctx, client.ObjectKey{Namespace: ns, Name: epName + "-dev"}, b))
				change(b)
				return store.Update(ctx, b)
			},
		}
	}
}

func TestStep_RetriesOnConflict(t *testing.T) {
	updates := 0
	svc, c := interceptedFixture(t, true, func(store client.WithWatch) interceptor.Funcs {
		return interceptor.Funcs{
			Update: func(ctx context.Context, _ client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*openchoreov1alpha1.EndpointReleaseBinding); ok {
					if updates++; updates == 1 {
						return apierrors.NewConflict(schema.GroupResource{Resource: "endpointreleasebindings"}, obj.GetName(), errors.New("stale"))
					}
				}
				return store.Update(ctx, obj, opts...)
			},
		}
	})

	_, err := svc.Step(context.Background(), ns, epName, StepRequest{Environment: "dev"})
	require.NoError(t, err)
	assert.Equal(t, 2, updates)
	assert.Equal(t, map[string]int32{"stable": 95, "v2": 5}, overridesOf(t, c))
}

func TestStep_KeepsAConcurrentRelease(t *testing.T) {
	svc, c := interceptedFixture(t, true, afterFirstBindingList(t, func(b *openchoreov1alpha1.EndpointReleaseBinding) {
		b.Spec.EndpointRelease = release + "-next"
	}))

	_, err := svc.Step(context.Background(), ns, epName, StepRequest{Environment: "dev"})
	require.NoError(t, err)
	b := &openchoreov1alpha1.EndpointReleaseBinding{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: epName + "-dev"}, b))
	assert.Equal(t, release+"-next", b.Spec.EndpointRelease, "the step doesn't undo the promotion")
	assert.Equal(t, map[string]int32{"stable": 95, "v2": 5}, overridesOf(t, c))
}

func TestStep_ExpectStepRecheckedOnRetry(t *testing.T) {
	svc, c := interceptedFixture(t, true, afterFirstBindingList(t, func(b *openchoreov1alpha1.EndpointReleaseBinding) {
		b.Spec.BackendOverrides = []openchoreov1alpha1.EndpointBackendOverride{
			{Name: "stable", Weight: ptr(int32(95))}, {Name: "v2", Weight: ptr(int32(5))},
		}
	}))

	_, err := svc.Step(context.Background(), ns, epName, StepRequest{Environment: "dev", ExpectedStep: ptr(0)})
	var conflictErr *ConflictError
	require.ErrorAs(t, err, &conflictErr)
	assert.Contains(t, conflictErr.Msg, "step 1")
	assert.Equal(t, map[string]int32{"stable": 95, "v2": 5}, overridesOf(t, c), "the other step stands")
}

func TestStart_ConflictBecomes409(t *testing.T) {
	svc, _ := interceptedFixture(t, false, func(store client.WithWatch) interceptor.Funcs {
		return interceptor.Funcs{
			Update: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.UpdateOption) error {
				return apierrors.NewConflict(schema.GroupResource{Resource: "endpoints"}, obj.GetName(), errors.New("stale"))
			},
		}
	})

	_, err := svc.Start(context.Background(), ns, epName, StartRequest{Rollout: canaryPlan()})
	var conflictErr *ConflictError
	require.ErrorAs(t, err, &conflictErr)
	assert.Contains(t, conflictErr.Msg, "changed while saving")
}
