# System Design

This document describes the end-to-end design of the current `clusterip-gw` system.

Related docs:

- [system-background.md](system-background.md)
- [system-requirements.md](system-requirements.md)
- [controller-design.md](controller-design.md)
- [agent-design.md](agent-design.md)

## Overview

The system is a two-binary design:

- `clusterip-gw-controller` is the control-plane reconciler
- `clusterip-gw-agent` is the node-local dataplane programmer

The integration point between them is the Kubernetes API, not a private RPC channel.

## Resource Flow

The intended flow is:

1. A `GatewayClass` named `clusterip-gw` identifies Gateways managed by this system.
2. A supported `Gateway` and attached `TCPRoute` select an `XEndpointSelector` backend.
3. The controller validates that object set.
4. The controller allocates a VIP from `ServiceCIDR` by creating an `IPAddress`.
5. The controller publishes the VIP in `Gateway.status.addresses`.
6. The controller creates one managed backend `EndpointSlice` per accepted Gateway listener.
7. The agent watches `Gateway` and managed `EndpointSlice` state.
8. The agent renders a full nftables table containing `numgen`-based DNAT load-balancing rules from the Gateway VIP and listener port to the ready backend Pod IPs and port.

## Controller-to-Agent Contract

The controller publishes the state the agent needs through two object families:

- `Gateway`
  - `spec.listeners` defines frontend ports
  - `status.addresses` carries the selected VIP
- managed `EndpointSlice`
  - labels identify the owning `Gateway` and listener
  - endpoints provide the ready backend Pod IPs
  - the slice port provides the backend TCP port

The agent trusts only objects that fit the supported contract. Unsupported or partial objects are ignored.

## Ownership Model

The controller owns:

- `IPAddress` objects labeled `ipaddress.kubernetes.io/managed-by=gateway.networking.x-k8s.io`
- gateway-owned backend `EndpointSlice` objects labeled `discovery.k8s.io/managed-by=clusterip-gw-controller.grosskur.github.io`
- Gateway API and `XEndpointSelector` status

The agent owns:

- the nftables table named by `--nftables-table-name`
- no Kubernetes status fields

This ownership split avoids direct controller-to-node mutation while still keeping the dataplane fully driven by declarative API state.

## Startup and Readiness

Each binary waits for its informers before advertising readiness.

- the controller waits for all control-plane caches it depends on
- the agent waits for both `Gateway` and `EndpointSlice` state

That means health endpoints reflect whether the process has enough local state to reconcile correctly, not just whether the process is running.

## Failure Model

The current design intentionally tolerates partial end-to-end progress:

- a Gateway may be accepted and allocated a VIP before dataplane programming happens
- a listener may remain unprogrammed when no ready backend exists
- the controller can succeed while the agent is still pending or misconfigured

This is why the controller keeps `Programmed=False` in this phase even after VIP allocation.

## Design Constraints

The design is intentionally constrained to keep the experiment small:

- a single IPv4 VIP per accepted Gateway
- stateless load balancing across the ready backends for each listener
- full-table nftables replacement rather than incremental rule patching
- no control-plane support for broader Gateway API or Service semantics

The system should stay within those constraints until the docs, tests, and code are explicitly broadened together.
