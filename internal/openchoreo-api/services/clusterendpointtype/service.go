// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package clusterendpointtype

import (
	"context"
	"fmt"
	"log/slog"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
)

// clusterEndpointTypeService handles cluster endpoint type business logic without authorization checks.
type clusterEndpointTypeService struct {
	k8sClient client.Client
	logger    *slog.Logger
}

var clusterEndpointTypeTypeMeta = metav1.TypeMeta{
	APIVersion: openchoreov1alpha1.GroupVersion.String(),
	Kind:       "ClusterEndpointType",
}

var _ Service = (*clusterEndpointTypeService)(nil)

// NewService creates a new cluster endpoint type service without authorization.
func NewService(k8sClient client.Client, logger *slog.Logger) Service {
	return &clusterEndpointTypeService{
		k8sClient: k8sClient,
		logger:    logger,
	}
}

func (s *clusterEndpointTypeService) ListClusterEndpointTypes(ctx context.Context, opts services.ListOptions) (*services.ListResult[openchoreov1alpha1.ClusterEndpointType], error) {
	s.logger.Debug("Listing cluster endpoint types", "limit", opts.Limit, "cursor", opts.Cursor)

	listOpts, err := services.BuildListOptions(opts)
	if err != nil {
		return nil, err
	}

	var cetList openchoreov1alpha1.ClusterEndpointTypeList
	if err := s.k8sClient.List(ctx, &cetList, listOpts...); err != nil {
		s.logger.Error("Failed to list cluster endpoint types", "error", err)
		return nil, fmt.Errorf("failed to list cluster endpoint types: %w", err)
	}

	for i := range cetList.Items {
		cetList.Items[i].TypeMeta = clusterEndpointTypeTypeMeta
	}

	result := &services.ListResult[openchoreov1alpha1.ClusterEndpointType]{
		Items:      cetList.Items,
		NextCursor: cetList.Continue,
	}
	if cetList.RemainingItemCount != nil {
		remaining := *cetList.RemainingItemCount
		result.RemainingCount = &remaining
	}

	s.logger.Debug("Listed cluster endpoint types", "count", len(cetList.Items))
	return result, nil
}

func (s *clusterEndpointTypeService) GetClusterEndpointType(ctx context.Context, cetName string) (*openchoreov1alpha1.ClusterEndpointType, error) {
	s.logger.Debug("Getting cluster endpoint type", "clusterEndpointType", cetName)

	cet := &openchoreov1alpha1.ClusterEndpointType{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Name: cetName}, cet); err != nil {
		if client.IgnoreNotFound(err) == nil {
			s.logger.Warn("Cluster endpoint type not found", "clusterEndpointType", cetName)
			return nil, ErrClusterEndpointTypeNotFound
		}
		s.logger.Error("Failed to get cluster endpoint type", "error", err)
		return nil, fmt.Errorf("failed to get cluster endpoint type: %w", err)
	}

	cet.TypeMeta = clusterEndpointTypeTypeMeta
	return cet, nil
}
