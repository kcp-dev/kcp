---
description: >
  The Terminology document contains definitions used throughout the kcp project. We strongly recommend familiarizing
  yourself with this terminology before proceeding. Note that some terms might get changed in the future, and that
  will be covered by respective design documents.
---

# Terminology

This document contains definitions used throughout the kcp project. We strongly recommend familiarizing yourself with
this terminology before proceeding.

!!! note
    Some terms might get changed as the project develops. This document will be kept up to date with all the relevant
    changes, while additional details and the history of changes will be covered by the respective design documents.

## `kcp` server

kcp server is a Kubernetes-like API server. It extends the Kubernetes API server to provide multi-tenancy and
capabilities for building platforms on top of it. The kcp server can be subdivided into multiple "logical clusters"
where different logical clusters can be used by different users in the organization.

The kcp server only provides resources to accomplish this, such as `LogicalCluster`, `Workspace`, `APIBinding` and
`APIExport`, on top of some core Kubernetes resources, like `ConfigMap` and `Secret`. It doesn't provide Kubernetes
resources for managing and orchestrating workloads, such as Pods and Deployments. The list of resources provided by
default can be found in the [Built-in APIs document](./apis/built-in).

kcp follows the Kubernetes API semantics in each logical cluster, i.e. kcp should be conformant to the subset of the
Kubernetes conformance suite that applies to the APIs available in kcp. In other words, if you're building a platform
on top of kcp, you'll be required to build Kubernetes-like resources and APIs, think of CustomResourceDefinitions
(CRDs) and controllers. The difference is that in some cases you might have to use a kcp-specific resources instead of
regular Kubernetes resources to accomplish something. For example, to define your own resource that can be used across
different logical clusters, you'll have to use the `APIResourceSchema` resource instead of `CustomResourceDefinition`.

As the kcp server is based on the Kubernetes API server, it provides very similar configuration options and uses etcd
as its datastore, so if you have experience with operating Kubernetes, you can very easily apply it to kcp.

At the end, the kcp server and all logical clusters created on top of it can be accessed using the regular Kubernetes
tooling such as kubectl and client-go.

