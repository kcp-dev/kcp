---
linkTitle: "kcp playground"
description: >
  Alternative approach for testing KCP
---

# KCP playground

The kcp playground is a kubectl plugin that allows users/developers to spin up playgrounds with kcp, pclusters, 
and an initial setup defined in a config file.

## Example playgrounds 

The easiest way to start using the playground is using example configurations show-casing kcp features by
reproducing tutorials already presented in this site and/or scenarios from kcp E2E tests.

### APIBinding and APIExports

This playground configuration show cases publishing APIs as a service provider,
as described in https://docs.kcp.io/kcp/main/en/concepts/quickstart-tenancy-and-apis/.

Use the following command to start the playground:

```shell
kubectl kcp playground start -f test/kubectl-kcp-playground/examples/apis/cowboy-api.yaml
```

This command gives you a playground with kcp already set-up with two workspaces: 
The `root:my-org:service-provider` workspace has an APIExport, offering a simple `Cowboy` API for
other workspaces to use.
The `root:my-org:applications` workspace has an APIBinding, allowing you to create `Cowboy` custom CRs.

```shell
# From another terminal window
export KUBECONFIG=.kcp/playground.kubeconfig

kubectl kcp playground use shard main
kubectl ws root:my-org:applications
kubectl apply -f - <<EOF
  apiVersion: wildwest.dev/v1alpha1
  kind: Cowboy
  metadata:
    name: woody
  spec: {}
EOF
```

Take your time to look at the APIBinding, the APIExport and the APIResourceSchema to understand how
those kcp primitives works.

TIPS: The playground config file contains some notes that could help you.

### East - West placement

This playground configuration show case deploying workloads on KCP,
showing what happens if a workload is scheduled to multiple locations.

Use the following command to start the playground:

```shell
kubectl kcp playground start -f test/kubectl-kcp-playground/examples/placement/east-west.yaml
```

This gives you a playground with two kind clusters and kcp already set-up with two workspaces:
- The `root:my-org:infrastructure` workspace has two SyncTargets, one for each kind cluster, and two Locations,
  one selecting the east SyncTarget, the other the west SyncTarget.
- The `root:my-org:application` workspace has two Placement rules, one selecting the east location, one the west 
  location; with this configuration, workloads deployed on this workspace are going to be placed on both locations.

Deploy a workload in KCP:

```shell
# From another terminal window
export KUBECONFIG=.kcp/playground.kubeconfig

kubectl kcp playground use shard main
kubectl ws root:my-org:applications
kubectl apply -f test/e2e/reconciler/deployment/workloads/deployment.yaml
kubectl get deploy -A
```

Check workload have been deployed on one (and only one) compute cluster:

```shell
kubectl kcp playground use pcluster [us-east1|us-west1]
kubectl get deploy -A
```

Take your time to look at this setup to understand how workload placement can be used in kcp; if you want
you can also change the config file and enable the deploymentCoordinator so your workloads are going to be
spread across the two locations.

### Pool of clusters

This playground configuration show case deploying workloads on KCP,
showing what happens if a workload is assigned to a Location which is backed by more than
one compute cluster.
Use the following command to start the playground:

```shell
kubectl kcp playground start -f test/kubectl-kcp-playground/examples/placement/pool-of-clusters.yaml
```

This playground gives you a setup similar to the one described in the previous example, but
both the two kind clusters are reference by a single Location/Placement rule, thus defining 
a pool of clusters; with this configuration, workloads deployed on this workspace are going
to be placed only one of the clusters in the pool.

Deploy a workload in KCP:

```shell
# From another terminal window
export KUBECONFIG=.kcp/playground.kubeconfig

kubectl kcp playground use shard main
kubectl ws root:my-org:applications
kubectl apply -f test/e2e/reconciler/deployment/workloads/deployment.yaml
kubectl get deploy -A
```

Check workload have been deployed on one (and only one) compute cluster:

```shell
kubectl kcp playground use pcluster [us-east1-cluster1|us-east1-cluster2]
kubectl get deploy -A
```

Take your time to look at this setup to understand how workload placement can be used in kcp.

### Custom resource synced to a compute cluster

This playground configuration shows how to combine together what we saw about APIBinding and APIExports
with SyncTargets/Location/Placement rules, and most specifically show cases how to publish APIs as a service provider,
use them in another workspace and also get them synchronized to a compute cluster.

Use the following command to start the playground:

```shell
kubectl kcp playground start -f test/kubectl-kcp-playground/examples/apis/cowboy-api-to-pcluster.yaml
```

This playground gives you the following setup under `root:my-org`:
- A service-provider workspaces, exposing the cowboys API
- A infrastructure workspaces with a synctarget, configured to sync the cowboys API, and the corresponding kind cluster.
- An application workspace biding to the cowboys API and with a placement rule targeting the above sync target.

With this configuration, when a cowboy object is going to be created in kcp, it will be synced to the compute cluster
as well like it usually happens for deployments.

```shell
# From another terminal window
export KUBECONFIG=.kcp/playground.kubeconfig

kubectl kcp playground use shard main
kubectl ws root:my-org:applications
kubectl apply -f - <<EOF
  apiVersion: wildwest.dev/v1alpha1
  kind: Cowboy
  metadata:
    name: woody
  spec: {}
EOF
```

Check workload have been deployed on one (and only one) compute cluster:

```shell
kubectl kcp playground use pcluster us-east1
kubectl get cowboys -A
```

Take your time to look at this setup to understand how API exports and workload placement can be used in kcp.

## Build your own playgrounds

You can build your own playgrounds by starting from one of the above config files
or defining a new config from scratch. [Here](kcp-playground.yaml) you can find
a config file with all options the available.
