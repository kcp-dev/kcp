---
description: >
  Contains the definitions shared across design documents around prototyping a kube-like control plane (in KCP).  This is
  a derivative work of other design documents intended to frame terminology.  All future statements that may be changed by
  designs is covered by those designs, and not duplicated here.
---

# Terminology for kcp

## Logical cluster

A logical cluster is a way to subdivide a single kube-apiserver + etcd storage into multiple clusters (different APIs,
separate semantics for access, policy, and control) without requiring multiple instances.  A logical cluster is a
mechanism for achieving separation, but may be modelled differently in different use cases.  A logical cluster is
similar to a virtual cluster as defined by sig-multicluster, but is able to amortize the cost of a new cluster to be
zero or near-zero memory and storage so that we can create tens of millions of empty clusters cheaply.

A logical cluster is a storage level concept that adds an additional attribute to an object’s identifier on a
kube-apiserver.  Regular servers identify objects by (group, version, resource, optional namespace, name).  A logical
cluster enriches an identifier: (group, version, resource, **logical cluster name**, optional namespace, name).

## Workload Cluster

A physical cluster is a “real Kubernetes cluster”, i.e. one that can run Kubernetes workloads and accepts standard
Kubernetes API objects.  For the near term, it is assumed that a physical cluster is a distribution of Kubernetes and
passes the conformance tests and exposes the behavior a regular Kubernetes admin or user expects.

## Workspace

A workspace models a set of user-facing APIs for CRUD.  Each workspace is backed by a logical cluster, but not all
logical clusters may be exposed as workspaces.  Creating a Workspace object results in a logical cluster being available
via a URL for the client to connect and create resources supported by the APIs in that workspace.  There could be
multiple different models that result in logical clusters being created, with different policies or lifecycles, but
Workspace is intended to be the most generic representation of the concept with the broadest possible utility to anyone
building control planes.

A workspace binds APIs and makes them accessible inside the logical cluster, allocates capacity for creating instances
of those APIs (quota), and defines how multi-workspace operations can be performed by users, clients, and controller
integrations.

To a user, a workspace appears to be a Kubernetes cluster minus all the container orchestration specific resources. It
has its own discovery, its own OpenAPI spec, and follows the kube-like constraints about uniqueness of
Group-Version-Resource and its behaviour (no two GVRs with different schemas can exist per workspace, but workspaces can
have different schemas). A user can define a workspace as a context in a kubeconfig file and `kubectl get all -A` would
return all objects in all namespaces of that workspace.

Workspace naming is chosen to be aligned with the Kubernetes Namespace object - a Namespace subdivides a workspace by
name, a workspace subdivides the universe into chunks of meaningful work.

Workspaces are the containers for all API objects, so users orient by viewing lists of workspaces from APIs.

## Workspace type

Workspaces have types, which are mostly oriented around a set of default or optional APIs exposed.  For instance, a
workspace intended for use deploying Kube applications might expose the same API objects a user would encounter on a
physical cluster.  A workspace intended for building functions might expose only the knative serving APIs, config maps
and secrets, and optionally enable knative eventing APIs.

At the current time there is no decision on whether a workspace type represents an inheritance or composition model,
although in general we prefer composition approaches.  We also do not have a fully resolved design.

## Virtual Workspace

An API object has one source of truth (is stored transactionally in one system), but may be exposed to different use
cases with different fields or schemas.  Since a workspace is the user facing interaction with an API object, if we want
to deal with Workspaces in aggregate, we need to be able to list them.  Since a user may have access to workspaces in
multiple different contexts, or for different use cases (a workspace that belongs to the user personally, or one that
belongs to a business organization), the list of “all workspaces” itself needs to be exposed as an API object to an end
user inside a workspace.  That workspace is “virtual” - it adapts or transforms the underlying source of truth for the
object and potentially the schema the user sees.

## Index (e.g. Workspace Index)

An index is the authoritative list of a particular API in their source of truth across the system.  For instance, in
order for a user to see all the workspaces they have available, they must consult the workspace index to return a list
of their workspaces.  It is expected that indices are suitable for consistent LIST/WATCHing (in the kubernetes sense) so
that integrations can be built to view the list of those objects.

Index in the control plane sense should not be confused with secondary indices (in the database sense), which may be
used to enable a particular index.

## Shard

A failure domain within the larger control plane service that cuts across the primary functionality. Most distributed
systems must separate functionality across shards to mitigate failures, and typically users interact with shards through
some transparent serving infrastructure.  Since the primary problem of building distributed systems is reasoning about
failure domains and dependencies across them, it is critical to allow operators to effectively match shards, understand
dependencies, and bring them together.

A control plane should be shardable in a way that maximizes application SLO - gives users a tool that allows them to
better define their applications not to fail.

## API Binding

The act of associating a set of APIs with a given logical cluster.  The Workspace model defines one particular
implementation of the lifecycle of a logical cluster and the APIs within it.  Because APIs and the implementations that
back an API evolve over time, it is important that the binding be introspectable and orchestrate-able - that a consumer
can provide a rolling deployment of a new API or new implementation across hundreds or thousands of workspaces.

There are likely a few objects involved in defining the APIs exposed within a workspace, but in general they probably
define a spec (which APIs / implementations to associate with) and a status (the chosen APIs / implementations that are
currently bound), allow a user to bulk associate APIs (i.e. multiple APIs at the same time, like “all knative serving
APIs”), and may be defaulted based on some attributes of a workspace type (all workspaces of this “type” get the default
Kube APIs, this other “type” get the knative apis).

The evolution of an API within a workspace and across workspaces is of key importance.
