---
description: >
  Sharding overview and best practices for deploying kcp with multiple shards.
---

# Sharding Overview

Sharding in kcp allows you to distribute workspaces across multiple kcp server instances (shards). This document explains the architectural implications of sharding and provides guidance for designing resilient multi-shard deployments.

## Key Concept: Capacity vs High Availability

**Important**: Without additional engineering effort from the platform team (building on top of kcp) at the code level, sharding in kcp is fundamentally a **capacity concept**, not a **high-availability concept**.

Adding more shards increases the total number of workspaces and resources your kcp deployment can handle, but it does not automatically provide redundancy or failover capabilities for individual workspaces or providers.

### Why Sharding Alone Doesn't Provide HA

Each workspace in kcp is allocated to exactly one shard. This allocation is static. A workspace does not automatically migrate or replicate across shards. If a shard becomes unavailable, all workspaces on that shard become unavailable until the shard recovers.

This has significant implications for provider workspaces and their consumers.

## Provider and Consumer Relationships

Consider a typical deployment topology with three shards (root, alpha, beta):

```
Shard: root                      Shard: alpha                     Shard: beta
├── root:provider (APIExport)    ├── root:consumer-alpha-1        ├── root:consumer-beta-1
├── root:consumer-root-1         └── root:consumer-alpha-2        └── root:consumer-beta-2
└── root:consumer-root-2
```

In this example:
- The `root:provider` workspace contains an `APIExport` that exposes APIs to consumer workspaces
- Consumer workspaces are distributed across all three shards and bind to this `APIExport` via `APIBindings`
- The provider workspace is allocated to **the root shard only**

### The Single Point of Failure Problem

When a provider workspace exists on a single shard, several components become potential single points of failure:

1. **APIExport Availability**: The `APIExport` resource lives on one shard. If that shard is down, the `APIExport` is inaccessible.

2. **APIExportEndpointSlice Access**: The `APIExportEndpointSlice` resources that advertise API endpoints are tied to the provider workspace's shard. Operators and controllers that need to discover these endpoints depend on this shard being available.

3. **Operator Behavior**: Operators typically watch specific URLs per shard. When an operator starts or restarts:
   - It queries the `APIExportEndpointSlice` to discover endpoints
   - If the provider's shard is unavailable, the operator cannot obtain the necessary URLs
   - This can cause the operator to fail entirely, even if the consumer workspaces it manages are on healthy shards

### Impact Scenario

If the root shard (hosting the provider) experiences downtime:

- Consumers on the root shard (`consumer-root-1`, `consumer-root-2`) are directly affected
- Consumers on alpha and beta shards may **also** be affected if:
  - Their operators need to reconnect to the provider's `APIExport`
  - Controllers need to refresh `APIExportEndpointSlice` information
  - Any component restarts and needs to rediscover provider endpoints

**Result**: A single shard failure can cascade into downtime affecting all shards if providers are not configured with resilience in mind.

## Mitigation Strategies

### Strategy 1: Accept and Plan for the Risk

For non-critical deployments or where simplicity is preferred:

- Document that provider availability is tied to a single shard
- Ensure the provider's shard has appropriate redundancy at the infrastructure level (node redundancy, persistent storage, etc.)
- Plan maintenance windows accordingly
- Monitor provider shard health proactively

### Strategy 2: Per-Shard Provider Deployment

Deploy independent provider instances on each shard:

```
Shard: alpha                              Shard: beta
├── root:provider-alpha (APIExport)       ├── root:provider-beta (APIExport)
├── root:consumer-alpha-1                 ├── root:consumer-beta-1
└── root:consumer-alpha-2                 └── root:consumer-beta-2
```

Each shard has its own provider workspace. Consumers bind to their local shard's provider.

**Advantages**:
- Shard failures are isolated
- No cross-shard dependencies for API access

**Disadvantages**:
- Multiple provider deployments to manage
- Data/state not shared between provider instances
- More complex operational model

### Strategy 3: Partitioned APIExportEndpointSlices

