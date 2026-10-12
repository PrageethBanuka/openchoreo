// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointreleasebinding

import (
	"context"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
)

// Service defines the endpoint release binding service interface.
//
// ListEndpointReleaseBindings filters by endpoint (spec.owner.endpointName) and
// environment (spec.environment); an empty value matches everything.
type Service interface {
	CreateEndpointReleaseBinding(ctx context.Context, namespaceName string, rb *openchoreov1alpha1.EndpointReleaseBinding) (*openchoreov1alpha1.EndpointReleaseBinding, error)
	UpdateEndpointReleaseBinding(ctx context.Context, namespaceName string, rb *openchoreov1alpha1.EndpointReleaseBinding) (*openchoreov1alpha1.EndpointReleaseBinding, error)
	ListEndpointReleaseBindings(ctx context.Context, namespaceName, endpointName, environment string, opts services.ListOptions) (*services.ListResult[openchoreov1alpha1.EndpointReleaseBinding], error)
	GetEndpointReleaseBinding(ctx context.Context, namespaceName, endpointReleaseBindingName string) (*openchoreov1alpha1.EndpointReleaseBinding, error)
	DeleteEndpointReleaseBinding(ctx context.Context, namespaceName, endpointReleaseBindingName string) error
}
