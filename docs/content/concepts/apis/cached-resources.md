# Cached resource API

!!! warning
    As of 0.29, this feature is of alpha-version quality. To use it, enable the `CachedAPIs` feature gate.

!!! note
    Only a cluster scoped ClusterCachedResource is supported for the time being.

A ClusterCachedResource object triggers replication of a user-defined resource from its workspace into kcp's [cache server](../sharding/cache-server.md), extending its [built-in resource set](../sharding/cache-server.md#built-in-resources) with custom types. This makes those resources available across shards, enabling implementors to build globally-aware kcp-native components. API providers can additionally expose replicated resources as read-only to consumer workspaces — see [Exporting ClusterCachedResources](#exporting-clustercachedresources).

## Resource replication

```yaml
apiVersion: cache.kcp.io/v1alpha1
kind: ClusterCachedResource
metadata:
  name: cpuflavors-v1
spec:
  group: cloud.example.com
  version: v1
  resource: cpuflavors
```

The snippet above shows an example where all `cpuflavors.v1.cloud.example.com` objects in the workspace are replicated to the cache. There are some constraints on what resources may be replicated:

- There may be only one ClusterCachedResource for a particular group-version-resource triplet in the workspace.
- The resource must be cluster scoped.
- The resource must not be a [built-in API](./built-in.md) or kcp system API belonging to `apis.kcp.io` group.
- The resource may be originating from a CRD or an APIBinding.

Once created, resource replication progress may be checked in ClusterCachedResource's status:

```yaml
apiVersion: cache.kcp.io/v1alpha1
kind: ClusterCachedResource
metadata:
  name: cpuflavors-v1
status:
  conditions:
  - lastTransitionTime: "2025-10-21T14:03:41Z"
    status: "True"
    type: IdentityValid
  - lastTransitionTime: "2025-10-21T14:03:42Z"
    status: "True"
    type: ReplicationStarted
  - lastTransitionTime: "2025-10-21T14:03:41Z"
    status: "True"
    type: ResourceValid
  identityHash: cd2eb0837...
  phase: Ready
  resourceCounts:
    cache: 8 # (1)
    local: 8 # (2)
```

1. `cache` resource count refers to the count of objects currently in cache for this ClusterCachedResource.
2. `local` resource count refers to the count of objects the ClusterCachedResource currently sees in its workspace.

The objects a ClusterCachedResource is watching are always replicated in the direction **from** ClusterCachedResource's workspace **into** cache. Note that this means the only way to modify the in-cache copies is to modify the original objects. In-cache objects can be then projected into a workspace as a read-only API. This is done by creating a respective APIExport with [ClusterCachedResource virtual resource](#exporting-clustercachedresources), and binding to it.

```mermaid
flowchart TD
    cacheServer["Cache server"]

    subgraph provider["API Provider Workspace"]
        cpuflavorsCRD["CPUFlavors CRD"]
        cpuflavorCRs["CPUFlavor objects..."]

        cpuflavorsClusterCachedResource["CPUFlavors ClusterCachedResource"]

        cpuflavorCRs -."From".-> cpuflavorsCRD
        cpuflavorsClusterCachedResource -.Watches.-> cpuflavorCRs
    end

    cpuflavorsClusterCachedResource -."Replicates CPUFlavor objects into".-> cacheServer
```

You can optionally configure the following additional aspects of a ClusterCachedResource:

- its identity
- resource selector

We'll talk about each of these next.

### ClusterCachedResource identity

