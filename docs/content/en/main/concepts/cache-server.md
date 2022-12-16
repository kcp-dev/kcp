---
title: "Cache Server"
linkTitle: "Cache Server"
weight: 1
description: >
  The cache server is a regular, Kubernetes-style CRUD API server with support of LIST/WATCH semantics.
---
### Purpose

The primary purpose of the cache server is to support cross-shard communication.
Shards need to communicate with each other in order to enable, among other things, migration and replication features.
Direct shard-to-shard communication is not feasible in a larger setup as it scales with n*(n-1).
The web of connections would be hard to reason about, maintain and troubleshoot.
Thus, the cache server serves as a central place for shards to store and read common data.
Note, that the final topology can have more than one cache server, i.e. one in some geographic region.

### High-level overview

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

### Running the server

The cache server can be run as a standalone binary or as part of a kcp server.

The standalone binary is in <https://github.com/kcp-dev/kcp/tree/main/cmd/cache-server> and can be run by issuing `go run ./cmd/cache-server/main.go` command.

To run it as part of a kcp server, pass `--cache-url` flag to the kcp binary.

### Client-side functionality

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

Not implemented at the moment

### Built-in resources

Out of the box, the server supports the following resources:

- `apiresourceschemas`
- `apiexports`
- `shards`

All those resources are represented as CustomResourceDefinitions and
stored in `system:cache:server` shard under `system:system-crds` cluster.

### Adding new resources

Not implemented at the moment.
Our near-term plan is to maintain a list of hard-coded resources that we want to keep in the cache server.
In the future, we will use the ReplicationClam which will describe schemas that need to be exposed by the cache server.

### Deletion of data

Not implemented at the moment.
Only deleting resources explicitly is possible.
In the future, some form of automatic removal will be implemented.

### Design details

The cache server is implemented as the `apiextensions-apiserver`.
It is based on the same fork used by the kcp server, extended with shard support.

Since the server serves as a secondary replica it doesn't support versioning, validation, pruning, or admission.
All resources persisted by the server are deprived of schema.
That means the schema is implicit, maintained, and enforced by the shards pushing/pulling data into/from the server.

#### On the HTTP level

The server exposes the following path:

`/services/cache/shards/{shard-name}/clusters/{cluster-name}/apis/group/version/namespaces/{namespace-name}/resource/{resource-name}`

Parameters:

`{shard-name}`: a required string holding a shard name, a wildcard is also supported indicating a cross-shard request

`{cluster-name}`: a required string holding a cluster name, a wildcard is also supported indicating a cross-cluster request.

`{namespace-name}`: an optional string holding a namespace the given request is for, not all resources are stored under a namespace.

`{resource-name}`: an optional string holding the name of a resource

For example:

`/services/cache/shards/*/clusters/*/apis/apis.kcp.dev/v1alpha1/apiexports`: for listing apiexports for all shards and clusters

`/services/cache/shards/amber/clusters/*/apis/apis.kcp.dev/v1alpha1/apiexports`: for listing apiexports for amber shard for all clusters

`/services/cache/shards/sapphire/clusters/system:sapphire/apis/apis.kcp.dev/v1alpha1/apiexports`: for listing apiexports for sapphire shard stored in system:sapphire cluster

#### On the storage layer

All resources stored by the cache server are prefixed with `/cache`.
Thanks to that the server can share the same database with the kcp server.

Ultimately a shard aware resources end up being stored under the following key:

`/cache/group/resource/{shard-name}/{cluster-name}/{namespace-name}/{resource-name}`

For more information about inner working of the storage layer,
please visit: TODO: add a link that explains the mapping of a URL to a storage prefix.
