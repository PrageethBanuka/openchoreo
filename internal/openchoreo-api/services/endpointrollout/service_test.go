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
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	clusterendpointtypesvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/clusterendpointtype"
	endpointsvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpoint"
	endpointreleasesvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointrelease"
	endpointreleasebindingsvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointreleasebinding"
	endpointtypesvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointtype"
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
		endpointreleasebindingsvc.NewService(c, logger), endpointtypesvc.NewService(c, logger),
		clusterendpointtypesvc.NewService(c, logger), logger), c
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

// asMirror turns the stored plan into a mirror rollout to v2.
func asMirror(t *testing.T, c client.Client) {
	t.Helper()
	ep := &openchoreov1alpha1.Endpoint{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: epName}, ep))
	ep.Spec.BackendRefs[1].Match = nil
	ep.Spec.BackendRefs[1].Mirror = &openchoreov1alpha1.EndpointMirror{Percent: 0}
	ep.Spec.Rollout.Strategy = openchoreov1alpha1.EndpointRolloutMirror
	ep.Spec.Rollout.Mirror, ep.Spec.Rollout.Canary = ep.Spec.Rollout.Canary, nil
	require.NoError(t, c.Update(context.Background(), ep))
}

func TestFinishDropsTheLeavingMirror(t *testing.T) {
	ctx := context.Background()
	svc, c := fixture(t, true)
	asMirror(t, c)

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

func TestStep_ReadsEachReleaseAndTheBindingsOnce(t *testing.T) {
	ctx := context.Background()
	releaseGets, bindingLists := 0, 0
	svc, c := interceptedFixture(t, true, func(store client.WithWatch) interceptor.Funcs {
		return interceptor.Funcs{
			Get: func(ctx context.Context, _ client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*openchoreov1alpha1.EndpointRelease); ok {
					releaseGets++
				}
				return store.Get(ctx, key, obj, opts...)
			},
			List: func(ctx context.Context, _ client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*openchoreov1alpha1.EndpointReleaseBindingList); ok {
					bindingLists++
				}
				return store.List(ctx, list, opts...)
			},
		}
	})
	staging := testutil.NewEndpointReleaseBinding(ns, project, epName, "staging", epName+"-staging")
	staging.Spec.EndpointRelease = release
	require.NoError(t, c.Create(ctx, staging))

	status, err := svc.Step(ctx, ns, epName, StepRequest{Environment: "dev"})
	require.NoError(t, err)
	assert.Len(t, status.Environments, 2)
	assert.Equal(t, 1, releaseGets, "both environments run the same release")
	assert.Equal(t, 1, bindingLists)
	assert.Equal(t, map[string]int32{"stable": 95, "v2": 5}, overridesOf(t, c))
}

// withType stores the fixture Endpoint's type, declaring the given
// capabilities.
func withType(t *testing.T, c client.Client, capabilities ...openchoreov1alpha1.EndpointCapability) {
	t.Helper()
	et := testutil.NewEndpointType(ns, "http-gateway")
	et.Spec.Capabilities = capabilities
	require.NoError(t, c.Create(context.Background(), et))
}

func mirrorStart(percents ...int32) StartRequest {
	steps := make([]openchoreov1alpha1.EndpointRolloutStep, 0, len(percents))
	for _, p := range percents {
		steps = append(steps, openchoreov1alpha1.EndpointRolloutStep{
			Backends: []openchoreov1alpha1.EndpointRolloutStepBackend{{Name: "v2", Percent: p}},
		})
	}
	shadow := candidate()
	shadow.Match = nil
	shadow.Mirror = &openchoreov1alpha1.EndpointMirror{Percent: 100}
	return StartRequest{
		Rollout: openchoreov1alpha1.EndpointRollout{
			Strategy:   openchoreov1alpha1.EndpointRolloutMirror,
			Stable:     "stable",
			Candidates: []openchoreov1alpha1.EndpointRolloutCandidate{{Name: "v2"}},
			Mirror:     &openchoreov1alpha1.EndpointRolloutSteps{Steps: steps},
		},
		Backends: []openchoreov1alpha1.EndpointBackendRef{shadow},
	}
}