Similar to the [APIExport identity](./exporting-apis.md#apiexport-identity) concept, there may be many ClusterCachedResources with the same group-version-resource triplet across the kcp installation. To differentiate between them and identify owners, a ClusterCachedResource object uses a unique identity key stored in a secret.

The identity **key** is considered a private key and should not be shared.

**Hash** calculated from that key, found at `.status.identityHash`, is considered a public key. APIExport's virtual resource definition expects this identity hash to be supplied when exporting a ClusterCachedResource.

By default, creating a ClusterCachedResource object triggers creation of an identity secret with a randomly generated key in its `key` data item. You can provide your own key by referencing your secret in the object's spec:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-cached-cpuflavors-identity
  namespace: default
stringData:
  key: "<Your identity key>"
---
apiVersion: cache.kcp.io/v1alpha1
kind: ClusterCachedResource
metadata:
  name: cpuflavors-v1
spec:
  identity:
    secretRef:
      name: my-cached-cpuflavors-identity
      namespace: default
  ...
```

### Selectors

ClusterCachedResource spec has an optional [`labelSelector`](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#label-selectors) field which can be used to shape the set of objects it picks up.

```yaml
apiVersion: cache.kcp.io/v1alpha1
kind: ClusterCachedResource
metadata:
  name: cpuflavors-v1
spec:
  group: cloud.example.com
  version: v1
  resource: cpuflavors
  labelSelector:
    cloud.example.com/visibility: Public
```

## Exporting ClusterCachedResources

You can project the replicated read-only objects of a ClusterCachedResource into a workspace using the standard APIExport-APIBinding relationship. Create an APIExport and define [virtual resource](./exporting-apis.md#virtual-resources) for the associated [ClusterCachedResourceEndpointSlice](#clustercachedresourceendpointslice). Consumers can then bind to it.

**Example user story:**

- You, the **service provider**, run a cloud _Cloud Co._, including a compute service.
- You offer a service of provisioning and running VM instances: users create an `Instance` object and voilà, they have a running VM!
    - But how do you design the API for configuring such a resource? How do you make your consumers know what configuration options are available, given the limited resources available in the cloud?
    - You've decided to offer different packages for CPUs, memory, storage, GPUs; these are represented as CRDs, e.g. `cpuflavors.cloud.example.com`, `memflavors.cloud.example.com`, with `cpu-small`, `cpu-medium`, `cpu-large`, `mem-medium`, `mem-large`, `mem-xlarge` respectively.
    - You've created a ClusterCachedResource for each flavor type.
    - You offer the service through an APIExport containing the main `instances.cloud.example.com` resource, as well as all flavors. These are exported as [virtual resources](./exporting-apis.md#virtual-resources).
- **Consumers** binding to your APIExport can list and get the available flavors from within their workspace (e.g. with `kubectl get cpuflavors`), and refer to them in their `Instance` spec. They cannot create, delete or otherwise modify the flavor objects in any way.

See an example usage at [github.com/kcp-dev/kcp/tree/main/config/examples/virtualresources](https://github.com/kcp-dev/kcp/tree/main/config/examples/virtualresources).

### ClusterCachedResourceEndpointSlice

The ClusterCachedResourceEndpointSlice tracks the consumers of the associated APIExport, and needs to be created when you want to export a ClusterCachedResource.

```mermaid
flowchart TD
    cacheServer["Cache server"]
    replicationVW["Replication VW"]

    subgraph provider["API Provider Workspace"]
        cpuflavorsClusterCachedResource["CPUFlavors ClusterCachedResource"]
        cpuflavorsClusterCachedResourceEndpointSlice["CPUFlavors ClusterCachedResourceEndpointSlice"]

        cpuflavorsClusterCachedResourceEndpointSlice --> cpuflavorsClusterCachedResource
    end

    replicationVW -."Reads from".-> cacheServer
    cpuflavorsClusterCachedResourceEndpointSlice -."Has an endpoint for".-> replicationVW
```

```yaml
apiVersion: cache.kcp.io/v1alpha1
kind: ClusterCachedResourceEndpointSlice
metadata:
  name: cpuflavors-v1
spec:
  clusterCachedResource:
    name: cpuflavors-v1 # (1)
  export:
    name: vm-provider # (2)
```

1. Name (and optionally the cluster path) of the ClusterCachedResource this endpoint slice is referencing.
2. Name (and optionally the cluster path) of the APIExport this endpoint slice is referenced by.

Both the `clusterCachedResource` and `export` references are immutable once set.

```yaml
apiVersion: apis.kcp.io/v1alpha2
kind: APIExport
metadata:
  name: compute.cloud.example.com
spec:
  resources:
  - group: cloud.example.com
    name: cpuflavors
    schema: v250801.cpuflavors.cloud.example.com # (1)
    storage:
      virtual:
        reference: # (2)
          apiGroup: cache.kcp.io
          kind: ClusterCachedResourceEndpointSlice
          name: cpuflavors-v1
        identityHash: cd2eb0837... # (3)
```

1. Resource schema must match the schema used by the resource in the associated ClusterCachedResource.
2. Reference to the ClusterCachedResourceEndpointSlice endpoint slice `cpuflavors-v1`.
3. Identity hash of the `cpuflavors-v1` ClusterCachedResource object.

A `virtual` storage definition needs (1) a reference to an [endpoint slice](./exporting-apis.md#endpoint-slices) object, and (2) a virtual resource identity. In the case of ClusterCachedResources, the endpoint slice is provided by ClusterCachedResourceEndpointSlice. The identity hash must match the one set in ClusterCachedResource's `.status.identityHash`.

```mermaid
flowchart TD
    subgraph provider["API Provider Workspace"]
        export["CPUFlavors APIExport"]
        schema["CPUFlavors APIResourceSchema"]
        crd["CPUFlavors CRD"]
        cpuflavorsClusterCachedResourceEndpointSlice["CPUFlavors ClusterCachedResourceEndpointSlice"]

        export --> schema
        export --> cpuflavorsClusterCachedResourceEndpointSlice
        schema -."Is equivalent to".-> crd
    end

    subgraph consumer1["Consumer Workspace"]
        binding["CPUFlavors APIBinding"]
        boundCPUFlavorVirtualResources["CPUFlavor objects..."]

        binding -."Projects CPUFlavors API from Replication VW".-> boundCPUFlavorVirtualResources
    end

    export --> binding
```

Note the APIResourceSchema referenced by the APIExport example above: the Replication VW follows that reference, and that schema is then used to serve the resource in consumers' workspaces. The provider must therefore ensure that it is kept in-sync with the schema of the original resource.

```sh
$ # In consumer ws.
$ kubectl get cpuflavors
NAME
cpu-small
cpu-medium
cpu-large
```
