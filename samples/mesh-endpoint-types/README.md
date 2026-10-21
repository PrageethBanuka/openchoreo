# Mesh EndpointTypes

These ClusterEndpointTypes route **east-west** (in-cluster) traffic through a
service mesh. Each one takes over a component's own Service: callers keep the
URL they already get from their connections
(`<component>.<data-plane namespace>.svc.cluster.local`), and the mesh splits
it across the Endpoint's backends. They are opt-in samples, not part of the
getting-started set, and are meant to ship with each mesh module.

| Type | Renders | Mesh |
| --- | --- | --- |
| [`mesh-gamma`](mesh-gamma.yaml) | Gateway API `HTTPRoute` with a Service parent (GAMMA) | Istio (ambient with a waypoint, or sidecars), Cilium 1.16+, Linkerd 2.14+ |
| [`istio-virtualservice`](istio-virtualservice.yaml) | Istio `VirtualService` | Istio with sidecars |
| [`cilium-cec`](cilium-cec.yaml) | `CiliumEnvoyConfig` | Cilium with `kubeProxyReplacement=true` and `envoyConfig.enabled=true` |

All three are `addressKind: Service`, so their Endpoints set `takeOver` and no
`address`.

## Traffic parameters

Every type accepts the same [traffic schema](traffic-schema.yaml) in
`spec.parameters`:

- **`rules`**: ordered rules, evaluated before the default split. The first
  matching rule wins. A rule matches on headers, a cookie, query parameters or
  the method, and sends the request to one or more backends by name.
- **`mirrors`**: copies a share of the default split's requests to a backend,
  usually a weight-0 one, and discards its responses.

The default split is `spec.backendRefs` by weight. A binding can change a
backend's weight for its environment with `spec.backendOverrides[].weight`,
without cutting a new release.

A type leaves out what its mesh can't render. Linkerd has no request mirroring,
so `mesh-gamma` refuses `mirrors` on a Linkerd data plane.

## Setting up a data plane

1. **Install the mesh and enrol the workloads.** Enrolment is the platform's
   job, not the EndpointType's:
   - **Istio sidecar:** `istioctl install --set profile=default`, then label the
     data-plane namespaces `istio-injection=enabled`.
   - **Istio ambient:** `istioctl install --set profile=ambient` (on k3d, follow
     Istio's [platform prerequisites](https://istio.io/latest/docs/ambient/install/platform-prerequisites/)).
     Label the data-plane namespace `istio.io/dataplane-mode=ambient`, then run
     `istioctl waypoint apply -n <ns> --enroll-namespace`. L7 routing in ambient
     needs a waypoint.
   - **Cilium:** install with `kubeProxyReplacement=true`,
     `envoyConfig.enabled=true`, `gatewayAPI.enabled=true` and `l7Proxy=true`.
2. **Tell OpenChoreo which mesh the data plane runs.** The types check this
   annotation and refuse to render without it:

   ```yaml
   apiVersion: openchoreo.dev/v1alpha1
   kind: DataPlane            # or ClusterDataPlane
   metadata:
     name: default
     annotations:
       openchoreo.dev/service-mesh: istio   # istio | cilium | linkerd
   ```

3. **Let the cluster agent apply the mesh kinds.** GAMMA HTTPRoutes are already
   allowed. VirtualServices and CiliumEnvoyConfigs need the opt-in RBAC in the
   data-plane chart:

   ```shell
   helm upgrade openchoreo-data-plane install/helm/openchoreo-data-plane --reuse-values \
     --set clusterAgent.rbac.serviceMesh.istio=true    # or .cilium=true
   ```

4. **Apply the types:** `kubectl apply -f samples/mesh-endpoint-types/mesh-gamma.yaml`
   (or the type for your mesh).

## Example

`orders` (v1) and `orders-v2` are two Components in project `shop`, each with an
HTTP workload endpoint named `http`. Callers already use the `orders` Service
URL. This Endpoint keeps that URL and sends 10% of its traffic to v2. Testers
who send `x-canary: true` always reach v2, and 5% of requests are mirrored to
v2:

```yaml
apiVersion: openchoreo.dev/v1alpha1
kind: Endpoint
metadata:
  name: orders
  namespace: default
spec:
  owner:
    projectName: shop
  type:
    kind: ClusterEndpointType
    name: mesh-gamma
  takeOver:
    component: { name: orders, endpoint: http }
  backendRefs:
    - { name: stable, component: { name: orders, endpoint: http }, weight: 90 }
    - { name: canary, component: { name: orders-v2, endpoint: http }, weight: 10 }
  parameters:
    rules:
      - name: testers
        matches:
          - headers: [{ name: x-canary, value: "true" }]
        backendRefs: [{ name: canary }]
    mirrors:
      - { backendRef: canary, percent: 5 }
---
apiVersion: openchoreo.dev/v1alpha1
kind: EndpointReleaseBinding
metadata:
  name: orders-development
  namespace: default
spec:
  owner: { projectName: shop, endpointName: orders }
  environment: development
  endpointRelease: <the Endpoint's status.latestRelease.name>
  backendOverrides:
    - { name: canary, weight: 50 }   # development runs 50/50; the release says 90/10
```

To check the split, call the Service 100 times from a meshed pod and count the
answers:

```shell
kubectl exec -n <data-plane namespace> <client pod> -- sh -c \
  'for i in $(seq 1 100); do curl -s http://orders:8080/; echo; done' | sort | uniq -c
```

Then send `-H 'x-canary: true'` and every answer should come from v2. Change
`backendOverrides[].weight` on the binding, and the split moves without a new
EndpointRelease.
