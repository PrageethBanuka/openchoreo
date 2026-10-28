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

// defaultRuleName names the rule that splits requests across the backends by
// weight.
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

// buildRules builds the routing rules the type renders: the split of the
// release's backends by their weight in this environment. A non-empty problem
// blocks rendering.
func buildRules(
	_ *openchoreov1alpha1.EndpointReleaseBinding,
	_ *openchoreov1alpha1.EndpointRelease,
	res *backendResolution,
) (rules []endpointpipeline.Rule, problem string) {
	if !hasWeight(res.backends) {
		return nil, "No backend with a non-zero weight is available; check the weights in spec.backendOverrides"
	}
	return []endpointpipeline.Rule{{
		Name:     defaultRuleName,
		Matches:  []endpointpipeline.Match{},
		Backends: res.backends,
		Mirrors:  []endpointpipeline.Mirror{},
	}}, ""
}

func hasWeight(backends []endpointpipeline.Backend) bool {
	for _, b := range backends {
		if b.Weight > 0 {
			return true
		}
	}
	return false
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
