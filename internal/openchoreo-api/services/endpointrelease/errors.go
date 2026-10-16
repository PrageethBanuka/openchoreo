// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointrelease

import "errors"

var (
	ErrEndpointReleaseNotFound = errors.New("endpoint release not found")
	ErrEndpointReleaseInUse    = errors.New("endpoint release is in use")
)

// InUseError refuses deleting a release that an environment pins or that is
// the Endpoint's latest release. It matches ErrEndpointReleaseInUse.
type InUseError struct {
	Message string
}

func (e *InUseError) Error() string { return e.Message }

// Is lets errors.Is match ErrEndpointReleaseInUse.
func (e *InUseError) Is(target error) bool { return target == ErrEndpointReleaseInUse }
