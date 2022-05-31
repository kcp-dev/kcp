# Locations and Scheduling

KCP implements *Compute as a Service* via a concept of Transparent Multi Cluster (TMC). TMC means that
Kubernetes clusters are attached to a kcp installation to execute workload objects from the users'
workspaces by syncing these workload objects down to those clusters and the objects' status
back up. This gives the illusion of native compute in KCP.

We call it *Compute as a Service* because the registered `WorkloadClusters` live in workspaces that
are (normally) invisible to the users, and the teams operating compute can be different from
the compute consumers.

The APIs used for Compute as a Service are:

1. `scheduling.kcp.dev/v1alpha1` – we call the outcome of this *placement* of namespaces.
2. `workloads.kcp.dev/v1alpha1` – responsible for the syncer component of TMC.

## Main Concepts

- `WorkloadCluster` in `workloads.kcp.dev/v1alpha1` – representations of Kubernetes clusters that are attached to a kcp installation to
  execute workload objects from the users' workspaces. On the workload clusters, there is one syncer
  process for each `WorkloadCluster` object. 

  Workload clusters are invisible to users, and (medium term) at most identified via a UID.

- `Location` in `scheduling.kcp.dev/v1alpha1` – represents a collection of `WorkloadCluster` objects selected via instance labels, and
  exposes labels (potentially different from the instance labels) to the users to describe, identify and select locations to be used
  for placement of user namespaces onto workload clusters.

  Locations are visible to users, but owned by the compute service team, i.e. read-only to the users and only projected
  into their workspaces for visibility. A placement decision references a location by name.

- *Compute Service Workspace* (previously *Negotiation Workspace*) – the workspace owned by the compute service team to hold
  the `APIExport` (named `kubernetes` today) with the synced resources, and `WorkloadCluster` and `Location` objects.

  The user binds to the `APIExport` called `kubernetes` using an `APIBinding`. From this moment on, the users' workspaces
  are subject to placement.

- *Placement* – the process of selecting a `Location` matching the scheduling constraints and a `WorkloadCluster` in that location
  for a user namespace. A placement decision is not necessarily permanent and can be changed over time. The placement onto a location
  is sticky, while the placement onto a workload cluster is not. I.e. when evicted from a workload cluster, another workload cluster
  in the same location is selected.

Note: binding to a compute service is a permanent decision. Unbinding (i.e. deleting of the APIBinding object) means deletion of the
workload objects.

Note: it is planned to allow multiple location workspaces for the same compute service, even with different owners.

## State Machines

There are two state machines involved in TMC.

1. the *placement state machine*, stored in the `scheduling.kcp.dev/placement` annotation on namespaces. This state machine is used to
   track the placement decisions of the user namespaces. The actors are:
   - the *placement controller* (= scheduler), which is responsible for the placement decision.
   - the *workload/namespace controller*, which is responsible for implementing the placement via syncing of workload objects to physical clusters.
2. the *syncing state machine*, stored in the `state.internal.workloads.kcp.dev/<cluster-id>` labels on workload objects. This state machine is used to
   track the syncing of workload objects to physical clusters. The actors are:
   - the *workload controller*, which is responsible for syncing workload objects to physical clusters.
   - the *workload/namespace controller*, which is responsible for implement the syncing via syncing of workload objects to physical clusters.

<img alt="Diagram of kcp" width="100%" src="./images/scheduling-state-machine.svg"></img>

### Placement

`scheduling.kcp.dev/placement` holds a JSON object, consisting of a map of from location strings to a placement state.

The placement state is one of
- `Pending` – the placement controller waits for the namespace controller to adopt the namespace 
  by setting setting the state to `Bound`.
- `Bound` – the namespace is bound to a workload cluster and with that to a syncer.
- `Removing` – the placement controller can set the state to `Removing` in order to ask the namespace controller to
  start the process of removing the namespace from the workload cluster.
- `Unbound` – the namespace has been removed and with that released by the workload cluster and with that from a syncer.
  This state is set by the namespace controller. The placement controller will notice, and remove the entry in the placement annotation.

The location strings are of the form `<locationClusterName>+<locationName>+<workloadClusterIdentifier>`.

Example:

```yaml
apiVersion: v1
kind: Namespace
  name: default
  annotations:
    scheduling.kcp.dev/placement: {"root:compute+us-east1-gcp+a7fcajg8a-a9sf-a738":"Bound"}
```

Note: multiple workload clusters can be bound at the same time.

The placement state machine is (to be) protected against mutation by the user via admission. The user interface
to influence the placement decisions is (will be) the `Placement` object.

### Resource Syncing

As soon as the `scheduling.kcp.dev/placement` annotation is set with state `Pending` on a namespace, the workload namespace 
controller will pick up the namespace and

1. set the `scheduling.kcp.dev/placement` annotation state to `Bound` and
2. set the `state.internal.workloads.kcp.dev/<cluster-id>` label to `Sync`.

Then the workload resource controller will copy the `state.internal.workloads.kcp.dev/<cluster-id>` label to the 
resources in that namespace.

Note: in the future, the label on the resources is first set to empty string `""`, and a coordination controller will be 
able to apply changes before syncing starts. This includes the ability to add per-location finalizers through the
`finalizers.workloads.kcp.dev/<cluster-id>` annotation such that the coordination controller gets full control over 
the downstream life-cycle of the objects per location (imagine an ingress that blocks downstream removal until the new replicas
have been launched on another workload cluster). Finally, the coordination controller will replace the empty string with `Sync`
such that the state machine continues.

With the state label set to `Sync`, the syncer will start seeing the resources in the namespace
and starts syncing them downstream, first by creating the namespace. Before syncing, it will also set 
a finalizer `workloads.kcp.dev/syncer-<cluster-id>` on the upstream object in order to delay upstream deletion until
the downstream object is also deleted.

When the `scheduling.kcp.dev/placement` annotation signals `Removing`, the namespace controller will
add the `deletion.internal.workloads.kcp.dev/<cluster-id>` annotation with a RFC3339 timestamp. The virtual workspace apiserver
will translate that annotation into a deletion timestamp on the object the syncer sees. The syncer
notices that as a started deletion flow. As soon as there are no coordination controller finalizers registered via the
`finalizers.workloads.kcp.dev/<cluster-id>` annotation anymore, the syncer will start a deletion of the downstream object.

When the downstream deletion is complete, the syncer will remove the finalizer from the upstream object, and the
`state.internal.workloads.kcp.dev/<cluster-id>` labels gets deleted as well. The syncer stops seeing the object in the virtual
workspace.

In case of namespaces, the workload namespace controller will notice that removal of the `state.internal.workloads.kcp.dev/<cluster-id>` 
labels and set the `scheduling.kcp.dev/placement` state to `Unbound`. The placement controller will notice and remove the
`scheduling.kcp.dev/placement` for that location.

Note: there is a missing bit in the implementation (in v0.5) about removal of the `state.internal.workloads.kcp.dev/<cluster-id>` 
label from namespaces: the syncer currently does not participate in the namespace deletion state-machine, but has to and signal finished
downstream namespace deletion via `state.internal.workloads.kcp.dev/<cluster-id>` label removal.
