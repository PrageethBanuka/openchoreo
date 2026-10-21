// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"context"
	"fmt"
	"log/slog"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
	"github.com/openchoreo/openchoreo/internal/labels"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	projectsvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/project"
)

// createdFromPortal is the created-from annotation value for Endpoints a
// person creates through the API (portal, occ, MCP).
const createdFromPortal = "portal"

var endpointTypeMeta = metav1.TypeMeta{
	APIVersion: openchoreov1alpha1.GroupVersion.String(),
	Kind:       "Endpoint",
}

// endpointService handles endpoint-related business logic without authorization checks.
// Other services within this layer should use this directly to avoid double authz.
type endpointService struct {
	k8sClient      client.Client
	projectService projectsvc.Service
	logger         *slog.Logger
}

var _ Service = (*endpointService)(nil)

// NewService creates a new endpoint service without authorization.
// It internally creates an unwrapped project service for project validation,
// avoiding double authz when used within the authz-wrapped endpoint service.
func NewService(k8sClient client.Client, logger *slog.Logger) Service {
	return &endpointService{
		k8sClient:      k8sClient,
		projectService: projectsvc.NewService(k8sClient, logger.With("component", "project-service-internal")),
		logger:         logger,
	}
}

func (s *endpointService) CreateEndpoint(ctx context.Context, namespaceName string, ep *openchoreov1alpha1.Endpoint) (*openchoreov1alpha1.Endpoint, error) {
	if ep == nil {
		return nil, fmt.Errorf("endpoint cannot be nil")
	}

	s.logger.Debug("Creating endpoint", "namespace", namespaceName, "endpoint", ep.Name)

	// Validate that the referenced project exists
	if _, err := s.projectService.GetProject(ctx, namespaceName, ep.Spec.Owner.ProjectName); err != nil {
		return nil, err
	}

	exists, err := s.endpointExists(ctx, namespaceName, ep.Name)
	if err != nil {
		s.logger.Error("Failed to check endpoint existence", "error", err)
		return nil, fmt.Errorf("failed to check endpoint existence: %w", err)
	}
	if exists {
		s.logger.Warn("Endpoint already exists", "namespace", namespaceName, "endpoint", ep.Name)
		return nil, ErrEndpointAlreadyExists
	}

	// Set defaults
	ep.Status = openchoreov1alpha1.EndpointStatus{}
	ep.Namespace = namespaceName
	if ep.Labels == nil {
		ep.Labels = make(map[string]string)
	}
	ep.Labels[labels.LabelKeyProjectName] = ep.Spec.Owner.ProjectName
	if ep.Annotations == nil {
		ep.Annotations = make(map[string]string)
	}
	if ep.Annotations[labels.AnnotationKeyEndpointCreatedFrom] == "" {
		ep.Annotations[labels.AnnotationKeyEndpointCreatedFrom] = createdFromPortal
	}

	if err := s.k8sClient.Create(ctx, ep); err != nil {
		if apierrors.IsAlreadyExists(err) {
			s.logger.Warn("Endpoint already exists", "namespace", namespaceName, "endpoint", ep.Name)
			return nil, ErrEndpointAlreadyExists
		}
		if apierrors.IsInvalid(err) {
			return nil, &services.ValidationError{Msg: services.ExtractValidationMessage(err)}
		}
		s.logger.Error("Failed to create endpoint CR", "error", err)
		return nil, fmt.Errorf("failed to create endpoint: %w", err)
	}

	s.logger.Debug("Endpoint created successfully", "namespace", namespaceName, "endpoint", ep.Name)
	ep.TypeMeta = endpointTypeMeta
	return ep, nil
}

