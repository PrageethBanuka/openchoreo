// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"

	"github.com/openchoreo/openchoreo/internal/openchoreo-api/api/gen"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/models"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	k8sresourcessvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/k8sresources"
)

// GetEndpointReleaseBindingK8sResourceTree returns the live Kubernetes resources
// deployed by the RenderedReleases owned by an endpoint release binding.
func (h *Handler) GetEndpointReleaseBindingK8sResourceTree(
	ctx context.Context,
	request gen.GetEndpointReleaseBindingK8sResourceTreeRequestObject,
) (gen.GetEndpointReleaseBindingK8sResourceTreeResponseObject, error) {
	h.logger.Debug("GetEndpointReleaseBindingK8sResourceTree called",
		"namespace", request.NamespaceName,
		"endpointReleaseBinding", request.EndpointReleaseBindingName)

	result, err := h.services.K8sResourcesService.GetEndpointResourceTree(ctx, request.NamespaceName, request.EndpointReleaseBindingName)
	if err != nil {
		return h.handleEndpointK8sResourceTreeError(err)
	}

	genReleases, err := h.convertResourceTree(result)
	if err != nil {
		return gen.GetEndpointReleaseBindingK8sResourceTree500JSONResponse{InternalErrorJSONResponse: internalError()}, nil
	}

	return gen.GetEndpointReleaseBindingK8sResourceTree200JSONResponse{
		RenderedReleases: genReleases,
	}, nil
}

// GetEndpointReleaseBindingK8sResourceEvents returns Kubernetes events for a
// resource in an endpoint release binding's resource tree.
func (h *Handler) GetEndpointReleaseBindingK8sResourceEvents(
	ctx context.Context,
	request gen.GetEndpointReleaseBindingK8sResourceEventsRequestObject,
) (gen.GetEndpointReleaseBindingK8sResourceEventsResponseObject, error) {
	h.logger.Debug("GetEndpointReleaseBindingK8sResourceEvents called",
		"namespace", request.NamespaceName,
		"endpointReleaseBinding", request.EndpointReleaseBindingName,
		"kind", request.Params.Kind,
		"name", request.Params.Name)

	group := ""
	if request.Params.Group != nil {
		group = *request.Params.Group
	}

	resp, err := h.services.K8sResourcesService.GetEndpointResourceEvents(
		ctx,
		request.NamespaceName,
		request.EndpointReleaseBindingName,
		group,
		request.Params.Version,
		request.Params.Kind,
		request.Params.Name,
	)
	if err != nil {
		return h.handleEndpointK8sResourceEventsError(err)
	}

	result, err := convert[models.ResourceEventsResponse, gen.ResourceEventsResponse](*resp)
	if err != nil {
		h.logger.Error("Failed to convert resource events response", "error", err)
		return gen.GetEndpointReleaseBindingK8sResourceEvents500JSONResponse{InternalErrorJSONResponse: internalError()}, nil
	}

	return gen.GetEndpointReleaseBindingK8sResourceEvents200JSONResponse(result), nil
}

func (h *Handler) handleEndpointK8sResourceTreeError(err error) (gen.GetEndpointReleaseBindingK8sResourceTreeResponseObject, error) {
	switch {
	case errors.Is(err, services.ErrForbidden):
		return gen.GetEndpointReleaseBindingK8sResourceTree403JSONResponse{ForbiddenJSONResponse: forbidden()}, nil
	case errors.Is(err, k8sresourcessvc.ErrEndpointReleaseBindingNotFound):
		return gen.GetEndpointReleaseBindingK8sResourceTree404JSONResponse{NotFoundJSONResponse: notFound("EndpointReleaseBinding")}, nil
	case errors.Is(err, k8sresourcessvc.ErrRenderedReleaseNotFound):
		return gen.GetEndpointReleaseBindingK8sResourceTree404JSONResponse{NotFoundJSONResponse: notFound("RenderedRelease")}, nil
	case errors.Is(err, k8sresourcessvc.ErrEnvironmentNotFound):
		return gen.GetEndpointReleaseBindingK8sResourceTree404JSONResponse{NotFoundJSONResponse: notFound("Environment")}, nil
	}
	h.logger.Error("Failed to get endpoint k8s resource tree", "error", err)
	return gen.GetEndpointReleaseBindingK8sResourceTree500JSONResponse{InternalErrorJSONResponse: internalError()}, nil
}

func (h *Handler) handleEndpointK8sResourceEventsError(err error) (gen.GetEndpointReleaseBindingK8sResourceEventsResponseObject, error) {
	switch {
	case errors.Is(err, services.ErrForbidden):
		return gen.GetEndpointReleaseBindingK8sResourceEvents403JSONResponse{ForbiddenJSONResponse: forbidden()}, nil
	case errors.Is(err, k8sresourcessvc.ErrEndpointReleaseBindingNotFound):
		return gen.GetEndpointReleaseBindingK8sResourceEvents404JSONResponse{NotFoundJSONResponse: notFound("EndpointReleaseBinding")}, nil
	case errors.Is(err, k8sresourcessvc.ErrRenderedReleaseNotFound):
		return gen.GetEndpointReleaseBindingK8sResourceEvents404JSONResponse{NotFoundJSONResponse: notFound("RenderedRelease")}, nil
	case errors.Is(err, k8sresourcessvc.ErrEnvironmentNotFound):
		return gen.GetEndpointReleaseBindingK8sResourceEvents404JSONResponse{NotFoundJSONResponse: notFound("Environment")}, nil
	case errors.Is(err, k8sresourcessvc.ErrResourceNotFound):
		return gen.GetEndpointReleaseBindingK8sResourceEvents404JSONResponse{NotFoundJSONResponse: notFound("Resource")}, nil
	case errors.Is(err, k8sresourcessvc.ErrResourceTreeIncomplete), errors.Is(err, k8sresourcessvc.ErrResourceMatchAmbiguous):
		// Not "the resource does not exist": an outage or a collision must stay
		// distinguishable from genuine non-membership.
		h.logger.Warn("Cannot resolve resource for endpoint events", "error", err)
		return gen.GetEndpointReleaseBindingK8sResourceEvents500JSONResponse{InternalErrorJSONResponse: internalError()}, nil
	}
	h.logger.Error("Failed to get endpoint k8s resource events", "error", err)
	return gen.GetEndpointReleaseBindingK8sResourceEvents500JSONResponse{InternalErrorJSONResponse: internalError()}, nil
}
