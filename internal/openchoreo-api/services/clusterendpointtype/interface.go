// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package clusterendpointtype

import (
	"context"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
)

// Service defines the cluster endpoint type service interface.
// Both the core service (no authz) and the authz-wrapped service implement this.
// Cluster endpoint types are read-only through the API for now; platform
// engineers manage them declaratively.
type Service interface {
	ListClusterEndpointTypes(ctx context.Context, opts services.ListOptions) (*services.ListResult[openchoreov1alpha1.ClusterEndpointType], error)
	GetClusterEndpointType(ctx context.Context, cetName string) (*openchoreov1alpha1.ClusterEndpointType, error)
}
