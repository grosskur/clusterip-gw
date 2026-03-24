# Controller Requirements

This document defines the supported behavior for `clusterip-gw-controller`.

Related docs:

- [controller-background.md](controller-background.md)
- [controller-design.md](controller-design.md)
- [system-requirements.md](system-requirements.md)

## Managed Objects

The controller must reconcile only Gateways that use:

- `GatewayClass` name `clusterip-gw`
- `GatewayClass.spec.controllerName=grosskur.github.io/clusterip-gw`

The controller must use these inputs:

- `GatewayClass`
- `Gateway`
- `TCPRoute`
- `XEndpointSelector`
- `Pod`
- `EndpointSlice`
- `ServiceCIDR`
- `IPAddress`

## Supported Gateway Requirements

The controller must accept only Gateways with:

- 1 to 10 listeners
- TCP listeners only
- non-empty listener names
- non-zero listener ports
- unique listener names
- unique listener ports
- no `spec.addresses`
- no unsupported `listener.allowedRoutes` configuration

If the listener set itself is invalid, the whole Gateway must be rejected.

If the listener set is valid, listeners must then be evaluated independently so that a Gateway can remain accepted when at least one listener is usable.

## Supported Route Requirements

For an attached `TCPRoute` to be supported, all of the following must hold:

- the route namespace matches the Gateway namespace
- the route has exactly one `parentRef`
- the route binds to exactly one listener by `sectionName` or `port`
- exactly one `TCPRoute` is attached to the selected listener
- the route has exactly one rule
- the rule has exactly one backend ref
- the backend ref group is `gateway.networking.x-k8s.io`
- the backend ref kind is `XEndpointSelector`
- the backend ref namespace matches the route namespace
- the backend ref sets `port`

Unsupported route shapes must be surfaced in listener and route status rather than silently ignored.

## VIP Allocation Requirements

For every accepted Gateway, the controller must:

- ensure exactly one owned IPv4 VIP reservation
- allocate from ready IPv4 `ServiceCIDR` ranges only
- represent the claim as `IPAddress/<ip>`
- label the reservation with:
  - `ipaddress.kubernetes.io/ip-family=IPv4`
  - `ipaddress.kubernetes.io/managed-by=gateway.networking.x-k8s.io`
- set `spec.parentRef` to the owning `Gateway`

On Gateway cleanup or loss of support, the controller must release owned reservations.

## EndpointSlice Requirements

For every accepted `(Gateway, listener, XEndpointSelector)` binding, the controller must create or update one managed `EndpointSlice` that:

- lives in the Gateway namespace
- is owned by the Gateway
- is labeled `discovery.k8s.io/managed-by=clusterip-gw-controller.grosskur.github.io`
- carries labels for Gateway namespace, Gateway name, listener name, and source `XEndpointSelector`
- uses `AddressType=IPv4`
- carries exactly one TCP port from the backend ref
- contains only ready, non-terminating, IPv4 Pod endpoints from the selector namespace

If the selector becomes invalid or unreferenced, the controller must delete its managed slices.

## Status Requirements

The controller must write:

- `GatewayClass.status.conditions`
- `Gateway.status.addresses`
- `Gateway.status.conditions`
- `Gateway.status.listeners[*]`
- `TCPRoute.status.parents[*]`
- `XEndpointSelector.status.conditions`

The controller must not set `Programmed=True` in this phase, because dataplane programming belongs to the agent.

## Out Of Scope

The controller must not add support for:

- user-specified static Gateway addresses
- multiple backend refs
- multiple attached routes per listener
- cross-namespace route attachment
- cross-namespace `XEndpointSelector` backends
- non-TCP listeners
- IPv6 VIPs
