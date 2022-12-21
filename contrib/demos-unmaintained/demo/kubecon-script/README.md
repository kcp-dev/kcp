# KubeCon Demo components and description

The `kubecon` demo shows a prototype using `kcp` to run a simple application across multiple clusters.

## `kcp`

`kcp` is a minimal Kubernetes API server.
It knows about the minimal possible set of types necessary, and the `CustomResourceDefinition` type to define other types.

`kcp` includes no controllers to take action on any resource, and instead expects CRD authors to provide those.

For example, out of the box, `kcp` doesn't know about Pods, and whoever defines that type is expected to also provide a controller that understands what to do when a Pod is created or updated.

## Clusters

`kcp` by itself is inert, inactive.
It stores data, but does little else with it.

In order to get the functionality of K8s-the-container-orchestrator, the demo connects `kcp` with real "physical" Kubernetes clusters that teach `kcp` about their types.

Instead of connecting real Kubernetes clusters, you could define a Pod CRD yourself, and provide a controller that understands what to do when users request Pods.

### Physical Clusters
#### Cluster CRD

To be able to connect multiple "physical" clusters, we define a `Cluster` CRD type.
This effectively includes a kubeconfig to locate and authenticate with the cluster.

```yaml
apiVersion: workload.kcp.io/v1alpha1
kind: SyncTarget
metadata:
  name: my-cluster
spec:
  kubeconfig: |
    apiVersion: v1
    kind: Config
    clusters:
    - name: cluster
      cluster:
        certificate-authority-data: ...
        server: https://5.6.7.8:5678
    users:
    - name: user
      user:
        client-certificate-data: ...
        client-data-key: ...
```

Again, `kcp` doesn't _need_ to know about this type at all; you can use `kcp` without connecting to any real clusters.

The Cluster type is also not intended to define "The One True Cluster Type", it's only for demonstration purposes.

#### Cluster Controller

If `kcp` _does_ know about the Cluster type, you also need some controller to run against it that knows what to do with objects of that type.
For this demo, that's the *Cluster Controller*.

As with the Cluster CRD, the Cluster Controller is for demonstration purposes.

The Cluster Controller watches for new Cluster objects, or updates to Cluster objects, and connects to that cluster using the provided kubeconfig information.

While it's connected it attempts to make sure that the [**Syncer**](#Syncer) is installed on the cluster.
The Cluster Controller watches each cluster's Syncer installation to determine whether the cluster is connected and healthy.

If the Cluster Controller determines the Syncer isn't installed, it will install it, giving the Syncer the config it needs to connect to the `kcp`.

