# Agent Requirements

This document defines the supported behavior for `clusterip-gw-agent`.

Related docs:

- [agent-background.md](agent-background.md)
- [agent-design.md](agent-design.md)
- [system-requirements.md](system-requirements.md)

## Supported Inputs

The agent must consume only:

- `Gateway` objects
- controller-managed `EndpointSlice` objects

The agent must ignore objects outside the supported contract.

## Supported Gateway Requirements

The agent must only program Gateways that satisfy all of the following:

- namespace and name are set
- `spec.gatewayClassName=clusterip-gw`
- the Gateway is not deleting
- 1 to 10 listeners are present
- every listener is TCP
- every listener has a non-empty name
- every listener has a non-zero port
- listener names are unique
- listener ports are unique
- `status.addresses` contains exactly one `IPAddress` entry
- the published VIP is IPv4

Unsupported Gateways must produce no frontend programming.

## Supported EndpointSlice Requirements

The agent must only consume `EndpointSlice` objects that satisfy all of the following:

- `discovery.k8s.io/managed-by=clusterip-gw-controller.grosskur.github.io`
- Gateway name label is present
- Gateway namespace label is present, or the slice namespace is used
- Gateway listener label is used as part of the frontend key lookup
- `AddressType=IPv4`
- the slice has a port
- the slice port protocol is TCP or unset
- endpoint readiness is true when `conditions.ready` is present
- endpoint addresses parse as IPv4

All matching endpoints must be grouped by `(Gateway namespace, Gateway name, listener name)`.

Slices without a usable listener value do not join to any supported frontend and therefore contribute no programming.

## Programming Requirements

For each supported Gateway listener with at least one ready backend endpoint, the agent must:

- build the full ready backend list for that listener
- install one nftables jump rule in `prerouting`
- install one nftables jump rule in `output`
- install one per-listener service chain containing `numgen`-based dispatch
- install backend DNAT targets for each ready backend reachable from that dispatch path

If no ready backend exists for a supported listener, the agent must emit no rules for that listener.

## Sync And Readiness Requirements

The agent must:

- wait for both `Gateway` and `EndpointSlice` informer sync before reporting ready
- support periodic sync and event-driven sync
- respect `nftables.minSyncPeriod` as a lower bound between applies
- support a dry-run mode by disabling rule application

## Out Of Scope

The agent must not add support for:

- `Service` inputs
- IPv6 rules
- `inet` family tables
- UDP
- SCTP
- NodePort
- `postrouting`, SNAT, or masquerade
- filter chains
- session affinity