func (s *endpointService) UpdateEndpoint(ctx context.Context, namespaceName string, ep *openchoreov1alpha1.Endpoint) (*openchoreov1alpha1.Endpoint, error) {
	if ep == nil {
		return nil, fmt.Errorf("endpoint cannot be nil")
	}

	s.logger.Debug("Updating endpoint", "namespace", namespaceName, "endpoint", ep.Name)

	existing := &openchoreov1alpha1.Endpoint{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Name: ep.Name, Namespace: namespaceName}, existing); err != nil {
		if client.IgnoreNotFound(err) == nil {
			s.logger.Warn("Endpoint not found", "namespace", namespaceName, "endpoint", ep.Name)
			return nil, ErrEndpointNotFound
		}
		s.logger.Error("Failed to get endpoint", "error", err)
		return nil, fmt.Errorf("failed to get endpoint: %w", err)
	}

	// Clear status from user input — status is server-managed
	ep.Status = openchoreov1alpha1.EndpointStatus{}

	// spec.owner and spec.type are immutable per CRD CEL, but check explicitly
	// so we return a friendly validation error instead of an admission rejection.
	if ep.Spec.Owner.ProjectName != existing.Spec.Owner.ProjectName {
		return nil, &services.ValidationError{Msg: "spec.owner.projectName is immutable"}
	}
	// An update that leaves the address out keeps it; changing it is refused,
	// since the address is the Endpoint's identity.
	if ep.Spec.Address == (openchoreov1alpha1.EndpointAddress{}) {
		ep.Spec.Address = existing.Spec.Address
	}
	if ep.Spec.Address != existing.Spec.Address {
		return nil, &services.ValidationError{Msg: "spec.address is immutable; create a new Endpoint to serve another address"}
	}

	// An update that leaves reclaimPolicy out keeps the current one rather than
	// falling back to the CRD default.
	if ep.Spec.ReclaimPolicy == "" {
		ep.Spec.ReclaimPolicy = existing.Spec.ReclaimPolicy
	}

	// Only apply user-mutable fields to the existing object, preserving server-managed fields
	createdFrom := existing.Annotations[labels.AnnotationKeyEndpointCreatedFrom]
	existing.Spec = ep.Spec
	existing.Labels = ep.Labels
	existing.Annotations = ep.Annotations
	if createdFrom != "" {
		if existing.Annotations == nil {
			existing.Annotations = make(map[string]string)
		}
		existing.Annotations[labels.AnnotationKeyEndpointCreatedFrom] = createdFrom
	}

	// Preserve special labels
	if existing.Labels == nil {
		existing.Labels = make(map[string]string)
	}
	existing.Labels[labels.LabelKeyProjectName] = existing.Spec.Owner.ProjectName

	if err := s.k8sClient.Update(ctx, existing); err != nil {
		if apierrors.IsInvalid(err) {
			return nil, &services.ValidationError{Msg: services.ExtractValidationMessage(err)}
		}
		s.logger.Error("Failed to update endpoint CR", "error", err)
		return nil, fmt.Errorf("failed to update endpoint: %w", err)
	}

	s.logger.Debug("Endpoint updated successfully", "namespace", namespaceName, "endpoint", ep.Name)
	existing.TypeMeta = endpointTypeMeta
	return existing, nil
}

func (s *endpointService) ListEndpoints(ctx context.Context, namespaceName, projectName, componentName string, opts services.ListOptions) (*services.ListResult[openchoreov1alpha1.Endpoint], error) {
	s.logger.Debug("Listing endpoints", "namespace", namespaceName, "project", projectName, "component", componentName, "limit", opts.Limit, "cursor", opts.Cursor)

	// Validate that the referenced project exists when filtering by project
	if projectName != "" {
		if _, err := s.projectService.GetProject(ctx, namespaceName, projectName); err != nil {
			return nil, err
		}
	}

	// PreFilteredList handles over-fetching and cursor tracking so pagination
	// remains correct.
	var filters []services.ItemFilter[openchoreov1alpha1.Endpoint]
	if projectName != "" {
		filters = append(filters, func(ep openchoreov1alpha1.Endpoint) bool {
			return ep.Spec.Owner.ProjectName == projectName
		})
	}
	if componentName != "" {
		filters = append(filters, func(ep openchoreov1alpha1.Endpoint) bool {
			return routesTo(ep, componentName)
		})
	}

	return services.PreFilteredList(s.listEndpointsResource(namespaceName), filters...)(ctx, opts)
}

