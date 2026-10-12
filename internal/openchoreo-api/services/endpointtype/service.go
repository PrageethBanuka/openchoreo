// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointtype

import (
	"context"
	"fmt"
	"log/slog"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
)

// endpointTypeService handles endpoint type business logic without authorization checks.
type endpointTypeService struct {
	k8sClient client.Client
	logger    *slog.Logger
}

var endpointTypeTypeMeta = metav1.TypeMeta{
	APIVersion: openchoreov1alpha1.GroupVersion.String(),
	Kind:       "EndpointType",
}

var _ Service = (*endpointTypeService)(nil)

// NewService creates a new endpoint type service without authorization.
func NewService(k8sClient client.Client, logger *slog.Logger) Service {
	return &endpointTypeService{
		k8sClient: k8sClient,
		logger:    logger,
	}
}

func (s *endpointTypeService) ListEndpointTypes(ctx context.Context, namespaceName string, opts services.ListOptions) (*services.ListResult[openchoreov1alpha1.EndpointType], error) {
	s.logger.Debug("Listing endpoint types", "namespace", namespaceName, "limit", opts.Limit, "cursor", opts.Cursor)

	commonOpts, err := services.BuildListOptions(opts)
	if err != nil {
		return nil, err
	}
	listOpts := append([]client.ListOption{client.InNamespace(namespaceName)}, commonOpts...)

	var etList openchoreov1alpha1.EndpointTypeList
	if err := s.k8sClient.List(ctx, &etList, listOpts...); err != nil {
		s.logger.Error("Failed to list endpoint types", "error", err)
		return nil, fmt.Errorf("failed to list endpoint types: %w", err)
	}

	for i := range etList.Items {
		etList.Items[i].TypeMeta = endpointTypeTypeMeta
	}

	result := &services.ListResult[openchoreov1alpha1.EndpointType]{
		Items:      etList.Items,
		NextCursor: etList.Continue,
	}
	if etList.RemainingItemCount != nil {
		remaining := *etList.RemainingItemCount
		result.RemainingCount = &remaining
	}

	s.logger.Debug("Listed endpoint types", "count", len(etList.Items))
	return result, nil
}

func (s *endpointTypeService) GetEndpointType(ctx context.Context, namespaceName, etName string) (*openchoreov1alpha1.EndpointType, error) {
	s.logger.Debug("Getting endpoint type", "namespace", namespaceName, "endpointType", etName)

	cet := &openchoreov1alpha1.EndpointType{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Name: etName, Namespace: namespaceName}, cet); err != nil {
		if client.IgnoreNotFound(err) == nil {
			s.logger.Warn("Endpoint type not found", "namespace", namespaceName, "endpointType", etName)
			return nil, ErrEndpointTypeNotFound
		}
		s.logger.Error("Failed to get endpoint type", "error", err)
		return nil, fmt.Errorf("failed to get endpoint type: %w", err)
	}

	cet.TypeMeta = endpointTypeTypeMeta
	return cet, nil
}
