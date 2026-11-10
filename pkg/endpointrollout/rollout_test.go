// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointrollout

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

func backend(name string, weight int32) openchoreov1alpha1.EndpointBackendRef {
	return openchoreov1alpha1.EndpointBackendRef{
		Name:      name,
		Kind:      openchoreov1alpha1.EndpointBackendKindComponent,
		Component: &openchoreov1alpha1.EndpointComponentBackend{Name: name, Endpoint: "http"},
		Weight:    ptr(weight),
	}
}

func steps(name string, percents ...int32) *openchoreov1alpha1.EndpointRolloutSteps {
	out := &openchoreov1alpha1.EndpointRolloutSteps{}
	for _, p := range percents {
		out.Steps = append(out.Steps, openchoreov1alpha1.EndpointRolloutStep{
			Backends: []openchoreov1alpha1.EndpointRolloutStepBackend{{Name: name, Percent: p}},
		})
	}
	return out
}

func canary() *openchoreov1alpha1.EndpointRollout {
	return &openchoreov1alpha1.EndpointRollout{
		Strategy:   openchoreov1alpha1.EndpointRolloutCanary,
		Stable:     "stable",
		Candidates: []openchoreov1alpha1.EndpointRolloutCandidate{{Name: "v2"}},
		Canary:     steps("v2", 5, 25, 100),
	}
}

func weightsOf(overrides []openchoreov1alpha1.EndpointBackendOverride) map[string]int32 {
	out := map[string]int32{}
	for _, o := range overrides {
		if o.Weight != nil {
			out[o.Name] = *o.Weight
		}
	}
	return out
}

func TestStages(t *testing.T) {
	assert.Len(t, Stages(canary()), 4, "stage 0, then each step")

	bg := &openchoreov1alpha1.EndpointRollout{
		Strategy: openchoreov1alpha1.EndpointRolloutBlueGreen, Stable: "stable",
		Candidates: []openchoreov1alpha1.EndpointRolloutCandidate{{Name: "green"}},
	}
	got := Stages(bg)
	require.Len(t, got, 2)
	assert.Equal(t, int32(100), got[1].percent("green"))

	ab := &openchoreov1alpha1.EndpointRollout{
		Strategy: openchoreov1alpha1.EndpointRolloutABTest, Stable: "stable",
		Candidates: []openchoreov1alpha1.EndpointRolloutCandidate{{Name: "b"}, {Name: "c"}},
	}
	assert.Len(t, Stages(ab), 1, "an abTest has no steps")
}

func TestOverrides_StableAndCandidateSplit100(t *testing.T) {
	r := canary()
	refs := []openchoreov1alpha1.EndpointBackendRef{backend("stable", 1), backend("v2", 0)}

	got, err := Overrides(r, refs, nil, Stages(r)[2])
	require.NoError(t, err)
	assert.Equal(t, map[string]int32{"stable": 75, "v2": 25}, weightsOf(got))
	assert.Equal(t, 2, Position(r, refs, got))
	assert.Equal(t, 0, Position(r, refs, nil), "no overrides is stage 0")
}

func TestOverrides_OtherBackendsKeepTheirShare(t *testing.T) {
	r := canary()
	refs := []openchoreov1alpha1.EndpointBackendRef{backend("stable", 60), backend("legacy", 40), backend("v2", 0)}

	got, err := Overrides(r, refs, nil, Stages(r)[1])
	require.NoError(t, err)
	assert.Equal(t, map[string]int32{"stable": 5700, "v2": 300, "legacy": 4000}, weightsOf(got),
		"v2 gets exactly 5% of stable's 60%, and legacy keeps its 40%")
	assert.Equal(t, 1, Position(r, refs, got))
}

