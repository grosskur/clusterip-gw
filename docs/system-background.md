# System Background

`clusterip-gw` is an experimental standalone scaffold for kube-proxy-like TCP routing around Gateway API-oriented inputs.

Related docs:

- [system-requirements.md](system-requirements.md)
- [system-design.md](system-design.md)
- [controller-background.md](controller-background.md)
- [agent-background.md](agent-background.md)

## Why This Repository Exists

The project explores a narrow split between control plane and dataplane:

- `clusterip-gw-controller` allocates Gateway VIPs and materializes backend state
- `clusterip-gw-agent` consumes that state and programs node-local nftables DNAT rules

This is intentionally not a full Gateway implementation and not a general-purpose Service proxy. The codebase keeps a small, explicit slice of functionality so the controller and dataplane behavior stay easy to inspect.

## Custom VIPs Versus Service ClusterIPs

The system uses Kubernetes `ServiceCIDR` and `IPAddress` APIs, but it does not create ordinary `Service` objects for traffic steering.

That distinction matters:

- a `Service` gives you stock Kubernetes ClusterIP behavior
- an `IPAddress` only reserves an address
- the actual forwarding behavior still has to be implemented elsewhere

In this repo, the controller owns the reservation and the agent owns the forwarding rules.

## Control Plane Inputs

The current system spans two sets of Kubernetes resources.

Shared or end-to-end objects:

- `GatewayClass`
- `Gateway`
- `TCPRoute`
- controller-managed `EndpointSlice`
- `ServiceCIDR`
- `IPAddress`

Controller-only inputs:

- `XEndpointSelector`
- `Pod`

The controller uses those inputs to decide whether a Gateway is supported, which VIP it should own, and which backend Pod IPs should become slice endpoints for each accepted listener.

## Dataplane Background

The agent does not watch `Service` objects. Instead, it reads:

- `Gateway.status.addresses`
- controller-managed `EndpointSlice` objects

That allows the dataplane to stay narrowly focused on Gateway VIP DNAT rather than on the full Kubernetes Service model.

The current dataplane is based on nftables:

- Linux only
- IPv4 only
- TCP only
- stateless load balancing across ready backends per listener

This is deliberately much smaller than kube-proxy.

## Architectural Split

The repo is organized around clear ownership boundaries:

- `internal/controller/...` owns Gateway API reconciliation and allocator behavior
- `internal/agent/state/...` owns reduced frontend and endpoint tracking
- `internal/agent/nftables/...` owns Linux nftables rendering and apply logic
- `internal/agent/...` wires the agent process and health/metrics servers

That split is part of the experiment. The controller should not leak Linux or netlink details, and the agent should not take over control-plane allocation or status responsibilities.

## Current Scope Philosophy

The important background constraint for all docs and code is that the repo is intentionally narrow.

It is not trying to silently grow into support for:

- NodePort
- LoadBalancer
- ExternalIPs
- IPv6
- UDP
- SCTP
- session affinity

Those omissions are design constraints, not missing polish.
