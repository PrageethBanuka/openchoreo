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
// routes to or serves the URL of, with the weight the release pinned in one
// environment gives it. Environment is empty for an Endpoint that is not bound
// in any environment yet. OtherBackends counts the release's backends that are
// not this component, so callers can tell whether the traffic could be moved
// elsewhere. ServesURL is true when the Endpoint serves this workload
// endpoint's URL; Weight is 0 when the component is not one of its backends.
type EndpointUsage struct {
	Endpoint         string
	WorkloadEndpoint string
	Environment      string
	Weight           int32
	OtherBackends    int
	ServesURL        bool
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
// routes to, and false for an external backend.
func BackendComponent(ref openchoreov1alpha1.EndpointBackendRef) (component, endpoint string, ok bool) {
	if ref.Component == nil {
		return "", "", false
	}
	return ref.Component.Name, ref.Component.Endpoint, true
}

// TakenOver returns the component workload endpoint whose URL the Endpoint
// takes over, or nil.
func TakenOver(ep *openchoreov1alpha1.Endpoint) *openchoreov1alpha1.EndpointComponentRef {
	if ep.Spec.TakeOver == nil {
		return nil
	}
	return &ep.Spec.TakeOver.Component
}

// componentSlot is one workload endpoint of the component an Endpoint uses.
type componentSlot struct {
	workloadEndpoint string
	servesURL        bool
}

// componentSlots returns the workload endpoints of component the Endpoint uses,
// as backends or as the URL it serves, sorted by name.
func componentSlots(ep *openchoreov1alpha1.Endpoint, component string) []componentSlot {
	idx := map[string]int{}
	var slots []componentSlot
	add := func(wl string, url bool) {
		if i, ok := idx[wl]; ok {
			slots[i].servesURL = slots[i].servesURL || url
			return
		}
		idx[wl] = len(slots)
		slots = append(slots, componentSlot{workloadEndpoint: wl, servesURL: url})
	}
	if t := TakenOver(ep); t != nil && t.Name == component {
		add(t.Endpoint, true)
	}
	for _, ref := range ep.Spec.BackendRefs {
		if name, wl, ok := BackendComponent(ref); ok && name == component {
			add(wl, false)
		}
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].workloadEndpoint < slots[j].workloadEndpoint })
	return slots
}