func TestOverrides_KeepsOtherOverrides(t *testing.T) {
	r := canary()
	shadow := backend("shadow", 0)
	shadow.Mirror = &openchoreov1alpha1.EndpointMirror{Percent: 100}
	refs := []openchoreov1alpha1.EndpointBackendRef{backend("stable", 1), backend("v2", 0), shadow}
	current := []openchoreov1alpha1.EndpointBackendOverride{
		{Name: "shadow", Mirror: &openchoreov1alpha1.EndpointMirror{Percent: 10}},
		{Name: "v2", Weight: ptr(int32(5))},
	}

	got, err := Overrides(r, refs, current, Stages(r)[3])
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "shadow", got[0].Name)
	assert.Equal(t, int32(10), got[0].Mirror.Percent)
	assert.Equal(t, map[string]int32{"stable": 0, "v2": 100}, weightsOf(got))
}

func TestOverrides_Mirror(t *testing.T) {
	r := &openchoreov1alpha1.EndpointRollout{
		Strategy: openchoreov1alpha1.EndpointRolloutMirror, Stable: "stable",
		Candidates: []openchoreov1alpha1.EndpointRolloutCandidate{{Name: "shadow"}},
		Mirror:     steps("shadow", 10, 50),
	}
	shadow := backend("shadow", 0)
	shadow.Mirror = &openchoreov1alpha1.EndpointMirror{Percent: 0}
	refs := []openchoreov1alpha1.EndpointBackendRef{backend("stable", 1), shadow}

	got, err := Overrides(r, refs, nil, Stages(r)[2])
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Nil(t, got[0].Weight)
	assert.Equal(t, int32(50), got[0].Mirror.Percent)
	assert.Equal(t, 2, Position(r, refs, got))
}

func TestOverrides_RefusesAReleaseWithoutTheCandidate(t *testing.T) {
	r := canary()
	_, err := Overrides(r, []openchoreov1alpha1.EndpointBackendRef{backend("stable", 1)}, nil, Stages(r)[1])
	assert.ErrorContains(t, err, "v2")
}

func TestPosition_ByHand(t *testing.T) {
	r := canary()
	refs := []openchoreov1alpha1.EndpointBackendRef{backend("stable", 1), backend("v2", 0)}
	overrides := []openchoreov1alpha1.EndpointBackendOverride{
		{Name: "stable", Weight: ptr(int32(90))}, {Name: "v2", Weight: ptr(int32(10))},
	}
	assert.Equal(t, -1, Position(r, refs, overrides))
	assert.Equal(t, int32(10), Percents(r, refs, overrides)["v2"])
}

func TestFinish(t *testing.T) {
	r := canary()
	v2 := backend("v2", 0)
	v2.Match = &openchoreov1alpha1.EndpointMatch{
		Headers: []openchoreov1alpha1.EndpointHeaderMatch{{Name: "x-canary", Value: "true"}},
	}
	refs := []openchoreov1alpha1.EndpointBackendRef{backend("stable", 60), backend("legacy", 40), v2}

	out, leaving, err := Finish(r, refs, "v2")
	require.NoError(t, err)
	assert.Equal(t, []string{"stable"}, leaving)
	require.Len(t, out, 2)
	assert.Equal(t, "legacy", out[0].Name)
	assert.Equal(t, "v2", out[1].Name)
	assert.Equal(t, int32(60), *out[1].Weight, "the candidate takes stable's weight")
	assert.Nil(t, out[1].Match)

	out, leaving, err = Finish(r, refs, "stable")
	require.NoError(t, err)
	assert.Equal(t, []string{"v2"}, leaving)
	assert.Len(t, out, 2)

	_, _, err = Finish(r, refs, "legacy")
	assert.Error(t, err)
}

func TestFinish_MirrorKeepsOnlyStable(t *testing.T) {
	r := &openchoreov1alpha1.EndpointRollout{
		Strategy: openchoreov1alpha1.EndpointRolloutMirror, Stable: "stable",
		Candidates: []openchoreov1alpha1.EndpointRolloutCandidate{{Name: "shadow"}},
		Mirror:     steps("shadow", 100),
	}
	_, _, err := Finish(r, []openchoreov1alpha1.EndpointBackendRef{backend("stable", 1), backend("shadow", 0)}, "shadow")
	assert.ErrorContains(t, err, "only receives copies")
}
