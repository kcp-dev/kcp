---
description: >
  The cache server is a regular, Kubernetes-style CRUD API server with support of LIST/WATCH semantics.
---

# Cache Server

### Purpose

The primary purpose of the cache server is to support cross-shard communication.
Shards need to communicate with each other in order to enable, among other things, migration and replication features.
Direct shard-to-shard communication is not feasible in a larger setup as it scales with n*(n-1).
The web of connections would be hard to reason about, maintain and troubleshoot.
Thus, the cache server serves as a central place for shards to store and read common data.
Note, that the final topology can have more than one cache server, i.e. one in some geographic region.

### High-level Overview

The cache server is a regular, Kubernetes-style CRUD API server with support of LIST/WATCH semantics.
Conceptually it supports two modes of operations.
The first mode is in which a shard gets its private space for storing its own data.
The second mode is in which a shard can read other shards' data.

The first mode of operation is implemented as a write controller that runs on a shard.
It holds some state/data from that shard in memory and pushes it to the cache server.
The controller uses standard informers for getting required resources both from a local kcp instance and remote cache server.
Before pushing data it has to compare remote data to its local copy and make sure both copies are consistent.

The second mode of operation is implemented as a read controller(s) that runs on some shard.
It holds other shards' data in an in-memory cache using an informer,
effectively pulling data from the central cache.
Thanks to having a separate informer for interacting with the cache server
a shard can implement a different resiliency strategy.
For example, it can tolerate the unavailability of the secondary during startup and become ready.

### Running the Server

The cache server can be run as a standalone binary or as part of a kcp server.

The standalone binary is in <https://github.com/kcp-dev/kcp/tree/main/cmd/cache-server> and can be run by issuing `go run ./cmd/cache-server/main.go` command.

To run it as part of a kcp server, pass `--cache-url` flag to the kcp binary.

### Client-side Functionality

In order to interact with the cache server from a shard, the <https://github.com/kcp-dev/kcp/tree/main/pkg/cache/client>
repository provides the following client-side functionality:

