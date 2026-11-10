// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

// Package endpointrollout carries out an Endpoint's rollout plan: it writes
// the plan on the Endpoint and each step as backendOverrides on an
// environment's EndpointReleaseBinding. It goes through the Endpoint, release
// and binding services, so their authorization applies.
package endpointrollout

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	clusterendpointtypesvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/clusterendpointtype"
	endpointsvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpoint"
	endpointreleasesvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointrelease"
	endpointreleasebindingsvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointreleasebinding"
	endpointtypesvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointtype"
	endpointpipeline "github.com/openchoreo/openchoreo/internal/pipeline/endpoint"
	"github.com/openchoreo/openchoreo/pkg/endpointrollout"
)

var (
	ErrNoRollout           = errors.New("the endpoint has no rollout plan")
	ErrEnvironmentNotBound = errors.New("the endpoint is not deployed in this environment")
)

// ConflictError is a change refused because of where the rollout stands.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

func conflict(format string, args ...any) error {
	return &ConflictError{Msg: fmt.Sprintf(format, args...)}
}

// Service runs an Endpoint's rollout.
type Service interface {
	Get(ctx context.Context, namespaceName, endpointName string) (*Status, error)
	Start(ctx context.Context, namespaceName, endpointName string, req StartRequest) (*Status, error)
	Step(ctx context.Context, namespaceName, endpointName string, req StepRequest) (*Status, error)
	Abort(ctx context.Context, namespaceName, endpointName string, req AbortRequest) (*Status, error)
	Finish(ctx context.Context, namespaceName, endpointName string, req FinishRequest) (*Status, error)
}

// StartRequest writes a rollout plan, adding or replacing the backends it
// names in the same update.
type StartRequest struct {
	Rollout  openchoreov1alpha1.EndpointRollout      `json:"rollout"`
	Backends []openchoreov1alpha1.EndpointBackendRef `json:"backends,omitempty"`
	DryRun   bool                                    `json:"dryRun,omitempty"`
}

// StepRequest moves an environment to a stage: the next one, or To.
// ExpectedStep refuses the change unless the environment is at that stage,
// so a retried call can't skip a step.
type StepRequest struct {
	Environment  string `json:"environment"`
	To           *int   `json:"to,omitempty"`
	ExpectedStep *int   `json:"expectedStep,omitempty"`
	DryRun       bool   `json:"dryRun,omitempty"`
}

// AbortRequest returns an environment's traffic to stable.
type AbortRequest struct {
	Environment string `json:"environment"`
	DryRun      bool   `json:"dryRun,omitempty"`
}

// FinishRequest ends the rollout keeping one backend.
type FinishRequest struct {
	Keep   string `json:"keep"`
	DryRun bool   `json:"dryRun,omitempty"`
}

// Status is an Endpoint's rollout and where each environment stands.
type Status struct {
	Endpoint      string                              `json:"endpoint"`
	LatestRelease string                              `json:"latestRelease,omitempty"`
	Rollout       *openchoreov1alpha1.EndpointRollout `json:"rollout,omitempty"`
	Stages        []endpointrollout.Stage             `json:"stages,omitempty"`
	Environments  []EnvironmentStatus                 `json:"environments"`
	Change        *Change                             `json:"change,omitempty"`
}

// EnvironmentStatus is what one environment sets and serves.
type EnvironmentStatus struct {
	Environment     string                                       `json:"environment"`
	BindingName     string                                       `json:"bindingName"`
	EndpointRelease string                                       `json:"endpointRelease,omitempty"`
	Step            *int                                         `json:"step,omitempty"`
	Percents        map[string]int32                             `json:"percents,omitempty"`
	Missing         []string                                     `json:"missing,omitempty"`
	Overrides       []openchoreov1alpha1.EndpointBackendOverride `json:"overrides"`
	Backends        []openchoreov1alpha1.ResolvedEndpointBackend `json:"backends"`
	Ready           bool                                         `json:"ready"`
	Message         string                                       `json:"message,omitempty"`
}

// Change is what a call writes; on a dry run it is returned instead.
type Change struct {
	Environment string                                       `json:"environment,omitempty"`
	Before      []openchoreov1alpha1.EndpointBackendOverride `json:"before,omitempty"`
	After       []openchoreov1alpha1.EndpointBackendOverride `json:"after,omitempty"`
	Endpoint    *openchoreov1alpha1.EndpointSpec             `json:"endpoint,omitempty"`
}

