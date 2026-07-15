# Testing Logical Cluster Migration

This guide explains how to test logical cluster migration in a local development environment.

For conceptual background on migration, see [Logical Cluster Migration](../concepts/sharding/migration.md).

## Prerequisites

You need a multi-shard kcp environment. The easiest way to set this up is using the
`sharded-test-server` tool:

```bash
make build WHAT=./cmd/sharded-test-server
./bin/sharded-test-server --number-of-shards=2
```

This starts a root shard, a secondary shard (`shard-1`), and a front-proxy.

## Enabling the Feature Gate

The `LogicalClusterMigration` feature gate must be enabled on all shards. When using
`sharded-test-server`, pass the feature gate flag:

```bash
./bin/sharded-test-server --number-of-shards=2 -- --feature-gates=LogicalClusterMigration=true
```

The `--` separates `sharded-test-server` flags from flags passed to the underlying kcp servers.

## Setting Up Test Workspaces

1. **Create a workspace on a specific shard:**

    ```bash
    export KUBECONFIG=.kcp/admin.kubeconfig
    
    # Create an org workspace (will be scheduled to any available shard)
    kubectl ws create org --enter
    
    # Create a workspace explicitly on the root shard
    kubectl ws create test-workspace --location-selector name=root
    ```

2. **Create some test data:**

    ```bash
    kubectl ws org:test-workspace
    kubectl create configmap test-data --from-literal=key=value
    kubectl create secret generic test-secret --from-literal=password=secret
    ```

3. **Note the logical cluster name:**

    ```bash
    kubectl get workspace test-workspace -o jsonpath='{.spec.cluster}'
    # Example output: 1a2b3c4d5e6f7g8h
    ```

## Binding the Migration API

The migration API must be bound before you can create `LogicalClusterMigration` resources:

```bash
kubectl ws org
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

# Wait for binding
kubectl wait apibinding migration --for=jsonpath='{.status.phase}'=Bound --timeout=60s
```

## Running a Migration

1. **Create the migration resource:**

    ```bash
    kubectl apply -f - <<EOF
    apiVersion: migration.kcp.io/v1alpha1
    kind: LogicalClusterMigration
    metadata:
      name: test-migration
    spec:
      logicalCluster: 1a2b3c4d5e6f7g8h  # Use your actual cluster name
      destinationShard: shard-1
    EOF
    ```

2. **Watch the migration progress:**

    ```bash
    kubectl get logicalclustermigration test-migration -w
    ```

    You should see the phase progress through: `Preparing` → `Migrating` → `OriginCleanup` →
    `DestinationFinalize` → `Completed`.

3. **Verify data integrity:**

    ```bash
    kubectl ws org:test-workspace
    kubectl get configmap test-data -o yaml
    kubectl get secret test-secret -o yaml
    ```

## Observing Migration Behavior

### Watching Connection Termination

To observe how active connections are handled during migration, set up a watch before starting the
migration:

```bash
# Terminal 1: Start a watch
kubectl ws org:test-workspace
kubectl get configmaps -w

# Terminal 2: Start the migration
kubectl ws org
kubectl apply -f migration.yaml
```

The watch in Terminal 1 will be disconnected when the migration enters the `Preparing` phase.

### Checking Shard Assignment

Before and after migration, you can verify which shard hosts the workspace by checking the
`core.kcp.io/shard` annotation on the `LogicalCluster`:

```bash
# Check via the workspace
kubectl get workspace test-workspace -o jsonpath='{.metadata.annotations.core\.kcp\.io/shard}'
```

## Running the E2E Tests

The migration feature includes end-to-end tests that verify the complete migration flow:

```bash
# Run only migration tests
go test -v ./test/e2e/logicalclustermigration/... -args -feature-gates=LogicalClusterMigration=true
```

The tests verify:

- Complete migration lifecycle
- Data integrity after migration
- Client reconnection behavior
- Watch and informer recovery
- RBAC restrictions on the migration API

## Troubleshooting

### Migration Stuck in Preparing Phase

Check that:

- The feature gate is enabled on the origin shard
- The origin shard can reach the cache server
- The logical cluster exists and is not already migrating

Inspect the migration conditions:

```bash
kubectl get logicalclustermigration test-migration -o jsonpath='{.status.conditions}' | jq
```

### Migration Stuck in Migrating Phase

Check that:

- The feature gate is enabled on the destination shard
- The destination shard can reach the origin shard's virtual workspace URL
- The `system:kcp:external-logical-cluster-admin` credentials are properly configured

Check the destination shard logs for errors related to the migration dump request.

### 503 Errors After Migration Completes

If clients still receive 503 errors after migration shows `Completed`:

- The front-proxy may have stale routing information – it should update automatically within a few
  seconds
- Check that the `LogicalCluster` object exists on the destination shard
- Verify the workspace's shard annotation has been updated
