// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointreleasebinding

import (
	"context"
	"log/slog"

	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	authz "github.com/openchoreo/openchoreo/internal/authz/core"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
)

const (
	resourceTypeEndpointReleaseBinding = "endpointreleasebinding"
)

// endpointReleaseBindingServiceWithAuthz wraps a Service and adds authorization checks.
// Bindings are authorized at the project level with the environment attribute,
// so policies can, for example, allow changing traffic in development but not
// in production.
type endpointReleaseBindingServiceWithAuthz struct {
	internal Service
	authz    *services.AuthzChecker
}

var _ Service = (*endpointReleaseBindingServiceWithAuthz)(nil)

// NewServiceWithAuthz creates an endpoint release binding service with authorization checks.
func NewServiceWithAuthz(k8sClient client.Client, authzPDP authz.PDP, logger *slog.Logger) Service {
	return &endpointReleaseBindingServiceWithAuthz{
		internal: NewService(k8sClient, logger),
		authz:    services.NewAuthzChecker(authzPDP, logger),
	}
}

func checkRequest(action, namespaceName, name string, spec openchoreov1alpha1.EndpointReleaseBindingSpec) services.CheckRequest {
	return services.CheckRequest{
		Action:       action,
		ResourceType: resourceTypeEndpointReleaseBinding,
		ResourceID:   name,
		Hierarchy: authz.ResourceHierarchy{
			Namespace: namespaceName,
			Project:   spec.Owner.ProjectName,
		},
		Context: authz.Context{
			Resource: authz.ResourceAttribute{Environment: services.FormatDualScopedResourceName(namespaceName, spec.Environment, false)},
		},
	}
}

func (s *endpointReleaseBindingServiceWithAuthz) CreateEndpointReleaseBinding(ctx context.Context, namespaceName string, rb *openchoreov1alpha1.EndpointReleaseBinding) (*openchoreov1alpha1.EndpointReleaseBinding, error) {
	if err := s.authz.Check(ctx, checkRequest(authz.ActionCreateEndpointReleaseBinding, namespaceName, rb.Name, rb.Spec)); err != nil {
		return nil, err
	}
	return s.internal.CreateEndpointReleaseBinding(ctx, namespaceName, rb)
}

func (s *endpointReleaseBindingServiceWithAuthz) UpdateEndpointReleaseBinding(ctx context.Context, namespaceName string, rb *openchoreov1alpha1.EndpointReleaseBinding) (*openchoreov1alpha1.EndpointReleaseBinding, error) {
	// Authorize against the stored owner and environment rather than the body,
	// so a caller cannot claim a project or environment they can edit.
	existing, err := s.internal.GetEndpointReleaseBinding(ctx, namespaceName, rb.Name)
	if err != nil {
		return nil, err
	}
	if err := s.authz.Check(ctx, checkRequest(authz.ActionUpdateEndpointReleaseBinding, namespaceName, rb.Name, existing.Spec)); err != nil {
		return nil, err
	}
	return s.internal.UpdateEndpointReleaseBinding(ctx, namespaceName, rb)
}

func (s *endpointReleaseBindingServiceWithAuthz) ListEndpointReleaseBindings(ctx context.Context, namespaceName, endpointName, environment string, opts services.ListOptions) (*services.ListResult[openchoreov1alpha1.EndpointReleaseBinding], error) {
	return services.FilteredList(ctx, opts, s.authz,
		func(ctx context.Context, pageOpts services.ListOptions) (*services.ListResult[openchoreov1alpha1.EndpointReleaseBinding], error) {
			return s.internal.ListEndpointReleaseBindings(ctx, namespaceName, endpointName, environment, pageOpts)
		},
		func(rb openchoreov1alpha1.EndpointReleaseBinding) services.CheckRequest {
			return checkRequest(authz.ActionViewEndpointReleaseBinding, namespaceName, rb.Name, rb.Spec)
		},
	)
}

func (s *endpointReleaseBindingServiceWithAuthz) GetEndpointReleaseBinding(ctx context.Context, namespaceName, endpointReleaseBindingName string) (*openchoreov1alpha1.EndpointReleaseBinding, error) {
	// Fetch first to get owner info for authz hierarchy
	rb, err := s.internal.GetEndpointReleaseBinding(ctx, namespaceName, endpointReleaseBindingName)
	if err != nil {
		return nil, err
	}
	if err := s.authz.Check(ctx, checkRequest(authz.ActionViewEndpointReleaseBinding, namespaceName, endpointReleaseBindingName, rb.Spec)); err != nil {
		return nil, err
	}
	return rb, nil
}

func (s *endpointReleaseBindingServiceWithAuthz) DeleteEndpointReleaseBinding(ctx context.Context, namespaceName, endpointReleaseBindingName string) error {
	// Fetch first to get owner info for authz hierarchy
	rb, err := s.internal.GetEndpointReleaseBinding(ctx, namespaceName, endpointReleaseBindingName)
	if err != nil {
		return err
	}
	if err := s.authz.Check(ctx, checkRequest(authz.ActionDeleteEndpointReleaseBinding, namespaceName, endpointReleaseBindingName, rb.Spec)); err != nil {
		return err
	}
	return s.internal.DeleteEndpointReleaseBinding(ctx, namespaceName, endpointReleaseBindingName)
}