type service struct {
	endpoints    endpointsvc.Service
	releases     endpointreleasesvc.Service
	bindings     endpointreleasebindingsvc.Service
	types        endpointtypesvc.Service
	clusterTypes clusterendpointtypesvc.Service
	logger       *slog.Logger
}

var _ Service = (*service)(nil)

// NewService creates the rollout service. Plan changes need endpoint update
// and steps need binding update in the environment. The type services only
// read what a type can render, so they are passed without authorization.
func NewService(
	endpoints endpointsvc.Service,
	releases endpointreleasesvc.Service,
	bindings endpointreleasebindingsvc.Service,
	types endpointtypesvc.Service,
	clusterTypes clusterendpointtypesvc.Service,
	logger *slog.Logger,
) Service {
	return &service{
		endpoints: endpoints, releases: releases, bindings: bindings,
		types: types, clusterTypes: clusterTypes, logger: logger,
	}
}

// environment is the binding a step changes, read with the Endpoint's other
// bindings and their releases so the step's status needs no second read.
type environment struct {
	binding  *openchoreov1alpha1.EndpointReleaseBinding
	refs     []openchoreov1alpha1.EndpointBackendRef
	bindings []openchoreov1alpha1.EndpointReleaseBinding
	releases releases
}

// releases holds the backends of each release a call has read, by name, so
// environments on the same release read it once.
type releases map[string][]openchoreov1alpha1.EndpointBackendRef

func (s *service) Get(ctx context.Context, namespaceName, endpointName string) (*Status, error) {
	ep, err := s.endpoints.GetEndpoint(ctx, namespaceName, endpointName)
	if err != nil {
		return nil, err
	}
	return s.status(ctx, namespaceName, ep)
}

func (s *service) Start(ctx context.Context, namespaceName, endpointName string, req StartRequest) (*Status, error) {
	return onFresh(func() (*Status, error) { return s.start(ctx, namespaceName, endpointName, req) })
}

func (s *service) start(ctx context.Context, namespaceName, endpointName string, req StartRequest) (*Status, error) {
	ep, err := s.endpoints.GetEndpoint(ctx, namespaceName, endpointName)
	if err != nil {
		return nil, err
	}
	if ep.Spec.Rollout != nil {
		return nil, conflict("a %s rollout from %s is already planned; finish it first", ep.Spec.Rollout.Strategy, ep.Spec.Rollout.Stable)
	}
	for _, b := range req.Backends {
		if i := slices.IndexFunc(ep.Spec.BackendRefs, func(r openchoreov1alpha1.EndpointBackendRef) bool { return r.Name == b.Name }); i >= 0 {
			ep.Spec.BackendRefs[i] = b
		} else {
			ep.Spec.BackendRefs = append(ep.Spec.BackendRefs, b)
		}
	}
	ep.Spec.Rollout = req.Rollout.DeepCopy()
	if err := s.renderable(ctx, namespaceName, ep); err != nil {
		return nil, err
	}
	return s.writeEndpoint(ctx, namespaceName, ep, req.DryRun)
}

