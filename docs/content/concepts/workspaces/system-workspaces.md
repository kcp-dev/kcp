---
description: >
  System workspaces are shard-local logical clusters with special meaning to kcp internals.
---

# System Workspaces

System workspaces are special logical clusters that exist on every shard (including the root
shard). They are used internally by kcp to store system-level resources and are not part of the
regular workspace hierarchy.

## Key Characteristics

All system workspaces share the following properties:

- **Naming convention:** All system workspace names are prefixed with `system:`.
- **Shard-local:** Each shard has its own independent copy of every system workspace.
- **Not user-accessible:** System workspaces are only accessible to shard-local admin users
  (e.g. members of `system:masters`). There is no `Workspace` object representing them and no
  validation that a given system workspace actually exists.
- **Cannot be migrated:** System workspaces are permanently bound to their shard and are never
  subject to workspace migration or inactivation.
- **Excluded from replication:** Objects stored in system workspaces are not replicated to the
  cache server.
- **No workspace content authorization:** The standard workspace content authorizer does not
  apply. Access requires global privilege (e.g. shard admin) that bypasses authorization entirely.

## System Workspaces Overview

The following system workspaces exist:

| Workspace | Purpose |
|-----------|---------|
| `system:admin` | Shard-scoped administrative resources (e.g. leader election leases) |
| `system:system-crds` | Central repository for all CRDs owned and managed by kcp |
| `system:bound-crds` | Storage for CRDs dynamically created from APIBindings |
| `system:shard` | Holds API bindings for root APIs required by every shard |
| `system:cached-crds` | Used by the cache server to manage CRDs for cached resources |

## `system:admin`

The `system:admin` workspace holds administrative objects scoped to the local shard. It is
accessible via `/clusters/system:admin`.

Its primary use is **leader election**: when kcp is deployed with multiple instances sharing the
same backing storage, controllers use `Lease` objects in `system:admin` to coordinate which
instance is the active leader. The relevant CLI flags are:

- `--enable-leader-election` – enables leader election for kcp controllers running in the
  `system:admin` workspace.
- `--leader-election-namespace` – the namespace within `system:admin` to use for the leader
  election `Lease`.
- `--leader-election-name` – the name of the `Lease` object.

The `system:admin` context in the generated admin kubeconfig uses a separate shard admin identity,
distinct from the kcp admin identity used for the `root` context.

## `system:system-crds`

The `system:system-crds` workspace is the central store for all CRDs that kcp itself owns and
manages. These CRDs are bootstrapped during kcp startup and should never be installed in any
other logical cluster.

This is where kcp's own APIs live – including `apis.kcp.io`, `core.kcp.io` and `cache.kcp.io`
resources. The exact list of CRDs is defined in `config/system-crds/bootstrap.go`.

### CRD Resolution Priority

When kcp resolves which CRDs are available in a given workspace, it uses a three-tier priority
system:

1. **System CRDs** (highest priority) – CRDs from `system:system-crds` are always included and
   override everything else.
2. **APIBinding CRDs** – CRDs from `system:bound-crds` that were created through APIBindings
   are added next, unless they conflict with a system CRD.
3. **Local workspace CRDs** (lowest priority) – CRDs installed directly in the workspace are
   only included if they do not conflict with system or APIBinding CRDs.

This priority system ensures that kcp's core APIs are always available and cannot be shadowed
by user-installed CRDs.

### Bootstrapping

System CRDs are bootstrapped during kcp startup. The bootstrap process continuously retries
until all CRDs are successfully created, blocking server startup until complete. This ensures
that the shard is fully functional before it begins serving requests.

## `system:bound-crds`

The `system:bound-crds` workspace stores CRDs that are dynamically created when users create
APIBindings. When an APIBinding references an APIExport, the API binding controller converts
the exported `APIResourceSchema` objects into CRDs and stores them in this workspace.

Each bound CRD is:

- **Named after the schema UID** of the originating `APIResourceSchema`.
- **Decorated with an identity hash annotation** so that the storage layer can assign the
  correct etcd resource prefix, keeping data from different APIExports separated.
- **Lifecycle-managed** – when an APIBinding is being deleted, the bound CRD is updated with
  a `DeletionTimestamp` and a terminating condition so that the `create` verb is suppressed
  while existing data can still be read.

## `system:shard`

The `system:shard` workspace holds essential API bindings that every shard needs to function.
During shard bootstrap, API bindings for the following API groups are created in this workspace:

- `shards.core.kcp.io`
- `tenancy.kcp.io`
- `topology.kcp.io`
- `cache.kcp.io`
- `migration.kcp.io`

These bindings ensure that every shard – including the root shard – has access to the root
APIs required for workspace scheduling, shard management and cache coordination. Additionally,
the shard bootstrap creates a default namespace and a `LogicalCluster` resource in this
workspace.

## `system:cached-crds`

The `system:cached-crds` workspace is specific to the **cache server**. It manages CRDs for
resources that are replicated across shards via the cache server.

The cache server's CRD lister operates with a two-tier lookup:

1. **System CRDs** from `system:system-crds` (same as the main kcp server).
2. **Synthetic CRDs** derived from `CachedResource` objects. These are not real CRDs stored in
   etcd, but are dynamically constructed from `CachedResource` metadata (resource kind, scope
   and identity hash).

This workspace is used only by the cache server and is not relevant to regular kcp operation.

## Cache Server System Resources

The cache server also bootstraps its own set of CRDs into `system:system-crds` (on the cache
server's shard, named `system:cache:server`). In addition to the CRDs that the main kcp server
bootstraps, the cache server's `system:system-crds` includes CRDs for:

- `logicalclustermigrations` (`migration.kcp.io`)
- `shards` (`core.kcp.io`)
- `cachedobjects` (`cache.kcp.io`)
- `workspacetypes` (`tenancy.kcp.io`)
- RBAC resources (`roles`, `clusterroles`, `rolebindings`, `clusterrolebindings`)
- Admission resources (`mutatingwebhookconfigurations`, `validatingwebhookconfigurations`,
  `mutatingadmissionpolicies`, `mutatingadmissionpolicybindings`,
  `validatingadmissionpolicies`, `validatingadmissionpolicybindings`)

These CRDs have their schemas stripped down (preserving unknown fields, no subresources) since
the cache server stores replicated copies and does not need full validation.
