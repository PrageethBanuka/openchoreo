// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"context"
	"log/slog"

	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	authz "github.com/openchoreo/openchoreo/internal/authz/core"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
)

const (
	resourceTypeEndpoint = "endpoint"
)

// endpointServiceWithAuthz wraps a Service and adds authorization checks.
// Handlers should use this. Other services should use the unwrapped Service directly.
//
// An Endpoint routes across several components of one project, so it is
// authorized at the project level.
type endpointServiceWithAuthz struct {
	internal Service
	authz    *services.AuthzChecker
}

var _ Service = (*endpointServiceWithAuthz)(nil)

// NewServiceWithAuthz creates an endpoint service with authorization checks.
func NewServiceWithAuthz(k8sClient client.Client, authzPDP authz.PDP, logger *slog.Logger) Service {
	return &endpointServiceWithAuthz{
		internal: NewService(k8sClient, logger),
		authz:    services.NewAuthzChecker(authzPDP, logger),
	}
}

func checkRequest(action, namespaceName, projectName, endpointName string) services.CheckRequest {
	return services.CheckRequest{
		Action:       action,
		ResourceType: resourceTypeEndpoint,
		ResourceID:   endpointName,
		Hierarchy: authz.ResourceHierarchy{
			Namespace: namespaceName,
			Project:   projectName,
		},
	}
}

func (s *endpointServiceWithAuthz) CreateEndpoint(ctx context.Context, namespaceName string, ep *openchoreov1alpha1.Endpoint) (*openchoreov1alpha1.Endpoint, error) {
	if err := s.authz.Check(ctx, checkRequest(authz.ActionCreateEndpoint, namespaceName, ep.Spec.Owner.ProjectName, ep.Name)); err != nil {
		return nil, err
	}
	return s.internal.CreateEndpoint(ctx, namespaceName, ep)
}

func (s *endpointServiceWithAuthz) UpdateEndpoint(ctx context.Context, namespaceName string, ep *openchoreov1alpha1.Endpoint) (*openchoreov1alpha1.Endpoint, error) {
	// Authorize against the stored project rather than the one in the body, so
	// a caller cannot name a project they can edit to change another's Endpoint.
	existing, err := s.internal.GetEndpoint(ctx, namespaceName, ep.Name)
	if err != nil {
		return nil, err
	}
	if err := s.authz.Check(ctx, checkRequest(authz.ActionUpdateEndpoint, namespaceName, existing.Spec.Owner.ProjectName, ep.Name)); err != nil {
		return nil, err
	}
	return s.internal.UpdateEndpoint(ctx, namespaceName, ep)
}

func (s *endpointServiceWithAuthz) ListEndpoints(ctx context.Context, namespaceName, projectName, componentName string, opts services.ListOptions) (*services.ListResult[openchoreov1alpha1.Endpoint], error) {
	return services.FilteredList(ctx, opts, s.authz,
		func(ctx context.Context, pageOpts services.ListOptions) (*services.ListResult[openchoreov1alpha1.Endpoint], error) {
			return s.internal.ListEndpoints(ctx, namespaceName, projectName, componentName, pageOpts)
		},
		func(ep openchoreov1alpha1.Endpoint) services.CheckRequest {
			return checkRequest(authz.ActionViewEndpoint, namespaceName, ep.Spec.Owner.ProjectName, ep.Name)
		},
	)
}

func (s *endpointServiceWithAuthz) GetEndpoint(ctx context.Context, namespaceName, endpointName string) (*openchoreov1alpha1.Endpoint, error) {
	// Fetch first to get the project for authz hierarchy
	ep, err := s.internal.GetEndpoint(ctx, namespaceName, endpointName)
	if err != nil {
		return nil, err
	}
	if err := s.authz.Check(ctx, checkRequest(authz.ActionViewEndpoint, namespaceName, ep.Spec.Owner.ProjectName, endpointName)); err != nil {
		return nil, err
	}
	return ep, nil
}

func (s *endpointServiceWithAuthz) DeleteEndpoint(ctx context.Context, namespaceName, endpointName string) error {
	// Fetch first to get the project for authz hierarchy
	ep, err := s.internal.GetEndpoint(ctx, namespaceName, endpointName)
	if err != nil {
		return err
	}
	if err := s.authz.Check(ctx, checkRequest(authz.ActionDeleteEndpoint, namespaceName, ep.Spec.Owner.ProjectName, endpointName)); err != nil {
		return err
	}
	return s.internal.DeleteEndpoint(ctx, namespaceName, endpointName)
}