[ShardRoundTripper](https://github.com/kcp-dev/kcp/blob/b739fa5b5c83fb2c43b631c6234264d5dd1fc6e4/pkg/cache/client/round_tripper.go#L54),
a shard aware wrapper around `http.RoundTripper`. It changes the URL path to target a shard from the context.

[DefaultShardRoundTripper](https://github.com/kcp-dev/kcp/blob/b739fa5b5c83fb2c43b631c6234264d5dd1fc6e4/pkg/cache/client/round_tripper.go#L128)
is a `http.RoundTripper` that sets a default shard name if not specified in the context.

For example, in order to make a client shard aware, inject the `http.RoundTrippers` to a `rest.Config`

```go
import (
  cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
  "github.com/kcp-dev/kcp/pkg/cache/client/shard"
)

// adds shards awareness to a client with a default `shard.Wildcard` name.
NewForConfig(cacheclient.WithShardRoundTripper(cacheclient.WithDefaultShardRoundTripper(serverConfig.LoopbackClientConfig, shard.Wildcard)))

// and then change the context when you need to access a specific shard and pass is when making a HTTP request
ctx = cacheclient.WithShardInContext(ctx, shard.New("cache"))
```

### Authorization/Authentication

The cache server can only be connected to using x509 client certificates
with the `system:masters` group. It does not support authz webhook,
service accounts or other means of authentication or authorization.

This is because the cache server is only meant to be used by the shards
directly, not by operators or humans.

The only exception are the health check endpoints, which are always
accessible without authentication.

### Built-in Resources

The cache server stores all replicated objects as CustomResourceDefinitions
in the `system:cache:server` shard under the `system:system-crds` cluster.

The set of object types replicated to the cache server is wired up by the
[main replication controller][replication-ctrl]. The full list as of today:

| Resource | API group | Filter — replicated when |
| --- | --- | --- |
| `apiexports` | `apis.kcp.io/v1alpha2` | always |
| `apiexportendpointslices` | `apis.kcp.io/v1alpha1` | always |
| `apiresourceschemas` | `apis.kcp.io/v1alpha1` | always |
| `apiconversions` | `apis.kcp.io/v1alpha1` | always |
| `cachedresources` | `cache.kcp.io/v1alpha1` | always |
| `cachedresourceendpointslices` | `cache.kcp.io/v1alpha1` | always |
| `shards` | `core.kcp.io/v1alpha1` | always |
| `workspacetypes` | `tenancy.kcp.io/v1alpha1` | always |
| `mutatingwebhookconfigurations` | `admissionregistration.k8s.io/v1` | always |
| `validatingwebhookconfigurations` | `admissionregistration.k8s.io/v1` | always |
| `validatingadmissionpolicies` | `admissionregistration.k8s.io/v1` | always |
| `validatingadmissionpolicybindings` | `admissionregistration.k8s.io/v1` | always |
| `logicalclusters` | `core.kcp.io/v1alpha1` | annotated `core.kcp.io/replicate` |
| `clusterroles` | `rbac.authorization.k8s.io/v1` | annotated `core.kcp.io/replicate` |
| `clusterrolebindings` | `rbac.authorization.k8s.io/v1` | annotated `core.kcp.io/replicate` |

User-defined types are added on top of this set via the
[CachedResource API](../apis/cached-resources.md).

Objects in clusters whose name starts with `system:` are excluded from
replication.

[replication-ctrl]: https://github.com/kcp-dev/kcp/blob/main/pkg/reconciler/cache/replication/replication_controller.go#L199

#### How RBAC objects are selected for replication

`ClusterRole` and `ClusterRoleBinding` are not replicated wholesale. A set of
"labeler" controllers stamps the `core.kcp.io/replicate` annotation onto only
the RBAC objects that are needed for cross-shard authorization decisions:

- The [APIs labeler][rbac-apis] marks `ClusterRole`s that grant `bind` on
  `apiexports` or any verb on `apiexports/content`, so a binding controller
  on another shard can authorize a new `APIBinding`.
- The [Core labeler][rbac-core] marks `ClusterRole`s granting non-resource
  URL access on logical clusters that themselves carry the replicate
  annotation.
- The [tenancy labeler][rbac-tenancy] marks `ClusterRole`s relevant to
  `WorkspaceType` use.
- The [`ClusterRoleBinding` labeler][rbac-crb] mirrors the annotation onto
  bindings whose `roleRef` points at a replicated `ClusterRole`, or whose
  subject is one of a small set of system-relevant principals.

The replication controller then uses the annotation as its filter, so
unrelated tenant RBAC stays local.

[rbac-apis]: https://github.com/kcp-dev/kcp/blob/main/pkg/reconciler/apis/replicateclusterrole/replicateclusterrole_controller.go
[rbac-core]: https://github.com/kcp-dev/kcp/blob/main/pkg/reconciler/core/replicateclusterrole/replicateclusterrole_controller.go
[rbac-tenancy]: https://github.com/kcp-dev/kcp/blob/main/pkg/reconciler/tenancy/replicateclusterrole/
[rbac-crb]: https://github.com/kcp-dev/kcp/blob/main/pkg/reconciler/cache/labelclusterrolebindings/labelclusterrolebinding_reconcile.go

#### What is intentionally not replicated

- **`Workspace` and most `LogicalCluster` objects.** Their cardinality
  (every workspace in the installation) would defeat the cache server's
  single-apiserver storage model. Cross-shard workspace lookups go through
  the front-proxy index instead.
- **`APIBinding`.** Bindings are local to the workspace that created them;
  the binding controller resolves the referenced `APIExport` via the cache
  but does not push the binding back.
- **`Partition` / `PartitionSet`.** These are configuration objects consumed
  in the workspace where they live and referenced by `APIExportEndpointSlice`.
- **Identity secrets.** A `CachedResource`'s identity secret is created
  locally per shard, not replicated.

### Adding New Resources

User-defined resources can be added to the cache server using the [CachedResource API](../apis/cached-resources.md). A CachedResource object triggers replication of a cluster-scoped resource from a workspace into the cache, making it available across shards alongside the built-in resource set.

### Deletion of Data

Not implemented at the moment.
Only deleting resources explicitly is possible.
In the future, some form of automatic removal will be implemented.

### Design Details

The cache server is implemented as the `apiextensions-apiserver`.
It is based on the same fork used by the kcp server, extended with shard support.

Since the server serves as a secondary replica it doesn't support versioning, validation, pruning, or admission.
All resources persisted by the server are deprived of schema.
That means the schema is implicit, maintained, and enforced by the shards pushing/pulling data into/from the server.

#### On the HTTP Level

The server exposes the following path:

`/services/cache/shards/{shard-name}/clusters/{cluster-name}/apis/group/version/namespaces/{namespace-name}/resource/{resource-name}`

Parameters:

`{shard-name}`: a required string holding a shard name, a wildcard is also supported indicating a cross-shard request

`{cluster-name}`: a required string holding a cluster name, a wildcard is also supported indicating a cross-cluster request.

`{namespace-name}`: an optional string holding a namespace the given request is for, not all resources are stored under a namespace.

`{resource-name}`: an optional string holding the name of a resource

For example:

`/services/cache/shards/*/clusters/*/apis/apis.kcp.io/v1alpha2/apiexports`: for listing apiexports for all shards and clusters

`/services/cache/shards/amber/clusters/*/apis/apis.kcp.io/v1alpha2/apiexports`: for listing apiexports for amber shard for all clusters

`/services/cache/shards/sapphire/clusters/system:sapphire/apis/apis.kcp.io/v1alpha2/apiexports`: for listing apiexports for sapphire shard stored in system:sapphire cluster

#### On the Storage Layer

All resources stored by the cache server are prefixed with `/cache`.
Thanks to that the server can share the same database with the kcp server.

Ultimately a shard aware resources end up being stored under the following key:

`/cache/group/resource/{shard-name}/{cluster-name}/{namespace-name}/{resource-name}`

For more information about inner working of the storage layer,
please visit: TODO: add a link that explains the mapping of a URL to a storage prefix.