Partitions combined with `APIExportEndpointSlice` resources provide a mechanism to distribute API access across shards, 
enabling continued operation even when the provider's home shard is unavailable. 

In this scenarion "home shard" refers to the shard where the provider workspace (and its APIExport) is located and hosted.

#### How It Works

1. **Create partitions** that match your shard topology (one partition per shard)
2. **Create child workspaces** of the provider on each target shard
3. **Deploy `APIExportEndpointSlice`** resources in each child workspace, targeting the local partition

This creates shard-local entry points for API access that remain available even if the provider's home shard goes down.

#### Architecture

```
Shard Root                        Shard Alpha                      Shard Beta
├── root:provider (APIExport)     ├── root:provider:alpha          ├── root:provider:beta
│   └── cowboys APIExport         │   ├── Partition: alpha         │   ├── Partition: beta
├── root:consumer-alpha-1 ────────┤   └── APIExportEndpointSlice   │   └── APIExportEndpointSlice
├── root:consumer-alpha-2 ────────┤       (targets alpha)          │       (targets beta)
├── root:consumer-beta-1 ─────────┼───────────────────────────────►│
└── root:consumer-beta-2 ─────────┼───────────────────────────────►│
```

#### Setup Steps

**1. Create the provider and consumer workspaces:**

```bash
# Provider on root shard
kubectl ws create provider --location-selector name=root

# Consumers distributed across shards
kubectl ws create consumer-alpha-1 --location-selector name=alpha
kubectl ws create consumer-alpha-2 --location-selector name=alpha
kubectl ws create consumer-beta-1 --location-selector name=beta
kubectl ws create consumer-beta-2 --location-selector name=beta
```

**2. Create APIExport in the provider workspace:**

```bash
kubectl ws use provider
kubectl create -f config/examples/cowboys/apiresourceschema.yaml
kubectl create -f config/examples/cowboys/apiexport.yaml
```

**3. Bind consumers to the APIExport:**

```bash
kubectl ws use :root:consumer-alpha-1
kubectl kcp bind apiexport root:provider:cowboys --name cowboys

kubectl ws use :root:consumer-alpha-2
kubectl kcp bind apiexport root:provider:cowboys --name cowboys

# Repeat for beta consumers...
```

**4. Create per-shard child workspaces with partitions and APIExportEndpointSlices:**

```bash
# Create child workspaces on each shard
kubectl ws use :root:provider
kubectl ws create alpha --location-selector name=alpha
kubectl ws create beta --location-selector name=beta

# Setup alpha shard endpoint
kubectl ws use :root:provider:alpha
kubectl apply -f - <<EOF
apiVersion: topology.kcp.io/v1alpha1
kind: Partition
metadata:
  name: alpha
spec:
  selector:
    matchLabels:
      name: alpha
---
apiVersion: apis.kcp.io/v1alpha1
kind: APIExportEndpointSlice
metadata:
  name: cowboys-alpha
spec:
  export:
    name: cowboys
    path: root:provider
  partition: alpha
EOF

# Setup beta shard endpoint
kubectl ws use :root:provider:beta
kubectl apply -f - <<EOF
apiVersion: topology.kcp.io/v1alpha1
kind: Partition
metadata:
  name: beta
spec:
  selector:
    matchLabels:
      name: beta
---
apiVersion: apis.kcp.io/v1alpha1
kind: APIExportEndpointSlice
metadata:
  name: cowboys-beta
spec:
  export:
    name: cowboys
    path: root:provider
  partition: beta
EOF
```

#### Failure Behavior

With this setup, if the **root shard goes down**:

| Component | Status |
|-----------|--------|
| `root:provider` workspace | Unavailable |
| `root:consumer-alpha-*` workspaces | Still accessible (on alpha shard) |
| `root:consumer-beta-*` workspaces | Still accessible (on beta shard) |
| `root:provider:alpha` workspace | Still accessible (on alpha shard) |
| `root:provider:beta` workspace | Still accessible (on beta shard) |
| APIExportEndpointSlice on alpha | Returns alpha shard endpoints |
| APIExportEndpointSlice on beta | Returns beta shard endpoints |
| Virtual API access via alpha endpoint | **Works** - can list/get cowboys on alpha consumers |
| Virtual API access via beta endpoint | **Works** - can list/get cowboys on beta consumers |

