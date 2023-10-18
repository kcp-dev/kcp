---
description: >
    Horizontal Scaling of kcp through sharding
---

# Shards

Every kcp shard is hosting a set of logical clusters. A logical cluster is
identified by a globally unique identifier called a *cluster name*. A shard
serves the logical clusters under `/clusters/<cluster-name>`.

A set of known shards comprises a kcp installation.

# Root Logical Cluster

Every kcp installation has one logical cluster called `root`. The `root` logical
cluster holds administrational objects. Among them are shard objects.

# Shard Objects

The set of shards in a kcp installation is defined by `Shard` objects in
`core.kcp.io/v1alpha1`.

A shard object specifies the network addresses, one for external access (usually 
some worldwide load balancer) and one for direct access (shard to shard).

# Logical Clusters and Workspace Paths

Logical clusters are defined through the existence of a `LogicalCluster` object
"in themselves", similar to a `.` directory defining the existence of a directory 
in Unix.

Every logical cluster name `name` is a *logical cluster path*. Every logical
cluster is reachable through `/cluster/<path>` for every of their paths.

A `Workspace` in `tenancy.kcp.io/v1alpha1` of name `name` references a logical
cluster specified in `Workspace.spec.cluster`. If that workspace object resided
in a logical cluster reachable via a path `path`, the referenced logical cluster
can be reached via a path `path:name`.

# Canonical Paths

The longest path a logical cluster is reachable under is called the *canonical
path*. By default, all canonical paths start with `root`, i.e. they start in
root logical cluster.

The logical cluster object annotated with `kcp.io/path: <canonical-path>`.

Additional subtrees of the workspace path hierarchy can be defined by creating
logical clusters with `kcp.io/path` annotation *not* starting in `root`. E.g.
a home workspace hierarchy could start at `home:<user-name>`. There is no need
for the parent (`home` in this case) to exist.

# Front Proxy

A front-proxy is aware of all logical clusters, their shard they live on,
their canonical paths and all `Workspaces`s. Non canoical paths can be 
reconstructed from the canonical path prefixes and the worksapce names.

Requests to `/cluster/<path>` are forwarded to the shard via inverse proxying.

A front-proxy in its most simplistic (toy) implementation watches all shards.
Clearly, that's neither feasible for scale nor good for availability. A front-
proxy could alternative be backed by any kind of external database with the
right scalability and availability properties.

There can be one front-proxy in front of a kcp installation, or many, e.g. one
or multiple per region or cloud provider.

# Consistency Domain

Every logical cluster provides a Kubernetes-compatible API root endpoint under
`/cluster/<path>` including its own discovery endpoint and their own set of 
API groups and resources.

Resources under such an endpoints satisfy the same consistency properties as with
a full Kubernetes cluster, e.g. the semantics of the resource versions of one
resource matches that of Kubernetes.

Across logical clusters the resource versions must be considered as unrelated,
i.e. resource versions cannot be compared.

# Wildcard Requests

The only exception to the upper rule are objects under a "wildcard endpoint"
`/clusters/*/apis/<group>/<v>/[namespaces/<ns>]/resource:<identity-hash>` per
shard. It serves the objects of the given resource on that shard across 
logical-clusters. The annotation `kcp.io/cluster` tells the consumer which
logical cluster each object belongs to.

The wildcard endpoint is privileged (requires `system:masters` group membership).
It is only accessible when talking directly to a shard, not through a 
front-proxy.

Note: for unprivileged access, virtual view apiservers can offer a highly
secured and filtered view, usually also per shard, e.g. for owners of APIs.

# Cross Logical Cluster References

Some objects reference other logical clusters or objects in other logical
clusters. These references can be by logical cluster name, by arbitrary logical
cluster path or by canonical path.

Referenced logical clusters must be assumed to live on other shard, maybe even
on shard of different regions or cloud providers.

For scalability reasons, it is usually not adequate to drive controllers by
informers that span all shard.

