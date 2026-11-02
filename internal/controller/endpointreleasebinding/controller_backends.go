// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointreleasebinding

import (
	"context"
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
	endpointpipeline "github.com/openchoreo/openchoreo/internal/pipeline/endpoint"
)

// clusterLocalSuffix is the cluster DNS suffix the ReleaseBinding controller
// builds in-cluster service hosts with ("<svc>.<ns>.svc.cluster.local").
const clusterLocalSuffix = "svc.cluster.local"

// resolvedBackends is the outcome of resolving every backendRef of a release in
// the binding's environment.
type resolvedBackends struct {
	// statuses is one entry per backendRef, in spec order, for status.backends.
	statuses []openchoreov1alpha1.ResolvedEndpointBackend

	// backends are the resolved backends handed to the pipeline, in spec order.
	backends []endpointpipeline.Backend

	// blocking describes each backend that should receive traffic but could not
	// be resolved. Non-empty blocks rendering.
	blocking []string
}

// backendResolution is the outcome of resolving one backendRef. Exactly one of
// backend or reason is set: reason explains why the backend cannot receive
// traffic in this environment.
type backendResolution struct {
	backend    *endpointpipeline.Backend
	serviceURL *openchoreov1alpha1.EndpointURL
	reason     string
	message    string
}

// resolveBackends resolves each backendRef of the release for the binding's
// environment.
//
// A backend that cannot be resolved is reported on status with a reason rather
// than failing the reconcile. It blocks rendering only when it should receive
// traffic: an unresolved weight-0 backend is skipped, which is what lets a
// canary be staged at 0% before it is deployed in every environment.
func (r *Reconciler) resolveBackends(
	ctx context.Context,
	binding *openchoreov1alpha1.EndpointReleaseBinding,
	release *openchoreov1alpha1.EndpointRelease,
	dpNamespace string,
) (*resolvedBackends, error) {
	out := &resolvedBackends{}

	for _, ref := range release.Spec.BackendRefs {
		weight := effectiveWeight(binding, ref)

		res, err := r.resolveBackend(ctx, binding, ref, dpNamespace)
		if err != nil {
			return nil, err
		}

		status := openchoreov1alpha1.ResolvedEndpointBackend{
			Name:   ref.Name,
			Kind:   ref.Kind,
			Weight: weight,
			Mirror: effectiveMirror(binding, ref),
		}
		if res.backend == nil {
			status.Reason = res.reason
			status.Message = res.message
			out.statuses = append(out.statuses, status)
			if weight > 0 {
				out.blocking = append(out.blocking, fmt.Sprintf("%s (%s)", ref.Name, res.message))
			}
			continue
		}

		res.backend.Weight = weight
		status.Resolved = true
		status.ServiceURL = res.serviceURL
		out.statuses = append(out.statuses, status)
		out.backends = append(out.backends, *res.backend)
	}

	out.backends = endpointpipeline.WithPercents(out.backends)
	return out, nil
}

// defaultRuleName names the rule that splits requests across the backends by
// weight.
const defaultRuleName = "default"

