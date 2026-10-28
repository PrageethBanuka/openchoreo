# Progressive delivery with Endpoints

Four demos of progressive delivery, each built on one Endpoint. Every one of
them runs the same split for gateway (north-south) and in-cluster (east-west)
callers.

| Demo | Strategy | Question it answers | Real-world story |
| --- | --- | --- | --- |
| [Canary: rewriting trips in Go](canary-trips-rewrite/) | A testers header, then weights stepped 5 → 25 → 50 → 100%, with a rollback | Is the new code safe? | Uber rewrote its Schemaless sharding layer from Python to Go and moved traffic over by percentage |
| [Blue/green: the EU VAT switch](bluegreen-eu-vat/) | A header (or path) preview, then an all-at-once switch | Can every request change at one moment? | The EU One-Stop Shop VAT rules took effect at 00:00 on 1 July 2021 |
| [A/B: cart recommendations](ab-cart-recommendations/) | Cookie groups, optionally mobile only | Is the new feature better? | Amazon's "customers who bought this also bought" in the cart |
| [Mirror: shadowing the trips rewrite](mirror-trips-shadow/) | Copies of 10%, then 100% of requests, then a canary | Does the new code survive real traffic before anyone depends on it? | Dark launches (Google SRE); Twitter's Diffy |

## What the Endpoint gives you

- **One Endpoint, several components.** The Endpoint lists each version as a
  backend (`spec.backendRefs`) with a weight. A canary adds a backend; it doesn't
  need a new Endpoint.
- **Path, header and cookie routing on a backend.** A backend's `match` sends
  every request that meets all of its conditions to that backend, whatever the
  weights; everything else is split by weight. A backend at weight 0 with a
  match is a preview. A backend's `mirror` copies a share of the split to it
  and discards its answers.
- **Per-environment weights without a release.** A binding's
  `spec.backendOverrides` changes a backend's weight or mirror share in one
  environment, in seconds, and cuts no new release. Matches keep working while
  the split is switched or rolled back.
- **One split for every path.** The `http` ClusterEndpointType emits a stable
  Service named after the Endpoint, one Service per backend, a gateway route per
  visibility (with a NetworkPolicy letting that gateway reach the backends), and
  a mesh route when the DataPlane runs a mesh. A request is
  split once: by the gateway, or by the mesh when the DataPlane is annotated
  `openchoreo.dev/gateway-in-mesh: "true"`.
- **`autoDeploy`.** The Endpoint controller binds each new release to the
  first environment of the project's deployment pipeline.

## Running the demos

You need the local k3d setup with this branch's controller, the
[`http`](../getting-started/cluster-endpoint-types/http.yaml) ClusterEndpointType,
and, for the east-west path, a mesh on the data plane (see
[mesh-endpoint-types](../mesh-endpoint-types/README.md#setting-up-a-data-plane)).

```shell
kubectl apply -f samples/getting-started/cluster-endpoint-types/http.yaml
make -C samples/progressive-delivery images     # builds the apps, imports them into k3d
make -C samples/progressive-delivery canary      # or bluegreen, ab, mirror
make -C samples/progressive-delivery clean
```

Each `demo.sh` narrates and pauses between steps; `AUTO=1` runs it straight
through. `MESH=0` skips the in-cluster path on a data plane without a mesh.
`REQUESTS` sets how many requests each tally sends (default 100).
`OPTIONAL_PATH=1` adds the blue/green preview by path.

The apps are deliberately tiny. [`apps/trips-python`](apps/trips-python) is the
original trips service. [`apps/demo-go`](apps/demo-go) serves the Go rewrite,
the checkout and the storefront, chosen by `MODE`. Every response names its
backend in the `X-Served-By` header, which the tallies count.
