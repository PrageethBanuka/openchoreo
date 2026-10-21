// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointreleasebinding

import (
	"context"
	"fmt"
	neturl "net/url"
	"strconv"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
	"github.com/openchoreo/openchoreo/internal/controller/endpointurl"
	dpkubernetes "github.com/openchoreo/openchoreo/internal/dataplane/kubernetes"
	endpointpipeline "github.com/openchoreo/openchoreo/internal/pipeline/endpoint"
)

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
// environment. A component backend resolves to the in-cluster Service of the
// referenced workload endpoint, through the provider lookup the ReleaseBinding
// controller uses for connections: the component's ReleaseBinding for
// (project, component, environment), and its status.endpoints[].serviceURL. An
// external backend resolves to the binding's override for it, or the release's
// default URL; it is always resolved, since OpenChoreo does not run it.
//
// A backend with weight 0 that cannot be resolved is reported but does not
// block rendering, so a canary can be staged at 0% before it is deployed
// everywhere. Any other unresolved backend blocks rendering.
func (r *Reconciler) resolveBackends(
	ctx context.Context,
	binding *openchoreov1alpha1.EndpointReleaseBinding,
	release *openchoreov1alpha1.EndpointRelease,
	md endpointpipeline.MetadataContext,
) (*backendResolution, error) {
	res := &backendResolution{}

	for _, ref := range release.Spec.BackendRefs {
		weight := controller.EndpointBackendWeight(ref)

		if ref.External != nil {
			url := externalURL(binding, ref)
			status := openchoreov1alpha1.ResolvedEndpointBackend{Name: ref.Name, ExternalURL: url, Weight: weight}
			ext, err := parseExternalURL(url)
			if err != nil {
				status.Reason = err.Error()
				res.statuses = append(res.statuses, status)
				if weight > 0 {
					res.unresolved = append(res.unresolved, ref.Name)
				}
				continue
			}
			status.Resolved = true
			res.statuses = append(res.statuses, status)
			res.backends = append(res.backends, endpointpipeline.Backend{
				Name:        ref.Name,
				Kind:        string(openchoreov1alpha1.EndpointBackendKindExternal),
				External:    ext,
				Protocol:    string(openchoreov1alpha1.EndpointProtocolHTTP),
				ServiceName: externalServiceName(md.Name, ref.Name),
				Namespace:   md.Namespace,
				Port:        ext.Port,
				Weight:      weight,
				ServiceURL:  url,
				BasePath:    "/",
			})
			continue
		}

		component, endpoint, _ := controller.BackendComponent(ref)
		status := openchoreov1alpha1.ResolvedEndpointBackend{
			Name:          ref.Name,
			ComponentName: component,
			Endpoint:      endpoint,
			Weight:        weight,
		}

		backend, reason, err := r.resolveBackend(ctx, binding, component, endpoint, md.Namespace)
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

		backend.Name = ref.Name
		backend.Weight = weight
		status.Resolved = true
		status.ServiceURL = backend.serviceURL
		res.statuses = append(res.statuses, status)
		res.backends = append(res.backends, backend.Backend)
	}

	return res, nil
}

// externalURL returns the URL an external backend calls in the binding's
// environment: the binding's override for it, or the release's default.
func externalURL(binding *openchoreov1alpha1.EndpointReleaseBinding, ref openchoreov1alpha1.EndpointBackendRef) string {
	for _, o := range binding.Spec.BackendOverrides {
		if o.Name == ref.Name && o.External != nil && o.External.URL != "" {
			return o.External.URL
		}
	}
	return ref.External.URL
}

// parseExternalURL splits an external backend URL into scheme, host and port,
// defaulting the port from the scheme.
func parseExternalURL(raw string) (*endpointpipeline.ExternalBackend, error) {
	u, err := neturl.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("external URL %q must be http(s)://host[:port]", raw)
	}
	port := int32(80)
	if u.Scheme == "https" {
		port = 443
	}
	if p := u.Port(); p != "" {
		n, err := strconv.ParseInt(p, 10, 32)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("external URL %q has an invalid port", raw)
		}
		port = int32(n)
	}
	return &endpointpipeline.ExternalBackend{Scheme: u.Scheme, Host: u.Hostname(), Port: port, URL: raw}, nil
}

// externalServiceName is the name templates give the object that stands for an
// external backend on the data plane (an ExternalName Service, or the
// gateway's own backend kind), unique per Endpoint, environment and backend.
func externalServiceName(routeName, backendName string) string {
	return dpkubernetes.GenerateK8sName(routeName, backendName)
}

// resolveTakeOver resolves the component URL the release takes over: the
// address that component serves for that workload endpoint in the binding's
// environment, which the Endpoint also serves. It reuses resolveBackend, so
// the component must be deployed here with the endpoint, whether or not it is
// a backend. A nil context with a reason means the take-over is pending in this
// environment; the Endpoint still serves its own address.
func (r *Reconciler) resolveTakeOver(
	ctx context.Context,
	binding *openchoreov1alpha1.EndpointReleaseBinding,
	release *openchoreov1alpha1.EndpointRelease,
	dpNamespace string,
) (*endpointpipeline.URLContext, string, error) {
	if release.Spec.TakeOver == nil {
		return nil, "", nil
	}
	ref := release.Spec.TakeOver.Component
	backend, reason, err := r.resolveBackend(ctx, binding, ref.Name, ref.Endpoint, dpNamespace)
	if err != nil || reason != "" {
		return nil, reason, err
	}
	return &endpointpipeline.URLContext{
		ComponentName: ref.Name,
		Endpoint:      ref.Endpoint,
		BasePath:      backend.BasePath,
		External:      backend.ComponentURLs.External,
		Internal:      backend.ComponentURLs.Internal,
	}, "", nil
}

