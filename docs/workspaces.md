# Workspaces

Multi-tenancy is implemented through workspaces. A workspace is a Kubernetes-cluster-like
HTTPS endpoint, i.e. an endpoint usual Kubernetes client tooling (client-go, controller-runtime
and others) and user interfaces (kubectl, helm, web console, ...) can talk to like to a
Kubernetes cluster.

Workspaces can be backed by a traditional REST store implementation through CRDs
or native resources, persisted in etcd. But there can be alternative implementations
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
  e.g. definitions of enduser workspaces, roles, policies, physical cluster
  registrations, accounting data, etc.
- **Root Workspace** is a singleton holding cross-organizational data and
  the definition of the organizations.

## ClusterWorkspaces

ClusterWorkspaces define traditional etcd-based, CRD enabled workspaces, available
under `/clusters/<parent-workspace-name>:<cluster-workspace-name>`. E.g. organization
workspaces are accessible at `/clusters/root:<org-name>`. An enduser workspace is
accessible at `/clusters/<org-name>:<enduser-workspace-name>`.

ClusterWorkspaces have a type. A type is defined by a ClusterWorkspaceType. A type
defines initializers. They are set on new ClusterWorkspace objects and block the
cluster workspace to leave the initializing phase. Both system components and 3rdparty
components can use initializers to customize ClusterWorkspaces on creation, e.g.
to bootstrap resources inside the workspace, or to set up permission in its parent.

An organization is a ClusterWorkspace of type `Organization`. A cluster workspace of
type `Universal` is a workspace without further initialization or special properties.

ClusterWorkspaces persisted in etcd on a shard have disjoint etcd prefix ranges.

## Organization Workspaces

Organization workspaces are ClusterWorkspaces of type `Organization`, defined in the
root workspace. Organization workspace are accessible at `/clusters/root:<org-name>`.

Organization workspaces have standard resources (on-top of `Universal` workspaces) 
which include the `ClusterWorkspace` API defined through an CRD deployed during
organization workspace initialization.

## Root Workspace

The root workspace is a singleton in the system accessible under `/clusters/root`.
It is not represented by a ClusterWorkspace anywhere, but shared the same properties.

Inside the root workspace at least the following resources are bootstrapped on
kcp startup:

- ClusterWorkspace CRD
- WorkspaceShard CRD
- Cluster CRD.

The root workspace is the only one that holds WorkspaceShard objects. WorkspaceShards
are used to schedule a new ClusterWorkspace to, i.e. to select in which etcd the
ClusterWorkspace is to be persisted.

## System Workspaces

System workspaces are local to a shard and are named in the pattern `system:<system-workspace-name>`.
They are only accessible via a shard-local admin user, and there is neither a definition 
via a ClusterWorkspace, nor is there any validation of requests that thet system 
workspace exists.

The `system:admin` system workspace is special as it is also accessible through `/`
of the shard, and at `/cluster/system:admin` at the same time.