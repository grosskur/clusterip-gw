# k8s_clusterip_gw

## Name

*k8s_clusterip_gw* - synthesizes `A` records for `Gateway` objects managed by `clusterip-gw`.

## Description

The plugin watches `gateway.networking.k8s.io/v1` `Gateway` objects and answers
queries under `gw.cluster.local.` using IPv4 values from `Gateway.status.addresses`.

It only publishes records for Gateways with:

- `spec.gatewayClassName: clusterip-gw`
- a non-deleting object
- at least one valid IPv4 address in `status.addresses`

For a Gateway named `demo` in namespace `default`, the plugin serves:

- `demo.default.gw.cluster.local. IN A`

Multiple IPv4 status addresses become multiple `A` records.

## Syntax

```txt
k8s_clusterip_gw [ZONE] {
    ttl SECONDS
}
```

- `ZONE` defaults to `gw.cluster.local`
- `ttl` defaults to `30`

## Examples

```txt
.:53 {
    ready
    k8s_clusterip_gw gw.cluster.local
    kubernetes cluster.local in-addr.arpa ip6.arpa
    forward . /etc/resolv.conf
}
```

## Ready

This plugin does not report readiness to the *ready* plugin. If its Gateway
informer cache is not yet usable, queries under the managed zone return
`SERVFAIL` without blocking global CoreDNS readiness.