// resolvedBackend pairs the pipeline-facing backend with its structured URL.
type resolvedBackend struct {
	endpointpipeline.Backend
	serviceURL *openchoreov1alpha1.EndpointURL
}

// resolveBackend resolves a single backendRef. It returns a non-empty reason
// (and no backend) when the backend is not usable in this environment; errors
// are reserved for transient API failures.
func (r *Reconciler) resolveBackend(
	ctx context.Context,
	binding *openchoreov1alpha1.EndpointReleaseBinding,
	component, endpoint string,
	dpNamespace string,
) (*resolvedBackend, string, error) {
	ref := struct{ ComponentName, Endpoint string }{component, endpoint}
	env := binding.Spec.Environment

	var rbList openchoreov1alpha1.ReleaseBindingList
	if err := r.List(ctx, &rbList,
		client.InNamespace(binding.Namespace),
		client.MatchingFields{controller.IndexKeyReleaseBindingOwnerEnv: controller.MakeReleaseBindingOwnerEnvKey(
			binding.Spec.Owner.ProjectName, ref.ComponentName, env)},
	); err != nil {
		return nil, "", fmt.Errorf("list ReleaseBindings for component %q: %w", ref.ComponentName, err)
	}

	switch len(rbList.Items) {
	case 0:
		return nil, fmt.Sprintf("component %q is not deployed in environment %q", ref.ComponentName, env), nil
	case 1:
	default:
		return nil, fmt.Sprintf("component %q has %d ReleaseBindings in environment %q",
			ref.ComponentName, len(rbList.Items), env), nil
	}

	rb := &rbList.Items[0]
	if rb.Spec.State == openchoreov1alpha1.ReleaseStateUndeploy {
		return nil, fmt.Sprintf("component %q is undeployed in environment %q", ref.ComponentName, env), nil
	}

	var epStatus *openchoreov1alpha1.EndpointURLStatus
	for i := range rb.Status.Endpoints {
		if rb.Status.Endpoints[i].Name == ref.Endpoint {
			epStatus = &rb.Status.Endpoints[i]
			break
		}
	}
	if epStatus == nil {
		return nil, fmt.Sprintf("endpoint %q of component %q is not available in environment %q",
			ref.Endpoint, ref.ComponentName, env), nil
	}
	if !endpointurl.IsRoutableProtocol(epStatus.Type) {
		return nil, fmt.Sprintf("endpoint %q of component %q uses protocol %s, which cannot be routed",
			ref.Endpoint, ref.ComponentName, epStatus.Type), nil
	}
	if epStatus.ServiceURL == nil {
		return nil, fmt.Sprintf("endpoint %q of component %q has no service URL in environment %q yet",
			ref.Endpoint, ref.ComponentName, env), nil
	}

	serviceName, serviceNamespace, ok := splitServiceHost(epStatus.ServiceURL.Host)
	if !ok {
		return nil, fmt.Sprintf("endpoint %q of component %q has an unrecognized service host %q",
			ref.Endpoint, ref.ComponentName, epStatus.ServiceURL.Host), nil
	}
	// Routes are rendered into the shared project data-plane namespace and
	// reference backend Services without a ReferenceGrant, so the backend must
	// live there too.
	if serviceNamespace != dpNamespace {
		return nil, fmt.Sprintf("endpoint %q of component %q is served from namespace %q, not %q",
			ref.Endpoint, ref.ComponentName, serviceNamespace, dpNamespace), nil
	}

	url := epStatus.ServiceURL.DeepCopy()
	return &resolvedBackend{
		Backend: endpointpipeline.Backend{
			ComponentName: ref.ComponentName,
			Endpoint:      ref.Endpoint,
			Protocol:      string(epStatus.Type),
			ServiceName:   serviceName,
			Namespace:     serviceNamespace,
			Port:          url.Port,
			ServiceURL:    endpointurl.String(url),
			BasePath:      basePathOf(url),
			ComponentURLs: endpointpipeline.ComponentURLs{
				External: gatewayAddress(epStatus.ExternalURLs),
				Internal: gatewayAddress(epStatus.InternalURLs),
			},
		},
		serviceURL: url,
	}, "", nil
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

// basePathOf returns the workload endpoint's base path, which the component's
// service URL carries, defaulting to "/".
func basePathOf(serviceURL *openchoreov1alpha1.EndpointURL) string {
	if serviceURL == nil || serviceURL.Path == "" {
		return "/"
	}
	return serviceURL.Path
}

// splitServiceHost splits "<svc>.<ns>.svc.cluster.local" into its Service name
// and namespace.
func splitServiceHost(host string) (name, namespace string, ok bool) {
	suffix := "." + endpointurl.ClusterLocalSuffix
	if !strings.HasSuffix(host, suffix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimSuffix(host, suffix), ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
