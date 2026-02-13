# Load Testing

## 10000 workspaces reference architecture

Assumptions & Requirements:

* We want to test how a kcp installation with 10000 workspaces behaves on synthetic workloads
* We do not want to test how easily kcp handles adding 10000 workspaces at once
* We don't want to run etcd on machines which are hosting kcp
* We will be using kcp-operator to setup a sharded kcp instance
* The minimum amount of replicas for any component is 3
* The loadtests are infrastructure provider agnostic. This will allow us and community members to
experiment with different infrastructure sizes
* We treat the rootshard like we would any other shard. It will be filled with regular workspaces
So shard1 = rootshard
* As the kcp-operator currently has no support for a dedicated cache server: We have decided to still
work with the default model of having an embedded cache in the rootshard (even if it overloads the
rootshard). Specifically this means when we have 3 shards, we create 1 Rootshard and 2 Shards
* Results are stored in some permanent storage so we can use them for comparison later

### Architecture

[drawing of the general layout](./architecture.excalidraw)

### Node calculation

All node calculations are based on the number of workspaces and use the following recommended constants:

* max_workspaces_per_shard = 3500
* min_replicas = 3
* kcp_server_buffer = 512MB
* #kcp_cache_nodes = 1
* #aux_nodes = 1
* #frontproxy_nodes = 3
* mem_per_workspace = 5MB

---

1. We calculate the number of shards

    ```txt
    #shards = round_up(#workspaces / max_workspaces_per_shard)
    ```

1. Now we can calculate the number of etcd nodes.

    ```txt
    #etcd_nodes = #shards * min_replicas
    ```

1. We can calculcate the number of shards and their size in relation to the number of workspaces

    ```txt
    #shard_nodes = #shards * min_replicas
    #actual_workspaces_per_shard = workspaces / shards
    kcp_server_node_mem = kcp_server_buffer + (#actual_workspaces * mem_per_workspace)
    ```

The total number of all required nodes is calculated as follows

  ```txt
  #total_nodes = #frontproxy_nodes + #kcp_cache_nodes + #etcd_nodes + #kcp_server_nodes + #aux_nodes
  ```

#### Example for 10000 workspaces

```txt
#shards = 10000 / 3500 = 2,85 = 3
#etcd_nodes = 3 * 3 = 9
#shard_nodes = 3 * 3 = 9
#actual_workspaces_per_shard = 10000 / 3 = 3333
kcp_server_node_mem = 512 + (3333 * 5) = 17777MB
#total_nodes =  3 + 1 + 9 + 3 + 1 = 17
```

### Testing Protocol

Mantra: We want to test how a kcp installation with 10000 workspaces behaves on simulated, regular
activities. We don't want to test how easily we can add 10000 workspaces at once

#### Procedure

1. Create 10000 workspaces, APIExports, etc. and patiently wait for all of them to become ready
2. Simulate real world activity by simulating end-users using custom kubeconfigs to create APIBindings
and then CRUD on their custom api-objects

##### Level 1 - 10000 empty workspaces

We are just going to put 10000 empty workspaces into a kcp installation. We will have a nesting level
setting so we can try out if nesting has any impact (it should not). This test case
is extremely deterministic and has should spread workspaces relatively equally across shards.
We mainly use this as a base consumption measurement and to verify nesting has no performance impact.

##### Level 2 - Basic CRUD

Every workspace has a type and we are going to do a basic parallel CRUD workflow which we will simulate
using simple Kubernetes Jobs. The workflow is done on basic objects from a single provider using a
singular APIExport.

##### Level 3 - Multiple Providers

We are multixplexing the level 2 example to use multiple providers.

##### Outlook

We want to keep the initial version of the tests simple and deterministic. As a result the following
topics have been discussed, but were considered not to be part of the first 3 level implementation:

* direct user interaction via simulated users
* custom workspacetypes with initializers and finalizers
* integrating the init-agent
* nested workspaces living on different shards
* having a chaos monkey randomly killing shards

### Scraping of Metrics

We plan on using a plain Prometheus to scrape all of the kcp-instanes: On a higher level we plan to
monitor:

* CPU + Mem on all components
* Number of Goroutines over time
* Request response times on front-proxy (probably percentiles). This could also alternatively be
measured inside the testing suite (clientside)
* Disk IO and size on both etcd and rootshard
* Total number of workspaces (to compare expected with actual)
