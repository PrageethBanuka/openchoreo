// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpoint

import (
	"testing"

	"k8s.io/utils/ptr"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

func TestPruneOverrides(t *testing.T) {
	overrides := []openchoreov1alpha1.EndpointBackendOverride{
		{Name: "checkout-blue", Weight: ptr.To[int32](0)},
		{Name: "checkout-green", Weight: ptr.To[int32](100)},
	}
	refs := []openchoreov1alpha1.EndpointBackendRef{{Name: "checkout-green"}}

	got := pruneOverrides(overrides, refs)
	if len(got) != 1 || got[0].Name != "checkout-green" {
		t.Fatalf("pruneOverrides kept %v, want only checkout-green", got)
	}
	if pruneOverrides(nil, refs) != nil {
		t.Fatal("pruneOverrides(nil) should stay nil")
	}
}
