// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
)

// EndpointUsage is one workload endpoint of a component that an Endpoint
// routes to, with the weight the release pinned in one environment gives it.
// Environment is empty for an Endpoint that is not bound in any environment
// yet. OtherBackends counts the release's backends that are not this
// component, so callers can tell whether the traffic could be moved elsewhere.
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

// BackendComponent returns the component and workload endpoint a backend
// routes to, and false when the backend has no component.
func BackendComponent(ref openchoreov1alpha1.EndpointBackendRef) (component, endpoint string, ok bool) {
	if ref.Component == nil {
		return "", "", false
	}
	return ref.Component.Name, ref.Component.Endpoint, true
}

// componentEndpoints returns the workload endpoints of component the Endpoint
// routes to, sorted by name.
func componentEndpoints(ep *openchoreov1alpha1.Endpoint, component string) []string {
	seen := map[string]bool{}
	var out []string
	for _, ref := range ep.Spec.BackendRefs {
		if name, wl, ok := BackendComponent(ref); ok && name == component && !seen[wl] {
			seen[wl] = true
			out = append(out, wl)
		}
	}
	sort.Strings(out)
	return out
}

// ComponentEndpointUsage lists every Endpoint in the project that has the
// component as a backend, with its weight in each
// environment's pinned release. It is shared by the Component reverse index,
// the Component delete guard and the openchoreo-api delete guard so they agree.
// It lists in the namespace and filters in memory, so it works with cached and
// uncached clients alike.
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
		if wls := componentEndpoints(ep, component); len(wls) > 0 {
			routing[ep.Name] = wls
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
		slots, ok := routing[b.Spec.Owner.EndpointName]
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
			if name, _, ok := BackendComponent(ref); !ok || name != component {
				others++
			}
		}
		for _, wl := range slots {
			var weight int32
			for _, ref := range rel.Spec.BackendRefs {
				if name, refWL, ok := BackendComponent(ref); ok && name == component && refWL == wl {
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

	for name, slots := range routing {
		if bound[name] {
			continue
		}
		for _, wl := range slots {
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
// release can be deleted; its Endpoint then has no backends left and is
// deleted with it.
func RoutedEndpointUsage(usages []EndpointUsage) []EndpointUsage {
	var out []EndpointUsage
	for _, u := range usages {
		if u.Environment != "" && u.OtherBackends > 0 && u.Weight > 0 {
			out = append(out, u)
		}
	}
	return out
}

// StillRoutedReason describes routed usages for a delete refusal, for example
// "receives traffic through payments-api in development (weight 10)", followed
// by what to do first.
func StillRoutedReason(routed []EndpointUsage) string {
	traffic := make([]string, 0, len(routed))
	for _, u := range routed {
		traffic = append(traffic, fmt.Sprintf("%s in %s (weight %d)", u.Endpoint, u.Environment, u.Weight))
	}
	return "receives traffic through " + strings.Join(traffic, ", ") + "; move its weight to 0 and roll out before deleting"
}

// ComponentEndpointRefs folds usages into the entries of Component.status.endpoints.
func ComponentEndpointRefs(usages []EndpointUsage) []openchoreov1alpha1.ComponentEndpointRef {
	var refs []openchoreov1alpha1.ComponentEndpointRef
	idx := map[[2]string]int{}
	for _, u := range usages {
		key := [2]string{u.Endpoint, u.WorkloadEndpoint}
		i, ok := idx[key]
		if !ok {
			refs = append(refs, openchoreov1alpha1.ComponentEndpointRef{
				Name: u.Endpoint, Endpoint: u.WorkloadEndpoint,
			})
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

// EndpointComponentNames returns the distinct components an Endpoint routes
// to, in backend order.
func EndpointComponentNames(ep *openchoreov1alpha1.Endpoint) []string {
	seen := make(map[string]struct{}, len(ep.Spec.BackendRefs)+1)
	names := make([]string, 0, len(ep.Spec.BackendRefs)+1)
	add := func(name string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for _, ref := range ep.Spec.BackendRefs {
		if name, _, ok := BackendComponent(ref); ok {
			add(name)
		}
	}
	return names
}

// EndpointSlot is a route slot an Endpoint claims: one workload endpoint of a
// component, in one visibility. Component ReleaseBindings don't render their
// own route for a claimed slot.
type EndpointSlot struct {
	Component  string
	Endpoint   string
	Visibility openchoreov1alpha1.EndpointVisibility
}

// EndpointSlots returns the slots an Endpoint claims: each component backend
// in each declared visibility.
func EndpointSlots(ep *openchoreov1alpha1.Endpoint) []EndpointSlot {
	var out []EndpointSlot
	seen := map[EndpointSlot]bool{}
	for _, ref := range ep.Spec.BackendRefs {
		name, wl, ok := BackendComponent(ref)
		if !ok {
			continue
		}
		for _, v := range ep.Spec.Visibility {
			s := EndpointSlot{Component: name, Endpoint: wl, Visibility: v}
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}

// SharedEndpointSlot returns a slot both Endpoints claim, if they are in the
// same namespace and project. Two Endpoints may route to the same component,
// but not in the same visibility: that would render two routes for it.
func SharedEndpointSlot(a, b *openchoreov1alpha1.Endpoint) (EndpointSlot, bool) {
	if a.Namespace != b.Namespace || a.Spec.Owner.ProjectName != b.Spec.Owner.ProjectName {
		return EndpointSlot{}, false
	}
	theirs := map[EndpointSlot]bool{}
	for _, s := range EndpointSlots(b) {
		theirs[s] = true
	}
	for _, s := range EndpointSlots(a) {
		if theirs[s] {
			return s, true
		}
	}
	return EndpointSlot{}, false
}

// ClaimsComponentEndpoint reports whether the Endpoint routes to the given
// component's workload endpoint.
func ClaimsComponentEndpoint(ep *openchoreov1alpha1.Endpoint, componentName, endpointKey string) bool {
	for _, ref := range ep.Spec.BackendRefs {
		if name, wl, ok := BackendComponent(ref); ok && name == componentName && wl == endpointKey {
			return true
		}
	}
	return false
}

// HostMatches reports whether host matches one of the patterns, where a
// leading "*." matches exactly one label.
func HostMatches(host string, patterns []string) bool {
	for _, p := range patterns {
		if p == host {
			return true
		}
		if strings.HasPrefix(p, "*.") {
			suffix := p[1:]
			if strings.HasSuffix(host, suffix) && !strings.Contains(strings.TrimSuffix(host, suffix), ".") &&
				len(host) > len(suffix) {
				return true
			}
		}
	}
	return false
}