// buildRules builds the routing rules the type renders as ${rules}, in order:
//
//   - one rule per backend with a match, in backendRefs order, sending every
//     request it matches to that backend alone, whatever the weights;
//   - the default rule, splitting every other request across the resolved
//     backends by their weight in this environment, and copying a share of
//     them to each mirror backend.
//
// A matched or mirror backend that is not resolved here is left out; its status entry
// says why, and the rest of the routing is unaffected, so a release can be
// promoted ahead of its preview component.
func buildRules(
	release *openchoreov1alpha1.EndpointRelease,
	resolved *resolvedBackends,
) []endpointpipeline.Rule {
	backends := resolved.backends
	byName := make(map[string]endpointpipeline.Backend, len(backends))
	for _, b := range backends {
		byName[b.Name] = b
	}
	mirrorOf := make(map[string]*openchoreov1alpha1.EndpointMirror, len(resolved.statuses))
	for _, s := range resolved.statuses {
		mirrorOf[s.Name] = s.Mirror
	}
	var rules []endpointpipeline.Rule
	mirrors := []endpointpipeline.Mirror{}
	for _, ref := range release.Spec.BackendRefs {
		b, ok := byName[ref.Name]
		if !ok {
			continue
		}
		if ref.Match != nil {
			matched := b
			matched.Weight, matched.Percent = 1, 100
			rules = append(rules, endpointpipeline.Rule{
				Name:     ref.Name,
				Matches:  []endpointpipeline.Match{convertMatch(ref.Match)},
				Backends: []endpointpipeline.Backend{matched},
				Mirrors:  []endpointpipeline.Mirror{},
			})
		}
		if m := mirrorOf[ref.Name]; m != nil && m.Percent > 0 {
			mirrors = append(mirrors, endpointpipeline.Mirror{Backend: b, Percent: m.Percent})
		}
	}
	return append(rules, endpointpipeline.Rule{
		Name:     defaultRuleName,
		Matches:  []endpointpipeline.Match{},
		Backends: backends,
		Mirrors:  mirrors,
	})
}

// convertMatch copies a backend's match into the render context, normalized
// so every type renders it the same way.
func convertMatch(m *openchoreov1alpha1.EndpointMatch) endpointpipeline.Match {
	var path *endpointpipeline.PathMatch
	if m.Path != nil {
		path = &endpointpipeline.PathMatch{Type: string(m.Path.Type), Value: m.Path.Value}
	}
	headers := make([]endpointpipeline.HeaderMatch, 0, len(m.Headers))
	for _, h := range m.Headers {
		headers = append(headers, endpointpipeline.HeaderMatch{Name: h.Name, Type: string(h.Type), Value: h.Value})
	}
	cookies := make([]endpointpipeline.CookieMatch, 0, len(m.Cookies))
	for _, c := range m.Cookies {
		cookies = append(cookies, endpointpipeline.CookieMatch{Name: c.Name, Value: c.Value})
	}
	return endpointpipeline.NewMatch(path, headers, cookies)
}

// findOverride returns the binding's override for the named backend, or nil.
func findOverride(binding *openchoreov1alpha1.EndpointReleaseBinding, name string) *openchoreov1alpha1.EndpointBackendOverride {
	for i := range binding.Spec.BackendOverrides {
		if binding.Spec.BackendOverrides[i].Name == name {
			return &binding.Spec.BackendOverrides[i]
		}
	}
	return nil
}

// effectiveWeight returns a backend's weight in the binding's environment: the
// binding's override for it, or the release's weight (1 when unset, the CRD
// default). A mirror backend only receives copies, so its weight stays 0
// whatever an override says.
func effectiveWeight(binding *openchoreov1alpha1.EndpointReleaseBinding, ref openchoreov1alpha1.EndpointBackendRef) int32 {
	if ref.Mirror != nil {
		return 0
	}
	if o := findOverride(binding, ref.Name); o != nil && o.Weight != nil {
		return *o.Weight
	}
	if ref.Weight != nil {
		return *ref.Weight
	}
	return 1
}

// effectiveMirror returns a backend's mirror share in the binding's
// environment: the binding's override for it, or the release's. Nil when the
// backend is not a mirror.
func effectiveMirror(binding *openchoreov1alpha1.EndpointReleaseBinding, ref openchoreov1alpha1.EndpointBackendRef) *openchoreov1alpha1.EndpointMirror {
	if ref.Mirror == nil {
		return nil
	}
	if o := findOverride(binding, ref.Name); o != nil && o.Mirror != nil {
		return o.Mirror.DeepCopy()
	}
	return ref.Mirror.DeepCopy()
}

