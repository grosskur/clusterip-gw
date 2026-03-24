# Controller Design

`clusterip-gw-controller` is the control-plane binary for this scaffold.

Related docs:

- [controller-background.md](controller-background.md)
- [controller-requirements.md](controller-requirements.md)
- [system-design.md](system-design.md)

## Responsibilities

The controller owns three design concerns:

- `GatewayClass`, `Gateway`, and `TCPRoute` evaluation for the supported subset
- VIP allocation from `ServiceCIDR` into `IPAddress`
- gateway-scoped backend `EndpointSlice` management for `clusterip-gw-agent`

It does not program dataplane rules directly. The controller stops at API state:

- resource status updates
- owned `IPAddress` reservations
- owned `EndpointSlice` objects

## Runtime Shape

The controller process:

- builds Kubernetes, dynamic, and Gateway API clients
- starts informers for `GatewayClass`, `Gateway`, `TCPRoute`, `XEndpointSelector`, `Pod`, `EndpointSlice`, `IPAddress`, and `ServiceCIDR`
- waits for all informer caches to sync before reporting ready
- runs separate workqueues for `GatewayClass`, `Gateway`, `TCPRoute`, and `XEndpointSelector`

The reconcile loop is informer-driven. Changes to a `Gateway`, `TCPRoute`, `XEndpointSelector`, backend `Pod`, or managed `EndpointSlice` enqueue the relevant owning objects.

## Reconciliation Model

### `GatewayClass`

The controller only accepts:

- `GatewayClass` name `clusterip-gw`
- `GatewayClass.spec.controllerName` `grosskur.github.io/clusterip-gw`

When accepted, the controller writes:

- `Accepted=True`
- `SupportedVersion=True`

Unsupported classes are ignored unless they look previously managed, in which case status is repaired.

### `Gateway`

Gateway reconciliation proceeds in this order:

1. Ignore or clean up Gateways whose `spec.gatewayClassName` is not `clusterip-gw`.
2. On deletion, release owned `IPAddress` objects and remove the controller finalizer.
3. Ensure the controller finalizer `grosskur.github.io/clusterip-gw-ip-address-protection`.
4. Load the owning `GatewayClass` and attached `TCPRoute` objects.
5. Validate the Gateway listener set.
6. Evaluate attached routes listener by listener.
7. If at least one listener is usable, ensure a VIP reservation exists.
8. Publish `Gateway.status.addresses`, top-level conditions, and listener conditions.

Gateway validation is intentionally narrow:

- 1 to 10 listeners
- TCP only
- unique listener names
- unique listener ports
- no `spec.addresses`
- no unsupported `allowedRoutes` variations

Invalid listener schema rejects the whole Gateway. Within an otherwise valid Gateway, listeners are evaluated independently so a mixed-validity Gateway can still be accepted.

### `TCPRoute`

`TCPRoute` reconciliation writes only this controller's `status.parents` entries.

The accepted route subset is:

- same-namespace attachment only
- exactly one `parentRef`
- exactly one `TCPRoute` rule
- exactly one backend ref
- backend ref kind `XEndpointSelector`
- backend ref group `gateway.networking.x-k8s.io`
- backend ref namespace equal to the route namespace
- backend ref `port` required

The controller resolves the selected listener from `sectionName` or `port`, verifies that exactly one route is attached to that listener, and rejects ambiguous or unsupported bindings.

### `XEndpointSelector`

The controller treats `XEndpointSelector` as a backend source definition. For every accepted selector:

- validate `spec.selector` as a label selector
- find all accepted `TCPRoute` bindings that reference it
- list matching ready Pods in the selector namespace
- build one gateway-owned backend `EndpointSlice` per `(Gateway, listener)`

The controller writes only the `Accepted` condition on `XEndpointSelector.status.conditions`.

## VIP Allocation Design

VIP allocation uses Kubernetes networking APIs rather than a private store:

- `ServiceCIDR` provides eligible IPv4 ranges
- `IPAddress` is the authoritative reservation record

Allocation behavior:

1. List controller-owned `IPAddress` objects for the Gateway.
2. If one already exists, keep the lexicographically first and delete extras.
3. Otherwise, list ready IPv4 `ServiceCIDR` prefixes.
4. Build the set of currently taken `IPAddress` names.
5. Walk allocatable addresses in sorted CIDR order.
6. Claim an address by creating `IPAddress/<ip>`.
7. On `AlreadyExists`, treat the IP as taken and continue.

The reservation shape is:

- `metadata.name=<allocated-ip>`
- `ipaddress.kubernetes.io/ip-family=IPv4`
- `ipaddress.kubernetes.io/managed-by=gateway.networking.x-k8s.io`
- `spec.parentRef.group=gateway.networking.k8s.io`
- `spec.parentRef.resource=gateways`
- `spec.parentRef.namespace=<gateway-namespace>`
- `spec.parentRef.name=<gateway-name>`

Cleanup deletes all controller-owned `IPAddress` objects for the Gateway.

## Managed `EndpointSlice` Design

For each accepted `(Gateway, listener, XEndpointSelector)` binding, the controller creates one `discovery.k8s.io/v1` `EndpointSlice` in the Gateway namespace.

The desired slice shape is:

- owner reference: the `Gateway`
- `discovery.k8s.io/managed-by=clusterip-gw-controller.grosskur.github.io`
- labels naming the source `XEndpointSelector`, `Gateway`, and listener
- address type `IPv4`
- exactly one TCP port from the backend ref
- endpoints built from matching ready Pods

Gateway cleanup does not explicitly delete those slices. They are Gateway-owned objects, so the current design relies on owner references rather than a direct delete in the Gateway cleanup path.

Pod-to-endpoint conversion is intentionally reduced:

- only ready Pods are included
- only IPv4 `status.podIP` values are included
- terminating Pods are excluded
- `nodeName` is copied when present
- endpoints are sorted for stable slice output

If a selector is no longer referenced or becomes invalid, the controller deletes its managed slices.

## Status Design

The controller writes:

- `GatewayClass.status.conditions`
- `Gateway.status.addresses`
- `Gateway.status.conditions`
- `Gateway.status.listeners[*]`
- `TCPRoute.status.parents[*]`
- `XEndpointSelector.status.conditions`

`Programmed=True` is never set by the controller in this phase. Even after successful VIP reservation, status remains effectively:

- `Accepted=True`
- `Programmed=False`

with a message that dataplane programming is pending on the agent.

## Health and Readiness

The controller reports ready only after all informer caches sync.

- `healthz-bind-address` serves `ok` once the controller is ready
- `metrics-bind-address` serves Prometheus metrics
