---
description: >
  Horizontal Scaling of kcp through sharding
---

# Shards

To learn how to run a sharded environment for development, see [Running a Sharded Environment](../../developers/running-sharded.md).

Every kcp shard is hosting a set of logical clusters. A logical cluster is
identified by a globally unique identifier called a **cluster name**. A shard
serves the logical clusters under `/clusters/<cluster-name>`.

A set of known shards comprises a kcp installation.

## Root Logical Cluster

Every kcp installation has one logical cluster called `root`. The `root` logical
cluster holds administrational objects. Among them are shard objects.

## Shard Objects

The set of shards in a kcp installation is defined by `Shard` objects in
`core.kcp.io/v1alpha1`.

A shard object specifies the network addresses, one for external access (usually
some worldwide load balancer) and one for direct access (shard to shard).

## Logical Clusters and Workspace Paths

Logical clusters are defined through the existence of a `LogicalCluster` object
"in themselves", similar to a `.` directory defining the existence of a directory
in Unix.

Every logical cluster name `name` is a **logical cluster path**. Every logical
cluster is reachable through `/cluster/<path>` for every of their paths.

A `Workspace` in `tenancy.kcp.io/v1alpha1` of name `name` references a logical
cluster specified in `Workspace.spec.cluster`. If that workspace object resided
in a logical cluster reachable via a path `path`, the referenced logical cluster
can be reached via a path `path:name`.

## Canonical Paths

The longest path a logical cluster is reachable under is called the **canonical
path**. By default, all canonical paths start with `root`, i.e. they start in
root logical cluster.

The logical cluster object annotated with `kcp.io/path: <canonical-path>`.

Additional subtrees of the workspace path hierarchy can be defined by creating
logical clusters with `kcp.io/path` annotation _not_ starting in `root`. E.g.
a home workspace hierarchy could start at `home:<user-name>`. There is no need
for the parent (`home` in this case) to exist.

## Front Proxy

A front-proxy is aware of all logical clusters, their shard they live on,
their canonical paths and all `Workspaces`. Non canonical paths can be
reconstructed from the canonical path prefixes and the workspace names.

Requests to `/cluster/<path>` are forwarded to the shard via inverse proxying.

A front-proxy in its most simplistic (toy) implementation watches all shards.
Clearly, that's neither feasible for scale nor good for availability. A front-
proxy could alternative be backed by any kind of external database with the
right scalability and availability properties.

There can be one front-proxy in front of a kcp installation, or many, e.g. one
or multiple per region or cloud provider.

## The Admin Workspace (`:admin`)

