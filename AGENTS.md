# AGENTS.md

## Repository intent

`clusterip-gw` is an experimental implementation for TCP-only `ClusterIP`
gateways.

The current scope is intentionally narrow. Keep that constraint visible in
reviews and changes:

- Linux only
- IPv4 only
- TCP only
- Gateway VIPs only
- Gateway API plus controller-managed backend state
- nftables DNAT programming only
- load balancing across ready backends per supported listener

Do not silently expand behavior to NodePort, LoadBalancer, ExternalIPs, IPv6,
UDP, SCTP, or session affinity. If a task explicitly broadens scope, update
docs and tests with the code change.

## Toolchain and commands

- Go version: `go 1.26` from `go.mod`
- Lint config: `.golangci.yml`
- Formatting config: `.yamlfmt.yml`
- Preferred task runner: `just` via `justfile`

Common local commands:

- `just build`
- `just test`
- `just lint`
- `just fmt`
- `go test ./...`
- `go build ./cmd/clusterip-gw-agent ./cmd/clusterip-gw-controller`
- `go run ./cmd/clusterip-gw-agent --kubeconfig ~/.kube/config`
- `go run ./cmd/clusterip-gw-controller --kubeconfig ~/.kube/config`
- `controller-gen object crd paths=./apis/v1alpha1 output:crd:artifacts:config=manifests/crds`

The README documents a repo-local tool-wrapper flow under `tools/bin`. Prefer
the existing `just` recipes when they cover the task rather than restating the
same shell workflow by hand.

Live end-to-end integration coverage is opt-in and environment-dependent:

- preferred disposable workflow: `just full-e2e`
- existing-cluster test: `CLUSTERIP_GW_RUN_E2E=1 CLUSTERIP_GW_KIND_CLUSTER=kind go test ./test/e2e -count=1 -run TestKindClusterTest000E2E`
- requirements: Linux, Docker, kind, kubectl, a compatible kubeconfig context,
  and local `clusterip-gw-agent:latest`, `clusterip-gw-controller:latest`, and
  `clusterip-gw-coredns:latest` images when not using `just full-e2e`

Repo-root `./clusterip-gw-*` binaries are gitignored local build artifacts. Do
not edit or commit them.

## Repository map

- `README.md`: primary operator and developer workflow guide
- `justfile`: canonical local build, test, lint, kind, and e2e recipes
- `cmd/clusterip-gw-agent/main.go`: agent CLI entrypoint
- `cmd/clusterip-gw-controller/main.go`: controller CLI entrypoint
- `internal/agent/app`: agent flag parsing, Linux startup path, health, and metrics servers
- `internal/agent/state`: reduced Gateway frontend and backend endpoint tracking
- `internal/agent/nftables`: nftables proxier, rendering, and netlink apply path
- `internal/controller`: GatewayClass, Gateway, TCPRoute, `XEndpointSelector`, VIP, and `EndpointSlice` reconciliation
- `internal/controller/app`: controller flag parsing, Linux startup path, health, and metrics servers
- `internal/kube/clientconfig`: shared client-go REST config construction
- `internal/gatewaymeta`: shared controller/agent labels and metadata constants
- `internal/apputil`: shared HTTP server and IPv4 bind-address helpers
- `apis/v1alpha1`: `XEndpointSelector` API type and generated deepcopy code
- `plugin/k8s_clusterip_gw`: CoreDNS plugin that serves Gateway VIP DNS records
- `docs/system-*.md`: end-to-end background, requirements, and design
- `docs/controller-*.md`: controller-specific background, requirements, and design
- `docs/agent-*.md`: agent-specific background, requirements, and design
- `manifests/agent`: agent DaemonSet and RBAC
- `manifests/controller`: controller Deployment and RBAC
- `manifests/coredns`: custom CoreDNS deployment, RBAC, and plugin config
- `manifests/crds`: generated CRD output for `XEndpointSelector`
- `manifests/test-000`: demo workload and Gateway API example objects
- `test/e2e`: opt-in kind-backed end-to-end coverage

## Change guidance

Keep the split between generic tracking code and nftables-specific rendering.
Avoid leaking Linux or netlink details into packages that are supposed to stay
source-agnostic.

When changing runtime defaults or flags, update all affected layers:

- `internal/agent/app/options.go`
- `internal/controller/app/options.go`
- `manifests/agent` and `manifests/controller` when deployed args change
- `README.md` when user-visible behavior changes
- tests covering option defaults or validation

When changing Gateway, TCPRoute, VIP allocation, or `XEndpointSelector`
behavior:

- update `docs/controller-design.md`
- update `docs/system-design.md` if the controller-agent contract changes
- add or adjust tests in `internal/controller`
- regenerate `manifests/crds` and deepcopy code if the `apis/v1alpha1` API changes

When changing Gateway/listener selection, endpoint handling, or nftables output:

- update `docs/agent-design.md`
- update `docs/system-design.md` if the published contract changes
- add or adjust tests near the touched agent package
- keep Linux-only code and tests behind build tags where appropriate

When changing Gateway VIP DNS behavior:

- update `plugin/k8s_clusterip_gw/README.md`
- add or adjust tests in `plugin/k8s_clusterip_gw`
- update `manifests/coredns` if plugin wiring or Corefile behavior changes

## Workflow expectations

- Read `README.md` before broad changes.
- Prefer small, package-local edits over large refactors.
- Preserve the intentionally reduced upstream shape in `internal/agent/state`
  and avoid pulling wider kube-proxy complexity into the repo unless the task
  requires it.
- Keep the docs split intact: Background explains context, Requirements states
  expected behavior, Design describes the implemented shape.
- Run targeted tests for the changed package, then `go test ./...` when practical.
