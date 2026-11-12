// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointpipeline

import (
	"fmt"
	"slices"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

// CapabilityProblems reports the routing features the backends use that their
// Endpoint's type does not declare. A type that declares no capabilities is
// not checked.
func CapabilityProblems(declared []openchoreov1alpha1.EndpointCapability, backendRefs []openchoreov1alpha1.EndpointBackendRef) []string {
	if len(declared) == 0 {
		return nil
	}
	var problems []string
	need := func(c openchoreov1alpha1.EndpointCapability, ref openchoreov1alpha1.EndpointBackendRef, what string) {
		if !slices.Contains(declared, c) {
			problems = append(problems, fmt.Sprintf("backend %q: %s needs the %s capability", ref.Name, what, c))
		}
	}
	for _, ref := range backendRefs {
		if ref.Match != nil {
			need(openchoreov1alpha1.EndpointCapabilityMatch, ref, "a match")
			if slices.ContainsFunc(ref.Match.Headers, func(h openchoreov1alpha1.EndpointHeaderMatch) bool {
				return h.Type == openchoreov1alpha1.EndpointHeaderMatchRegularExpression
			}) {
				need(openchoreov1alpha1.EndpointCapabilityRegularExpression, ref, "a regular expression header match")
			}
		}
		if ref.Mirror != nil {
			need(openchoreov1alpha1.EndpointCapabilityMirror, ref, "a mirror")
			if ref.Mirror.Percent < 100 {
				need(openchoreov1alpha1.EndpointCapabilityMirrorPercent, ref, "mirroring a share of requests")
			}
		}
	}
	return problems
}