**Example: Accessing APIs during root shard outage:**

```bash
# Direct access to alpha shard's virtual API endpoint
kubectl -s 'https://alpha.example.io:6443/services/apiexport/<identity>/cowboys/clusters/*' \
  get cowboys.wildwest.dev -A

# Returns cowboys from alpha shard consumers only
NAMESPACE   NAME
default     john-wayne
default     john-wayne
```

#### Key Benefits

- **Shard isolation**: Each shard has its own entry point to the virtual API
- **Continued operation**: Consumers remain functional even when the provider's home shard is down
- **Partition-aware routing**: Each APIExportEndpointSlice only returns endpoints for its target partition/shard

#### Limitations

- Requires creating child workspaces and partitions on each shard
- Cross-shard queries (e.g., listing all cowboys across all shards) require the root shard
- The provider's home shard is still needed for creating/modifying the APIExport itself

For a complete working example, see the [kcp-zheng production deployment guide](./production/kcp-zheng.md#optional-create-partitions).

### Strategy 4: Smart Caching in Operators

Operators can be designed to be more resilient to provider unavailability:

- **Cache APIExportEndpointSlice data**: Operators can cache endpoint information locally and continue operating with stale data during provider outages
- **Graceful degradation**: Design operators to continue serving existing workloads even when they cannot reach the provider
- **Retry with backoff**: Implement robust retry logic for provider connectivity issues
- **Health-aware routing**: If multiple provider endpoints exist, operators can route around unhealthy endpoints

This approach requires custom engineering in your operators but provides the most flexibility.

## Geo-Distributed Deployments

When deploying kcp across multiple regions or data centers, the sharding considerations become even more critical:

```
Region US-East (Shard: us-east)     Region EU-West (Shard: eu-west)
├── root:provider                   ├── root:consumer-eu-1
├── root:consumer-us-1              └── root:consumer-eu-2
└── root:consumer-us-2
```

In this topology:
- Cross-region latency affects API binding operations
- Region failures can impact consumers in other regions if they depend on a remote provider
- Network partitions between regions can cause split-brain scenarios

### Recommendations for Geo-Distribution

1. **Deploy providers in each region** where consumers exist
2. **Minimize cross-region dependencies** for critical paths
3. **Use region-local bindings** where possible
4. **Plan for network partition scenarios** in your operational runbooks

## Summary

| Approach | Complexity | Isolation | Data Consistency |
|----------|------------|-----------|------------------|
| Single provider | Low | None | Simple |
| Per-shard providers | Medium | Full | Requires sync strategy |
| Partitioned APIExportEndpointSlices | Medium | Partial | Consistent (single source of truth) |
| Smart caching | High | Partial | Eventually consistent |

When designing your kcp deployment:

1. **Understand that sharding equals capacity**, not automatic HA
2. **Map your provider-consumer dependencies** across shards
3. **Choose a mitigation strategy** appropriate for your availability requirements
4. **Test failure scenarios** to validate your design



## Building your platform

Remember, kcp provides the building blocks, but you'll need to design and implement the higher-level abstractions that make sense for your users and use cases. To understand how to leverage kcp's capabilities effectively, explore our additional documentation:

- [Concepts](../concepts/index.md) - a high level overview of kcp concepts
- [Workspaces](../concepts/workspaces/index.md) - a more thorough introduction on kcp's workspaces
- [kubectl plugin](./kubectl-plugin.md)
- [Authorization](../concepts/authorization/index.md) - how kcp manages access control to workspaces and content
- [Virtual workspaces](../concepts/workspaces/virtual-workspaces.md) - details on kcp's mechanism for virtual views of workspace content
