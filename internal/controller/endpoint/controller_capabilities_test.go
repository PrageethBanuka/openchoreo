// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"testing"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

func TestCapabilityProblems(t *testing.T) {
	regex := componentBackend("canary", "greeter-v2", "http", 0)
	regex.Match = &openchoreov1alpha1.EndpointMatch{Headers: []openchoreov1alpha1.EndpointHeaderMatch{{
		Name: "user-agent", Type: openchoreov1alpha1.EndpointHeaderMatchRegularExpression, Value: ".*Mobi.*",
	}}}
	shadow := componentBackend("shadow", "greeter-v3", "http", 0)
	shadow.Mirror = &openchoreov1alpha1.EndpointMirror{Percent: 10}
	ep := newEndpoint("ep", "p", clusterTypeRef("cet"), componentBackend("stable", "greeter", "http", 100), regex, shadow)

	if got := capabilityProblems(nil, ep); got != nil {
		t.Fatalf("a type without capabilities is not checked, got %v", got)
	}
	all := []openchoreov1alpha1.EndpointCapability{
		openchoreov1alpha1.EndpointCapabilityMatch, openchoreov1alpha1.EndpointCapabilityRegularExpression,
		openchoreov1alpha1.EndpointCapabilityMirror, openchoreov1alpha1.EndpointCapabilityMirrorPercent,
	}
	if got := capabilityProblems(all, ep); got != nil {
		t.Fatalf("every feature is declared, got %v", got)
	}
	got := capabilityProblems([]openchoreov1alpha1.EndpointCapability{openchoreov1alpha1.EndpointCapabilityMatch}, ep)
	if len(got) != 3 {
		t.Fatalf("want problems for the regex, the mirror and its share, got %v", got)
	}

	shadow.Mirror.Percent = 100
	whole := newEndpoint("ep", "p", clusterTypeRef("cet"), componentBackend("stable", "greeter", "http", 100), shadow)
	if got := capabilityProblems([]openchoreov1alpha1.EndpointCapability{openchoreov1alpha1.EndpointCapabilityMirror}, whole); got != nil {
		t.Fatalf("mirroring every request needs only Mirror, got %v", got)
	}
}
