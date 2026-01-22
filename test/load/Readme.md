# Load Testing

Structure

* `reference` contains a script to deploy the 10000 workspaces reference architecture
* `tests` contains an actual loadtest, which can be run on any type of architecture. You just need to tweak the //TODO variables

## 10000 workspaces reference architecture

Assumptions & Requirements:

* We want to test how a kcp installation with 10000 workspaces behaves on simulated, regular activities. We don't want to test how easily we can add 10000 workspaces at once
* You don't want to run etcd on machines which are hosting kcp
* We will be using kcp operator to setup a sharded kcp instance
* The minimum amount of replicas for any group is 3
* The loadtests are infrastructure agnostic. Yes your kcp needs to have the apporpriate size, but the test does not require any specific architecture. This will allow us and community members to experiment with different architectures
* We treat the rootshard like we would any other shard. It will be filled with regulard workspaces. So shard1 = rootshard
* Results are stored in some permanent storage so we can use them for comparison later

### Architecture

// TODO insert architecture diagram

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

1. Now we can calculate the number of etcd nodes. //TODO size per etcd node

    ```txt
    #etcd_nodes = #shards * min_replicas
    ```

1. For simplicity, the number of kcp-server nodes remains constant, but their size is calculated in relation to the number of workspaces

    ```txt
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
#actual_workspaces_per_shard = 10000 / 3 = 3333
kcp_server_node_mem = 512 + (3333 * 5) = 17777MB
#total_nodes =  3 + 1 + 9 + 3 + 1 = 17
```

### Testing Protocol

Mantra: We want to test how a kcp installation with 10000 workspaces behaves on simulated, regular activities. We don't want to test how easily we can add 10000 workspaces at once

#### Procedure

1. Create 10000 workspaces, APIExports, etc. and patiently wait for all of them to become ready
2. Simulate real workd activity by simulating end-users using custom kubeconfigs to create APIBindings and then CRUD on their custom api-objects

#### Must haves

* custom workspacetypes
* nested workspaces living on different shards
* APIExports to share apis between workspaces
* TBD: api-syncagent connected to some workspaces as a stand-in for a typical multiclusterruntime controller? Would be cool because we are testing this component as well. However it introduces quite a bit of additional work, as we would have to provide several instances of it and then all of them connected to different kubernetes clusters (or at least we have to make sure the k8s clusters are not bottlenecks; maybe we could just use kcp as the downstream provider? :D)
* TBD: init-agent. I think it is a bit early to integrate this in our initial planning. Also we have a lot on our plate already as well

## Open Questions

* Do we want to kcp shards to live on separate clusters? I personally think this is something which requires some code changes in kcp itself. I am aware that there is POC using secret bundling already around, but I have not even seen the code so far and I think this would complicate the setup even more
* Do we want to test initialization / finalization in the first batch? I understand that this is important down the road, so we can make sure that these features are not unintentionally putting extreme strain on kcp, but it might complicate the testint procedure even more

## Some personal thoughts

I am aware that Kubernetes has a variety of custom written [perf-test](https://github.com/kubernetes/perf-tests) tools, but my gut feeling tells me that building the majority of the test framework from scratch like this, might not be doable with the current number of kcp contributors.

Therefore I think makeing use of an E2E testing framework for the start makes sense. Among them I, would prefer to use k6, since we can write the tests directly in Go, has a proven track-record and is open-source.