func TestStart_RefusesWhatTheTypeCannotRender(t *testing.T) {
	ctx := context.Background()
	svc, c := fixture(t, false)
	withType(t, c, openchoreov1alpha1.EndpointCapabilityMatch)

	_, err := svc.Start(ctx, ns, epName, mirrorStart(100))
	var validationErr *services.ValidationError
	require.ErrorAs(t, err, &validationErr)
	assert.Contains(t, validationErr.Msg, `EndpointType "http-gateway" can't render this rollout`)
	assert.Contains(t, validationErr.Msg, "Mirror capability")

	ep := &openchoreov1alpha1.Endpoint{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: epName}, ep))
	assert.Nil(t, ep.Spec.Rollout, "nothing is saved")

	_, err = svc.Start(ctx, ns, epName, StartRequest{Rollout: canaryPlan()})
	assert.NoError(t, err, "a canary whose testers match a header needs only Match")
}

func TestStart_MirrorShareNeedsMirrorPercent(t *testing.T) {
	svc, c := fixture(t, false)
	withType(t, c, openchoreov1alpha1.EndpointCapabilityMirror)

	_, err := svc.Start(context.Background(), ns, epName, mirrorStart(10, 100))
	var validationErr *services.ValidationError
	require.ErrorAs(t, err, &validationErr)
	assert.Contains(t, validationErr.Msg, "MirrorPercent")

	_, err = svc.Start(context.Background(), ns, epName, mirrorStart(100))
	assert.NoError(t, err, "mirroring every request needs only Mirror")
}

func TestStart_TypeWithoutCapabilitiesIsNotChecked(t *testing.T) {
	svc, c := fixture(t, false)
	withType(t, c)
	_, err := svc.Start(context.Background(), ns, epName, mirrorStart(10, 100))
	assert.NoError(t, err)
}

// serve sets what the dev binding reports each backend gets.
func serve(t *testing.T, c client.Client, backends ...openchoreov1alpha1.ResolvedEndpointBackend) {
	t.Helper()
	b := &openchoreov1alpha1.EndpointReleaseBinding{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: epName + "-dev"}, b))
	b.Status.Backends = backends
	require.NoError(t, c.Update(context.Background(), b))
}

func deployed(name string, weight int32) openchoreov1alpha1.ResolvedEndpointBackend {
	return openchoreov1alpha1.ResolvedEndpointBackend{Name: name, Weight: weight, Resolved: true}
}

func gone(name string, weight int32) openchoreov1alpha1.ResolvedEndpointBackend {
	return openchoreov1alpha1.ResolvedEndpointBackend{
		Name: name, Weight: weight, Reason: "NotDeployed",
		Message: `component "greeter-v2" is not deployed in environment "dev"`,
	}
}

func TestStep_RefusesTrafficToAnUndeployedBackend(t *testing.T) {
	ctx := context.Background()
	svc, c := fixture(t, true)
	serve(t, c, deployed("stable", 100), gone("v2", 0))

	_, err := svc.Step(ctx, ns, epName, StepRequest{Environment: "dev"})
	var conflictErr *ConflictError
	require.ErrorAs(t, err, &conflictErr)
	assert.Contains(t, conflictErr.Msg, `v2, which isn't deployed in dev (component "greeter-v2" is not deployed`)
	assert.Empty(t, overridesOf(t, c), "nothing is saved")

	_, err = svc.Step(ctx, ns, epName, StepRequest{Environment: "dev", DryRun: true})
	require.ErrorAs(t, err, &conflictErr, "a dry run is refused the same way")

	_, err = svc.Abort(ctx, ns, epName, AbortRequest{Environment: "dev"})
	require.NoError(t, err, "moving traffic off it is allowed")
	assert.Equal(t, map[string]int32{"stable": 100, "v2": 0}, overridesOf(t, c))
}

func TestAbort_RefusedWhenStableIsGone(t *testing.T) {
	ctx := context.Background()
	svc, c := fixture(t, true)
	_, err := svc.Step(ctx, ns, epName, StepRequest{Environment: "dev"})
	require.NoError(t, err)
	serve(t, c, gone("stable", 95), deployed("v2", 5))

	_, err = svc.Abort(ctx, ns, epName, AbortRequest{Environment: "dev"})
	var conflictErr *ConflictError
	require.ErrorAs(t, err, &conflictErr)
	assert.Contains(t, conflictErr.Msg, "stable, which isn't deployed in dev")

	_, err = svc.Step(ctx, ns, epName, StepRequest{Environment: "dev", To: ptr(2)})
	require.NoError(t, err, "the last step moves everything to the candidate")
	assert.Equal(t, map[string]int32{"stable": 0, "v2": 100}, overridesOf(t, c))
}

