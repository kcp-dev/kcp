# Running a Sharded Environment

This guide explains how to run a sharded kcp environment locally for development and testing purposes.

## Using `sharded-test-server`

The easiest way to stand up a sharded environment is using the `sharded-test-server` tool. This tool automates the creation of a root shard, additional shards, a front proxy, and all necessary certificates.

### Prerequisites

build the tool before running it:

```bash
make build WHAT=./cmd/sharded-test-server
```

### Running the Server

To start a cluster with 2 shards (Shard-0/Root and Shard-1):

```bash
./bin/sharded-test-server --number-of-shards=2
```

This command will launch:

- **Shard-0 (Root):** Hosting the root logical cluster and core APIs.
- **Shard-1:** A secondary shard joined to the root.
- **Front Proxy:** Handles routing requests to the appropriate shard.

### Accessing the Cluster

The tool generates several kubeconfig files in the current directory (or the path specified by `--work-dir-path`):

- `.kcp/admin.kubeconfig`: **Primary Admin Access.** connects via the front-proxy. Use this for most operations.
- `.kcp-0/admin.kubeconfig`: Direct access to Shard-0 (Root).
- `.kcp-1/admin.kubeconfig`: Direct access to Shard-1.

### Verification

To verify that your shards are running and registered, query the `Shard` resources from the root shard:

```bash
KUBECONFIG=.kcp/admin.kubeconfig kubectl get shards
```

You should see both `root` (shard-0) and `shard-1` listed.

```
NAME      REGION      URL                      EXTERNAL URL             AGE
root      us-east-2   https://127.0.0.1:6444   https://127.0.0.1:6443   111s
shard-1   us-east-1   https://127.0.0.1:6445   https://127.0.0.1:6443   95s
```
