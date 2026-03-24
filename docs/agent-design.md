# Agent Design

This document describes the current `clusterip-gw-agent` dataplane design.

Related docs:

- [agent-background.md](agent-background.md)
- [agent-requirements.md](agent-requirements.md)
- [system-design.md](system-design.md)

## Runtime Shape

`clusterip-gw-agent`:

- builds Kubernetes and Gateway API clients
- starts informers for `Gateway` and `EndpointSlice`
- wires informer events into the nftables proxier
- reports ready only after both informer caches sync

The process also serves:

- a health endpoint that returns `ok` after initial sync
- a metrics endpoint
- `/proxyMode`, which returns `nftables`

## Tracking Model

The proxier keeps two independent snapshots:

- `GatewayChangeTracker` materializes supported Gateway frontend ports
- `EndpointsChangeTracker` materializes ready backend endpoints from managed `EndpointSlice` objects

The desired ruleset is derived by joining those snapshots on:

- Gateway namespace
- Gateway name
- listener name

TCP-only support is enforced by the supported Gateway and `EndpointSlice` filtering, not by a protocol field in the frontend key itself.

The agent never mutates Kubernetes objects. Its only external side effect is nftables application when `--apply-rules=true`.

## Gateway Frontend Derivation

For each supported Gateway, the tracker produces one frontend entry per listener.

The frontend identity is:

- namespace
- Gateway name
- listener name

The frontend address and port come from:

- `Gateway.status.addresses[0]`
- `Gateway.spec.listeners[*].port`

If the Gateway falls outside the supported subset, it produces no frontend entries.

## Endpoint Derivation

For each managed backend `EndpointSlice`, the endpoint tracker:

- resolves the owning frontend key from Gateway labels
- reads the backend port from the slice port
- collects ready IPv4 endpoint addresses
- sorts endpoints lexicographically by `IP:port`

The proxier keeps that full sorted ready backend list for each listener.

## Table Layout

`clusterip-gw-agent` manages exactly one nftables table:

```nft
table ip <tableName>
```

The family is fixed to `ip`, not `inet`, so the current design does not program IPv6 rules.

The table name comes from `--nftables-table-name`. The default is `clusterip-gw`.

## Chains

The proxier creates exactly two base chains:

```nft
chain prerouting {
  type nat hook prerouting priority dstnat; policy accept;
}

chain output {
  type nat hook output priority dstnat; policy accept;
}
```

For every supported listener with at least one ready backend, the proxier also creates one regular service chain and one backend chain per ready backend:

```nft
chain svc_<hash> {
  numgen random mod <backendCount> vmap @map_<hash>
}

chain svc_<hash>_be0 {
  dnat to <endpointIP>:<endpointPort>
}
```

It also creates one per-listener verdict map:

```nft
map map_<hash> {
  type integer : verdict
  elements = { 0 : jump svc_<hash>_be0 }
}
```

## Chain Naming

Per-listener service chains are named:

```text
svc_<hex>
```

`<hex>` is built by:

1. taking the frontend identity string
2. hashing it with SHA-256
3. taking the first 8 bytes
4. encoding those bytes as 16 lowercase hex characters

The frontend identity string is currently:

- `namespace/name:listenerName`

The numeric listener port is not part of the hash input.

The corresponding dispatch map and backend chain names reuse the same hash:

- `map_<hex>`
- `svc_<hex>_be<index>`

## Matching Rules

For each supported listener with at least one ready backend, the agent installs:

```nft
ip daddr <gatewayVIP> tcp dport <listenerPort> jump svc_<hash>
```

in both `prerouting` and `output`.

Each programmed listener therefore yields:

- one jump rule in `prerouting`
- one jump rule in `output`
- one dedicated service chain
- one dedicated verdict map
- one backend chain per ready backend
- one `dnat` statement inside each backend chain

## Backend Selection

The dataplane load balances across the current ready backend list.

For each listener it:

1. builds the ready backend list from controller-managed `EndpointSlice`
2. sorts endpoints lexicographically by `IP:port`
3. creates one backend chain per sorted backend
4. creates a verdict map from backend index to backend chain jump
5. emits `numgen random mod <backendCount>` in the service chain and looks the result up in that verdict map

If no ready backend exists, the listener contributes no rules.

## Sync Model

The proxier waits for both `Gateway` and `EndpointSlice` informer sync before it considers itself ready or applies rules.

After startup it syncs:

- periodically, using `nftables.syncPeriod`
- on informer change notifications
- but never more often than `nftables.minSyncPeriod`

When a change arrives before `minSyncPeriod` expires, the proxier records that a sync is pending and retries after the remaining delay.

## Apply Model

The agent does not patch rules incrementally.

On each apply it:

1. renders the full managed ruleset in memory
2. deletes the managed table if it exists
3. recreates the table, base chains, service chains, backend chains, dispatch maps, and jump rules in one batched netlink transaction

This is a full-table replace model.

When `--apply-rules=false`, the proxier still renders the desired ruleset and advances its sync loop, but it skips the nftables netlink apply.

## Example

For a Gateway:

- namespace: `default`
- name: `demo`
- listener name: `tcp`
- listener port: `80`
- VIP: `10.96.0.10`

and ready backends:

- endpoints: `10.0.0.2:80`, `10.0.0.3:80`

the managed structure looks like:

```nft
table ip clusterip-gw {
  chain prerouting {
    type nat hook prerouting priority dstnat; policy accept;
    ip daddr 10.96.0.10 tcp dport 80 jump svc_<hash>
  }

  chain output {
    type nat hook output priority dstnat; policy accept;
    ip daddr 10.96.0.10 tcp dport 80 jump svc_<hash>
  }

  chain svc_<hash> {
    numgen random mod 2 vmap @map_<hash>
  }

  map map_<hash> {
    type integer : verdict
    elements = {
      0 : jump svc_<hash>_be0,
      1 : jump svc_<hash>_be1
    }
  }

  chain svc_<hash>_be0 {
    dnat to 10.0.0.2:80
  }

  chain svc_<hash>_be1 {
    dnat to 10.0.0.3:80
  }
}
```
