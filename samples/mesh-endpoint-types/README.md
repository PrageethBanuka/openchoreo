# Mesh EndpointTypes

These ClusterEndpointTypes serve an Endpoint to **east-west** (in-cluster)
callers only, through one service mesh each. Like the getting-started
[`http`](../getting-started/cluster-endpoint-types/http.yaml) type, each one
emits a stable Service named after the Endpoint (selecting the primary
backend's pods) and one Service per backend. It then renders its mesh's own
routing resource on the stable Service, so callers of
`<endpoint>.<data-plane namespace>.svc.cluster.local` get the Endpoint's rules
and split. They are opt-in samples, meant to ship with each mesh module.

| Type | Renders | Mesh |
| --- | --- | --- |
| [`mesh-gamma`](mesh-gamma.yaml) | Gateway API `HTTPRoute` with a Service parent (GAMMA) | Istio (ambient with a waypoint, or sidecars), Cilium 1.16+, Linkerd 2.14+ |
| [`istio-virtualservice`](istio-virtualservice.yaml) | Istio `VirtualService` | Istio with sidecars |
| [`cilium-cec`](cilium-cec.yaml) | `CiliumEnvoyConfig` | Cilium with `kubeProxyReplacement=true` and `envoyConfig.enabled=true` |

Their Endpoints set `visibility: [namespace]`. To serve a gateway too, with the
same split, use the `http` type: it composes the GAMMA route whenever the
DataPlane has a mesh.

## Routing by path, header or cookie

A backendRef can carry a `match` and a `mirror`. The binding controller turns
them into `${rules}` for the environment, and every type renders that list:

- One rule per backend with a `match`, in `backendRefs` order. A request that
  meets all of a match's conditions (a path, headers, a cookie) goes to that
  backend alone, whatever the weights; when several backends match, the first
  one wins. A backend at weight 0 with a match is a preview: only matching
  requests reach it.
- Then the default rule: every other request, split across the backends by
  weight, with a copy of a share of it sent to each backend that has a
  `mirror` (always a weight-0 backend without a match).
- A binding changes a backend's weight or mirror share for its environment
  with `spec.backendOverrides`, without cutting a new release. Only the default
  rule changes; matches keep working.

Each match comes ready to render. `m.conditions` holds its header conditions
with names lowercased (Istio requires lowercase keys), the cookie as an
escaped regular expression on the `cookie` header, and each header at most
once. `m.path`, when set, is relative to where the Endpoint is served: the
gateway route joins it to the Endpoint's path prefix, the mesh routes use it
as is, and `m.path.regex` matches the same paths segment by segment for
routing APIs whose prefix is a plain string prefix (Istio, Envoy). The
Endpoint webhook refuses a header matched twice, a `cookie` header next to a
cookie condition, header patterns RE2 can't compile, paths with empty, `.` or
`..` segments, and two backends with the same match, so every mesh routes the
same requests.

A type refuses what its mesh can't render. Linkerd has no request mirroring,
so `mesh-gamma` refuses mirrors on a Linkerd data plane.

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
2. **Tell OpenChoreo which mesh the data plane runs.** `mesh-gamma`,
   `cilium-cec` and the `http` type's mesh route check this annotation and
   refuse to render without it. It is a convention of this branch, not yet an
   OpenChoreo API. `istio-virtualservice` doesn't read it: installing that
   type is the opt-in, so only install it on Istio data planes.

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
HTTP workload endpoint named `http`, and `orders-v3` is a rewrite nobody
should depend on yet. In-cluster callers use the Endpoint's Service
`orders-api`. 10% of its traffic goes to v2, testers who send `x-canary: true`
always reach v2, and 5% of requests are copied to v3:

```yaml
apiVersion: openchoreo.dev/v1alpha1
kind: Endpoint
metadata:
  name: orders-api
  namespace: default
spec:
  owner:
    projectName: shop
  type:
    kind: ClusterEndpointType
    name: mesh-gamma
  visibility: [namespace]
  autoDeploy: true
  backendRefs:
    - name: stable
      component: { name: orders, endpoint: http }
      weight: 90
    - name: canary
      component: { name: orders-v2, endpoint: http }
      weight: 10
      match:
        headers: [{ name: x-canary, value: "true" }]
    - name: shadow
      component: { name: orders-v3, endpoint: http }
      weight: 0
      mirror: { percent: 5 }
---
apiVersion: openchoreo.dev/v1alpha1
kind: EndpointReleaseBinding
metadata:
  name: orders-api-development
  namespace: default
spec:
  owner: { projectName: shop, endpointName: orders-api }
  environment: development
  endpointRelease: <the Endpoint's status.latestRelease.name>
  backendOverrides:
    - { name: stable, weight: 50 }                # development runs 50/50; the release says 90/10
    - { name: canary, weight: 50 }
    - { name: shadow, mirror: { percent: 100 } }  # and copies every request to v3
```

With `autoDeploy`, the Endpoint controller creates the binding for the
pipeline's first environment; add `backendOverrides` to it.

To check the split, call the Service 100 times from a meshed pod and count the
answers:

```shell
kubectl exec -n <data-plane namespace> <client pod> -- sh -c \
  'for i in $(seq 1 100); do curl -s http://orders-api/; echo; done' | sort | uniq -c
```

Then send `-H 'x-canary: true'` and every answer should come from v2. Change
`backendOverrides[].weight` on the binding, and the split moves without a new
EndpointRelease.

## Progressive delivery

Canary, blue/green, A/B and mirror rollouts only edit the Endpoint's backends
(their weights, matches and mirrors) and each environment's binding
overrides, so they run unchanged on every type here. With `istio-virtualservice`, the split happens
in the caller's sidecar: callers use the stable Service
(`<endpoint>.<data-plane namespace>`) and need a sidecar, so enrol every
environment's data-plane namespace (`istio-injection=enabled`), not just the
first.
