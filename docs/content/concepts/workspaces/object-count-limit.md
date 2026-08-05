---
description: >
  Enforce a hard limit on the total number of objects in a workspace.
---

# Total Object Count Limit

kcp can enforce a hard limit on the total number of objects (across all resource types)
in a logical cluster. This protects a shard from a single workspace creating so many
objects that it degrades or takes down the shard. It complements the standard Kubernetes
`ResourceQuota` support, which can only limit counts per resource type
(`count/<resource>.<group>`), not the total across all types.

## Configuration

Enforcement is off by default and can be enabled two ways:

* **Shard-wide default**: start kcp with `--logical-cluster-total-object-limit=<N>`.
  Every logical cluster on the shard is then limited to `N` objects, unless overridden
  per logical cluster. The default does **not** apply to the `root` and `system:*`
  logical clusters: the shard writes bootstrap content there, and a default limit
  must never break shard bootstrapping or upgrades. Use the annotation to bound
  `root` explicitly if desired.
* **Per logical cluster**: set the `core.kcp.io/max-total-objects` annotation on the
  `LogicalCluster` object of a workspace. The annotation always takes precedence over
  the shard-wide default, and works even when no shard-wide default is configured.
  A value of `0` (or any value `<= 0`) disables the limit for that logical cluster.

The annotation is protected by the `apis.kcp.io/ReservedMetadata` admission plugin:
only privileged system users (e.g. shard admins) can set or change it. Workspace users
cannot raise their own limit.

```sh
# example: limit the workspace to 1000 objects total
kubectl annotate logicalcluster cluster core.kcp.io/max-total-objects=1000 --overwrite
```

## Semantics

* Only object creation is limited. **Deletes are always allowed** (and free up capacity),
  as are updates and subresource requests.
* Enforcement only starts once the logical cluster is `Ready`; workspace bootstrapping
  and initialization are never blocked.
* `Events` (both `v1` and `events.k8s.io`) are neither counted nor blocked, so an
  exhausted workspace can still be debugged.
* When the limit is reached, create requests are rejected with `403 Forbidden` and a
  message that includes the current count and the limit.

## Accuracy

This is deliberately a "poor man's quota": object counts are maintained per shard by a
periodic etcd scan (`--logical-cluster-object-count-scan-interval`, default `60s`)
combined with in-memory bookkeeping between scans. As a consequence:

* Small, short-lived overshoots of the limit are possible under highly concurrent writes.
* Drift (e.g. from writes that fail after admission) self-corrects within one scan interval.
* After enabling enforcement (flag or first annotation), it becomes active within one
  scan interval.

## Metrics

The shard publishes two gauges, `kcp_logicalcluster_object_count` and
`kcp_logicalcluster_object_limit` (labels: `shard`, `cluster`), **only** for logical
clusters at or above 90% of their effective limit. This keeps the metric cardinality
bounded while still alerting on workspaces that are about to run out of capacity.