// validateBackendOverrides checks that every backendOverrides entry names a
// backend of the pinned release, and sets a mirror share only on a mirror
// backend, so a typo can't silently leave the routing unchanged. A binding can be applied before its release (GitOps), so this is
// checked here rather than at admission.
func validateBackendOverrides(release *openchoreov1alpha1.EndpointRelease, binding *openchoreov1alpha1.EndpointReleaseBinding) error {
	known := make(map[string]openchoreov1alpha1.EndpointBackendRef, len(release.Spec.BackendRefs))
	for _, ref := range release.Spec.BackendRefs {
		known[ref.Name] = ref
	}
	var unknown, notMirrors []string
	for _, o := range binding.Spec.BackendOverrides {
		ref, ok := known[o.Name]
		switch {
		case !ok:
			unknown = append(unknown, o.Name)
		case o.Mirror != nil && ref.Mirror == nil:
			notMirrors = append(notMirrors, o.Name)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("spec.backendOverrides names backend(s) not in EndpointRelease %q: %s",
			release.Name, strings.Join(unknown, ", "))
	}
	if len(notMirrors) > 0 {
		return fmt.Errorf("spec.backendOverrides sets mirror on backend(s) that have no mirror in EndpointRelease %q: %s",
			release.Name, strings.Join(notMirrors, ", "))
	}
	return nil
}

// resolveBackend resolves one backendRef to the data-plane Service that
// serves it in the binding's environment. It is the single seam between the
// binding and how a backend is located, so the strategy behind a kind can
// change without touching the pipeline or templates. Errors are reserved for
// transient API failures; a backend that cannot be used is a resolution with a
// reason.
func (r *Reconciler) resolveBackend(
	ctx context.Context,
	binding *openchoreov1alpha1.EndpointReleaseBinding,
	ref openchoreov1alpha1.EndpointBackendRef,
	dpNamespace string,
) (backendResolution, error) {
	switch {
	case ref.Kind == openchoreov1alpha1.EndpointBackendKindComponent && ref.Component != nil:
		return r.resolveComponentBackend(ctx, binding, ref, dpNamespace)
	default:
		// The CRD admits only Component today; reported rather than returned as
		// an error so a kind this controller predates cannot wedge the binding.
		return backendResolution{
			reason:  BackendReasonUnsupportedKind,
			message: fmt.Sprintf("backend kind %q is not supported", ref.Kind),
		}, nil
	}
}

// resolveComponentBackend resolves a Component backend through the component's
// ReleaseBinding for the environment, reading the in-cluster address its
// status reports for the workload endpoint. This reuses the lookup the
// connection/dependency path already relies on.
//
// TODO: resolve by selector equality instead: compute the component's
// podSelectors for (component, environment) independently and match them
// against the Services on its RenderedRelease. That does not depend on how a
// ComponentType names or shapes its Service, and it is the approach that
// extends to release lines once podSelectors carries one.
func (r *Reconciler) resolveComponentBackend(
	ctx context.Context,
	binding *openchoreov1alpha1.EndpointReleaseBinding,
	ref openchoreov1alpha1.EndpointBackendRef,
	dpNamespace string,
) (backendResolution, error) {
	env := binding.Spec.Environment
	component := ref.Component.Name
	endpoint := ref.Component.Endpoint
	unresolved := func(reason, format string, args ...any) (backendResolution, error) {
		return backendResolution{reason: reason, message: fmt.Sprintf(format, args...)}, nil
	}

	var rbList openchoreov1alpha1.ReleaseBindingList
	if err := r.List(ctx, &rbList,
		client.InNamespace(binding.Namespace),
		client.MatchingFields{controller.IndexKeyReleaseBindingOwnerEnv: controller.MakeReleaseBindingOwnerEnvKey(
			binding.Spec.Owner.ProjectName, component, env)},
	); err != nil {
		return backendResolution{}, fmt.Errorf("list ReleaseBindings for component %q: %w", component, err)
	}

	switch len(rbList.Items) {
	case 0:
		return unresolved(BackendReasonNotDeployed, "component %q is not deployed in environment %q", component, env)
	case 1:
	default:
		return unresolved(BackendReasonAmbiguous, "component %q has %d ReleaseBindings in environment %q",
			component, len(rbList.Items), env)
	}

	rb := &rbList.Items[0]
	if rb.Spec.State == openchoreov1alpha1.ReleaseStateUndeploy {
		return unresolved(BackendReasonNotDeployed, "component %q is undeployed in environment %q", component, env)
	}

	var epStatus *openchoreov1alpha1.EndpointURLStatus
	for i := range rb.Status.Endpoints {
		if rb.Status.Endpoints[i].Name == endpoint {
			epStatus = &rb.Status.Endpoints[i]
			break
		}
	}
	if epStatus == nil || epStatus.ServiceURL == nil {
		return unresolved(BackendReasonEndpointNotAvailable,
			"endpoint %q of component %q has no in-cluster address in environment %q yet", endpoint, component, env)
	}
	if !isRoutableProtocol(epStatus.Type) {
		return unresolved(BackendReasonUnsupportedProtocol,
			"endpoint %q of component %q uses protocol %s, which no route can carry", endpoint, component, epStatus.Type)
	}

	serviceName, serviceNamespace, ok := splitServiceHost(epStatus.ServiceURL.Host)
	if !ok {
		return unresolved(BackendReasonEndpointNotAvailable,
			"endpoint %q of component %q has an unrecognized service host %q", endpoint, component, epStatus.ServiceURL.Host)
	}
	// Routes render into the project's shared data-plane namespace and
	// reference backend Services without a ReferenceGrant, so the backend must
	// live in that namespace too.
	if serviceNamespace != dpNamespace {
		return unresolved(BackendReasonNamespaceMismatch,
			"endpoint %q of component %q is served from namespace %q, but routes render into %q",
			endpoint, component, serviceNamespace, dpNamespace)
	}

	url := epStatus.ServiceURL.DeepCopy()
	return backendResolution{
		backend: &endpointpipeline.Backend{
			Name:          ref.Name,
			Kind:          string(ref.Kind),
			ComponentName: component,
			Endpoint:      endpoint,
			Protocol:      string(epStatus.Type),
			ServiceName:   serviceName,
			Namespace:     serviceNamespace,
			Port:          url.Port,
			ServiceURL:    formatURL(url),
			BasePath:      basePathOf(url),
		},
		serviceURL: url,
	}, nil
}

// isRoutableProtocol reports whether any route kind can carry the protocol.
// Mirrors the ReleaseBinding controller's routeKindCompat: HTTP-family and gRPC
// ride HTTPRoute/GRPCRoute, TCP rides TLSRoute, and nothing carries UDP. A type
// that supports a narrower set says so through its validations, since the
// protocol reaches templates as backends[].protocol.
func isRoutableProtocol(p openchoreov1alpha1.EndpointProtocol) bool {
	return p != openchoreov1alpha1.EndpointProtocolUDP
}

// splitServiceHost splits "<svc>.<ns>.svc.cluster.local" into its Service name
// and namespace.
func splitServiceHost(host string) (name, namespace string, ok bool) {
	suffix := "." + clusterLocalSuffix
	if !strings.HasSuffix(host, suffix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimSuffix(host, suffix), ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// basePathOf returns the path the backend serves under, defaulting to "/".
func basePathOf(u *openchoreov1alpha1.EndpointURL) string {
	if u.Path == "" {
		return "/"
	}
	return u.Path
}

// formatURL renders an EndpointURL as scheme://host:port/path.
func formatURL(u *openchoreov1alpha1.EndpointURL) string {
	var b strings.Builder
	if u.Scheme != "" {
		b.WriteString(u.Scheme)
		b.WriteString("://")
	}
	b.WriteString(u.Host)
	if u.Port != 0 {
		fmt.Fprintf(&b, ":%d", u.Port)
	}
	if u.Path != "" && u.Path != "/" {
		if !strings.HasPrefix(u.Path, "/") {
			b.WriteString("/")
		}
		b.WriteString(u.Path)
	}
	return b.String()
}