// renderable refuses a rollout that uses a routing feature the Endpoint's
// type does not declare, which the controller would refuse to release. A type
// that can't be read is left for the controller to report.
func (s *service) renderable(ctx context.Context, namespaceName string, ep *openchoreov1alpha1.Endpoint) error {
	var declared []openchoreov1alpha1.EndpointCapability
	switch ref := ep.Spec.Type; ref.Kind {
	case openchoreov1alpha1.EndpointTypeRefKindClusterEndpointType:
		t, err := s.clusterTypes.GetClusterEndpointType(ctx, ref.Name)
		if errors.Is(err, clusterendpointtypesvc.ErrClusterEndpointTypeNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		declared = t.Spec.Capabilities
	default:
		t, err := s.types.GetEndpointType(ctx, namespaceName, ref.Name)
		if errors.Is(err, endpointtypesvc.ErrEndpointTypeNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		declared = t.Spec.Capabilities
	}
	problems := endpointpipeline.CapabilityProblems(declared, ep.Spec.BackendRefs)
	if slices.Contains(declared, openchoreov1alpha1.EndpointCapabilityMirror) &&
		!slices.Contains(declared, openchoreov1alpha1.EndpointCapabilityMirrorPercent) &&
		len(problems) == 0 && mirrorsAShare(ep.Spec.Rollout) {
		problems = append(problems, fmt.Sprintf("a mirror step below 100%% needs the %s capability",
			openchoreov1alpha1.EndpointCapabilityMirrorPercent))
	}
	if len(problems) == 0 {
		return nil
	}
	return &services.ValidationError{Msg: fmt.Sprintf("%s %q can't render this rollout: %s",
		ep.Spec.Type.Kind, ep.Spec.Type.Name, strings.Join(problems, "; "))}
}

// mirrorsAShare reports whether a mirror plan copies part of the requests at
// some step.
func mirrorsAShare(r *openchoreov1alpha1.EndpointRollout) bool {
	if r == nil || r.Strategy != openchoreov1alpha1.EndpointRolloutMirror || r.Mirror == nil {
		return false
	}
	for _, st := range r.Mirror.Steps {
		for _, b := range st.Backends {
			if b.Percent > 0 && b.Percent < 100 {
				return true
			}
		}
	}
	return false
}

func (s *service) Step(ctx context.Context, namespaceName, endpointName string, req StepRequest) (*Status, error) {
	return onFresh(func() (*Status, error) { return s.step(ctx, namespaceName, endpointName, req) })
}

func (s *service) step(ctx context.Context, namespaceName, endpointName string, req StepRequest) (*Status, error) {
	ep, err := s.requireRollout(ctx, namespaceName, endpointName)
	if err != nil {
		return nil, err
	}
	env, err := s.environment(ctx, namespaceName, ep, req.Environment)
	if err != nil {
		return nil, err
	}
	r := ep.Spec.Rollout
	stages := endpointrollout.Stages(r)
	at := endpointrollout.Position(r, env.refs, env.binding.Spec.BackendOverrides)
	if req.ExpectedStep != nil && *req.ExpectedStep != at {
		return nil, conflict("%s is at %s, not step %d; check its status and retry", req.Environment, describe(at), *req.ExpectedStep)
	}
	to := at + 1
	if at < 0 {
		to = nextAfter(r, env, stages)
	}
	if req.To != nil {
		to = *req.To
	}
	if to < 0 || to >= len(stages) {
		if req.To == nil {
			return nil, conflict("%s is at the last step; finish the rollout", req.Environment)
		}
		return nil, &services.ValidationError{Msg: fmt.Sprintf("step must be from 0 to %d", len(stages)-1)}
	}
	return s.setStage(ctx, namespaceName, ep, env, stages[to], req.DryRun)
}

func (s *service) Abort(ctx context.Context, namespaceName, endpointName string, req AbortRequest) (*Status, error) {
	return s.Step(ctx, namespaceName, endpointName, StepRequest{Environment: req.Environment, To: ptr(0), DryRun: req.DryRun})
}

func (s *service) Finish(ctx context.Context, namespaceName, endpointName string, req FinishRequest) (*Status, error) {
	var leaving []string
	status, err := onFresh(func() (*Status, error) {
		var status *Status
		var err error
		status, leaving, err = s.finish(ctx, namespaceName, endpointName, req)
		return status, err
	})
	if err != nil || req.DryRun {
		return status, err
	}
	if err := s.dropMirrorOverrides(ctx, namespaceName, endpointName, leaving); err != nil {
		return nil, err
	}
	return status, nil
}

func (s *service) finish(ctx context.Context, namespaceName, endpointName string, req FinishRequest) (*Status, []string, error) {
	ep, err := s.requireRollout(ctx, namespaceName, endpointName)
	if err != nil {
		return nil, nil, err
	}
	r := ep.Spec.Rollout
	refs, leaving, err := endpointrollout.Finish(r, ep.Spec.BackendRefs, req.Keep)
	if err != nil {
		return nil, nil, &services.ValidationError{Msg: err.Error()}
	}
	if r.Strategy != openchoreov1alpha1.EndpointRolloutABTest {
		bindings, err := s.listBindings(ctx, namespaceName, ep.Name)
		if err != nil {
			return nil, nil, err
		}
		var serving []string
		for i := range bindings {
			for _, b := range bindings[i].Status.Backends {
				if slices.Contains(leaving, b.Name) && b.Weight > 0 {
					serving = append(serving, fmt.Sprintf("%s in %s", b.Name, bindings[i].Spec.Environment))
				}
			}
		}
		if len(serving) > 0 {
			return nil, nil, conflict("%s still gets traffic; move it to 0%% there first", strings.Join(serving, ", "))
		}
	}
	ep.Spec.BackendRefs = refs
	ep.Spec.Rollout = nil
	status, err := s.writeEndpoint(ctx, namespaceName, ep, req.DryRun)
	return status, leaving, err
}

// onFresh runs a call again from a fresh read when what it read changed
// before it saved, so it never saves over someone else's change. A conflict
// that outlasts the retries is refused as one.
func onFresh(call func() (*Status, error)) (*Status, error) {
	var status *Status
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var err error
		status, err = call()
		return err
	})
	if apierrors.IsConflict(err) {
		return nil, conflict("the Endpoint changed while saving; check its status and retry")
	}
	return status, err
}

