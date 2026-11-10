// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

// Package endpointrollout turns an Endpoint's rollout plan into the weights
// and mirror shares an environment's EndpointReleaseBinding sets, and reads
// back where an environment stands. openchoreo-api and the automation module
// both use it, so every client steps a rollout the same way.
package endpointrollout

import (
	"fmt"
	"math"
	"slices"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

const maxWeight = 1_000_000

// Stage is one stop of a rollout: each candidate's percent. Stage 0 has every
// candidate at 0, so all traffic is on stable.
type Stage struct {
	Index    int                                             `json:"index"`
	Backends []openchoreov1alpha1.EndpointRolloutStepBackend `json:"backends"`
}

func (s Stage) percent(name string) int32 {
	for _, b := range s.Backends {
		if b.Name == name {
			return b.Percent
		}
	}
	return 0
}

// Stages lists a rollout's stops in order. A canary or mirror stops at each
// of its steps, a blueGreen switches its candidate to 100, and an abTest has
// no steps: its candidates get the requests their cookie matches until it is
// finished.
func Stages(r *openchoreov1alpha1.EndpointRollout) []Stage {
	zero := Stage{Index: 0}
	for _, c := range r.Candidates {
		zero.Backends = append(zero.Backends, openchoreov1alpha1.EndpointRolloutStepBackend{Name: c.Name})
	}
	var steps []openchoreov1alpha1.EndpointRolloutStep
	switch r.Strategy {
	case openchoreov1alpha1.EndpointRolloutCanary:
		if r.Canary != nil {
			steps = r.Canary.Steps
		}
	case openchoreov1alpha1.EndpointRolloutMirror:
		if r.Mirror != nil {
			steps = r.Mirror.Steps
		}
	case openchoreov1alpha1.EndpointRolloutBlueGreen:
		steps = []openchoreov1alpha1.EndpointRolloutStep{{
			Backends: []openchoreov1alpha1.EndpointRolloutStepBackend{{Name: r.Candidates[0].Name, Percent: 100}},
		}}
	}
	out := make([]Stage, 0, 1+len(steps))
	out = append(out, zero)
	for i, s := range steps {
		out = append(out, Stage{Index: i + 1, Backends: s.Backends})
	}
	return out
}

func refWeight(ref openchoreov1alpha1.EndpointBackendRef) int32 {
	if ref.Mirror != nil {
		return 0
	}
	if ref.Weight == nil {
		return 1
	}
	return *ref.Weight
}

func overrideFor(
	overrides []openchoreov1alpha1.EndpointBackendOverride, name string,
) *openchoreov1alpha1.EndpointBackendOverride {
	for i := range overrides {
		if overrides[i].Name == name {
			return &overrides[i]
		}
	}
	return nil
}

// EffectiveWeights is each backend's weight and mirror share once an
// environment's overrides apply, as the binding controller resolves them.
func EffectiveWeights(
	refs []openchoreov1alpha1.EndpointBackendRef,
	overrides []openchoreov1alpha1.EndpointBackendOverride,
) (weights, mirrors map[string]int32) {
	weights, mirrors = map[string]int32{}, map[string]int32{}
	for _, ref := range refs {
		weights[ref.Name] = refWeight(ref)
		if ref.Mirror != nil {
			mirrors[ref.Name] = ref.Mirror.Percent
		}
		o := overrideFor(overrides, ref.Name)
		if o == nil {
			continue
		}
		if o.Weight != nil && ref.Mirror == nil {
			weights[ref.Name] = *o.Weight
		}
		if o.Mirror != nil && ref.Mirror != nil {
			mirrors[ref.Name] = o.Mirror.Percent
		}
	}
	return weights, mirrors
}

// Missing lists the rollout's backends that refs don't have, for example
// when an environment is pinned to a release from before the rollout.
func Missing(r *openchoreov1alpha1.EndpointRollout, refs []openchoreov1alpha1.EndpointBackendRef) []string {
	var out []string
	names := append([]string{r.Stable}, candidateNames(r)...)
	for _, n := range names {
		if !slices.ContainsFunc(refs, func(ref openchoreov1alpha1.EndpointBackendRef) bool { return ref.Name == n }) {
			out = append(out, n)
		}
	}
	return out
}

func candidateNames(r *openchoreov1alpha1.EndpointRollout) []string {
	out := make([]string, 0, len(r.Candidates))
	for _, c := range r.Candidates {
		out = append(out, c.Name)
	}
	return out
}

// Position is where an environment stands: the index of the stage its
// weights match, or -1 when they were set by hand and match none.
func Position(
	r *openchoreov1alpha1.EndpointRollout,
	refs []openchoreov1alpha1.EndpointBackendRef,
	overrides []openchoreov1alpha1.EndpointBackendOverride,
) int {
	percents := Percents(r, refs, overrides)
	for _, s := range Stages(r) {
		if !slices.ContainsFunc(r.Candidates, func(c openchoreov1alpha1.EndpointRolloutCandidate) bool {
			return s.percent(c.Name) != percents[c.Name]
		}) {
			return s.Index
		}
	}
	return -1
}

// Percents is each candidate's share as a rollout step states it: of the
// traffic stable and the candidates share, or for a mirror, the share of
// requests copied to it.
func Percents(
	r *openchoreov1alpha1.EndpointRollout,
	refs []openchoreov1alpha1.EndpointBackendRef,
	overrides []openchoreov1alpha1.EndpointBackendOverride,
) map[string]int32 {
	weights, mirrors := EffectiveWeights(refs, overrides)
	out := map[string]int32{}
	if r.Strategy == openchoreov1alpha1.EndpointRolloutMirror {
		for _, c := range r.Candidates {
			out[c.Name] = mirrors[c.Name]
		}
		return out
	}
	total := int64(weights[r.Stable])
	for _, c := range r.Candidates {
		total += int64(weights[c.Name])
	}
	for _, c := range r.Candidates {
		if total > 0 {
			out[c.Name] = int32(math.Round(float64(weights[c.Name]) * 100 / float64(total)))
		}
	}
	return out
}

// Overrides is an environment's backendOverrides at a stage. Each candidate
// gets its percent of stable's weight and stable keeps the rest; backends
// outside the rollout keep their weights. Overrides the stage doesn't set,
// such as another backend's mirror share, are kept.
func Overrides(
	r *openchoreov1alpha1.EndpointRollout,
	refs []openchoreov1alpha1.EndpointBackendRef,
	current []openchoreov1alpha1.EndpointBackendOverride,
	stage Stage,
) ([]openchoreov1alpha1.EndpointBackendOverride, error) {
	if missing := Missing(r, refs); len(missing) > 0 {
		return nil, fmt.Errorf("the release has no backend %v", missing)
	}
	set := map[string]openchoreov1alpha1.EndpointBackendOverride{}
	if r.Strategy == openchoreov1alpha1.EndpointRolloutMirror {
		for _, c := range r.Candidates {
			set[c.Name] = openchoreov1alpha1.EndpointBackendOverride{
				Name: c.Name, Mirror: &openchoreov1alpha1.EndpointMirror{Percent: stage.percent(c.Name)},
			}
		}
		return merge(current, set), nil
	}
	for name, w := range stageWeights(r, refs, stage) {
		set[name] = openchoreov1alpha1.EndpointBackendOverride{Name: name, Weight: ptr(w)}
	}
	return merge(current, set), nil
}

// stageWeights splits stable's weight between it and the candidates. When
// other backends share the route, every weight is scaled by 100 so each
// percent is exact; otherwise stable and the candidates split 100.
func stageWeights(
	r *openchoreov1alpha1.EndpointRollout, refs []openchoreov1alpha1.EndpointBackendRef, stage Stage,
) map[string]int32 {
	inRollout := append([]string{r.Stable}, candidateNames(r)...)
	others := map[string]int32{}
	var stable int32
	for _, ref := range refs {
		switch {
		case ref.Name == r.Stable:
			stable = refWeight(ref)
		case !slices.Contains(inRollout, ref.Name) && refWeight(ref) > 0:
			others[ref.Name] = refWeight(ref)
		}
	}
	out := map[string]int32{}
	var slice int64 = 100
	if len(others) > 0 {
		scale := int64(100)
		for _, w := range others {
			if int64(w)*scale > maxWeight {
				scale = 1
			}
		}
		if int64(stable)*scale > maxWeight {
			scale = 1
		}
		slice = int64(stable) * scale
		for name, w := range others {
			out[name] = weight(int64(w) * scale)
		}
	}
	left := slice
	for _, c := range r.Candidates {
		w := int64(math.Round(float64(slice) * float64(stage.percent(c.Name)) / 100))
		out[c.Name] = weight(w)
		left -= w
	}
	out[r.Stable] = weight(left)
	return out
}

func merge(
	current []openchoreov1alpha1.EndpointBackendOverride,
	set map[string]openchoreov1alpha1.EndpointBackendOverride,
) []openchoreov1alpha1.EndpointBackendOverride {
	out := make([]openchoreov1alpha1.EndpointBackendOverride, 0, len(current)+len(set))
	for _, o := range current {
		if _, ok := set[o.Name]; !ok {
			out = append(out, o)
		}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		out = append(out, set[name])
	}
	return out
}

// Finish is the Endpoint's backends once the rollout ends keeping one
// backend. Keeping stable drops the candidates. Keeping a candidate gives it
// stable's weight without its match, and drops stable and the other
// candidates. Leaving lists the dropped backends.
func Finish(
	r *openchoreov1alpha1.EndpointRollout,
	refs []openchoreov1alpha1.EndpointBackendRef,
	keep string,
) (out []openchoreov1alpha1.EndpointBackendRef, leaving []string, err error) {
	candidates := candidateNames(r)
	switch {
	case keep == r.Stable:
		leaving = candidates
	case slices.Contains(candidates, keep):
		if r.Strategy == openchoreov1alpha1.EndpointRolloutMirror {
			return nil, nil, fmt.Errorf("%s only receives copies in a mirror rollout; keep %s, then start a canary to %s",
				keep, r.Stable, keep)
		}
		others := slices.DeleteFunc(slices.Clone(candidates), func(n string) bool { return n == keep })
		leaving = append([]string{r.Stable}, others...)
	default:
		return nil, nil, fmt.Errorf("keep must be %s or one of the candidates %v", r.Stable, candidates)
	}
	var stableWeight *int32
	for _, ref := range refs {
		if ref.Name == r.Stable {
			stableWeight = ptr(refWeight(ref))
		}
	}
	for _, ref := range refs {
		if slices.Contains(leaving, ref.Name) {
			continue
		}
		if ref.Name == keep && keep != r.Stable {
			ref.Weight, ref.Match, ref.Mirror = stableWeight, nil, nil
		}
		out = append(out, ref)
	}
	return out, leaving, nil
}

// weight bounds a computed weight to what a backend accepts.
func weight(w int64) int32 {
	return int32(min(max(w, 0), maxWeight)) //nolint:gosec // bounded to [0, maxWeight]
}

func ptr[T any](v T) *T { return &v }
