# Controller Background

`clusterip-gw-controller` is the Gateway-oriented control-plane binary in this repo.

Related docs:

- [controller-requirements.md](controller-requirements.md)
- [controller-design.md](controller-design.md)
- [system-background.md](system-background.md)

## Why A Separate Controller Exists

The controller exists because a reserved VIP and a working dataplane are different concerns.

Kubernetes already has APIs for address-pool description and reservation:

- `ServiceCIDR` describes valid service-address ranges
- `IPAddress` records a claim on one exact IP

Those APIs do not automatically make a `Gateway` act like a Service VIP. A separate control-plane component is still needed to:

- decide which Gateways are supported
- claim a VIP
- publish status
- materialize backend state for the dataplane

## Why It Uses `IPAddress`

This controller shares Kubernetes' modern service-address pool model instead of inventing a private allocator.

The intended allocator mental model is:

- `ServiceCIDR` defines valid ranges
- `IPAddress` proves ownership of a single address
- controller reconciliation chooses and claims one candidate IP

That gives the repo a Kubernetes-native reservation object while still leaving room for a custom dataplane.

## Why It Creates Backend `EndpointSlice` Objects

The agent only needs a compact description of:

- which Gateway listener exists
- which backend TCP port to use
- which ready Pod IPs are eligible backends

Gateway-scoped `EndpointSlice` objects are the bridge. They let the controller resolve Gateway API and selector semantics once, then hand the dataplane a small, explicit backend view.

## Why The Supported Gateway API Shape Is Narrow

The controller does not attempt to implement broad Gateway API support. Instead, it restricts the model to a subset that is easy to reason about:

- one listener maps to one route
- one route maps to one selector backend
- one selector backend maps to ready Pods

That keeps the control-plane design aligned with the repo’s narrow scope and the agent’s simple nftables load-balancing dataplane.
