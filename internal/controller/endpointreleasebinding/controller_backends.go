// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointreleasebinding

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
	"github.com/openchoreo/openchoreo/internal/controller/endpointurl"
	dpkubernetes "github.com/openchoreo/openchoreo/internal/dataplane/kubernetes"
	"github.com/openchoreo/openchoreo/internal/labels"
	endpointpipeline "github.com/openchoreo/openchoreo/internal/pipeline/endpoint"
)

// defaultRuleName names the catch-all rule built from backendRefs when the
// Endpoint declares none.
const defaultRuleName = "default"

// backendResolution is the outcome of resolving every backendRef of a release
// in the binding's environment.
type backendResolution struct {
	// statuses is one entry per backendRef, in spec order, for status.backends.
	statuses []openchoreov1alpha1.ResolvedEndpointBackend

	// backends are the resolved, weighted backends handed to the pipeline.
	backends []endpointpipeline.Backend

	// unresolved names the weighted backendRefs that could not be resolved.
	// Non-empty blocks rendering.
	unresolved []string
}

// byName returns the resolved backend with the given name.
func (res *backendResolution) byName(name string) (endpointpipeline.Backend, bool) {
	for _, b := range res.backends {
		if b.Name == name {
			return b, true
		}
	}
	return endpointpipeline.Backend{}, false
}

