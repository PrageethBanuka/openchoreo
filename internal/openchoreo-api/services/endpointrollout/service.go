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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	endpointsvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpoint"
	endpointreleasesvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointrelease"
	endpointreleasebindingsvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointreleasebinding"
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
	endpoints endpointsvc.Service
	releases  endpointreleasesvc.Service
	bindings  endpointreleasebindingsvc.Service
	logger    *slog.Logger
}

var _ Service = (*service)(nil)

// NewService creates the rollout service. Plan changes need endpoint update
// and steps need binding update in the environment.
func NewService(
	endpoints endpointsvc.Service,
	releases endpointreleasesvc.Service,
	bindings endpointreleasebindingsvc.Service,
	logger *slog.Logger,
) Service {
	return &service{endpoints: endpoints, releases: releases, bindings: bindings, logger: logger}
}

type environment struct {
	binding *openchoreov1alpha1.EndpointReleaseBinding
	refs    []openchoreov1alpha1.EndpointBackendRef
}

func (s *service) Get(ctx context.Context, namespaceName, endpointName string) (*Status, error) {
	ep, err := s.endpoints.GetEndpoint(ctx, namespaceName, endpointName)
	if err != nil {
		return nil, err
	}
	return s.status(ctx, namespaceName, ep)
}

func (s *service) Start(ctx context.Context, namespaceName, endpointName string, req StartRequest) (*Status, error) {
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
	return s.writeEndpoint(ctx, namespaceName, ep, req.DryRun)
}

func (s *service) Step(ctx context.Context, namespaceName, endpointName string, req StepRequest) (*Status, error) {
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
	ep, err := s.requireRollout(ctx, namespaceName, endpointName)
	if err != nil {
		return nil, err
	}
	r := ep.Spec.Rollout
	refs, leaving, err := endpointrollout.Finish(r, ep.Spec.BackendRefs, req.Keep)
	if err != nil {
		return nil, &services.ValidationError{Msg: err.Error()}
	}
	if r.Strategy != openchoreov1alpha1.EndpointRolloutABTest {
		bindings, err := s.listBindings(ctx, namespaceName, ep.Name)
		if err != nil {
			return nil, err
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
			return nil, conflict("%s still gets traffic; move it to 0%% there first", strings.Join(serving, ", "))
		}
	}
	ep.Spec.BackendRefs = refs
	ep.Spec.Rollout = nil
	return s.writeEndpoint(ctx, namespaceName, ep, req.DryRun)
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
	change := &Change{Environment: env.binding.Spec.Environment, Before: before, After: after}
	if !dryRun {
		env.binding.Spec.BackendOverrides = after
		if _, err := s.bindings.UpdateEndpointReleaseBinding(ctx, namespaceName, env.binding); err != nil {
			return nil, err
		}
		s.logger.Info("Endpoint rollout stepped", "namespace", namespaceName, "endpoint", ep.Name,
			"environment", env.binding.Spec.Environment, "step", stage.Index)
	}
	status, err := s.status(ctx, namespaceName, ep)
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
	for i := range bindings {
		if bindings[i].Spec.Environment != name {
			continue
		}
		refs, err := s.releaseRefs(ctx, namespaceName, &bindings[i])
		if err != nil {
			return nil, err
		}
		return &environment{binding: &bindings[i], refs: refs}, nil
	}
	return nil, fmt.Errorf("%w: %s is not deployed in %s", ErrEnvironmentNotBound, ep.Name, name)
}

// releaseRefs is the backends of the release an environment runs, which a
// step's weights are computed against.
func (s *service) releaseRefs(ctx context.Context, namespaceName string, b *openchoreov1alpha1.EndpointReleaseBinding) ([]openchoreov1alpha1.EndpointBackendRef, error) {
	if b.Spec.EndpointRelease == "" {
		return nil, nil
	}
	release, err := s.releases.GetEndpointRelease(ctx, namespaceName, b.Spec.EndpointRelease)
	if errors.Is(err, endpointreleasesvc.ErrEndpointReleaseNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return release.Spec.BackendRefs, nil
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
			refs, err := s.releaseRefs(ctx, namespaceName, b)
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
