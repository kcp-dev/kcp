---
description: >
  Migrating logical clusters between shards.
---

# Logical Cluster Migration

Logical cluster migration allows moving a workspace from one kcp shard to another. This is useful
when scaling a kcp installation from single-shard to multi-shard, or when rebalancing workspaces
across shards.

!!! warning "Alpha Feature"
    Logical cluster migration is an alpha feature and must be explicitly enabled via the
    `LogicalClusterMigration` feature gate. The feature is under active development and the
    API may change in future releases.

## Overview

When a `LogicalClusterMigration` resource is created, kcp coordinates the transfer of all data
belonging to that logical cluster from the origin shard to the destination shard. The migration
process ensures data integrity by:

1. Preventing access to the workspace during migration
2. Copying all etcd data directly between shards
3. Cleaning up the origin shard after successful transfer

## Prerequisites

Before initiating a migration, ensure that:

- The `LogicalClusterMigration` feature gate is enabled on all participating shards and the cache
  server
- The `migration.kcp.io` API is bound in the workspace where you want to create the
  `LogicalClusterMigration` resource (typically the parent of the workspace being migrated)
- No other migration is currently in progress for the same logical cluster

## Migration Process

The migration proceeds through several phases:

| Phase | Description |
|-------|-------------|
| `Preparing` | Origin shard marks the logical cluster as migrating, cancels active connections, and purges informer caches |
| `Migrating` | Destination shard pulls all etcd data from the origin via the migration virtual workspace |
| `OriginCleanup` | Origin shard deletes all etcd data belonging to the logical cluster |
| `DestinationFinalize` | Destination shard removes the migration annotation and updates informers |
| `Completed` | Migration finished successfully |
| `Failed` | Migration encountered an unrecoverable error |

## Initiating a Migration

To migrate a workspace, create a `LogicalClusterMigration` resource in a workspace that has bound
the `migration.kcp.io` API:

```yaml
apiVersion: migration.kcp.io/v1alpha1
kind: LogicalClusterMigration
metadata:
  name: migrate-my-workspace
spec:
  logicalCluster: <logical-cluster-name>
  destinationShard: <target-shard-name>
```

The `logicalCluster` field must contain the logical cluster name (the value from
`Workspace.spec.cluster`), not the workspace name or path.

You can find the logical cluster name by examining the workspace:

```bash
kubectl get workspace my-workspace -o jsonpath='{.spec.cluster}'
```

## Monitoring Migration Progress

Watch the migration status to track progress:

```bash
kubectl get logicalclustermigration migrate-my-workspace -o yaml
```

The `status.phase` field indicates the current phase. The `status.conditions` provide detailed
information about each step:

- `OriginReady` – origin shard has prepared for migration
- `DataCopied` – destination shard has received all data
- `OriginCleaned` – origin shard has deleted its copy
- `Completed` – migration is fully complete

## Implications of Migration

### Workspace Unavailability

During migration, the workspace is **unavailable**. All requests to the migrating workspace return
`503 Service Unavailable`. Plan migrations during maintenance windows or periods of low activity.

### Client Impact

Clients connected to the workspace experience the following:

- **Active connections** are terminated when the migration begins
- **Watches** receive a disconnect and must be re-established
- **Resource versions change** – the etcd data is written to a new shard with new resource versions.
  Clients using `resourceVersion` for consistency (e.g., informers, watch continuations) will
  receive `410 Gone` responses and must relist

Well-behaved Kubernetes clients (including informers and retry watchers) handle these conditions
automatically by relisting, but there may be a brief period of stale data.

### Controllers and Operators

Controllers watching the migrating workspace:

- Have their watches terminated during the `Preparing` phase
- Need to reconnect after migration completes
- Should relist to pick up any changes that occurred during migration

If your controller uses standard Kubernetes informers or the `RetryWatcher`, reconnection is
handled automatically.

## Limitations

- Only one migration can be active for a given logical cluster at a time
- The migration API requires elevated privileges – only users in the
  `system:kcp:external-logical-cluster-admin` group can bind it
- Migration cannot be cancelled once started – it must complete or fail
- Child workspaces are **not** automatically migrated – each workspace must be migrated separately
  if desired

## Example: Full Migration Workflow

1. **Identify the workspace to migrate:**

    ```bash
    kubectl ws tree
    # Identify the workspace path, e.g., root:org:team:my-workspace
    ```

2. **Get the logical cluster name:**

    ```bash
    kubectl get workspace my-workspace -o jsonpath='{.spec.cluster}'
    # Returns something like: 2xj8f9k3m5n7p1q4
    ```

3. **Identify available shards:**

    ```bash
    kubectl ws root
    kubectl get shards
    ```

4. **Bind the migration API in the parent workspace:**

    ```bash
    kubectl ws root:org:team
    kubectl apply -f - <<EOF
    apiVersion: apis.kcp.io/v1alpha2
    kind: APIBinding
    metadata:
      name: migration
    spec:
      reference:
        export:
          path: root
          name: migration.kcp.io
    EOF
    ```

5. **Wait for the binding to be ready:**

    ```bash
    kubectl wait apibinding migration --for=jsonpath='{.status.phase}'=Bound
    ```

6. **Create the migration:**

    ```bash
    kubectl apply -f - <<EOF
    apiVersion: migration.kcp.io/v1alpha1
    kind: LogicalClusterMigration
    metadata:
      name: migrate-my-workspace
    spec:
      logicalCluster: 2xj8f9k3m5n7p1q4
      destinationShard: shard-2
    EOF
    ```

7. **Monitor progress:**

    ```bash
    kubectl get logicalclustermigration migrate-my-workspace -w
    ```

8. **Verify the workspace is accessible on the new shard:**

    ```bash
    kubectl ws root:org:team:my-workspace
    kubectl get pods  # or any other resource to verify access
    ```
