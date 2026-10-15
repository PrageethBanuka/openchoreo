// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

// EndpointUsage is one backend of an Endpoint that points at a component, with
// the weight the release pinned in one environment gives it. Environment is
// empty for an Endpoint that is not bound in any environment yet.
// OtherBackends counts the release's backends that are not this component, so
// callers can tell whether the traffic could be moved elsewhere.
type EndpointUsage struct {
	Endpoint         string
	WorkloadEndpoint string
	Environment      string
	Weight           int32
	OtherBackends    int
}

// EndpointBackendWeight returns the effective weight of a backendRef. An unset
// weight defaults to 1, as in the CRD.
func EndpointBackendWeight(ref openchoreov1alpha1.EndpointBackendRef) int32 {
	if ref.Weight == nil {
		return 1
	}
	return *ref.Weight
}

// ComponentEndpointUsage lists every Endpoint in the project that has the
// component as a backend, with its weight in each environment's pinned release.
// It is shared by the Component reverse index, the Component delete guard and
// the openchoreo-api delete guard so they agree. It lists in the namespace and
// filters in memory, so it works with cached and uncached clients alike.
func ComponentEndpointUsage(ctx context.Context, c client.Reader, namespace, project, component string) ([]EndpointUsage, error) {
	var endpoints openchoreov1alpha1.EndpointList
	if err := c.List(ctx, &endpoints, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list endpoints: %w", err)
	}

	routing := map[string][]string{} // endpoint name -> workload endpoints of this component
	for i := range endpoints.Items {
		ep := &endpoints.Items[i]
		if ep.Spec.Owner.ProjectName != project {
			continue
		}
		for _, ref := range ep.Spec.BackendRefs {
			if ref.ComponentName == component {
				routing[ep.Name] = append(routing[ep.Name], ref.Endpoint)
			}
		}
	}
	if len(routing) == 0 {
		return nil, nil
	}

	var bindings openchoreov1alpha1.EndpointReleaseBindingList
	if err := c.List(ctx, &bindings, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list endpoint release bindings: %w", err)
	}

	bound := map[string]bool{}
	var usages []EndpointUsage
	for i := range bindings.Items {
		b := &bindings.Items[i]
		if b.Spec.Owner.ProjectName != project || b.Spec.EndpointRelease == "" {
			continue
		}
		wls, ok := routing[b.Spec.Owner.EndpointName]
		if !ok {
			continue
		}
		var rel openchoreov1alpha1.EndpointRelease
		if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: b.Spec.EndpointRelease}, &rel); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("get endpoint release %s: %w", b.Spec.EndpointRelease, err)
		}
		bound[b.Spec.Owner.EndpointName] = true
		others := 0
		for _, ref := range rel.Spec.BackendRefs {
			if ref.ComponentName != component {
				others++
			}
		}
		for _, wl := range wls {
			var weight int32
			for _, ref := range rel.Spec.BackendRefs {
				if ref.ComponentName == component && ref.Endpoint == wl {
					weight = EndpointBackendWeight(ref)
				}
			}
			usages = append(usages, EndpointUsage{
				Endpoint:         b.Spec.Owner.EndpointName,
				WorkloadEndpoint: wl,
				Environment:      b.Spec.Environment,
				Weight:           weight,
				OtherBackends:    others,
			})
		}
	}

	for name, wls := range routing {
		if bound[name] {
			continue
		}
		for _, wl := range wls {
			usages = append(usages, EndpointUsage{Endpoint: name, WorkloadEndpoint: wl})
		}
	}

	sort.Slice(usages, func(i, j int) bool {
		a, b := usages[i], usages[j]
		if a.Endpoint != b.Endpoint {
			return a.Endpoint < b.Endpoint
		}
		if a.WorkloadEndpoint != b.WorkloadEndpoint {
			return a.WorkloadEndpoint < b.WorkloadEndpoint
		}
		return a.Environment < b.Environment
	})
	return usages, nil
}

// RoutedEndpointUsage returns the usages that block deleting the component: it
// has a weight above 0 in some environment and the release has other backends
// the traffic can be moved to first. A component that is the only backend of a
// release can be deleted; its Endpoint then has no backends and its
// reclaimPolicy applies.
func RoutedEndpointUsage(usages []EndpointUsage) []EndpointUsage {
	var out []EndpointUsage
	for _, u := range usages {
		if u.Environment != "" && u.Weight > 0 && u.OtherBackends > 0 {
			out = append(out, u)
		}
	}
	return out
}

// ComponentEndpointRefs folds usages into the entries of Component.status.endpoints.
func ComponentEndpointRefs(usages []EndpointUsage) []openchoreov1alpha1.ComponentEndpointRef {
	var refs []openchoreov1alpha1.ComponentEndpointRef
	idx := map[[2]string]int{}
	for _, u := range usages {
		key := [2]string{u.Endpoint, u.WorkloadEndpoint}
		i, ok := idx[key]
		if !ok {
			refs = append(refs, openchoreov1alpha1.ComponentEndpointRef{Name: u.Endpoint, Endpoint: u.WorkloadEndpoint})
			i = len(refs) - 1
			idx[key] = i
		}
		if u.Environment != "" {
			refs[i].Environments = append(refs[i].Environments,
				openchoreov1alpha1.ComponentEndpointEnvironment{Name: u.Environment, Weight: u.Weight})
		}
	}
	return refs
}

// ExposedEndpointName returns the name of the Endpoint a Workload endpoint is
// served through: its endpointName, or <component>-<endpoint> by default.
func ExposedEndpointName(componentName, endpointKey string, ep openchoreov1alpha1.WorkloadEndpoint) string {
	if ep.EndpointName != "" {
		return ep.EndpointName
	}
	return componentName + "-" + endpointKey
}

// EndpointTypeRefEqual compares two EndpointTypeRefs, treating an empty kind as
// EndpointType like the CRD default does.
func EndpointTypeRefEqual(a, b openchoreov1alpha1.EndpointTypeRef) bool {
	kind := func(k openchoreov1alpha1.EndpointTypeRefKind) openchoreov1alpha1.EndpointTypeRefKind {
		if k == "" {
			return openchoreov1alpha1.EndpointTypeRefKindEndpointType
		}
		return k
	}
	return kind(a.Kind) == kind(b.Kind) && a.Name == b.Name
}

// HasEndpointBackend reports whether the Endpoint routes to the given
// component's workload endpoint.
func HasEndpointBackend(ep *openchoreov1alpha1.Endpoint, componentName, endpointKey string) bool {
	for _, ref := range ep.Spec.BackendRefs {
		if ref.ComponentName == componentName && ref.Endpoint == endpointKey {
			return true
		}
	}
	return false
}
