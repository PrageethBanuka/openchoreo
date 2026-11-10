// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointpipeline

import (
	"testing"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

func TestCapabilityProblems(t *testing.T) {
	backend := func(name string) openchoreov1alpha1.EndpointBackendRef {
		return openchoreov1alpha1.EndpointBackendRef{Name: name, Kind: openchoreov1alpha1.EndpointBackendKindComponent}
	}
	regex := backend("canary")
	regex.Match = &openchoreov1alpha1.EndpointMatch{Headers: []openchoreov1alpha1.EndpointHeaderMatch{{
		Name: "user-agent", Type: openchoreov1alpha1.EndpointHeaderMatchRegularExpression, Value: ".*Mobi.*",
	}}}
	shadow := backend("shadow")
	shadow.Mirror = &openchoreov1alpha1.EndpointMirror{Percent: 10}
	refs := []openchoreov1alpha1.EndpointBackendRef{backend("stable"), regex, shadow}

	if got := CapabilityProblems(nil, refs); got != nil {
		t.Fatalf("a type without capabilities is not checked, got %v", got)
	}
	all := []openchoreov1alpha1.EndpointCapability{
		openchoreov1alpha1.EndpointCapabilityMatch, openchoreov1alpha1.EndpointCapabilityRegularExpression,
		openchoreov1alpha1.EndpointCapabilityMirror, openchoreov1alpha1.EndpointCapabilityMirrorPercent,
	}
	if got := CapabilityProblems(all, refs); got != nil {
		t.Fatalf("every feature is declared, got %v", got)
	}
	got := CapabilityProblems([]openchoreov1alpha1.EndpointCapability{openchoreov1alpha1.EndpointCapabilityMatch}, refs)
	if len(got) != 2 {
		t.Fatalf("want problems for the regex and the mirror, not its share, got %v", got)
	}
	got = CapabilityProblems([]openchoreov1alpha1.EndpointCapability{openchoreov1alpha1.EndpointCapabilityMirror}, refs)
	if len(got) != 2 {
		t.Fatalf("want problems for the match and the mirror's share, not the regex, got %v", got)
	}

	shadow.Mirror.Percent = 100
	whole := []openchoreov1alpha1.EndpointBackendRef{backend("stable"), shadow}
	if got := CapabilityProblems([]openchoreov1alpha1.EndpointCapability{openchoreov1alpha1.EndpointCapabilityMirror}, whole); got != nil {
		t.Fatalf("mirroring every request needs only Mirror, got %v", got)
	}
}
