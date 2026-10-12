// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/api/gen"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services"
	clusterendpointtypesvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/clusterendpointtype"
	endpointtypesvc "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/endpointtype"
)

// ListClusterEndpointTypes returns a paginated list of cluster-scoped endpoint types.
func (h *Handler) ListClusterEndpointTypes(
	ctx context.Context,
	request gen.ListClusterEndpointTypesRequestObject,
) (gen.ListClusterEndpointTypesResponseObject, error) {
	h.logger.Debug("ListClusterEndpointTypes called")

	opts := NormalizeListOptions(request.Params.Limit, request.Params.Cursor, request.Params.LabelSelector)

	result, err := h.services.ClusterEndpointTypeService.ListClusterEndpointTypes(ctx, opts)
	if err != nil {
		if errors.Is(err, services.ErrForbidden) {
			return gen.ListClusterEndpointTypes403JSONResponse{ForbiddenJSONResponse: forbidden()}, nil
		}
		var validationErr *services.ValidationError
		if errors.As(err, &validationErr) {
			return gen.ListClusterEndpointTypes400JSONResponse{BadRequestJSONResponse: badRequest(validationErr.Msg)}, nil
		}
		h.logger.Error("Failed to list cluster endpoint types", "error", err)
		return gen.ListClusterEndpointTypes500JSONResponse{InternalErrorJSONResponse: internalError()}, nil
	}

	items, err := convertList[openchoreov1alpha1.ClusterEndpointType, gen.ClusterEndpointType](result.Items)
	if err != nil {
		h.logger.Error("Failed to convert cluster endpoint types", "error", err)
		return gen.ListClusterEndpointTypes500JSONResponse{InternalErrorJSONResponse: internalError()}, nil
	}

	return gen.ListClusterEndpointTypes200JSONResponse{
		Items:      items,
		Pagination: ToPagination(result),
	}, nil
}

// GetClusterEndpointType returns details of a specific cluster-scoped endpoint type.
func (h *Handler) GetClusterEndpointType(
	ctx context.Context,
	request gen.GetClusterEndpointTypeRequestObject,
) (gen.GetClusterEndpointTypeResponseObject, error) {
	h.logger.Debug("GetClusterEndpointType called", "cetName", request.CetName)

	cet, err := h.services.ClusterEndpointTypeService.GetClusterEndpointType(ctx, request.CetName)
	if err != nil {
		if errors.Is(err, services.ErrForbidden) {
			return gen.GetClusterEndpointType403JSONResponse{ForbiddenJSONResponse: forbidden()}, nil
		}
		if errors.Is(err, clusterendpointtypesvc.ErrClusterEndpointTypeNotFound) {
			return gen.GetClusterEndpointType404JSONResponse{NotFoundJSONResponse: notFound("ClusterEndpointType")}, nil
		}
		h.logger.Error("Failed to get cluster endpoint type", "error", err)
		return gen.GetClusterEndpointType500JSONResponse{InternalErrorJSONResponse: internalError()}, nil
	}

	genCET, err := convert[openchoreov1alpha1.ClusterEndpointType, gen.ClusterEndpointType](*cet)
	if err != nil {
		h.logger.Error("Failed to convert cluster endpoint type", "error", err)
		return gen.GetClusterEndpointType500JSONResponse{InternalErrorJSONResponse: internalError()}, nil
	}

	return gen.GetClusterEndpointType200JSONResponse(genCET), nil
}

// ListEndpointTypes returns a paginated list of endpoint types within a namespace.
func (h *Handler) ListEndpointTypes(
	ctx context.Context,
	request gen.ListEndpointTypesRequestObject,
) (gen.ListEndpointTypesResponseObject, error) {
	h.logger.Debug("ListEndpointTypes called", "namespaceName", request.NamespaceName)

	opts := NormalizeListOptions(request.Params.Limit, request.Params.Cursor, request.Params.LabelSelector)

	result, err := h.services.EndpointTypeService.ListEndpointTypes(ctx, request.NamespaceName, opts)
	if err != nil {
		if errors.Is(err, services.ErrForbidden) {
			return gen.ListEndpointTypes403JSONResponse{ForbiddenJSONResponse: forbidden()}, nil
		}
		var validationErr *services.ValidationError
		if errors.As(err, &validationErr) {
			return gen.ListEndpointTypes400JSONResponse{BadRequestJSONResponse: badRequest(validationErr.Msg)}, nil
		}
		h.logger.Error("Failed to list endpoint types", "error", err)
		return gen.ListEndpointTypes500JSONResponse{InternalErrorJSONResponse: internalError()}, nil
	}

	items, err := convertList[openchoreov1alpha1.EndpointType, gen.EndpointType](result.Items)
	if err != nil {
		h.logger.Error("Failed to convert endpoint types", "error", err)
		return gen.ListEndpointTypes500JSONResponse{InternalErrorJSONResponse: internalError()}, nil
	}

	return gen.ListEndpointTypes200JSONResponse{
		Items:      items,
		Pagination: ToPagination(result),
	}, nil
}

// GetEndpointType returns details of a specific endpoint type.
func (h *Handler) GetEndpointType(
	ctx context.Context,
	request gen.GetEndpointTypeRequestObject,
) (gen.GetEndpointTypeResponseObject, error) {
	h.logger.Debug("GetEndpointType called", "namespaceName", request.NamespaceName, "etName", request.EtName)

	et, err := h.services.EndpointTypeService.GetEndpointType(ctx, request.NamespaceName, request.EtName)
	if err != nil {
		if errors.Is(err, services.ErrForbidden) {
			return gen.GetEndpointType403JSONResponse{ForbiddenJSONResponse: forbidden()}, nil
		}
		if errors.Is(err, endpointtypesvc.ErrEndpointTypeNotFound) {
			return gen.GetEndpointType404JSONResponse{NotFoundJSONResponse: notFound("EndpointType")}, nil
		}
		h.logger.Error("Failed to get endpoint type", "error", err)
		return gen.GetEndpointType500JSONResponse{InternalErrorJSONResponse: internalError()}, nil
	}

	genET, err := convert[openchoreov1alpha1.EndpointType, gen.EndpointType](*et)
	if err != nil {
		h.logger.Error("Failed to convert endpoint type", "error", err)
		return gen.GetEndpointType500JSONResponse{InternalErrorJSONResponse: internalError()}, nil
	}

	return gen.GetEndpointType200JSONResponse(genET), nil
}
