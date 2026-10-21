// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"k8s.io/apimachinery/pkg/runtime"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/pkg/hash"
)

// ReleaseSpec captures the immutable inputs that uniquely identify an
// EndpointRelease. Owner is intentionally excluded so the hash represents the
// configuration, not the ownership pointer (mirrors resource.ReleaseSpec).
type ReleaseSpec struct {
	EndpointType openchoreov1alpha1.EndpointReleaseEndpointType `json:"endpointType"`
	Address      *openchoreov1alpha1.EndpointAddress            `json:"address,omitempty"`
	TakeOver     *openchoreov1alpha1.EndpointTakeOver           `json:"takeOver,omitempty"`
	BackendRefs  []openchoreov1alpha1.EndpointBackendRef        `json:"backendRefs"`
	Parameters   *runtime.RawExtension                          `json:"parameters,omitempty"`
}

// computeReleaseHash returns a deterministic hash for a ReleaseSpec.
func computeReleaseHash(spec ReleaseSpec) string {
	return hash.ComputeHash(spec, nil)
}