!!! note
    The only exception to this are Kubernetes controllers. The regular Kubernetes controllers can work with a single
    (logical) cluster. However, if you're building a developer platform on top of kcp, you likely want your controller
    to reconcile objects across multiple logical clusters. At the moment, this is not supported by upstream Kubernetes
    controller-runtime. Instead you have to use
    [our fork of it](https://github.com/kcp-dev/controller-runtime/tree/kcp-0.19/examples/kcp).
    We're working with the upstream community on adding support for the multi-cluster setups to the Kubernetes
    controller-runtime.

## Logical Cluster

A logical cluster is a way to subdivide a kcp server and its `etcd` datastore into multiple clusters without requiring
multiple API server and etcd instances. A logical cluster is able to amortize the cost of a new cluster to be near-zero
memory and storage (similar cost as a namespace), so that we can create a huge number of empty clusters cheaply.

Each logical cluster has it's own set of APIs installed, it's own objects, and different access semantics and
policies, i.e. RBAC rules. In other words, each logical cluster is isolated from other logical clusters.

This is accomplished by adding an additional attribute to an object's identifier in etcd and kcp server to associate
an object with its logical cluster. On the storage level, the regular Kubernetes API server identifies objects using
(group, version, resource, optional namespace, name). The kcp server enriches this identifier with logical cluster's
name, so we have (group, version, resource, **logical cluster name**, optional namespace, name).

A logical cluster provides Kubernetes-cluster-like HTTPS/CRUD endpoints, i.e. endpoints that the typical Kubernetes
client tooling (e.g. client-go, controller-runtime, and others) can interact with as they would with regular Kubernetes
clusters. Using the appropriate kubeconfig file, a user can run `kubectl get foo -A` to return all objects of type
`foo` across all namespaces in that concrete logical cluster.

To a user, a logical cluster appears to be a Kubernetes cluster without any of the container orchestration specific
resources (e.g. Pods, ReplicaSets, Deployments...). It has its own discovery, its own OpenAPI spec, and follows the
Kubernetes-like constraints about uniqueness of Group-Version-Resource and its behavior. For example, it's not
possible to have two identical GVRs with different schemas in one logical cluster, but it's possible to have two
identical GVRs with different schemas in different logical clusters.

However, in the most of cases, you'll never create a logical cluster on its own. Instead, you would use an abstraction,
such as Workspace, to create logical cluster.

!!! note
    There could be multiple different models that result in logical clusters being created, with different policies or
    lifecycle. The Workspace is intended to be the most canonical way to create logical clusters for anyone willing
    to build control planes.

!!! note
    Throughout the documentation, terms "workspace" and "logical cluster" might be interchanged. In the most of cases,
    unless stated otherwise, what applies to a workspace applies to a logical cluster as well, because Workspaces are
    abstractions built on top of logical clusters.

## Workspace

A Workspace is an abstraction used to create and provision logical clusters. Aside from creating the LogicalCluster
object, the responsibility of a Workspace is to initialize the given logical cluster by installing the required
resources for the given workspace type, creating the initial objects, and more.

Workspaces are managed via the `Workspace` resource. It's a simple resource that mainly contains the workspace name,
type, and URL.

Workspaces are organized in a multi-root tree structure. The root workspace is created by the kcp server by default
and it doesn't have its own `Workspace` object (it only has the corresponding `LogicalCluster` object). Each type of
workspace may restrict the types of its children and may restrict the types it may be a child of; a parent-child
relationship is allowed if and only if the parent allows the child and the child allows the parent.

A Workspace's path is based on the hierarchy and the user provided name. For example, if you create a workspace called
`bar` in a workspace called `foo` (which is a child of the root workspace), the relevant workspace paths will be:

- `root:foo` for the `foo` workspace
- `root:foo:bar` for the `bar` workspace

The workspace path is used for building the workspace URL and for accessing the workspace via the `ws` kubectl plugin.

More information, including examples, can be found in the the [Workspaces document](../workspaces).

### Workspace Types

Workspaces have types, which are mostly oriented around a set of default or optional APIs exposed. For example:

- a workspace intended for deploying applications might expose the same API objects a user would encounter on a
  physical cluster,
- a workspace intended for building Knative functions might expose only the Knative serving APIs, ConfigMaps, Secrets,
  and optionally enable Knative Eventing APIs.

By default, each workspace has the [built-in APIs installed and available to its users](./apis/built-in).

More information, including a list of Workspace Types and examples, can be found in the
[Workspace Types document](../workspaces/workspace-types/).

## Virtual Workspaces

A Virtual Workspace API server is a Kubernetes-like API server under a custom URL that's serving Virtual Workspaces
or more precisely Virtual Logical Clusters. The API surface looks very similar to the kcp server itself sharing the
same HTTP path structure, with each (Virtual) Logical Cluster having its own HTTP endpoints under
`clusters/<logical cluster>`.

That API server provides Virtual Workspaces as a set of HTTP/CRUD endpoints that follow the same semantics as the real
Workspaces, but that can serve any data (as decided by the virtual workspace developer) in a similar way as the real
Workspaces serve resources and objects. The term Virtual Logical Cluster has the same meaning, but it's used in a
context when the data that's served is the data that's coming from the real logical cluster.

Most often, the Virtual Workspaces are used to collect and show objects from multiple real Workspaces in a single
Virtual Workspace. The Virtual Workspaces mechanism also allows transforming that data, e.g. to hide the sensitive
information or to add additional information. This allows viewing resources across different logical clusters in a
"single pane of glass" fashion, which enables two important use cases:

- the user may have access to different workspaces: a workspace that belongs to the user personally, and one that
  belongs to a business organization. The user might want to list and manage their API objects from all workspaces in a
  single call (e.g. single `kubectl get` invocation),
- a service provider might want to get a list of all instances (from all users) of the API resource they provide,
  so that they can do reconciliation, have insights into how their API is used, and more.

For that reason, the Virtual Workspaces are often tied to another resource, such as `APIExport` or `WorkspaceType`.
For example, the Virtual Workspace that's showing some resource from all real workspaces might be tied to an APIExport
(more about APIExports later in the document). However, this is not a strict requirement, there might be Virtual
Workspaces that are standalone in this way.

The RBAC mechanism is used to control what the Virtual Workspaces can access and in what manner. It's important to note
that the Virtual Workspaces are not read-only, but that they can also mutate resources in the corresponding real
logical cluster.

**Finally, for simplicity, it has been decided to call the Virtual Workspace API server simply as the Virtual
Workspace.** This is very important to understand, and this is coming from the fact that the Virtual Workspaces,
as introduced above, are simply different endpoints served by that API server. The Virtual Workspace doesn't exist as
a resource in kcp, it's purely a very flexible API (server) that can connect to the kcp server for the sake of
gathering and mutating the needed objects.

kcp provides a set of default Virtual Workspaces, but you can also build and run your own. The Virtual Workspaces are
implemented in the Go programming language using the [appropriate kcp library](https://github.com/kcp-dev/kcp/tree/main/pkg/virtual/framework).
Naturally, such Virtual Workspaces are not integrated in the kcp binary, but are supposed to be run as a dedicated
binary alongside the kcp server.

!!! note
    In a sharded setup, you need to run the Virtual Workspace (API server) for each kcp shard/server that you have.

More information, including concrete examples and a list of frequently asked questions, can be found in the
[Virtual Workspaces document](../workspaces/virtual-workspaces/).

## Exporting/Binding APIs

One of the core values of the kcp project is to enable providing APIs that can be consumed inside multiple logical
clusters. This is done via an exporting/binding mechanism, i.e. via API Exports and API Bindings.

The general workflow is:

- Define your API resources through `APIResourceSchemes` similar to how you would define CustomResourceDefinitions
  (CRDs)
- Create `APIExport` in the same workspace/logical cluster with a list of `APIResourceSchemes` that you want to export
- Users can create `APIBinding` in their workspaces referring to your `APIExport` if you grant the permission. This
  will result in API resources that are defined in the referenced `APIExport` to be installed in the user's workspace
- Users can now create API resources of those types in their workspace. You can build and run controllers that are
  going to reconcile those resources across different workspaces.

## Shard

A failure domain within the larger control plane service that cuts across the primary functionality. Most distributed
systems must separate functionality across shards to mitigate failures, and typically users interact with shards
through some transparent serving infrastructure. Since the primary problem of building distributed systems is reasoning
about failure domains and dependencies across them, it is critical to allow operators to effectively match shards,
understand dependencies, and bring them together.

A control plane should be shardable in a way that maximizes application SLO - gives users a tool that allows them to
better define their applications not to fail.

### Sharding in kcp

In the sense of kcp, sharding involves:

- running multiple kcp server instances, each with its own etcd datastore,
- registering all kcp instances with the root shard by creating a `Shard` object in the root workspace.

A shard hosts its own set of Workspaces. The sharding mechanism in kcp allows you to make the workspace hierarchy span
many shards transparently, while ensuring you can bind APIs from logical clusters running in different shards.
The [Sharding documentation](./sharding/shards/) has more details about possibilities of sharding, and we strongly
recommend reading this document if you want to shard your kcp setup.

In a sharded setup, you'll be sending requests to a component called Front Proxy (`kcp-front-proxy`) instead to a
specific shard (kcp server). Front Proxy is a shard-aware stateless proxy that's running in front of kcp API servers.
Front proxy is able to determine the shard where your workspace is running and redirect/forward your request to that
shard. Upon creating a workspace, you'll be sending request to front proxy, which will schedule a workspace on one of
available shards.

!!! note
    Depending on how you setup kcp, you might have front proxy running even though you don't have a sharded setup.