func TestFinish_AfterTheCandidateIsGone(t *testing.T) {
	ctx := context.Background()
	svc, c := fixture(t, true)
	_, err := svc.Step(ctx, ns, epName, StepRequest{Environment: "dev"})
	require.NoError(t, err)
	serve(t, c, deployed("stable", 95), gone("v2", 5))

	_, err = svc.Finish(ctx, ns, epName, FinishRequest{Keep: "stable"})
	var conflictErr *ConflictError
	require.ErrorAs(t, err, &conflictErr)
	assert.Contains(t, conflictErr.Msg, "v2 in dev still gets traffic")

	_, err = svc.Abort(ctx, ns, epName, AbortRequest{Environment: "dev"})
	require.NoError(t, err)
	serve(t, c, deployed("stable", 100), gone("v2", 0))

	_, err = svc.Finish(ctx, ns, epName, FinishRequest{Keep: "stable"})
	require.NoError(t, err)
	ep := &openchoreov1alpha1.Endpoint{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: epName}, ep))
	require.Len(t, ep.Spec.BackendRefs, 1)
	assert.Equal(t, "stable", ep.Spec.BackendRefs[0].Name)
}

func stagingOn(t *testing.T, c client.Client, releaseName string) {
	t.Helper()
	b := testutil.NewEndpointReleaseBinding(ns, project, epName, "staging", epName+"-staging")
	b.Spec.EndpointRelease = releaseName
	require.NoError(t, c.Create(context.Background(), b))
}

func stagingOverrides(t *testing.T, c client.Client) []openchoreov1alpha1.EndpointBackendOverride {
	t.Helper()
	b := &openchoreov1alpha1.EndpointReleaseBinding{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: epName + "-staging"}, b))
	return b.Spec.BackendOverrides
}

// A release promoted mid-rollout starts the next environment at 0%, and each
// environment then moves on its own.
func TestStep_EnvironmentsMoveIndependently(t *testing.T) {
	ctx := context.Background()
	svc, c := fixture(t, true)
	stagingOn(t, c, release)

	status, err := svc.Step(ctx, ns, epName, StepRequest{Environment: "dev"})
	require.NoError(t, err)
	steps := map[string]int{}
	for _, e := range status.Environments {
		steps[e.Environment] = *e.Step
	}
	assert.Equal(t, map[string]int{"dev": 1, "staging": 0}, steps)
	assert.Empty(t, stagingOverrides(t, c))

	_, err = svc.Step(ctx, ns, epName, StepRequest{Environment: "staging", ExpectedStep: ptr(0)})
	require.NoError(t, err)
	assert.Len(t, stagingOverrides(t, c), 2)
	assert.Equal(t, map[string]int32{"stable": 95, "v2": 5}, overridesOf(t, c), "dev is unchanged")
}

func TestStep_RefusedAfterARollback(t *testing.T) {
	ctx := context.Background()
	svc, c := fixture(t, true)
	old := testutil.NewEndpointRelease(ns, project, epName, epName+"-0")
	require.NoError(t, c.Create(ctx, old))
	stagingOn(t, c, old.Name)

	_, err := svc.Step(ctx, ns, epName, StepRequest{Environment: "staging"})
	var conflictErr *ConflictError
	require.ErrorAs(t, err, &conflictErr)
	assert.Contains(t, conflictErr.Msg, "which has no backend v2")
}

func TestStart_DryRunChecksThePlan(t *testing.T) {
	svc, _ := fixture(t, false)
	plan := canaryPlan()
	plan.Strategy, plan.Canary, plan.Candidates = openchoreov1alpha1.EndpointRolloutBlueGreen, nil, nil

	_, err := svc.Start(context.Background(), ns, epName, StartRequest{Rollout: plan, DryRun: true})
	var validationErr *services.ValidationError
	require.ErrorAs(t, err, &validationErr)
	assert.Contains(t, validationErr.Msg, "exactly one candidate")

	plan = canaryPlan()
	plan.Stable = "missing"
	_, err = svc.Start(context.Background(), ns, epName, StartRequest{Rollout: plan, DryRun: true})
	require.ErrorAs(t, err, &validationErr)
	assert.Contains(t, validationErr.Msg, "rollout.stable must name a backend")
}