// ComponentEndpointUsage lists every Endpoint in the project that has the
// component as a backend or serves one of its URLs, with its weight in each
// environment's pinned release. It is shared by the Component reverse index,
// the Component delete guard and the openchoreo-api delete guard so they agree.
// It lists in the namespace and filters in memory, so it works with cached and
// uncached clients alike.
func ComponentEndpointUsage(ctx context.Context, c client.Reader, namespace, project, component string) ([]EndpointUsage, error) {
	var endpoints openchoreov1alpha1.EndpointList
	if err := c.List(ctx, &endpoints, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list endpoints: %w", err)
	}

	routing := map[string][]componentSlot{} // endpoint name -> workload endpoints of this component
	for i := range endpoints.Items {
		ep := &endpoints.Items[i]
		if ep.Spec.Owner.ProjectName != project {
			continue
		}
		if slots := componentSlots(ep, component); len(slots) > 0 {
			routing[ep.Name] = slots
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
		for _, slot := range slots {
			var weight int32
			for _, ref := range rel.Spec.BackendRefs {
				if name, wl, ok := BackendComponent(ref); ok && name == component && wl == slot.workloadEndpoint {
					weight = EndpointBackendWeight(ref)
				}
			}
			usages = append(usages, EndpointUsage{
				Endpoint:         b.Spec.Owner.EndpointName,
				WorkloadEndpoint: slot.workloadEndpoint,
				Environment:      b.Spec.Environment,
				Weight:           weight,
				OtherBackends:    others,
				ServesURL:        slot.servesURL,
			})
		}
	}

	for name, slots := range routing {
		if bound[name] {
			continue
		}
		for _, slot := range slots {
			usages = append(usages, EndpointUsage{Endpoint: name, WorkloadEndpoint: slot.workloadEndpoint, ServesURL: slot.servesURL})
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

// RoutedEndpointUsage returns the usages that block deleting the component:
// either it has a weight above 0 in some environment and the release has other
// backends the traffic can be moved to first, or an Endpoint bound in some
// environment serves its URL to other backends, which deleting the component
// would take offline. A component that is the only backend of a release and
// serves its URL can be deleted; its Endpoint then has no backends and its
// reclaimPolicy applies.
func RoutedEndpointUsage(usages []EndpointUsage) []EndpointUsage {
	var out []EndpointUsage
	for _, u := range usages {
		if u.Environment == "" || u.OtherBackends == 0 {
			continue
		}
		if u.Weight > 0 || u.ServesURL {
			out = append(out, u)
		}
	}
	return out
}

// StillRoutedReason describes routed usages for a delete refusal, for example
// "receives traffic through payments-api in development (weight 10); serves the
// URL of payments-api in staging", followed by what to do first.
func StillRoutedReason(routed []EndpointUsage) string {
	var traffic, urls []string
	for _, u := range routed {
		if u.Weight > 0 {
			traffic = append(traffic, fmt.Sprintf("%s in %s (weight %d)", u.Endpoint, u.Environment, u.Weight))
		}
		if u.ServesURL {
			urls = append(urls, fmt.Sprintf("%s in %s", u.Endpoint, u.Environment))
		}
	}
	var parts, fixes []string
	if len(traffic) > 0 {
		parts = append(parts, "receives traffic through "+strings.Join(traffic, ", "))
		fixes = append(fixes, "move its weight to 0 and roll out")
	}
	if len(urls) > 0 {
		parts = append(parts, "has its URL served by "+strings.Join(urls, ", "))
		fixes = append(fixes, "delete the Endpoint serving its URL")
	}
	return strings.Join(parts, "; ") + "; " + strings.Join(fixes, ", and ") + " before deleting"
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
				Name: u.Endpoint, Endpoint: u.WorkloadEndpoint, ServesURL: u.ServesURL,
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

// EndpointComponentNames returns the distinct components an Endpoint
// references: the component whose URL it serves first, then its backends in
// order.
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
	if t := TakenOver(ep); t != nil {
		add(t.Name)
	}
	for _, ref := range ep.Spec.BackendRefs {
		if name, _, ok := BackendComponent(ref); ok {
			add(name)
		}
	}
	return names
}

// SameEndpointAddress reports whether two Endpoints claim the same address.
// An address on the environment's gateway host (no hostname) shares that host
// with every project in the namespace, so those compare within a namespace; an
// address with its own hostname compares across namespaces. An Endpoint without
// an address (a Service-addressed type) claims none.
func SameEndpointAddress(a, b *openchoreov1alpha1.Endpoint) bool {
	if a.Spec.Address == nil || b.Spec.Address == nil ||
		a.Spec.Address.Path == "" || *a.Spec.Address != *b.Spec.Address {
		return false
	}
	return a.Spec.Address.Hostname != "" || a.Namespace == b.Namespace
}

// SameEndpointTakeOver reports whether two Endpoints of one project take over
// the same component URL.
func SameEndpointTakeOver(a, b *openchoreov1alpha1.Endpoint) bool {
	ta, tb := TakenOver(a), TakenOver(b)
	return ta != nil && tb != nil && a.Namespace == b.Namespace &&
		a.Spec.Owner.ProjectName == b.Spec.Owner.ProjectName && *ta == *tb
}

// ServesEndpointURL reports whether the Endpoint takes over the URL of the
// given component's workload endpoint.
func ServesEndpointURL(ep *openchoreov1alpha1.Endpoint, componentName, endpointKey string) bool {
	t := TakenOver(ep)
	return t != nil && t.Name == componentName && t.Endpoint == endpointKey
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
