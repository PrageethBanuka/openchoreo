// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointrelease

import (
	"context"
	"log/slog"

	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	authz "github.com/openchoreo/openchoreo/internal/authz/core"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
)

const (
	resourceTypeEndpointRelease = "endpointrelease"
)

// endpointReleaseServiceWithAuthz wraps a Service and adds authorization checks.
type endpointReleaseServiceWithAuthz struct {
	internal Service
	authz    *services.AuthzChecker
}

var _ Service = (*endpointReleaseServiceWithAuthz)(nil)

// NewServiceWithAuthz creates an endpoint release service with authorization checks.
func NewServiceWithAuthz(k8sClient client.Client, authzPDP authz.PDP, logger *slog.Logger) Service {
	return &endpointReleaseServiceWithAuthz{
		internal: NewService(k8sClient, logger),
		authz:    services.NewAuthzChecker(authzPDP, logger),
	}
}

func viewRequest(namespaceName string, er *openchoreov1alpha1.EndpointRelease) services.CheckRequest {
	return services.CheckRequest{
		Action:       authz.ActionViewEndpointRelease,
		ResourceType: resourceTypeEndpointRelease,
		ResourceID:   er.Name,
		Hierarchy: authz.ResourceHierarchy{
			Namespace: namespaceName,
			Project:   er.Spec.Owner.ProjectName,
		},
	}
}

func (s *endpointReleaseServiceWithAuthz) ListEndpointReleases(ctx context.Context, namespaceName, endpointName string, opts services.ListOptions) (*services.ListResult[openchoreov1alpha1.EndpointRelease], error) {
	return services.FilteredList(ctx, opts, s.authz,
		func(ctx context.Context, pageOpts services.ListOptions) (*services.ListResult[openchoreov1alpha1.EndpointRelease], error) {
			return s.internal.ListEndpointReleases(ctx, namespaceName, endpointName, pageOpts)
		},
		func(er openchoreov1alpha1.EndpointRelease) services.CheckRequest {
			return viewRequest(namespaceName, &er)
		},
	)
}

func (s *endpointReleaseServiceWithAuthz) GetEndpointRelease(ctx context.Context, namespaceName, endpointReleaseName string) (*openchoreov1alpha1.EndpointRelease, error) {
	// Fetch first to get owner info for authz hierarchy
	er, err := s.internal.GetEndpointRelease(ctx, namespaceName, endpointReleaseName)
	if err != nil {
		return nil, err
	}
	if err := s.authz.Check(ctx, viewRequest(namespaceName, er)); err != nil {
		return nil, err
	}
	return er, nil
}

// DeleteEndpointRelease prunes the Endpoint's release history, so it is
// authorized as an update of the owning Endpoint.
func (s *endpointReleaseServiceWithAuthz) DeleteEndpointRelease(ctx context.Context, namespaceName, endpointReleaseName string) error {
	er, err := s.internal.GetEndpointRelease(ctx, namespaceName, endpointReleaseName)
	if err != nil {
		return err
	}
	if err := s.authz.Check(ctx, services.CheckRequest{
		Action:       authz.ActionUpdateEndpoint,
		ResourceType: "endpoint",
		ResourceID:   er.Spec.Owner.EndpointName,
		Hierarchy: authz.ResourceHierarchy{
			Namespace: namespaceName,
			Project:   er.Spec.Owner.ProjectName,
		},
	}); err != nil {
		return err
	}
	return s.internal.DeleteEndpointRelease(ctx, namespaceName, endpointReleaseName)
}
