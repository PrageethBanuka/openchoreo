// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointrelease

import (
	"context"
	"fmt"
	"log/slog"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
)

// endpointReleaseService handles endpoint release business logic without authorization checks.
type endpointReleaseService struct {
	k8sClient client.Client
	logger    *slog.Logger
}

var endpointReleaseTypeMeta = metav1.TypeMeta{
	APIVersion: openchoreov1alpha1.GroupVersion.String(),
	Kind:       "EndpointRelease",
}

var _ Service = (*endpointReleaseService)(nil)

// NewService creates a new endpoint release service without authorization.
func NewService(k8sClient client.Client, logger *slog.Logger) Service {
	return &endpointReleaseService{
		k8sClient: k8sClient,
		logger:    logger,
	}
}

func (s *endpointReleaseService) ListEndpointReleases(ctx context.Context, namespaceName, endpointName string, opts services.ListOptions) (*services.ListResult[openchoreov1alpha1.EndpointRelease], error) {
	s.logger.Debug("Listing endpoint releases", "namespace", namespaceName, "endpoint", endpointName, "limit", opts.Limit, "cursor", opts.Cursor)

	listFn := func(ctx context.Context, pageOpts services.ListOptions) (*services.ListResult[openchoreov1alpha1.EndpointRelease], error) {
		commonOpts, err := services.BuildListOptions(pageOpts)
		if err != nil {
			return nil, err
		}
		listOpts := append([]client.ListOption{client.InNamespace(namespaceName)}, commonOpts...)

		var erList openchoreov1alpha1.EndpointReleaseList
		if err := s.k8sClient.List(ctx, &erList, listOpts...); err != nil {
			s.logger.Error("Failed to list endpoint releases", "error", err)
			return nil, fmt.Errorf("failed to list endpoint releases: %w", err)
		}

		for i := range erList.Items {
			erList.Items[i].TypeMeta = endpointReleaseTypeMeta
		}

		result := &services.ListResult[openchoreov1alpha1.EndpointRelease]{
			Items:      erList.Items,
			NextCursor: erList.Continue,
		}
		if erList.RemainingItemCount != nil {
			remaining := *erList.RemainingItemCount
			result.RemainingCount = &remaining
		}
		return result, nil
	}

	if endpointName != "" {
		return services.PreFilteredList(listFn, func(er openchoreov1alpha1.EndpointRelease) bool {
			return er.Spec.Owner.EndpointName == endpointName
		})(ctx, opts)
	}

	return listFn(ctx, opts)
}

func (s *endpointReleaseService) GetEndpointRelease(ctx context.Context, namespaceName, endpointReleaseName string) (*openchoreov1alpha1.EndpointRelease, error) {
	s.logger.Debug("Getting endpoint release", "namespace", namespaceName, "endpointRelease", endpointReleaseName)

	er := &openchoreov1alpha1.EndpointRelease{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Name: endpointReleaseName, Namespace: namespaceName}, er); err != nil {
		if client.IgnoreNotFound(err) == nil {
			s.logger.Warn("Endpoint release not found", "namespace", namespaceName, "endpointRelease", endpointReleaseName)
			return nil, ErrEndpointReleaseNotFound
		}
		s.logger.Error("Failed to get endpoint release", "error", err)
		return nil, fmt.Errorf("failed to get endpoint release: %w", err)
	}

	er.TypeMeta = endpointReleaseTypeMeta
	return er, nil
}