// resolveBackends resolves each backendRef of the release for the binding's
// environment. The Endpoint renders a Service per backend, so a component
// backend resolves to what that Service needs, not to the component's own
// Service: the pods of the component deployed here, and the port of the
// referenced workload endpoint in the ComponentRelease it runs.
//
// A backend with weight 0 that cannot be resolved is reported but does not
// block rendering, so a canary can be staged at 0% before it is deployed
// everywhere. Any other unresolved backend blocks rendering.
func (r *Reconciler) resolveBackends(
	ctx context.Context,
	binding *openchoreov1alpha1.EndpointReleaseBinding,
	in *reconcileInputs,
	md endpointpipeline.MetadataContext,
) (*backendResolution, error) {
	res := &backendResolution{}

	for _, ref := range in.release.Spec.BackendRefs {
		weight := effectiveWeight(binding, ref)
		component, endpoint, _ := controller.BackendComponent(ref)
		status := openchoreov1alpha1.ResolvedEndpointBackend{
			Name:          ref.Name,
			ComponentName: component,
			Endpoint:      endpoint,
			Weight:        weight,
		}

		backend, reason, err := r.resolveBackend(ctx, binding, in, md, ref.Name, component, endpoint)
		if err != nil {
			return nil, err
		}

		if reason != "" {
			status.Reason = reason
			res.statuses = append(res.statuses, status)
			if weight > 0 {
				res.unresolved = append(res.unresolved, fmt.Sprintf("%s/%s", component, endpoint))
			}
			continue
		}

		backend.Weight = weight
		status.Resolved = true
		status.ServiceURL = backend.serviceURL
		res.statuses = append(res.statuses, status)
		res.backends = append(res.backends, backend.Backend)
	}

	return res, nil
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
// binding's override for it, or the release's weight.
func effectiveWeight(binding *openchoreov1alpha1.EndpointReleaseBinding, ref openchoreov1alpha1.EndpointBackendRef) int32 {
	if o := findOverride(binding, ref.Name); o != nil && o.Weight != nil {
		return *o.Weight
	}
	return controller.EndpointBackendWeight(ref)
}

// buildRules resolves the release's rules against the resolved backends, in
// order, and ends the list with a catch-all: the Endpoint's own, or the split
// of backendRefs by weight when the last rule has matches (or there are none).
//
// The binding's weight overrides apply to rule backends too, but only where a
// rule splits between several backends: a single-backend rule sends all its
// requests to that backend whatever its weight, so a header preview keeps
// working while the catch-all is switched or rolled back.
//
// A rule backend that is not resolved here blocks rendering unless its weight
// is 0, as for backendRefs. A mirror to a backend that is not resolved is
// dropped: it never affects the response. A non-empty problem blocks rendering.
func buildRules(
	binding *openchoreov1alpha1.EndpointReleaseBinding,
	release *openchoreov1alpha1.EndpointRelease,
	res *backendResolution,
) (rules []endpointpipeline.Rule, problem string) {
	for _, rule := range release.Spec.Rules {
		out := endpointpipeline.Rule{
			Name:    rule.Name,
			Matches: convertMatches(rule.Matches),
			Mirrors: []endpointpipeline.Mirror{},
		}
		for _, rb := range rule.Backends {
			weight := int32(1)
			if rb.Weight != nil {
				weight = *rb.Weight
			}
			if len(rule.Backends) > 1 {
				if o := findOverride(binding, rb.Name); o != nil && o.Weight != nil {
					weight = *o.Weight
				}
			}
			b, ok := res.byName(rb.Name)
			if !ok {
				if weight > 0 {
					return nil, fmt.Sprintf("rule %q routes to backend %q, which is not available in environment %q",
						rule.Name, rb.Name, binding.Spec.Environment)
				}
				continue
			}
			b.Weight = weight
			out.Backends = append(out.Backends, b)
		}
		if !hasWeight(out.Backends) {
			return nil, fmt.Sprintf("rule %q has no backend with a non-zero weight in environment %q",
				rule.Name, binding.Spec.Environment)
		}
		for _, m := range rule.Mirrors {
			if b, ok := res.byName(m.Backend); ok {
				out.Mirrors = append(out.Mirrors, endpointpipeline.Mirror{Backend: b, Percent: m.Percent})
			}
		}
		rules = append(rules, out)
	}

	if n := len(release.Spec.Rules); n == 0 || len(release.Spec.Rules[n-1].Matches) > 0 {
		if !hasWeight(res.backends) {
			return nil, "No backend with a non-zero weight is available; check the weights in spec.backendOverrides"
		}
		rules = append(rules, endpointpipeline.Rule{
			Name:     catchAllName(release.Spec.Rules),
			Matches:  []endpointpipeline.Match{},
			Backends: res.backends,
			Mirrors:  []endpointpipeline.Mirror{},
		})
	}
	return rules, ""
}

// catchAllName returns the name of the generated catch-all rule, avoiding the
// names of the Endpoint's own rules.
func catchAllName(rules []openchoreov1alpha1.EndpointRule) string {
	name := defaultRuleName
	for taken := true; taken; {
		taken = false
		for _, r := range rules {
			if r.Name == name {
				name = "catch-all-" + name
				taken = true
				break
			}
		}
	}
	return name
}

func hasWeight(backends []endpointpipeline.Backend) bool {
	for _, b := range backends {
		if b.Weight > 0 {
			return true
		}
	}
	return false
}

// convertMatches copies rule matches into the render context, with every list
// non-nil and header match types defaulted.
func convertMatches(in []openchoreov1alpha1.EndpointMatch) []endpointpipeline.Match {
	out := make([]endpointpipeline.Match, 0, len(in))
	for _, m := range in {
		cm := endpointpipeline.Match{
			Headers:     make([]endpointpipeline.HeaderMatch, 0, len(m.Headers)),
			Cookies:     make([]endpointpipeline.CookieMatch, 0, len(m.Cookies)),
			QueryParams: make([]endpointpipeline.QueryParamMatch, 0, len(m.QueryParams)),
		}
		for _, h := range m.Headers {
			t := string(h.Type)
			if t == "" {
				t = string(openchoreov1alpha1.EndpointHeaderMatchExact)
			}
			cm.Headers = append(cm.Headers, endpointpipeline.HeaderMatch{Name: h.Name, Type: t, Value: h.Value})
		}
		for _, c := range m.Cookies {
			cm.Cookies = append(cm.Cookies, endpointpipeline.CookieMatch{Name: c.Name, Value: c.Value})
		}
		for _, q := range m.QueryParams {
			cm.QueryParams = append(cm.QueryParams, endpointpipeline.QueryParamMatch{Name: q.Name, Value: q.Value})
		}
		out = append(out, cm)
	}
	return out
}

// resolvedBackend pairs the pipeline-facing backend with its structured URL.
type resolvedBackend struct {
	endpointpipeline.Backend
	serviceURL *openchoreov1alpha1.EndpointURL
}

// resolveBackend resolves a single component backend. It returns a non-empty
// reason (and no backend) when the backend is not usable in this environment;
// errors are reserved for transient API failures.
func (r *Reconciler) resolveBackend(
	ctx context.Context,
	binding *openchoreov1alpha1.EndpointReleaseBinding,
	in *reconcileInputs,
	md endpointpipeline.MetadataContext,
	name, componentName, endpointName string,
) (*resolvedBackend, string, error) {
	env := binding.Spec.Environment
	if componentName == "" {
		return nil, fmt.Sprintf("backend %q has no component", name), nil
	}

	var rbList openchoreov1alpha1.ReleaseBindingList
	if err := r.List(ctx, &rbList,
		client.InNamespace(binding.Namespace),
		client.MatchingFields{controller.IndexKeyReleaseBindingOwnerEnv: controller.MakeReleaseBindingOwnerEnvKey(
			binding.Spec.Owner.ProjectName, componentName, env)},
	); err != nil {
		return nil, "", fmt.Errorf("list ReleaseBindings for component %q: %w", componentName, err)
	}

	switch len(rbList.Items) {
	case 0:
		return nil, fmt.Sprintf("component %q is not deployed in environment %q", componentName, env), nil
	case 1:
	default:
		return nil, fmt.Sprintf("component %q has %d ReleaseBindings in environment %q",
			componentName, len(rbList.Items), env), nil
	}

	rb := &rbList.Items[0]
	if rb.Spec.State == openchoreov1alpha1.ReleaseStateUndeploy {
		return nil, fmt.Sprintf("component %q is undeployed in environment %q", componentName, env), nil
	}
	if rb.Spec.ReleaseName == "" {
		return nil, fmt.Sprintf("component %q has no release in environment %q yet", componentName, env), nil
	}

	cr := &openchoreov1alpha1.ComponentRelease{}
	if err := r.Get(ctx, types.NamespacedName{Name: rb.Spec.ReleaseName, Namespace: binding.Namespace}, cr); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Sprintf("ComponentRelease %q of component %q not found", rb.Spec.ReleaseName, componentName), nil
		}
		return nil, "", fmt.Errorf("get ComponentRelease %q: %w", rb.Spec.ReleaseName, err)
	}
	wl, ok := cr.Spec.Workload.Endpoints[endpointName]
	if !ok {
		return nil, fmt.Sprintf("component %q has no endpoint %q in release %q",
			componentName, endpointName, cr.Name), nil
	}
	if !endpointurl.IsRoutableProtocol(wl.Type) {
		return nil, fmt.Sprintf("endpoint %q of component %q uses protocol %s, which cannot be routed",
			endpointName, componentName, wl.Type), nil
	}

	component := &openchoreov1alpha1.Component{}
	if err := r.Get(ctx, types.NamespacedName{Name: componentName, Namespace: binding.Namespace}, component); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Sprintf("component %q not found", componentName), nil
		}
		return nil, "", fmt.Errorf("get Component %q: %w", componentName, err)
	}

	targetPort := wl.TargetPort
	if targetPort == 0 {
		targetPort = wl.Port
	}
	basePath := wl.BasePath
	if basePath == "" {
		basePath = "/"
	}

	serviceName := dpkubernetes.GenerateK8sName(md.Name, name)
	url := &openchoreov1alpha1.EndpointURL{
		Scheme: endpointurl.ServiceScheme(wl.Type),
		Host:   fmt.Sprintf("%s.%s.%s", serviceName, md.Namespace, endpointurl.ClusterLocalSuffix),
		Port:   wl.Port,
		Path:   basePath,
	}

	backend := endpointpipeline.Backend{
		Name:          name,
		Kind:          string(openchoreov1alpha1.EndpointBackendKindComponent),
		ComponentName: componentName,
		Endpoint:      endpointName,
		Protocol:      string(wl.Type),
		PodSelector:   podSelector(binding, in, componentName, string(component.UID)),
		TargetPort:    targetPort,
		ServiceName:   serviceName,
		Namespace:     md.Namespace,
		Port:          wl.Port,
		ServiceURL:    endpointurl.String(url),
		BasePath:      basePath,
	}
	for i := range rb.Status.Endpoints {
		if rb.Status.Endpoints[i].Name == endpointName {
			backend.ComponentURLs = endpointpipeline.ComponentURLs{
				External: gatewayAddress(rb.Status.Endpoints[i].ExternalURLs),
				Internal: gatewayAddress(rb.Status.Endpoints[i].InternalURLs),
			}
		}
	}
	return &resolvedBackend{Backend: backend, serviceURL: url}, "", nil
}

