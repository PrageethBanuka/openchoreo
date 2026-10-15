// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package component

import "errors"

var (
	ErrComponentNotFound        = errors.New("component not found")
	ErrComponentAlreadyExists   = errors.New("component already exists")
	ErrComponentReleaseNotFound = errors.New("component release not found")
	ErrWorkloadNotFound         = errors.New("workload not found")
	ErrTraitNotFound            = errors.New("trait not found")
	ErrValidation               = errors.New("validation error")
	ErrComponentTypeNotFound    = errors.New("component type not found")
	ErrComponentStillRouted     = errors.New("component still receives traffic through an Endpoint")
)

// StillRoutedError refuses deleting a component that an Endpoint still sends
// traffic to in some environment. It matches ErrComponentStillRouted.
type StillRoutedError struct {
	Message string
}

func (e *StillRoutedError) Error() string { return e.Message }

// Is lets errors.Is match ErrComponentStillRouted.
func (e *StillRoutedError) Is(target error) bool { return target == ErrComponentStillRouted }