func TestStart_BackendByNameKeepsItsComponent(t *testing.T) {
	ctx := context.Background()
	svc, c := fixture(t, false)
	byName := openchoreov1alpha1.EndpointBackendRef{
		Name: "v2", Kind: openchoreov1alpha1.EndpointBackendKindComponent, Weight: ptr(int32(0)),
	}

	_, err := svc.Start(ctx, ns, epName, StartRequest{Rollout: canaryPlan(), Backends: []openchoreov1alpha1.EndpointBackendRef{byName}})
	require.NoError(t, err)
	ep := &openchoreov1alpha1.Endpoint{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: epName}, ep))
	require.NotNil(t, ep.Spec.BackendRefs[1].Component)
	assert.Equal(t, "greeter-v2", ep.Spec.BackendRefs[1].Component.Name)
	assert.Nil(t, ep.Spec.BackendRefs[1].Match, "the rest is taken from the request")
}

func TestStart_RefusesAMatchTheControllerRefuses(t *testing.T) {
	svc, _ := fixture(t, false)
	v2 := candidate()
	v2.Match.Headers = append(v2.Match.Headers, openchoreov1alpha1.EndpointHeaderMatch{Name: "X-Canary", Value: "yes"})

	_, err := svc.Start(context.Background(), ns, epName, StartRequest{Rollout: canaryPlan(), Backends: []openchoreov1alpha1.EndpointBackendRef{v2}})
	var validationErr *services.ValidationError
	require.ErrorAs(t, err, &validationErr)
	assert.Contains(t, validationErr.Msg, `header "X-Canary" appears more than once`)
}

func TestFinish_RefusedWhileAStepIsBeingApplied(t *testing.T) {
	ctx := context.Background()
	svc, c := fixture(t, true)
	_, err := svc.Step(ctx, ns, epName, StepRequest{Environment: "dev"})
	require.NoError(t, err)
	serve(t, c, deployed("stable", 100), deployed("v2", 0))

	_, err = svc.Finish(ctx, ns, epName, FinishRequest{Keep: "stable"})
	var conflictErr *ConflictError
	require.ErrorAs(t, err, &conflictErr, "the binding asks for 5% even though it still routes 0%")
	assert.Contains(t, conflictErr.Msg, "v2 in dev still gets traffic")
}

func TestFinish_SavedWhenAMirrorCantBeCleared(t *testing.T) {
	ctx := context.Background()
	denied := false
	svc, c := interceptedFixture(t, true, func(store client.WithWatch) interceptor.Funcs {
		return interceptor.Funcs{
			Update: func(ctx context.Context, _ client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*openchoreov1alpha1.EndpointReleaseBinding); ok && denied {
					return apierrors.NewForbidden(schema.GroupResource{Resource: "endpointreleasebindings"}, obj.GetName(), errors.New("denied"))
				}
				return store.Update(ctx, obj, opts...)
			},
		}
	})
	asMirror(t, c)
	b := &openchoreov1alpha1.EndpointReleaseBinding{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: epName + "-dev"}, b))
	b.Spec.BackendOverrides = []openchoreov1alpha1.EndpointBackendOverride{
		{Name: "v2", Mirror: &openchoreov1alpha1.EndpointMirror{Percent: 50}},
	}
	require.NoError(t, c.Update(ctx, b))
	denied = true

	status, err := svc.Finish(ctx, ns, epName, FinishRequest{Keep: "stable"})
	require.NoError(t, err)
	assert.Nil(t, status.Rollout)
}

func TestStep_ABTestHasNoSteps(t *testing.T) {
	ctx := context.Background()
	svc, c := fixture(t, true)
	ep := &openchoreov1alpha1.Endpoint{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: epName}, ep))
	ep.Spec.Rollout.Strategy, ep.Spec.Rollout.Canary = openchoreov1alpha1.EndpointRolloutABTest, nil
	require.NoError(t, c.Update(ctx, ep))

	_, err := svc.Step(ctx, ns, epName, StepRequest{Environment: "dev"})
	var conflictErr *ConflictError
	require.ErrorAs(t, err, &conflictErr)
	assert.Contains(t, conflictErr.Msg, "an A/B test has no steps")

	_, err = svc.Abort(ctx, ns, epName, AbortRequest{Environment: "dev"})
	assert.NoError(t, err, "abort still returns weights set by hand to stable")
}

func TestStep_EnvironmentWithoutARelease(t *testing.T) {
	svc, c := fixture(t, true)
	stagingOn(t, c, "")

	_, err := svc.Step(context.Background(), ns, epName, StepRequest{Environment: "staging"})
	var conflictErr *ConflictError
	require.ErrorAs(t, err, &conflictErr)
	assert.Contains(t, conflictErr.Msg, "staging has no release yet")
}