Every shard serves the *Admin workspace* at `/services/admin`: an
installation-wide administrative view, reachable identically through any
shard. Its resources are aggregated views of shard-owned objects: all
`Shard` objects, and all `CoreIdentitySet` objects (see
[Core API identities](#core-api-identities)). The views are backed by the
cache server, where every shard's objects are replicated, so all shards
serve an identical view in a single `resourceVersion` space: consumers can
fail over between shards and resume watches with the same resource version.
Over time the Admin workspace is the home for further installation-wide
admin surfaces.

The view is writable for exactly one purpose: the allow-listed operational
fields, currently the `experimental.core.kcp.io/unschedulable` annotation
for cordoning and `spec.resourceLimits` for scheduling limits. Such a write
is validated, applied to the cache copy of the target shard, and flows from
the cache to the shard hosting the authoritative object, which applies it -
no component ever connects to another shard directly, and intent for a
temporarily unavailable shard parks in the cache until it reconnects.
Everything else on a `Shard` is read-only through the Admin workspace:
shards register themselves and own the rest of their configuration.

Because such a write only records intent, the owning shard acknowledges it
in status, which travels the other way (shard -> cache) and is therefore
proof that the round trip completed: the `Schedulable` condition for
cordoning, and the `ResourceLimitsApplied` condition - whose message names
the limits in force - for the scheduling limits.

The kcp workspace plugin exposes it as the reserved pseudo-workspace
`:admin`:

```sh
$ kubectl ws use :admin
Current workspace is ':admin' (aggregated view of all shards).
$ kubectl get shards
NAME      REGION      URL                      ...
root      us-east-2   https://127.0.0.1:6444   ...
shard-1   us-east-1   https://127.0.0.1:6445   ...
$ kubectl get coreidentitysets
NAME      KEYID      OBSERVED   CONSISTENT   KEYCHANGED   AGE
root      5c3e...    5c3e...    True         False        3d
shard-1   5c3e...    5c3e...    True         False        3d
```

Access requires membership in the `system:kcp:admin` group; the serving
path does not depend on the root shard. To reach the view through a
front-proxy, add a `/services/` path mapping; any shard can be the backend.

The front-proxy itself discovers shards through the Admin workspace: the
kubeconfigs given via `--shard-peer-kubeconfig` (falling back to the
`--root-kubeconfig` server) are seeds, used to bootstrap discovery and as a
last-resort fallback only. Every shard observed through the view joins the
failover peer set at runtime and decommissioned shards leave it; requests are
distributed across the discovered shards, not the seeds. With short-lived
shards the discovery channel therefore keeps working as long as any currently
registered shard is reachable, even after all seeds are gone, and stale seeds
do not receive traffic.

The `root` workspace keeps a familiar read-only view: the shard hosting the
root workspace runs a controller that mirrors the cache server's Shard
objects into `root` as representations, so `kubectl get shards` in `root`
continues to work. The representations are eventually consistent and
informational; the Admin workspace remains the authoritative surface and the
only place operational writes (cordoning, scheduling limits) are accepted.

`Shard` objects in the `root` workspace are protected by admission: shards
register themselves and own their objects, so all direct writes (create,
update including annotations, delete) by non-system users are denied with a
pointer at the Admin workspace. Shard configuration changes go through the
shard's own flags/deployment; operational actions - cordoning via the
`experimental.core.kcp.io/unschedulable` annotation and scheduling limits via
`spec.resourceLimits` - go through the Admin workspace.

## Consistency Domain

Every logical cluster provides a Kubernetes-compatible API root endpoint under
`/cluster/<path>` including its own discovery endpoint and their own set of
API groups and resources.

Resources under such an endpoints satisfy the same consistency properties as with
a full Kubernetes cluster, e.g. the semantics of the resource versions of one
resource matches that of Kubernetes.

Across logical clusters the resource versions must be considered as unrelated,
i.e. resource versions cannot be compared.

## Wildcard Requests

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

## Cross Logical Cluster References

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
`LogicalCluster` object initially. It picks a target shard at random from the
set of valid `Shard` objects matching `Workspace.spec.location.selector` (see
[`chooseShardAndMarkCondition`][choose-shard]), then generates an
optimistic logical-cluster name (a 16-character base36 hash of a random 32-byte
token — see [`randomClusterName`][random-name], which gives
P(collision) < 10⁻⁹ for 2²⁶ clusters) and tries to create the `LogicalCluster`
on that shard. On `AlreadyExists` it checks whether the existing object belongs
to this `Workspace`; if not, it drops the cluster-name annotation and the next
reconcile loop picks a new name and shard. The `Workspace`-hosting shard does
not maintain a continuous watch on the remote `LogicalCluster`; it relies on
the periodic reconcile of the workspace to observe initialization progress.

[choose-shard]: https://github.com/kcp-dev/kcp/blob/main/pkg/reconciler/tenancy/workspace/workspace_reconcile_scheduling.go#L240
[random-name]: https://github.com/kcp-dev/kcp/blob/main/pkg/reconciler/tenancy/workspace/workspace_reconcile_scheduling.go#L427

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

## Cache Server Replication

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

## Replication

Each shard pushes a restricted set of objects to the cache server. As the cache
server replication is costly, this set is as minimal as possible. For example,
certain RBAC objects are replicated in case they are needed to successfully
authorize bindings of an API, or to use a workspace type.

By the nature of replication, objects in the cache server can be old and
incomplete. For instance, the non-existence of an object in the cache server
does not mean it does not exist in its respective shard. The replication
could be just delayed or the object was not identified to be worth to replicate.

## Controllers and Sharding

### Where controllers run

Standard kcp controllers (workspace scheduler, APIBinding, APIExport endpoint
slices, replication, RBAC labelers, etc.) run on **every shard**. There is no
leader election across shards; each shard owns the logical clusters scheduled
to it and reconciles its own subset of objects through wildcard informers
scoped to that shard. This works because objects of a given logical cluster
live on exactly one shard.

### Dual-informer pattern: local + cache fallback

Most cross-shard-aware controllers run two informers per type:

- a **local** informer over the shard's own etcd, and
- a **global** informer over the cache server (via the
  `CacheKcpSharedInformerFactory`).

Lookups follow a "local first, cache fallback" pattern, typically via
[`indexers.ByPathAndNameWithFallback`][by-path-fallback]: try the local copy,
then the cached copy if the object lives on a different shard. The APIBinding
controller is a representative example — it resolves an `APIExport` reference
by trying the local informer and falling back to the cache.

[by-path-fallback]: https://github.com/kcp-dev/kcp/blob/main/pkg/indexers/indexers.go

### Cross-shard write pattern

!!! warning "Anti-pattern, tracked for removal"
    Today the workspace scheduler writes directly to a remote shard to create
    the `LogicalCluster`. This is the only common cross-shard write path in
    kcp and we want to replace it with a pull-based, cache-mediated handoff.
    Tracked in [kcp-dev/kcp#4054](https://github.com/kcp-dev/kcp/issues/4054).
    New controllers should not introduce additional cross-shard writes.

When a controller has to write to another shard (workspace scheduling is the
only common case today), it acquires an admin client targeted at that shard's
`spec.baseURL` (see [`createLogicalCluster`][create-lc]). There is no special
backoff or circuit breaker for cross-shard writes — failures bubble up as
queue requeues with the standard rate-limited backoff. This means cross-shard
writes are best kept rare and idempotent.

[create-lc]: https://github.com/kcp-dev/kcp/blob/main/pkg/reconciler/tenancy/workspace/workspace_reconcile_scheduling.go#L290

### Front-proxy routing

The front-proxy maintains an in-memory index built by the
[workspace-index controller][index-controller]: it watches `Shard` objects on
the root shard, then for each shard starts informers over its `Workspace` and
`LogicalCluster` objects. Requests to `/clusters/<path>/...` are resolved by
exact-match lookup against this index and reverse-proxied to the owning
shard. Unknown paths return 403. The proxy itself does not perform consistent
hashing or load balancing across shards; routing is purely a function of where
a logical cluster was scheduled.

[index-controller]: https://github.com/kcp-dev/kcp/blob/main/pkg/proxy/index/index_controller.go

## Failure Modes and Bottlenecks

### Shard unavailability

There is no automatic failover for an unhealthy shard. The
[`isValidShard`][is-valid] check is currently a no-op and shard health is
inferred only from request errors at the moment of access. Concretely:

- **Workspaces already scheduled** to the unavailable shard are unreachable
  through the front-proxy until it returns.
- **Workspace scheduling** continues to succeed for new workspaces as long as
  at least one valid shard is reachable; the random selector simply skips
  shards annotated `kcp.io/unschedulable`.
- **Bound APIs** on a workspace whose `APIExport` lives on an unavailable
  shard keep functioning: the CRD is materialized locally and the binding
  controller already has the schema cached. New bindings or schema updates
  will stall until the export's shard returns.
- **Cross-shard writes** (e.g. workspace creation racing against the target
  shard's outage) requeue and retry with the standard workqueue backoff.

[is-valid]: https://github.com/kcp-dev/kcp/blob/main/pkg/reconciler/tenancy/workspace/workspace_reconcile_scheduling.go#L423

### Logical cluster migration

Workspaces can be moved between shards using the logical cluster migration feature. This allows
rebalancing workspaces across shards or evacuating a shard for maintenance. During migration,
the workspace is temporarily unavailable. See [Logical Cluster Migration](migration.md) for
details.

### Cache server bottlenecks

The cache server is the only fan-in point for cross-shard data, which makes it
the most important scaling consideration in a kcp installation:

- **Cardinality is bounded by what a single apiserver can hold.** The cache
  server is an `apiextensions-apiserver` backed by etcd. The total count of
  replicated objects across all shards must fit in one such apiserver. As a
  rule of thumb, treat ~10,000 objects of any single replicated type as the
  threshold to start planning partitioning (see "Partitioning strategies"
  below).
- **Write amplification is N → 1.** Every shard pushes its replication-marked
  objects to the same cache server. Large numbers of frequently-updated
  replicated objects (e.g. webhook configurations or RBAC marked for
  replication) translate directly into write load on the cache.
- **Replication is eventually consistent.** Controllers reading from the
  cache informer can observe stale or temporarily missing objects.
  Reconciliation logic must tolerate this — never use the cache as the
  authoritative source for "does X exist?" decisions, and never compare cache
  resource versions to local resource versions.
- **Cache server unavailability degrades, not breaks.** Local informers and
  the local apiserver keep working; what stops is cross-shard visibility.
  New `APIBinding`s referencing exports on other shards will fail to resolve
  until the cache returns.

### Front-proxy bottleneck

The default front-proxy implementation watches `Shard`, `Workspace`, and
`LogicalCluster` objects across every shard. This is fine for tens of shards
but does not scale to large installations. Production deployments are
expected to back the proxy index with an external store rather than rely on
the toy in-memory implementation, or to run multiple regional proxies each
serving a subset of shards.

### Partitioning strategies

The only partitioning lever implemented today is **`Partition` and
`PartitionSet` objects**, which scope `APIExportEndpointSlice` consumption to
a subset of shards (see [Partitions](partitions.md)). This reduces per-
controller informer load by letting an `APIExport`'s consumers talk only to
the shards in their partition, but it does not reduce cache-server
cardinality — every replicated object from every shard still lives in the
single cache server.

!!! warning "Future work, not implemented"
    Two further scaling levers are commonly discussed but **not built today**:

    - **Per-region (or per-tenant) cache servers** — currently the cache
      endpoint is a single global config (`--cache-kubeconfig`); there is no
      mechanism to map shards to different caches.
    - **Cache hierarchies** — the replication path is strictly
      shard → cache; there is no cache → cache forwarding, so a regional
      cache cannot promote a curated subset to a higher-level global cache.

    Both are tracked in
    [kcp-dev/kcp#4055](https://github.com/kcp-dev/kcp/issues/4055). Until
    they exist, the cache server is an installation-wide scaling ceiling and
    a single point of failure for cross-shard visibility.

## Bootstrapping

A new shard starting up will run a number of standard controllers (e.g. for
workspaces, API bindings and more). These will need a number of standard
informers both watching objects locally on that shard through wildcard informers
and watching the corresponding cache server.

Wildcard informers require the `APIExport` identity. The core APIs
(`tenancy.kcp.io`, `topology.kcp.io`, `shards.core.kcp.io`,
`migration.kcp.io`, `cache.kcp.io`) have their `APIExport` in the root
logical cluster, so every shard, the front-proxy and the virtual-workspaces
server need these identities before they can do any work. How they get them
is described next; after retrieving them the informers can be started.

## Core API identities

An identity hash is part of every cross-workspace request for a core API,
e.g. `/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces:<hash>`.
Historically the root shard created the identities randomly on its first
boot and every other component asked the root shard for them, so nothing
could start while the root shard was down.

### The core identities key

The shard flag `--core-identities-key-file` points to a PEM-encoded private
key (ECDSA, Ed25519 or RSA with at least 2048 bits) that is shared by **all
shards**. Each core API's identity is derived from the key and the API's
name, so every shard computes the same identities from the same key and no
shard has to ask the root shard. The format of the key file (PKCS#1, PKCS#8,
SEC1) does not matter, only the key itself.

If the flag is not set, `core-identities.key` in the root directory
(`.kcp`) is used and generated on first start, like the serving certificate
and `sa.key`, so a plain `kcp start` uses derived identities. A key generated
per shard derives different identities on every shard, so a deployment with
several shards or replicas passes the installation's key to all of them
explicitly, as it already does for the other key material.

In a real installation the key is usually the `tls.key` of a cert-manager
`Certificate`, mounted into every shard. Only the private key matters; the certificate is just a
convenient way to create and store the key. cert-manager creates a new key on
renewal by default, so set `rotationPolicy: Never`: changing the key means
changing identities.

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: kcp-core-identities
spec:
  secretName: kcp-core-identities
  commonName: kcp-core-identities
  issuerRef: {name: kcp-ca, kind: Issuer}   # any CA
  privateKey:
    algorithm: ECDSA
    size: 256
    rotationPolicy: Never   # changing the key means changing identities
```

The key must be dedicated to this purpose. It must not be a shard's serving
or client key, because those differ per shard and change over time. A leaked
key lets an attacker forge core identities; it is as sensitive as the
identity secrets in the `root` workspace.

### Existing identities never change

The key **never replaces an identity that already exists**. It only fills in
missing ones: all of them on a fresh install, and the ones of core APIs added
by a kcp upgrade. Existing installations keep exactly the same identities
after adding a key, with no required action.

Where each component gets its identities, in order of preference:

| Component | Sources |
|---|---|
| root shard | identity secrets already stored in `root` (seeded from `--root-identities-file` (dev only) and from the key before the APIExports are created) |
| other shards | the identities they already use (their `CoreIdentitySet`) → the deprecated `apiexport-identity-cache` ConfigMap → the cache server (the root workspace's APIExports and the other shards' `CoreIdentitySet`s) → the key → the root shard via `--root-shard-kubeconfig-file` (legacy) |
| virtual-workspaces | its shard's `CoreIdentitySet` → the ConfigMap → the cache server → the root workspace |
| front-proxy | the `CoreIdentitySet`s in `:admin` → the root shard via `--root-kubeconfig` (legacy) |

- Only shards get the key.
- An existing shard always has its own identities, so it restarts without
  the root shard or the cache server.
- A new shard gets identities from the cache server. If the cache server is
  down, it derives them from the key and stays not-ready (readiness check
  `core-identities`) until another shard confirms them.
- The front-proxy starts and reports itself live right away. It only becomes
  ready once it has the identities.

### `CoreIdentitySet`

Every shard publishes what it uses in a `CoreIdentitySet` named after the
shard, in its shard-local `system:shard` logical cluster. Only that shard
writes it, it has no `spec`, and it is readable before the shard has resolved
its identities. It is replicated to the cache server and all shards' objects
are visible together in the Admin workspace:

```sh
$ kubectl ws use :admin
$ kubectl get coreidentitysets
NAME      KEYID      OBSERVED   CONSISTENT   KEYCHANGED   AGE
root      5c3e...    5c3e...    True         False        3d
shard-1   5c3e...    5c3e...    True         False        3d
shard-2   5c3e...    9d10...    True         True         3d
```

```yaml
apiVersion: admin.kcp.io/v1alpha1
kind: CoreIdentitySet
metadata:
  name: shard-1
status:
  shard: shard-1
  keyID: 5c3e...                # the active key: the first key the shard saw
  observedKeyID: 5c3e...        # the key currently mounted
  exports:
  - name: tenancy.kcp.io
    identityHash: 0e2fc432...   # what this shard uses
    source: Legacy              # Legacy | Key | DevFile
  - name: cache.kcp.io          # added by an upgrade, filled in from the key
    identityHash: c71d0a9e...
    source: Key
    keyID: 5c3e...
    derivation: hkdf-sha256/v1
  conditions:
  - type: Consistent            # matches the root shard (or, without it, the other shards)
    status: "True"
  - type: KeyChanged            # the mounted key differs from the active key
    status: "False"
```

`keyID` and `derivation` record, per export, which key and which derivation
produced the identity. This metadata lives next to the identity, not inside
the hash: the hash stays a plain SHA-256 hex, the same format as every other
APIExport identity, because it is part of request URLs, storage keys and
labels.

The `CoreIdentitySet` replaces the `apiexport-identity-cache` ConfigMap,
which is deprecated and kept for one more release for older
virtual-workspaces servers. Direct writes to `CoreIdentitySet` objects by
non-system users are denied by admission.

### `APIIdentity`

Every APIExport identity in the installation, core or not, is listed as an
`APIIdentity` named after the identity value. The shard hosting the owning
export derives it into its `system:shard` logical cluster; all shards' objects
are replicated to the cache server and shown together in the Admin workspace:

```sh
$ kubectl ws use :admin
$ kubectl get apiidentities
NAME                          OWNER              CLUSTER   CORE    SHARED   LEGACY   AGE
system-tenancy.kcp.io         tenancy.kcp.io     root      true                      3d
2v9w7bq1rt2mdzn6-5f1c...      networking         2v9w...   false   True              2d
```

```yaml
apiVersion: admin.kcp.io/v1alpha1
kind: APIIdentity
metadata:
  name: 2v9w7bq1rt2mdzn6-5f1c...
spec: {}                            # operator knobs of the legacy conversion only
status:
  owner: {cluster: 2v9w7bq1rt2mdzn6, export: networking}
  groupResources:
  - {group: networking.io, resource: vnets}
  storageSharedWith:                # exports whose bindings sit on this prefix
  - export: {cluster: 1k3f..., export: compute}
    resources: [{group: networking.io, resource: vnets}]
  conditions:
  - type: StorageShared
    status: "True"
```

The object outlives its export: when the owner is deleted it stays with
`Orphaned: "True"` until no binding on the shard stores under the identity
any more, so that no other export can pin the identity while consumers' data
may still sit under it.

Identities that several exports hold, which was possible by copying an
identity secret, are reported with `LegacyShared: "True"` and
`status.legacyHolders`. With the `APIIdentityDecoupling` feature gate enabled
kcp converts them once: the owner (`spec.owner`, by default the oldest holder)
keeps the identity, every other holder gets a fresh one and keeps the shared
one as an alias in its `status.identityAliasHashes`, so permission claims and
wildcard requests naming the old identity keep resolving for the resources
that holder serves. `status.aliases[].references` counts the claims still
naming an alias; an alias is retired once no claim references it for a week,
or at once with `spec.retireAliases: true`. `spec.convert: false` holds a
conversion. Only `spec` may be written through the Admin workspace; everything
else is derived.

### Identities do not change

kcp continuously checks that every shard agrees with the root shard (or,
when the root shard's view is not visible, with the other shards) and reports
it in the `Consistent` condition. The rules:

1. An identity that is already in use never changes.
2. Identities for new core APIs (added by a kcp upgrade) can be added.
3. A `--root-identities-file` entry that disagrees with an existing identity
   is an error: the root shard keeps the existing identity, reports
   `Consistent=False` and not-ready until the configuration is fixed.
4. **The key changed** (for example, the certificate was re-keyed). Not
   fatal: the shard only needs the identities it already has. The shard
   keeps using its current identities, reports `KeyChanged=True`, and does
   not derive identities for new core APIs until the previous key is
   restored.

A shard that disagrees with the root shard reports `Consistent=False` and
not-ready. Core identities never change.

### `--root-identities-file`: development only

`--root-identities-file` bootstraps the identity secrets in `root` from a
plain-text YAML file. It works as before, but is meant for development and
tests only: it stores identity secrets in plain text in a config file. It is
deprecated and kept for one more release in favor of
`--core-identities-key-file`. Entries of the file win over the key; using
both logs a warning.