Q: When the Cluster object is deleted, should Syncer be uninstalled, or otherwise told to stop syncing? Perhaps using a [Finalizer](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/#finalizers)?

### Logical Clusters
By default, there is a single logical cluster created in `kcp`. To create more logical clusters, you have two options:

- Pass `--server=https://localhost:6443/clusters/<logical-cluster-name> --insecure-skip-tls-verify` to your `kubectl` command.
- Edit your kubeconfig to add another cluster and context

You can also use /clusters/* to try listing/watching all clusters from kubectl.

## Syncer

Once installed in the cluster, the Syncer is responsible for keeping the status of the "real" underlying cluster in sync with the `kcp` that it's attached to.
In order to stay in sync, it needs to watch _all objects_ of _all types_, and in order to do that it first needs to keep _types_ in sync with its `kcp`.

NB: In the section below, "downstream" means syncing `kcp` state to the underlying cluster, and "upstream" means syncing the underlying cluster up to its `kcp`.

### Type Syncer

In order to sync state between an underlying cluster and `kcp`, Syncer needs to have a shared understanding of the types known to its local API server and its `kcp`.

NB: Since we can't issue a Watch API request to the `api-resources` endpoint, we'll poll that endpoint and compare against our last view of the response.

NB: Type syncing might be done by the Cluster Controller or some other component external to the cluster, and not by the Syncer running inside the cluster as described below.

#### Downstream Type Syncing

The Syncer will watch for changes to the types known to its `kcp`, to make those changes to the local API server.

Since real clusters define immutable built-in types, some `kcp` type updates might not be compatible with the local cluster.
For example, `kcp` may change its definition of the Pod type in a way that the underlying cluster can't accept.

#### Upstream Type Syncing

The Syncer will also watch for changes to types known to its local API server, to report those to its `kcp`.

In addition to CRD types which can easily change for real underlying clusters (e.g., installing or upgrading some cluster add-on), even built-in types can change when a cluster performs an upgrade.
These updates should also be reported to the `kcp`.

### Object Syncer

For all types the Syncer knows about, it will watch for creations/updates/deletions of all objects of those types that carry a [label](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/) identifying it as for this cluster.

```
metadata:
  labels:
    workloads.kcp.io/cluster: my-cluster
```

This label identifies an object as being intended for the cluster `my-cluster`.
Other clusters should ignore these objects, and in the future `kcp` might even hide these objects from other clusters to further prevent leakage.

#### Downstream Object Syncing

The Syncer will watch for all creations/updates/deletions of all objects in the `kcp`,  of all types the Syncer knows about.

- When an object is created in `kcp`, Syncer will create an equivalent object in its cluster's local API server.
- When an object's `.spec` is updated in `kcp`, Syncer will make an equivalent update to the object in its cluster's local API server.
- When an object is deleted in `kcp`, Syncer will delete that object in its cluster's local API server.

The Syncer mainly expects to update objects' `.spec`s downstream, but should also be able to update `.status` if the status is updated in `kcp` by another controller.

#### Upstream Object Syncing

The Syncer will also watch for all creations/updates/deletions of all objects in its local API server, of all types the Syncer knows about.

- When an object is created in the local API server, Syncer will create an equivalent object in `kcp`.
- When an object's `.status` is updated in the local API server, Syncer will make an equivalent update to the object in `kcp`.
- When an object is deleted in the local API server, Syncer will delete that object in `kcp`.

The Syncer mainly expects to update objects' `.status`es upstream, but should also be able to update `.spec` if the spec is updated in the local API server by another controller (e.g., a HorizontalPodAutoscaler updating `.spec.replicas`)

---
NB: Syncing objects should be resilient to updates in either direction being invalid due to invalid types; the object syncing loop shouldn't be dependent on the type syncing loop being up-to-date and healthy.

The Syncer demonstrates a possible generalized pattern for dealing with objects of any type, unaware of what the objects mean.
The Syncer doesn't know what any particular object represents, it just knows it should copy things assigned to it in the `kcp` to its local physical cluster, and sync any observed status back up to `kcp`.

As we build on this prototype, we can consider more robust and expressive mechanisms for assigning objects to clusters, which can produce better syncing behavior.
We might also consider exposing syncing status on objects' `.status.conditions` for better visibility into the syncing process.

### Deployment Splitter

With the above components, we can demonstrate a powerful use case for a `kcp` that knows about multiple Clusters: telling `kcp` to create a [Deployment](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/), a long-running, possibly-replicated containerized workload.

With a `kcp` aware of multiple Clusters, this replicated containerized workload can be split across those Clusters for geographic redundancy and resiliency, without requiring any changes to the Deployment configuration, using the *Deployment Splitter*!

Out of the box, `kcp` doesn't know about the Deployment type, but when a Cluster is attached its Syncer process will advertise this type to the `kcp` so that clients can talk about this type.

The new Deployment Splitter component is responsible for interpreting and acting on Deployment objects, which it does by sharding it across multiple available Clusters.

When the Deployment Splitter sees a new Deployment object in the `kcp`, it will list the available Clusters known to that `kcp`, and determine (rather crudely, for now) how many replicas should be assigned to one Cluster or another.

```
$ kubectl create -f deployment.yaml
deployment.apps/my-deployment created
$ kubectl get deployments
NAME                      READY
my-deployment
```

If the initial Deployment specifies `replicas: 15` and there are two available Clusters, the Deployment Splitter might decide to schedule six replicas on one cluster and nine on another.
Having made this determination, the Deployment Splitter will create two "virtual" Deployment objects in `kcp`, each with a [label](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/) identifying it as for one of the two available Clusters.
These two virtual Deployments will have `.spec.replicas` set, based on the determination above.

```
$ kubectl get deployments
NAME                      READY
my-deployment
my-deployment--cluster-1
my-deployment--cluster-2
```

These

At this point the clusters' Syncers will be notified of these virtual Deployment objects, and will sync them to their local API servers, which have controllers running to create Pods, schedule those Pods to Nodes, start containers on those Nodes and bubble the status of those containers back up to the cluster's API server, and the Syncers will sync that status back up to the `kcp`.

```
$ kubectl get deployments
NAME                      READY
my-deployment
my-deployment--cluster-1  1/6
my-deployment--cluster-2  7/9
```

The Deployment Splitter will watch the status of these virtual Deployments and aggregate them back into the parent Deployment's status, reporting on the total number of ready replicas, and any other status such as unscheduleable or failing replicas.

```
$ kubectl get deployments
NAME                      READY
my-deployment             8/15
my-deployment--cluster-1  1/6
my-deployment--cluster-2  7/9
```

Eventually, under normal circumstances, all of the replicas for both virtual Deployments should become ready, and the parent Deployment should aggregate that status to show it as fully ready:

```
$ kubectl get deployments
NAME                      READY
my-deployment             15/15
my-deployment--cluster-1  6/6
my-deployment--cluster-2  9/9
```

---

For now the process of splitting Deployment replicas is random, and not even a little bit intelligent.
This is just an example of how multi-cluster Deployment scheduling could work.

Future improvements could be made to:
- intelligently spread replicas across multiple availability zones
- schedule replicas with affinity or anti-affinity to other workloads
- dynamically schedule based on resources available on underlying clusters
- dynamically reschedule replicas as clusters join and leave the `kcp`
- ...and much more

## Extending the Pattern

The pattern demonstrated above can be generalized and applied to other types, including other built-in types:

- A [CronJob](https://kubernetes.io/docs/reference/kubernetes-api/workload-resources/cron-job-v1/) controller can run workloads on a schedule, running that workload on any available cluster.
- A [DaemonSet](https://kubernetes.io/docs/reference/kubernetes-api/workload-resources/daemon-set-v1/) controller can create virtual per-cluster DaemonSets to schedule workloads on every Node across many clusters.
- A [StatefulSet](https://kubernetes.io/docs/reference/kubernetes-api/workload-resources/stateful-set-v1/) controller could run workloads with stable identities, even as they migrate across clusters.
- A [HorizontalPodAutoscaler](https://kubernetes.io/docs/reference/kubernetes-api/workload-resources/horizontal-pod-autoscaler-v2beta2/) (HPA) controller could create per-cluster virtual HPAs to autoscale replicas within the original requested bounds.
- ...and so on.

And this pattern can extend built-in types to provide higher-level concepts:

A ClusterDaemonSet CRD and controller can ensure that at least one replica of a Pod is running on every available Cluster, even if they don't run on every Node in every cluster.

And with cross-cluster networking and service discovery, controllers for Services, Endpoints, Ingresses, LoadBalancers, etc., can route and manage traffic across multiple clusters as well.

A more intelligent Deployment controller could interpret and execute Deployments on other infrastructure, outside of Kubernetes entirely.

This prototype is intended to demonstrate what kinds of things are possible as we separate Kubernetes-the-API from Kubernetes-the-container-orchestrator.
If you have questions or ideas, please share them. :)