In other words, logical involving cross-logical-cluster referenced have a cost
higher than in-logical-cluster references and logic must be carefully planned:

For example, during workspace creation and scheduling the scheduler running on
the shard hosting the `Workspace` object will access another shard to create the
`LogicalCluster` object initially. It does that by choosing a random logical
cluster name (optimistically) and choosing a shard that name maps to (through
consistent hashing). It then tries to create the `LogicalCluster`. On conflict,
it can check whether the existing object belong the given `Workspace` object or 
not. If not, another name and shard is chosen, until scheduling succeeds. During
initialization the controller on the `Workspace` hosting shard will keep watching
the logical cluster on the other shard, with some exponential backoff. In other
words, the `Workspace` hosting shard does not continuously watch the object on
the other shard.

Another example is API binding, but it is different than workspace scheduling:
a binding controller running on the shard hosting the `APIBinding` object will
be aware of all `APIExport`s in the kcp installation through caching replication
(see next section). What is special is that this controller has all the 
information necessary to bind a new API and to keep bound APIs working even if 
the shard of the `APIExport` is unavailable.

Note: usually it a bad idea to create logic dependent on the parent workspace. If
such logic is desired, for availability and scalability reasons some kind of
replication is required. E.g. a child workspace must stay operation even if the
parent is not accessible.

# Cache Server Replication

The cache server is a special API server that can hold replicas of objects that
must be available globally in an eventual consistent way. E.g. the `APIExport`s
and `APIResourceSchemas` are replicated that way and made available to the 
corresponding controllers via informers.

The cache server holds objects by logical clusters, and it can hold objects from
many or all shards in a kcp installation, served through wildcard informers.
The resource versions of those objects have no meaning beyond driving the cache 
informers running in the shards.

Cache servers can be 1:1 with shards, or there can be shared cache servers, e.g.
by region.

Cache servers can form a cache hierarchy.

Controllers that make use of cached objects, will usually have informers against
local objects and against the same objects in the cache server. If the former
returns a "NotFound" error, the controllers will look up in the cache informers.

The cache server technique is only useful for APIs whose object cardinality 
across all shards does not go beyond the cardinality sensibly storable in a
kube-based apiserver.

Note that objects like `Workspace`s and `LogicalCluster`s fall not into that
category. This means that in particular the logical cluster canonical path
cannot be derived from cached `LogicalCluster`s. Instead, the cached objects
must hold their own `kcp.io/path` annotation in order to be indexable by that
value. This is crucial to implement cross-logical-cluster references by 
canonical path.

Note: the `APIExport` example assumes that there are never more than e.g. 10,000
API exports in a kcp installation. If that is not an acceptable constraint, 
other partitioning mechanism would be need to hold the number of `APIExport`
objects per cache server below the critical number. E.g. there could be cache
servers per big tenant, and that would hold only public exports and 
tenant-internal exports. A more complex caching hierarchy would make sure the
right objects are replicated, while the "really public" exports would only be a
small number.

# Replication

Each shard pushes a restricted set of objects to the cache server. As the cache
server replication is costly, this set is as minimal as possible. For example,
certain RBAC objects are replicated in case they are needed to successfully
authorize bindings of an API, or to use a workspace type.

By the nature of replication, objects in the cache server can be old and 
incomplete. For instance, the non-existence of an object in the cache server
does not mean it does not exist in its respective shard. The replication 
could be just delayed or the object was not identified to be worth to replicate.

# Bootstrapping

A new shard starting up will run a number of standard controllers (e.g. for
workspaces, API bindings and more). These will need a number of standard
informers both watching objects locally on that shard through wildcard informers
and watching the corresponding cache server.

Wildcard informers require the `APIExport` identity. This identity varies by
installation as it is cryptographically created during creation of the `APIExport`.

The core and tenancy APIs have their `APIExport` in the root logical cluster.
A new shard will connect to that root logical cluster in order to extract the
identies for these APIs. It will cache these for later use in case of a network
partition or unavailability of the root shard. After retrieving these identities
the informers can be started.
