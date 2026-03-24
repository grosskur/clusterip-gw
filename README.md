# clusterip-gw

Experimental implementation of TCP-only `ClusterIP` gateways.

* `clusterip-gw-controller` reconciles `Gateway`, `TCPRoute`, and
  `XEndpointSelector` resources and allocates VIPs from `ServiceCIDR`.

* `clusterip-gw-agent` watches the resulting `Gateway` and `EndpointSlice`
  objects and programs node-local nftables DNAT rules.

* `clusterip-gw-coredns` publishes `{gateway}.{namespace}.gw.cluster.local`
  records for Gateway VIPs.

Scope is intentionally narrow: Linux, IPv4, TCP-only, nftables.

No NodePort, LoadBalancer, UDP, SCTP, or session affinity.

## Prerequisites

The documented workflows require:

- Linux
- Docker
- `just`
- `kind`
- `kubectl`

If you want to use the repo-local tool stubs instead of installing those CLI
tools yourself, install `dotslash` and put it on your `PATH`, then run:

```bash
export PATH="$PWD/tools/bin:$PATH"
```

That makes the repo-managed wrappers for tools such as `just`, `kind`,
`kubectl`, `go`, and related helpers available on your `PATH`. Docker is still
required separately.

## Quick start

Use this flow for a disposable local kind cluster.

Create a cluster, install the required CRDs, scale down the stock CoreDNS
deployment so the custom `*.gw.cluster.local` records can resolve, deploy the
stack, apply the demo workload, and curl it from the client pod:

```bash
just create-kind
just install-gateway-api-crds
just scale-default-coredns
just push-kind
just apply-test-000
just curl-test-000
```

Run `just curl-test-000` a few times. The test server replies with
`Hello from <hostname>`, so the output shows which backend pod handled the
request.

Delete the disposable cluster when you are done:

```bash
just delete-kind
```

## Automated e2e

Run the disposable end-to-end workflow with:

```bash
just full-e2e
```

`full-e2e` creates a kind cluster, installs the Gateway API CRDs, scales down
the stock CoreDNS deployment, builds and loads the local images, deploys the
stack, runs `TestKindClusterTest000E2E`, and deletes the cluster.

## Just targets

| Target | Description |
|---|---|
| `just` | Show available recipes (`default`) |
| `just build` | Build all Go binaries |
| `just test` | Run the full test suite |
| `just lint` | Run `golangci-lint` with the repo config |
| `just fmt` | Run all formatting targets |
| `just fmt-go` | Format all Go packages |
| `just fmt-yaml` | Format manifests with `yamlfmt` |
| `just images` | Build all local container images with `docker buildx bake --load` |
| `just generate-crds` | Generate the `XEndpointSelector` CRD and deepcopy code with `controller-gen` |
| `just create-kind [cluster_name]` | Create a kind cluster from `manifests/kind/kind.yaml`; defaults to `kind` |
| `just delete-kind [cluster_name]` | Delete a kind cluster by name; defaults to `kind` |
| `just install-gateway-api-crds` | Install the Gateway API v1.5.0 experimental CRDs into the current cluster |
| `just scale-default-coredns [replicas]` | Scale the default `kube-system/coredns` Deployment; defaults to `0` |
| `just push-kind [cluster_name]` | Build images, load them into kind, apply `manifests`, restart workloads, and wait for rollout |
| `just apply-test-000` | Apply the `manifests/test-000/` demo workload |
| `just curl-test-000 [port]` | Exec into the `test-000` client pod and curl `server.test-000.gw.cluster.local`; defaults to port `80` |
| `just test-e2e [cluster_name]` | Run the opt-in live kind end-to-end test against an existing cluster after `push-kind` |
| `just full-e2e` | Run the full disposable workflow: create cluster, prepare it, deploy, test, and delete |

## Docs

Documentation uses a Background / Requirements / Design split:

| Component | Background | Requirements | Design |
|---|---|---|---|
| System | [system-background.md](docs/system-background.md) | [system-requirements.md](docs/system-requirements.md) | [system-design.md](docs/system-design.md) |
| Controller | [controller-background.md](docs/controller-background.md) | [controller-requirements.md](docs/controller-requirements.md) | [controller-design.md](docs/controller-design.md) |
| Agent | [agent-background.md](docs/agent-background.md) | [agent-requirements.md](docs/agent-requirements.md) | [agent-design.md](docs/agent-design.md) |

## License

[Apache-2.0](LICENSE)