// podSelector returns the labels the component's pods carry in the binding's
// environment: the selector the ReleaseBinding controller gives the component's
// workload.
func podSelector(binding *openchoreov1alpha1.EndpointReleaseBinding, in *reconcileInputs, componentName, componentUID string) map[string]string {
	return map[string]string{
		labels.LabelKeyNamespaceName:   binding.Namespace,
		labels.LabelKeyProjectName:     binding.Spec.Owner.ProjectName,
		labels.LabelKeyComponentName:   componentName,
		labels.LabelKeyEnvironmentName: binding.Spec.Environment,
		labels.LabelKeyComponentUID:    componentUID,
		labels.LabelKeyEnvironmentUID:  string(in.environment.UID),
		labels.LabelKeyProjectUID:      string(in.project.UID),
	}
}

// gatewayAddress returns the address a component serves through one gateway,
// preferring the https listener, then http, then tls. Nil when the component
// does not expose the endpoint through that gateway.
func gatewayAddress(urls *openchoreov1alpha1.EndpointGatewayURLs) *endpointpipeline.Address {
	if urls == nil {
		return nil
	}
	for _, u := range []*openchoreov1alpha1.EndpointURL{urls.HTTPS, urls.HTTP, urls.TLS} {
		if u == nil || u.Host == "" {
			continue
		}
		path := u.Path
		if path == "" {
			path = "/"
		}
		return &endpointpipeline.Address{Scheme: u.Scheme, Host: u.Host, Port: u.Port, Path: path}
	}
	return nil
}
