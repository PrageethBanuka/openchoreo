// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointreleasebinding

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/controller"
	endpointpipeline "github.com/openchoreo/openchoreo/internal/pipeline/endpoint"
)

const (
	addressKindAddress  = "Address"
	addressKindTakeOver = "TakeOver"
)

// resolveAddress returns the Endpoint's own address in the binding's
// environment. The host is the binding's hostname override, else the
// Endpoint's own hostname, else the environment's external gateway host in the
// form component URLs use (<environment>-<namespace>.<gateway host>). An own
// hostname must match the EndpointType's allowedHostnames. A non-empty problem
// means the address can't be served here.
func resolveAddress(binding *openchoreov1alpha1.EndpointReleaseBinding, in *reconcileInputs) (endpointpipeline.AddressContext, string) {
	addr := endpointpipeline.AddressContext{Path: in.release.Spec.Address.Path}

	host := binding.Spec.Hostname
	if host == "" {
		host = in.release.Spec.Address.Hostname
	}
	if host != "" {
		allowed := in.release.Spec.EndpointType.Spec.AllowedHostnames
		if !controller.HostMatches(host, allowed) {
			return addr, fmt.Sprintf("hostname %q is not allowed by %s %q; allowed: %v",
				host, in.release.Spec.EndpointType.Kind, in.release.Spec.EndpointType.Name, allowed)
		}
		addr.Host = host
		addr.OwnHostname = true
		return addr, ""
	}

	gateway := endpointpipeline.BuildEnvironmentContext(in.environment, in.dataPlane).Gateway
	gwHost := ""
	if gateway != nil && gateway.Ingress != nil && gateway.Ingress.External != nil {
		ext := gateway.Ingress.External
		switch {
		case ext.HTTPS != nil && ext.HTTPS.Host != "":
			gwHost = ext.HTTPS.Host
		case ext.HTTP != nil && ext.HTTP.Host != "":
			gwHost = ext.HTTP.Host
		}
	}
	if gwHost == "" {
		return addr, fmt.Sprintf("environment %q has no external gateway host; set a hostname", binding.Spec.Environment)
	}
	addr.Host = binding.Spec.Environment + "-" + binding.Namespace + "." + gwHost
	return addr, ""
}

// servedAddresses lists the host and path pairs the binding serves: the
// Endpoint's own address, and the taken-over component URL when there is one.
func servedAddresses(addr endpointpipeline.AddressContext, takeOver *endpointpipeline.URLContext) []openchoreov1alpha1.EndpointResolvedAddress {
	out := []openchoreov1alpha1.EndpointResolvedAddress{{Kind: addressKindAddress, Host: addr.Host, Path: addr.Path}}
	if takeOver != nil && takeOver.External != nil {
		t := openchoreov1alpha1.EndpointResolvedAddress{Kind: addressKindTakeOver, Host: takeOver.External.Host, Path: takeOver.External.Path}
		if t.Host != addr.Host || t.Path != addr.Path {
			out = append(out, t)
		}
	}
	return out
}

// addressConflict returns why the binding must not render, if another,
// older binding already serves one of its addresses, in any namespace, or a
// component's own route in the same environment serves its address without
// being taken over by this Endpoint. Gateway API would otherwise give the path
// to whichever route is oldest, silently.
func (r *Reconciler) addressConflict(
	ctx context.Context,
	binding *openchoreov1alpha1.EndpointReleaseBinding,
	release *openchoreov1alpha1.EndpointRelease,
	addrs []openchoreov1alpha1.EndpointResolvedAddress,
) (string, error) {
	var bindings openchoreov1alpha1.EndpointReleaseBindingList
	if err := r.List(ctx, &bindings); err != nil {
		return "", fmt.Errorf("list EndpointReleaseBindings: %w", err)
	}
	for i := range bindings.Items {
		other := &bindings.Items[i]
		if (other.Name == binding.Name && other.Namespace == binding.Namespace) || !other.DeletionTimestamp.IsZero() ||
			!olderBinding(other, binding) {
			continue
		}
		for _, theirs := range other.Status.Addresses {
			for _, ours := range addrs {
				if theirs.Host == ours.Host && theirs.Path == ours.Path {
					return fmt.Sprintf("%s%s is already served by Endpoint %s/%s in environment %q",
						ours.Host, ours.Path, other.Namespace, other.Spec.Owner.EndpointName, other.Spec.Environment), nil
				}
			}
		}
	}

	var rbs openchoreov1alpha1.ReleaseBindingList
	if err := r.List(ctx, &rbs, client.InNamespace(binding.Namespace)); err != nil {
		return "", fmt.Errorf("list ReleaseBindings: %w", err)
	}
	own := addrs[0]
	for i := range rbs.Items {
		rb := &rbs.Items[i]
		if rb.Spec.Environment != binding.Spec.Environment {
			continue
		}
		for _, ep := range rb.Status.Endpoints {
			if t := release.Spec.TakeOver; t != nil && t.Component.Name == rb.Spec.Owner.ComponentName && t.Component.Endpoint == ep.Name {
				continue
			}
			if servesHostPath(ep.ExternalURLs, own.Host, own.Path) || servesHostPath(ep.InternalURLs, own.Host, own.Path) {
				return fmt.Sprintf("%s%s is served by component %s (endpoint %s); take it over to move it to this Endpoint",
					own.Host, own.Path, rb.Spec.Owner.ComponentName, ep.Name), nil
			}
		}
	}
	return "", nil
}

// servesHostPath reports whether any of the gateway URLs is host+path.
func servesHostPath(urls *openchoreov1alpha1.EndpointGatewayURLs, host, path string) bool {
	if urls == nil {
		return false
	}
	for _, u := range []*openchoreov1alpha1.EndpointURL{urls.HTTPS, urls.HTTP, urls.TLS} {
		if u == nil {
			continue
		}
		p := u.Path
		if p == "" {
			p = "/"
		}
		if u.Host == host && p == path {
			return true
		}
	}
	return false
}

// olderBinding reports whether a was created before b, ties broken by
// namespace and name.
func olderBinding(a, b *openchoreov1alpha1.EndpointReleaseBinding) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	if a.Namespace != b.Namespace {
		return a.Namespace < b.Namespace
	}
	return a.Name < b.Name
}
