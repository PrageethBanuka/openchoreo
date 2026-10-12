// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointtype

import (
	"context"
	"log/slog"

	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	authz "github.com/openchoreo/openchoreo/internal/authz/core"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
)

const (
	resourceTypeEndpointType = "endpointtype"
)

// endpointTypeServiceWithAuthz wraps a Service and adds authorization checks.
type endpointTypeServiceWithAuthz struct {
	internal Service
	authz    *services.AuthzChecker
}

var _ Service = (*endpointTypeServiceWithAuthz)(nil)

// NewServiceWithAuthz creates a endpoint type service with authorization checks.
func NewServiceWithAuthz(k8sClient client.Client, authzPDP authz.PDP, logger *slog.Logger) Service {
	return &endpointTypeServiceWithAuthz{
		internal: NewService(k8sClient, logger),
		authz:    services.NewAuthzChecker(authzPDP, logger),
	}
}

func (s *endpointTypeServiceWithAuthz) ListEndpointTypes(ctx context.Context, namespaceName string, opts services.ListOptions) (*services.ListResult[openchoreov1alpha1.EndpointType], error) {
	return services.FilteredList(ctx, opts, s.authz,
		func(ctx context.Context, pageOpts services.ListOptions) (*services.ListResult[openchoreov1alpha1.EndpointType], error) {
			return s.internal.ListEndpointTypes(ctx, namespaceName, pageOpts)
		},
		func(cet openchoreov1alpha1.EndpointType) services.CheckRequest {
			return services.CheckRequest{
				Action:       authz.ActionViewEndpointType,
				ResourceType: resourceTypeEndpointType,
				ResourceID:   cet.Name,
				Hierarchy:    authz.ResourceHierarchy{Namespace: namespaceName},
			}
		},
	)
}

func (s *endpointTypeServiceWithAuthz) GetEndpointType(ctx context.Context, namespaceName, etName string) (*openchoreov1alpha1.EndpointType, error) {
	if err := s.authz.Check(ctx, services.CheckRequest{
		Action:       authz.ActionViewEndpointType,
		ResourceType: resourceTypeEndpointType,
		ResourceID:   etName,
		Hierarchy:    authz.ResourceHierarchy{Namespace: namespaceName},
	}); err != nil {
		return nil, err
	}
	return s.internal.GetEndpointType(ctx, namespaceName, etName)
}