// routesTo reports whether the Endpoint has a backendRef to the component or
// takes over one of its URLs.
func routesTo(ep openchoreov1alpha1.Endpoint, componentName string) bool {
	if t := controller.TakenOver(&ep); t != nil && t.Name == componentName {
		return true
	}
	for _, ref := range ep.Spec.BackendRefs {
		if name, _, ok := controller.BackendComponent(ref); ok && name == componentName {
			return true
		}
	}
	return false
}

// listEndpointsResource returns a ListResource that fetches endpoints from K8s for the given namespace.
func (s *endpointService) listEndpointsResource(namespaceName string) services.ListResource[openchoreov1alpha1.Endpoint] {
	return func(ctx context.Context, opts services.ListOptions) (*services.ListResult[openchoreov1alpha1.Endpoint], error) {
		commonOpts, err := services.BuildListOptions(opts)
		if err != nil {
			return nil, err
		}
		listOpts := append([]client.ListOption{client.InNamespace(namespaceName)}, commonOpts...)

		var epList openchoreov1alpha1.EndpointList
		if err := s.k8sClient.List(ctx, &epList, listOpts...); err != nil {
			s.logger.Error("Failed to list endpoints", "error", err)
			return nil, fmt.Errorf("failed to list endpoints: %w", err)
		}

		for i := range epList.Items {
			epList.Items[i].TypeMeta = endpointTypeMeta
		}

		result := &services.ListResult[openchoreov1alpha1.Endpoint]{
			Items:      epList.Items,
			NextCursor: epList.Continue,
		}
		if epList.RemainingItemCount != nil {
			remaining := *epList.RemainingItemCount
			result.RemainingCount = &remaining
		}

		return result, nil
	}
}

func (s *endpointService) GetEndpoint(ctx context.Context, namespaceName, endpointName string) (*openchoreov1alpha1.Endpoint, error) {
	s.logger.Debug("Getting endpoint", "namespace", namespaceName, "endpoint", endpointName)

	ep := &openchoreov1alpha1.Endpoint{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Name: endpointName, Namespace: namespaceName}, ep); err != nil {
		if client.IgnoreNotFound(err) == nil {
			s.logger.Warn("Endpoint not found", "namespace", namespaceName, "endpoint", endpointName)
			return nil, ErrEndpointNotFound
		}
		s.logger.Error("Failed to get endpoint", "error", err)
		return nil, fmt.Errorf("failed to get endpoint: %w", err)
	}

	ep.TypeMeta = endpointTypeMeta
	return ep, nil
}

func (s *endpointService) DeleteEndpoint(ctx context.Context, namespaceName, endpointName string) error {
	s.logger.Debug("Deleting endpoint", "namespace", namespaceName, "endpoint", endpointName)

	ep := &openchoreov1alpha1.Endpoint{}
	ep.Name = endpointName
	ep.Namespace = namespaceName

	if err := s.k8sClient.Delete(ctx, ep); err != nil {
		if apierrors.IsNotFound(err) {
			return ErrEndpointNotFound
		}
		s.logger.Error("Failed to delete endpoint CR", "error", err)
		return fmt.Errorf("failed to delete endpoint: %w", err)
	}

	s.logger.Debug("Endpoint deleted successfully", "namespace", namespaceName, "endpoint", endpointName)
	return nil
}

func (s *endpointService) endpointExists(ctx context.Context, namespaceName, endpointName string) (bool, error) {
	ep := &openchoreov1alpha1.Endpoint{}
	err := s.k8sClient.Get(ctx, client.ObjectKey{Name: endpointName, Namespace: namespaceName}, ep)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			return false, nil
		}
		return false, fmt.Errorf("checking existence of endpoint %s/%s: %w", namespaceName, endpointName, err)
	}
	return true, nil
}
