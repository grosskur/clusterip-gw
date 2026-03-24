# System Requirements

This document captures the end-to-end requirements for the current `clusterip-gw` scaffold.

Related docs:

- [system-background.md](system-background.md)
- [system-design.md](system-design.md)
- [controller-requirements.md](controller-requirements.md)
- [agent-requirements.md](agent-requirements.md)

## Scope Requirements

The system must remain within the current experimental scope:

- Linux only
- IPv4 only
- TCP only
- Gateway VIPs only
- Gateway API plus controller-managed backend state
- nftables DNAT only
- load balancing across ready backends per supported listener

The system must not silently expand to support:

- NodePort
- LoadBalancer
- ExternalIPs
- IPv6
- UDP
- SCTP
- session affinity

## Control Plane Requirements

The control plane must:

- accept only `GatewayClass/clusterip-gw` controlled by `grosskur.github.io/clusterip-gw`
- allocate exactly one IPv4 VIP per accepted Gateway from ready `ServiceCIDR` ranges
- record the reservation as a Kubernetes `IPAddress`
- publish the VIP in `Gateway.status.addresses`
- publish Gateway, listener, route, and `XEndpointSelector` status reflecting the supported subset
- manage gateway-scoped backend `EndpointSlice` objects for accepted listener bindings

The controller must leave dataplane programming to the agent and therefore must not claim `Programmed=True` in this phase.

## Dataplane Requirements

The dataplane must:

- consume `Gateway` and controller-managed `EndpointSlice` state
- ignore unsupported or incomplete objects
- translate each supported `(Gateway VIP, listener port)` into nftables DNAT rules
- load balance across the ready backend endpoints for each supported listener
- remove rules when a listener loses a valid VIP or ready backend

The dataplane must not depend on `Service` objects or kube-proxy-managed Service state.

## Component Boundary Requirements

The controller and agent must communicate only through Kubernetes API objects and shared label conventions.

The controller owns:

- `IPAddress` allocation and release
- status publication
- backend `EndpointSlice` creation and cleanup

The agent owns:

- frontend and endpoint snapshots derived from informers
- nftables rendering
- nftables application to the local node

Neither component should take over the other's responsibilities.

## Operational Requirements

Both binaries must:

- wait for required informer caches before reporting ready
- expose health endpoints
- support metrics endpoints
- run without component config files when deployed with the in-cluster manifests

The repo documentation must describe the current scope and component split in a way that matches the code and manifests.
