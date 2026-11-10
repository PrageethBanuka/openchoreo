// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

// Package endpointrollout turns an Endpoint's rollout plan into the weights
// and mirror shares an environment's EndpointReleaseBinding sets, and reads
// back where an environment stands. openchoreo-api and the automation module
// both use it, so every client steps a rollout the same way.
package endpointrollout

import (
	"errors"
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
	steps := stepsOf(r)
	if r.Strategy == openchoreov1alpha1.EndpointRolloutBlueGreen && len(r.Candidates) > 0 {
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

// stepsOf is the steps a canary or mirror plan lists.
func stepsOf(r *openchoreov1alpha1.EndpointRollout) []openchoreov1alpha1.EndpointRolloutStep {
	switch {
	case r.Strategy == openchoreov1alpha1.EndpointRolloutCanary && r.Canary != nil:
		return r.Canary.Steps
	case r.Strategy == openchoreov1alpha1.EndpointRolloutMirror && r.Mirror != nil:
		return r.Mirror.Steps
	}
	return nil
}

// Validate checks a plan against the Endpoint's backends as the CRD does,
// which a dry run never reaches, and that it can be carried out: the
// candidate starts with no traffic, each step gives it more, and a canary
// ends with all of it.
func Validate(r *openchoreov1alpha1.EndpointRollout, refs []openchoreov1alpha1.EndpointBackendRef) error {
	switch r.Strategy {
	case openchoreov1alpha1.EndpointRolloutCanary, openchoreov1alpha1.EndpointRolloutBlueGreen,
		openchoreov1alpha1.EndpointRolloutABTest, openchoreov1alpha1.EndpointRolloutMirror:
	default:
		return fmt.Errorf("strategy must be canary, blueGreen, abTest or mirror, not %q", r.Strategy)
	}
	if len(r.Candidates) != 1 {
		return errors.New("a rollout has exactly one candidate")
	}
	find := func(name string) *openchoreov1alpha1.EndpointBackendRef {
		i := slices.IndexFunc(refs, func(ref openchoreov1alpha1.EndpointBackendRef) bool { return ref.Name == name })
		if i < 0 {
			return nil
		}
		return &refs[i]
	}
	name := r.Candidates[0].Name
	stable, candidate := find(r.Stable), find(name)
	mirror := r.Strategy == openchoreov1alpha1.EndpointRolloutMirror
	switch {
	case stable == nil:
		return fmt.Errorf("rollout.stable must name a backend in backendRefs, not %q", r.Stable)
	case stable.Mirror != nil:
		return errors.New("rollout.stable can't be a mirror backend: a mirror only receives copies")
	case name == r.Stable:
		return errors.New("a rollout candidate can't also be stable")
	case candidate == nil:
		return fmt.Errorf("the candidate %q must name a backend in backendRefs", name)
	case (candidate.Mirror != nil) != mirror:
		return errors.New("a mirror rollout needs a mirror on its candidate, and only a mirror rollout can have one")
	case r.Strategy == openchoreov1alpha1.EndpointRolloutABTest && !hasCookie(candidate):
		return errors.New("an abTest rollout needs a cookie match on its candidate")
	case !mirror && refWeight(*candidate) != 0:
		return fmt.Errorf("%s must start at weight 0, so it gets no traffic before the first step", name)
	case (r.Canary != nil) != (r.Strategy == openchoreov1alpha1.EndpointRolloutCanary):
		return errors.New("set canary for a canary rollout only")
	case (r.Mirror != nil) != mirror:
		return errors.New("set mirror for a mirror rollout only")
	}
	steps := stepsOf(r)
	if (r.Canary != nil || r.Mirror != nil) && len(steps) == 0 {
		return errors.New("add at least one step")
	}
	var last int32
	for i, st := range steps {
		var share int32
		for _, b := range st.Backends {
			if b.Name != name {
				return fmt.Errorf("step %d names %q; a rollout step can only name candidates", i+1, b.Name)
			}
			if b.Percent < 0 || b.Percent > 100 {
				return fmt.Errorf("step %d: a percent is from 0 to 100", i+1)
			}
			share += b.Percent
		}
		if share <= last {
			return fmt.Errorf("step %d (%d%%) must give %s more than %d%%", i+1, share, name, last)
		}
		last = share
	}
	if r.Strategy == openchoreov1alpha1.EndpointRolloutCanary && last != 100 {
		return fmt.Errorf("a canary's last step must be 100%%, so %s takes all the traffic before the rollout finishes", name)
	}
	return nil
}

func hasCookie(ref *openchoreov1alpha1.EndpointBackendRef) bool {
	return ref.Match != nil && len(ref.Match.Cookies) > 0
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
		if samePercents(r, stagePercents(r, refs, overrides, s), percents) {
			return s.Index
		}
	}
	return -1
}

// Next is the stage a step moves an environment to: the first after its own
// that changes its shares, or when they were set by hand, the first that
// gives every candidate more. -1 when there is none.
func Next(
	r *openchoreov1alpha1.EndpointRollout,
	refs []openchoreov1alpha1.EndpointBackendRef,
	overrides []openchoreov1alpha1.EndpointBackendOverride,
) int {
	percents := Percents(r, refs, overrides)
	at := Position(r, refs, overrides)
	for _, s := range Stages(r)[at+1:] {
		got := stagePercents(r, refs, overrides, s)
		if at >= 0 && !samePercents(r, got, percents) {
			return s.Index
		}
		if at < 0 && !slices.ContainsFunc(r.Candidates, func(c openchoreov1alpha1.EndpointRolloutCandidate) bool {
			return got[c.Name] <= percents[c.Name]
		}) {
			return s.Index
		}
	}
	return -1
}

// stagePercents is each candidate's share once a stage's weights are set,
// which rounding can move off the stage's own percents.
func stagePercents(
	r *openchoreov1alpha1.EndpointRollout,
	refs []openchoreov1alpha1.EndpointBackendRef,
	overrides []openchoreov1alpha1.EndpointBackendOverride,
	s Stage,
) map[string]int32 {
	if o, err := Overrides(r, refs, overrides, s); err == nil {
		return Percents(r, refs, o)
	}
	out := map[string]int32{}
	for _, c := range r.Candidates {
		out[c.Name] = s.percent(c.Name)
	}
	return out
}

func samePercents(r *openchoreov1alpha1.EndpointRollout, a, b map[string]int32) bool {
	return !slices.ContainsFunc(r.Candidates, func(c openchoreov1alpha1.EndpointRolloutCandidate) bool {
		return a[c.Name] != b[c.Name]
	})
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
	for name, w := range stageWeights(r, refs, current, stage) {
		set[name] = openchoreov1alpha1.EndpointBackendOverride{Name: name, Weight: ptr(w)}
	}
	return merge(current, set), nil
}

// stageWeights splits stable's weight between it and the candidates. When
// other backends share the route, every weight is scaled by 100 so each
// percent is exact; otherwise stable and the candidates split 100. A backend
// outside the rollout that the environment turned off stays off.
func stageWeights(
	r *openchoreov1alpha1.EndpointRollout,
	refs []openchoreov1alpha1.EndpointBackendRef,
	current []openchoreov1alpha1.EndpointBackendOverride,
	stage Stage,
) map[string]int32 {
	inRollout := append([]string{r.Stable}, candidateNames(r)...)
	effective, _ := EffectiveWeights(refs, current)
	others := map[string]int32{}
	var stable int32
	for _, ref := range refs {
		switch {
		case ref.Name == r.Stable:
			stable = refWeight(ref)
		case !slices.Contains(inRollout, ref.Name) && refWeight(ref) > 0 && effective[ref.Name] > 0:
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
