# Workspaces

Multi-tenancy is implemented through workspaces. A workspace is a Kubernetes-cluster-like
HTTPS endpoint, i.e. an endpoint usual Kubernetes client tooling (client-go, controller-runtime
and others) and user interfaces (kubectl, helm, web console, ...) can talk to like to a
Kubernetes cluster.

Workspaces can be backed by a traditional REST store implementation through CRDs
or native resources persisted in etcd. But there can be alternative implementations
for special access patterns, e.g. a virtual workspace apiserver that transforms
other APIs e.g. by projections (Workspace in kcp is a projection of ClusterWorkspace)
or by applying visibility filters (e.g. showing all workspaces or all namespaces 
the current user has access to).

Workspaces are represented to the user via the Workspace kind, e.g.

```yaml
kind: Workspace
apiVersion: tenancy.kcp.dev/v1beta1
spec:
  type: Universal
status:
  url: https://kcp.example.com/clusters/myapp
```

There is a 3-level hierarchy of workspaces:

- **Enduser Workspaces** are workspaces holding enduser resources, e.g.
  applications with services, secrets, configmaps, deployments, etc.
- **Organization Workspaces** are workspaces holding organizational data, 
  e.g. definitions of enduser workspaces, roles, policies, accounting data.
- **Root Workspace** is a singleton holding cross-organizational data and
  the definition of the organizations.

## ClusterWorkspaces

ClusterWorkspaces define traditional etcd-based, CRD enabled workspaces, available
under `/clusters/<parent-workspace-name>:<cluster-workspace-name>`. E.g. organization
workspaces are accessible at `/clusters/root:<org-name>`. An enduser workspace is
accessible at `/clusters/<org-name>:<enduser-workspace-name>`.

ClusterWorkspaces have a type. A type is defined by a ClusterWorkspaceType. A type
defines initializers. They are set on new ClusterWorkspace objects and block the
cluster workspace from leaving the initializing phase. Both system components and 
3rd party components can use initializers to customize ClusterWorkspaces on creation, 
e.g. to bootstrap resources inside the workspace, or to set up permission in its parent.

A cluster workspace of type `Universal` is a workspace without further initialization 
or special properties by default, and it can be used without a corresponding 
ClusterWorkspaceType object (though one can be added and its initializers will be 
applied). ClusterWorkSpaces of type `Organization` are described in the next section.

Note: in order to create cluster workspaces of a given type (including `Universal`) 
you must have `use` permissions against the `clusterworkspacetypes` resources with the 
lower-case name of the cluster workspace type (e.g. `universal`). All `system:authenticated`
users inherit this permission automatically for type `Universal`.

ClusterWorkspaces persisted in etcd on a shard have disjoint etcd prefix ranges, i.e.
they have independent behaviour and no cluster workspace sees objects from other
cluster workspaces. In contrast to namespace in Kubernetes, this includes non-namespaced
objects, e.g. like CRDs where each workspace can have its own set of CRDs installed.

## User Home Workspaces

TODO: explaining the concept and related virtual existence and automatic creation 

The following cluster workspace types are created internally to support User Home Workspaces:

* `Homeroot`: the workspace that will contain all the Home workspaces, spread accross buckets. - Can contain only Homebucket workspaces
* `Homebucket`: the type of workspaces that will contains a subset of all home workspaces. - Can contain either Homebucket (multi-level bucketing) or Home workspaces
* `Home`: the ClusterWorkspace of home workspaces - can contain any type of workspaces as children (especially universal workspaces)

## Organization Workspaces

Organization workspaces are ClusterWorkspaces of type `Organization`, defined in the
root workspace. Organization workspaces are accessible at `/clusters/root:<org-name>`.

Note: the organization ClusterWorkspaceType can only be created in the root workspace
verified through admission.

Organization workspaces have standard resources (on-top of `Universal` workspaces) 
which include the `ClusterWorkspace` API defined through an CRD deployed during
organization workspace initialization.

## Root Workspace

The root workspace is a singleton in the system accessible under `/clusters/root`.
It is not represented by a ClusterWorkspace anywhere, but shares the same properties.

Inside the root workspace at least the following resources are bootstrapped on
kcp startup:

- ClusterWorkspace CRD
- WorkspaceShard CRD
- Cluster CRD.

The root workspace is the only one that holds WorkspaceShard objects. WorkspaceShards
are used to schedule a new ClusterWorkspace to, i.e. to select in which etcd the
cluster workspace content is to be persisted.

## System Workspaces

System workspaces are local to a shard and are named in the pattern `system:<system-workspace-name>`.

They are only accessible to a shard-local admin user and there is neither a definition
via a ClusterWorkspace nor any per-request check for workspace existence.

System workspace are only accessible to a shard-local admin user, and there is 
neither a definition via a ClusterWorkspace, nor is there any validation of requests 
that the system workspace exists.

The `system:admin` system workspace is special as it is also accessible through `/`
of the shard, and at `/cluster/system:admin` at the same time.

