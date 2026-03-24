# Agent Background

`clusterip-gw-agent` is the node-local dataplane process for this scaffold.

Related docs:

- [agent-requirements.md](agent-requirements.md)
- [agent-design.md](agent-design.md)
- [system-background.md](system-background.md)

## Why A Separate Agent Exists

The controller can reserve VIPs and describe desired backends, but it should not push Linux-specific packet-processing details into the control plane.

The agent exists to keep those concerns separate:

- Kubernetes API reconciliation stays in the controller
- nftables programming stays on the node

That split keeps the repo closer to a kube-proxy-style model, where object selection and node-local packet steering are different layers.

## Why The Agent Watches `Gateway` And Managed `EndpointSlice`

The current system does not model frontend traffic through `Service` objects. The agent instead derives dataplane state from:

- `Gateway.spec.listeners`
- `Gateway.status.addresses`
- controller-managed backend `EndpointSlice` objects

That gives the agent just enough state to render DNAT rules without taking on broader Service semantics.

## Why nftables

The repo uses nftables because the current experiment is specifically about a minimal Linux DNAT dataplane. The agent therefore programs:

- one nftables table
- NAT hooks in `prerouting` and `output`
- per-listener service chains that dispatch to backend DNAT chains

This is intentionally smaller than kube-proxy's full nftables implementation.

## Why The Load Balancing Model Stays Simple

The agent load balances across the current ready backend list for each listener with nftables `numgen random`.

That still keeps the agent focused on:

- object tracking
- ruleset determinism
- end-to-end correctness for a narrow supported subset
- no session affinity or broader Service semantics

Richer traffic semantics are still future work, not implicit requirements of the current scaffold.