// unchanged reports a conflict when the stored object moved past the one a
// call read, since the update services save the whole spec they are given.
func unchanged(read, stored metav1.Object, resource string) error {
	if read.GetResourceVersion() == stored.GetResourceVersion() {
		return nil
	}
	return apierrors.NewConflict(schema.GroupResource{Group: openchoreov1alpha1.GroupVersion.Group, Resource: resource},
		read.GetName(), errors.New("it changed since it was read"))
}

// dropMirrorOverrides removes the mirror shares the rollout set on the
// backends leaving the Endpoint, so a backend added again later doesn't
// inherit them. Their weights stay: an environment still on the old release
// routes by them until it is promoted. Each binding is read again on a
// conflict, since autoDeploy may be moving it to the new release.
func (s *service) dropMirrorOverrides(ctx context.Context, namespaceName, endpointName string, leaving []string) error {
	bindings, err := s.listBindings(ctx, namespaceName, endpointName)
	if err != nil {
		return err
	}
	for i := range bindings {
		name := bindings[i].Name
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			b, err := s.bindings.GetEndpointReleaseBinding(ctx, namespaceName, name)
			if err != nil {
				return err
			}
			kept, changed := withoutMirrors(b.Spec.BackendOverrides, leaving)
			if !changed {
				return nil
			}
			b.Spec.BackendOverrides = kept
			_, err = s.bindings.UpdateEndpointReleaseBinding(ctx, namespaceName, b)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func withoutMirrors(
	overrides []openchoreov1alpha1.EndpointBackendOverride, leaving []string,
) ([]openchoreov1alpha1.EndpointBackendOverride, bool) {
	kept := make([]openchoreov1alpha1.EndpointBackendOverride, 0, len(overrides))
	changed := false
	for _, o := range overrides {
		if o.Mirror != nil && slices.Contains(leaving, o.Name) {
			changed = true
			if o.Weight == nil {
				continue
			}
			o.Mirror = nil
		}
		kept = append(kept, o)
	}
	return kept, changed
}

// undeployed describes the rollout backends the overrides send traffic to
// that the environment reports it can't resolve. The controller keeps the
// last route while a backend with traffic is unresolved, so such a step would
// not apply; one that moves traffic off it does.
func undeployed(
	r *openchoreov1alpha1.EndpointRollout,
	binding *openchoreov1alpha1.EndpointReleaseBinding,
	overrides []openchoreov1alpha1.EndpointBackendOverride,
) []string {
	inRollout := append([]string{r.Stable}, candidateNames(r)...)
	var gone []string
	for _, o := range overrides {
		traffic := (o.Weight != nil && *o.Weight > 0) || (o.Mirror != nil && o.Mirror.Percent > 0)
		if !traffic || !slices.Contains(inRollout, o.Name) {
			continue
		}
		i := slices.IndexFunc(binding.Status.Backends, func(b openchoreov1alpha1.ResolvedEndpointBackend) bool {
			return b.Name == o.Name
		})
		if i < 0 || binding.Status.Backends[i].Resolved {
			continue
		}
		reason := binding.Status.Backends[i].Message
		if reason == "" {
			reason = binding.Status.Backends[i].Reason
		}
		gone = append(gone, fmt.Sprintf("%s, which isn't deployed in %s (%s)", o.Name, binding.Spec.Environment, reason))
	}
	return gone
}

func candidateNames(r *openchoreov1alpha1.EndpointRollout) []string {
	names := make([]string, 0, len(r.Candidates))
	for _, c := range r.Candidates {
		names = append(names, c.Name)
	}
	return names
}

// nextAfter is the first stage past the percents an environment was set to
// by hand.
func nextAfter(r *openchoreov1alpha1.EndpointRollout, env *environment, stages []endpointrollout.Stage) int {
	percents := endpointrollout.Percents(r, env.refs, env.binding.Spec.BackendOverrides)
	for _, st := range stages {
		ahead := true
		for _, b := range st.Backends {
			if b.Percent <= percents[b.Name] {
				ahead = false
			}
		}
		if ahead {
			return st.Index
		}
	}
	return -1
}

func (s *service) setStage(
	ctx context.Context,
	namespaceName string,
	ep *openchoreov1alpha1.Endpoint,
	env *environment,
	stage endpointrollout.Stage,
	dryRun bool,
) (*Status, error) {
	if missing := endpointrollout.Missing(ep.Spec.Rollout, env.refs); len(missing) > 0 {
		return nil, conflict("%s runs release %s, which has no backend %s; deploy the latest release there first",
			env.binding.Spec.Environment, env.binding.Spec.EndpointRelease, strings.Join(missing, ", "))
	}
	before := env.binding.Spec.BackendOverrides
	after, err := endpointrollout.Overrides(ep.Spec.Rollout, env.refs, before, stage)
	if err != nil {
		return nil, err
	}
	if gone := undeployed(ep.Spec.Rollout, env.binding, after); len(gone) > 0 {
		return nil, conflict("this step sends traffic to %s; deploy it there first, or move traffic away from it",
			strings.Join(gone, " and "))
	}
	change := &Change{Environment: env.binding.Spec.Environment, Before: before, After: after}
	if !dryRun {
		stored, err := s.bindings.GetEndpointReleaseBinding(ctx, namespaceName, env.binding.Name)
		if err != nil {
			return nil, err
		}
		if err := unchanged(env.binding, stored, "endpointreleasebindings"); err != nil {
			return nil, err
		}
		env.binding.Spec.BackendOverrides = after
		updated, err := s.bindings.UpdateEndpointReleaseBinding(ctx, namespaceName, env.binding)
		if err != nil {
			return nil, err
		}
		*env.binding = *updated
		s.logger.Info("Endpoint rollout stepped", "namespace", namespaceName, "endpoint", ep.Name,
			"environment", env.binding.Spec.Environment, "step", stage.Index)
	}
	status, err := s.statusOf(ctx, namespaceName, ep, env.bindings, env.releases)
	if err != nil {
		return nil, err
	}
	status.Change = change
	return status, nil
}

func (s *service) writeEndpoint(ctx context.Context, namespaceName string, ep *openchoreov1alpha1.Endpoint, dryRun bool) (*Status, error) {
	if dryRun {
		status, err := s.status(ctx, namespaceName, ep)
		if err != nil {
			return nil, err
		}
		status.Change = &Change{Endpoint: &ep.Spec}
		return status, nil
	}
	stored, err := s.endpoints.GetEndpoint(ctx, namespaceName, ep.Name)
	if err != nil {
		return nil, err
	}
	if err := unchanged(ep, stored, "endpoints"); err != nil {
		return nil, err
	}
	updated, err := s.endpoints.UpdateEndpoint(ctx, namespaceName, ep)
	if err != nil {
		return nil, err
	}
	s.logger.Info("Endpoint rollout plan updated", "namespace", namespaceName, "endpoint", updated.Name)
	return s.status(ctx, namespaceName, updated)
}

func (s *service) requireRollout(ctx context.Context, namespaceName, endpointName string) (*openchoreov1alpha1.Endpoint, error) {
	ep, err := s.endpoints.GetEndpoint(ctx, namespaceName, endpointName)
	if err != nil {
		return nil, err
	}
	if ep.Spec.Rollout == nil {
		return nil, ErrNoRollout
	}
	return ep, nil
}

func (s *service) environment(ctx context.Context, namespaceName string, ep *openchoreov1alpha1.Endpoint, name string) (*environment, error) {
	bindings, err := s.listBindings(ctx, namespaceName, ep.Name)
	if err != nil {
		return nil, err
	}
	read := releases{}
	for i := range bindings {
		if bindings[i].Spec.Environment != name {
			continue
		}
		refs, err := s.releaseRefs(ctx, namespaceName, &bindings[i], read)
		if err != nil {
			return nil, err
		}
		return &environment{binding: &bindings[i], refs: refs, bindings: bindings, releases: read}, nil
	}
	return nil, fmt.Errorf("%w: %s is not deployed in %s", ErrEnvironmentNotBound, ep.Name, name)
}

// releaseRefs is the backends of the release an environment runs, which a
// step's weights are computed against.
func (s *service) releaseRefs(
	ctx context.Context, namespaceName string, b *openchoreov1alpha1.EndpointReleaseBinding, read releases,
) ([]openchoreov1alpha1.EndpointBackendRef, error) {
	name := b.Spec.EndpointRelease
	if name == "" {
		return nil, nil
	}
	if refs, ok := read[name]; ok {
		return refs, nil
	}
	var refs []openchoreov1alpha1.EndpointBackendRef
	release, err := s.releases.GetEndpointRelease(ctx, namespaceName, name)
	switch {
	case errors.Is(err, endpointreleasesvc.ErrEndpointReleaseNotFound):
	case err != nil:
		return nil, err
	default:
		refs = release.Spec.BackendRefs
	}
	read[name] = refs
	return refs, nil
}

func (s *service) listBindings(ctx context.Context, namespaceName, endpointName string) ([]openchoreov1alpha1.EndpointReleaseBinding, error) {
	var out []openchoreov1alpha1.EndpointReleaseBinding
	opts := services.ListOptions{Limit: 100}
	for {
		page, err := s.bindings.ListEndpointReleaseBindings(ctx, namespaceName, endpointName, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		if page.NextCursor == "" {
			return out, nil
		}
		opts.Cursor = page.NextCursor
	}
}

func (s *service) status(ctx context.Context, namespaceName string, ep *openchoreov1alpha1.Endpoint) (*Status, error) {
	bindings, err := s.listBindings(ctx, namespaceName, ep.Name)
	if err != nil {
		return nil, err
	}
	return s.statusOf(ctx, namespaceName, ep, bindings, releases{})
}

// statusOf is where each of the given bindings stands in the Endpoint's
// rollout.
func (s *service) statusOf(
	ctx context.Context,
	namespaceName string,
	ep *openchoreov1alpha1.Endpoint,
	bindings []openchoreov1alpha1.EndpointReleaseBinding,
	read releases,
) (*Status, error) {
	status := &Status{Endpoint: ep.Name, Rollout: ep.Spec.Rollout, Environments: []EnvironmentStatus{}}
	if ep.Status.LatestRelease != nil {
		status.LatestRelease = ep.Status.LatestRelease.Name
	}
	r := ep.Spec.Rollout
	if r != nil {
		status.Stages = endpointrollout.Stages(r)
	}
	for i := range bindings {
		b := &bindings[i]
		env := EnvironmentStatus{
			Environment:     b.Spec.Environment,
			BindingName:     b.Name,
			EndpointRelease: b.Spec.EndpointRelease,
			Overrides:       b.Spec.BackendOverrides,
			Backends:        b.Status.Backends,
		}
		if env.Overrides == nil {
			env.Overrides = []openchoreov1alpha1.EndpointBackendOverride{}
		}
		if env.Backends == nil {
			env.Backends = []openchoreov1alpha1.ResolvedEndpointBackend{}
		}
		env.Ready, env.Message = ready(b)
		if r != nil {
			refs, err := s.releaseRefs(ctx, namespaceName, b, read)
			if err != nil {
				return nil, err
			}
			if env.Missing = endpointrollout.Missing(r, refs); len(env.Missing) == 0 {
				env.Step = ptr(endpointrollout.Position(r, refs, b.Spec.BackendOverrides))
				env.Percents = endpointrollout.Percents(r, refs, b.Spec.BackendOverrides)
			}
		}
		status.Environments = append(status.Environments, env)
	}
	return status, nil
}

// ready says whether the binding serves its latest spec.
func ready(b *openchoreov1alpha1.EndpointReleaseBinding) (bool, string) {
	c := meta.FindStatusCondition(b.Status.Conditions, "Ready")
	switch {
	case c == nil || c.ObservedGeneration < b.Generation:
		return false, "the latest change is still being applied"
	case c.Status != metav1.ConditionTrue:
		return false, c.Message
	}
	return true, ""
}

func describe(step int) string {
	if step < 0 {
		return "weights set by hand"
	}
	return fmt.Sprintf("step %d", step)
}

func ptr[T any](v T) *T { return &v }
