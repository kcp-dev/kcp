---
description: >
  The Terminology document contains definitions used throughout the kcp project. We strongly recommend familiarizing
  yourself with this terminology before proceeding. Note that some terms might get changed in the future, and that
  will be covered by respective design documents.
---

# Terminology

This document contains definitions used throughout the kcp project. We strongly recommend familiarizing yourself with
this terminology before proceeding. Note that some terms might get changed in the future, and that will be covered by
respective design documents.

## Logical Cluster

A logical cluster is a way to subdivide a single `kube-apiserver` instance and `etcd` datastore into multiple
clusters (different APIs, separate semantics for access, policy, and control) without requiring multiple instances.

A logical cluster is a mechanism for achieving separation, but may be modelled differently in different use cases.
A logical cluster is able to amortize the cost of a new cluster to be near-zero memory and storage, so that we can
create a huge number of empty clusters cheaply.

A logical cluster is a storage level concept that adds an additional attribute to an object’s identifier in etcd
and kube-apiserver. Regular API servers identify objects by (group, version, resource, optional namespace, name).
A logical cluster enriches an identifier with its name:
(group, version, resource, **logical cluster name**, optional namespace, name).

## Workspace

A workspace provides Kubernetes-cluster-like HTTPS/CRUD endpoints, i.e., endpoints that typical Kubernetes client
tooling (e.g., client-go, controller-runtime, and others) can interact with as they would with classic Kubernetes
clusters. Workspace provides multi-tenancy in a kcp setup.

Each workspace is backed by a logical cluster, but not all logical clusters may be exposed as workspaces.
Creating a `Workspace` object results in a logical cluster being created and available via an URL for the client to
connect and create resources supported by the APIs in that workspace. The list of default APIs depends on the chosen
Workspace type, which will be covered later in this document.

There could be multiple different models that result in logical clusters being created, with different policies or
lifecycle, but Workspace is intended to be the most generic representation of the concept with the broadest possible
utility to anyone building control planes.

A workspace can be used to:

- export APIs, making them bindable inside different workspaces and logical clusters
- bind APIs, i.e. "install" APIs from different workspaces and logical clusters into the current workspace and logical
  cluster
- allocate capacity for creating instances of those APIs (quota)

Additionally, a workspace defines how multi-workspace operations can be performed by users, clients, and controller
integrations.

To a user, a workspace appears to be a Kubernetes cluster without all the container orchestration specific resources.
(e.g. Pods, ReplicaSets, Deployments...). It has its own discovery, its own OpenAPI spec, and follows the
Kubernetes-like constraints about uniqueness of Group-Version-Resource and its behavior. For example, it's not
possible to have two identical GVRs with different schemas in one workspace, but it's possible to have two identical
GVRs with different schemas in different workspaces.

A user can define a workspace as a context in a kubeconfig file and `kubectl get all -A` would return all objects in
all namespaces of that workspace. Workspace naming is chosen to be aligned with the Kubernetes Namespace object -
a Namespace subdivides a workspace by name, a workspace subdivides the universe into chunks of meaningful work.

Workspaces are the containers for all API objects, so users orient by viewing lists of workspaces from APIs.

More information, including examples, can be found in the the [Workspaces document](../workspaces).

### Workspace Types

Workspaces have types, which are mostly oriented around a set of default or optional APIs exposed. For example:

- a workspace intended for deploying applications might expose the same API objects a user would encounter on a
  physical cluster,
- a workspace intended for building functions might expose only the Knative serving APIs, ConfigMaps, Secrets, and
  optionally enable Knative Eventing APIs.

At the current time there is no decision on whether a workspace type represents an inheritance or composition model,
although in general we prefer composition approaches. We also do not have a fully resolved design.

More information, including a list of Workspace Types and examples, can be found in the
[Workspace Types document](../workspaces/workspace-types/).

### Virtual Workspace

Virtual Workspaces are proxy-like API servers under a custom URL that provide a computed view of real Workspaces.
That means that Virtual Workspaces provide Kubernetes-cluster-like HTTPs endpoints, just like classic Workspaces,
so you can use the typical Kubernetes client tooling to interact with Virtual Workspaces. Moreover, Virtual Workspaces
are the most often used for reconciling API objects exposed through the API binding mechanism (in a way that your
controller will target the virtual workspace instead of each specific workspace).

An API object has one source of truth, i.e. it's transactional stored in one system. In the context of kcp, this
means that an API object is stored in one workspace/logical cluster. However, a use case that arises is how can someone
view API objects across different workspaces in a "single pane of glass" fashion. Concretely:

- the user may have access to different workspaces: a workspace that belongs to the user personally, or one that
  belongs to a business organization. User might want to list and manage their API objects from all workspaces in a
  single call (e.g. single `kubectl get` invocation),
- Service Provider might want to get a list of all instances (from all users) of the API resource they provide, so that
  they can do reconciliation, have insights into how their API is used, and more.

More information, including concrete examples and a list of frequently asked questions, can be found in the
[Virtual Workspaces document](../workspaces/virtual-workspaces/).

## Exporting/Binding APIs

One of the core values of the kcp project is to enable providing APIs that can be consumed by multiple workspaces.
This is done via exporting/binding mechanism, i.e. via API Exports and API Bindings.

The general workflow is:

- Define your API resources similar to how you would define CustomResourceDefinitions (CRDs)
- Create an API Export (in the same workspace) with a list of API resources that you want to export
- User can create an API Binding in their workspaces referring to your API Export. This will result in API resources
  that are defined in the referenced API Export to be installed in user's workspace
- User can now create API resources of those types in their workspace. You can build and run controllers that are going
  to reconcile those resources across different workspaces.

The Workspace model defines one particular implementation of the lifecycle of a logical cluster and the APIs within it.
Because APIs and the implementations that back an API evolve over time, it is important that the binding be
introspectable and orchestrate-able - that a consumer can provide a rolling deployment of a new API or new
implementation across hundreds or thousands of workspaces. The evolution of an API within a workspace and across
workspaces is of key importance.

## Shard

A failure domain within the larger control plane service that cuts across the primary functionality. Most distributed
systems must separate functionality across shards to mitigate failures, and typically users interact with shards
through some transparent serving infrastructure. Since the primary problem of building distributed systems is reasoning
about failure domains and dependencies across them, it is critical to allow operators to effectively match shards,
understand dependencies, and bring them together.

A control plane should be shardable in a way that maximizes application SLO - gives users a tool that allows them to
better define their applications not to fail.

## Index (e.g. Workspace Index)

An index is the authoritative list of a particular API in their source of truth across the system. For instance, in
order for a user to see all the workspaces they have available, they must consult the workspace index to return a list
of their workspaces. It is expected that indices are suitable for consistent LIST/WATCH-ing (in the Kubernetes sense),
so that integrations can be built to view the list of those objects.

Index in the control plane sense should not be confused with secondary indices (in the database sense), which may be
used to enable a particular index.
