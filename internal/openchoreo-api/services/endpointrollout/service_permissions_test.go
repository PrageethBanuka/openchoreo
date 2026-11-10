// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointrollout

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	authzcore "github.com/openchoreo/openchoreo/internal/authz/core"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	clusterendpointtypesvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/clusterendpointtype"
	endpointsvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpoint"
	endpointreleasesvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointrelease"
	endpointreleasebindingsvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointreleasebinding"
	endpointtypesvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointtype"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/testutil"
)

// grantedPDP allows exactly the actions it holds.
type grantedPDP map[string]bool

func (p grantedPDP) Evaluate(_ context.Context, req *authzcore.EvaluateRequest) (*authzcore.Decision, error) {
	return &authzcore.Decision{Decision: p[req.Action], Context: &authzcore.DecisionContext{}}, nil
}

func (p grantedPDP) BatchEvaluate(_ context.Context, req *authzcore.BatchEvaluateRequest) (*authzcore.BatchEvaluateResponse, error) {
	decisions := make([]authzcore.Decision, len(req.Requests))
	for i, r := range req.Requests {
		decisions[i] = authzcore.Decision{Decision: p[r.Action]}
	}
	return &authzcore.BatchEvaluateResponse{Decisions: decisions}, nil
}

func (p grantedPDP) GetSubjectProfile(context.Context, *authzcore.ProfileRequest) (*authzcore.UserCapabilitiesResponse, error) {
	return nil, nil
}

func grant(actions ...string) grantedPDP {
	p := grantedPDP{}
	for _, a := range actions {
		p[a] = true
	}
	return p
}

var viewer = []string{
	authzcore.ActionViewEndpoint, authzcore.ActionViewEndpointRelease, authzcore.ActionViewEndpointReleaseBinding,
}

// authorized is the fixture's rollout service as handlers get it, on the
// authorized Endpoint, release and binding services.
func authorized(t *testing.T, pdp authzcore.PDP) (Service, client.Client) {
	t.Helper()
	_, c := fixture(t, true)
	logger := testutil.TestLogger()
	return NewService(endpointsvc.NewServiceWithAuthz(c, pdp, logger), endpointreleasesvc.NewServiceWithAuthz(c, pdp, logger),
		endpointreleasebindingsvc.NewServiceWithAuthz(c, pdp, logger), endpointtypesvc.NewService(c, logger),
		clusterendpointtypesvc.NewService(c, logger), logger), c
}

func TestPermissions_ViewerCanOnlyRead(t *testing.T) {
	ctx := testutil.AuthzContext()
	svc, c := authorized(t, grant(viewer...))

	status, err := svc.Get(ctx, ns, epName)
	require.NoError(t, err)
	assert.Len(t, status.Environments, 1)

	_, err = svc.Step(ctx, ns, epName, StepRequest{Environment: "dev"})
	assert.ErrorIs(t, err, services.ErrForbidden, "step")
	_, err = svc.Abort(ctx, ns, epName, AbortRequest{Environment: "dev"})
	assert.ErrorIs(t, err, services.ErrForbidden, "abort")
	_, err = svc.Finish(ctx, ns, epName, FinishRequest{Keep: "stable"})
	assert.ErrorIs(t, err, services.ErrForbidden, "finish")

	assert.Empty(t, overridesOf(t, c))
	ep := &openchoreov1alpha1.Endpoint{}
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: epName}, ep))
	assert.NotNil(t, ep.Spec.Rollout, "the plan is still there")
}

func TestPermissions_ViewerCannotStart(t *testing.T) {
	ctx := testutil.AuthzContext()
	svc, c := authorized(t, grant(viewer...))
	ep := &openchoreov1alpha1.Endpoint{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: epName}, ep))
	ep.Spec.Rollout = nil
	require.NoError(t, c.Update(ctx, ep))

	_, err := svc.Start(ctx, ns, epName, StartRequest{Rollout: canaryPlan()})
	assert.ErrorIs(t, err, services.ErrForbidden)
}

// Moving traffic needs update on the environment's binding only; changing
// the plan needs update on the Endpoint.
func TestPermissions_StepperMovesTrafficButNotThePlan(t *testing.T) {
	ctx := testutil.AuthzContext()
	svc, c := authorized(t, grant(append(viewer, authzcore.ActionUpdateEndpointReleaseBinding)...))

	_, err := svc.Step(ctx, ns, epName, StepRequest{Environment: "dev"})
	require.NoError(t, err)
	assert.Equal(t, map[string]int32{"stable": 95, "v2": 5}, overridesOf(t, c))
	_, err = svc.Abort(ctx, ns, epName, AbortRequest{Environment: "dev"})
	require.NoError(t, err)
	assert.Equal(t, map[string]int32{"stable": 100, "v2": 0}, overridesOf(t, c))

	_, err = svc.Finish(ctx, ns, epName, FinishRequest{Keep: "stable"})
	assert.ErrorIs(t, err, services.ErrForbidden)
}
