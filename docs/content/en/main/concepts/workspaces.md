---
title: "Workspaces"
linkTitle: "Workspaces"
weight: 1
description: >
  What are workspaces and how to use them
---

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

There are different types of workspaces, and workspaces are arranged
in a tree.  Each type of workspace may restrict the types of its
children and may restrict the types it may be a child of; a
parent-child relationship is allowed if and only if the parent allows
the child and the child allows the parent.  The kcp binary has a
built-in set of workspace types, and the admin may create objects that
define additional types.

- **Root Workspace** is a singleton.  It holds some data that applies
  to all workspaces, such as the set of defined workspace types
  (objects of type `WorkspaceType`).
- **HomeRoot Workspace** is normally a singleton, holding the branch
  of workspaces that contains the user home workspaces as descendants.
  Can only be a child of the root workspace, and can only have
  HomeBucket children.
- **HomeBucket Workspace** are intermediate vertices in the hierarhcy
  between the HomeRoot and the user home workspaces.  Can be a child
  of the root or another HomeBucket workspace.  Allowed children are
  home and HomeBucket workspaces.
- **Home Workspace** is a user's home workspace.  These hold user
  resources such as applications with services, secrets, configmaps,
  deployments, etc.  Can only be a child of a HomeBucket workspace.
- **Organization Workspace** are workspaces holding organizational
  data, e.g. definitions of user workspaces, roles, policies,
  accounting data.  Can only be a child of root.
- **Team Workspace** can only be a child of an Organization workspace.
- **Universal Workspace** is a basic type of workspace with no
  particular nature.  Has no restrictions on parent or child workspace
  types.

## ClusterWorkspaces

ClusterWorkspaces define traditional etcd-based, CRD enabled workspaces, available
under `/clusters/<parent-workspace-name>:<cluster-workspace-name>`. E.g. organization
workspaces are accessible at `/clusters/root:<org-name>`. A user workspace is
accessible at `/clusters/root:users:<bucket-d1>:..:<bucket-dN>:<user-workspace-name>`.

ClusterWorkspaces have a type. A type is defined by a WorkspaceType. A type
defines initializers. They are set on new ClusterWorkspace objects and block the
cluster workspace from leaving the initializing phase. Both system components and
3rd party components can use initializers to customize ClusterWorkspaces on creation,
e.g. to bootstrap resources inside the workspace, or to set up permission in its parent.

A cluster workspace of type `Universal` is a workspace without further initialization
or special properties by default, and it can be used without a corresponding
WorkspaceType object (though one can be added and its initializers will be
applied). ClusterWorkSpaces of type `Organization` are described in the next section.

{{% alert title="Note" color="primary" %}}
In order to create cluster workspaces of a given type (including `Universal`)
you must have `use` permissions against the `workspacetypes` resources with the
lower-case name of the cluster workspace type (e.g. `universal`). All `system:authenticated`
users inherit this permission automatically for type `Universal`.
{{% /alert %}}

ClusterWorkspaces persisted in etcd on a shard have disjoint etcd prefix ranges, i.e.
they have independent behaviour and no cluster workspace sees objects from other
cluster workspaces. In contrast to namespace in Kubernetes, this includes non-namespaced
objects, e.g. like CRDs where each workspace can have its own set of CRDs installed.

## User Home Workspaces

User home workspaces are an optional feature of kcp. If enabled (through `--enable-home-workspaces`), there is a special
virtual `Workspace` called `~` in the root workspace. It is used by `kubectl ws` to derive the full path to the user
home workspace, similar to how Unix `cd ~` move the users to their home.

The full path for a user's home workspace has a number of parts: `<prefix>(:<bucket>)+:<user-name>`. Buckets are used to
ensure that at most ~1000 sub-buckets or users exist in any bucket, for scaling reasons. The bucket names are deterministically
derived from the user name (via some hash). Example for user `adam` when using default configuration:
`root:users:a8:f1:adam`.

User home workspaces are created on-demand when they are first accessed, but this is not visible to the user, allowing
the system to only incur the cost of these workspaces when they are needed. Only users of the configured
home-creator-groups (default `system:authenticated`) will have a home workspace.

### Bucket configuration options

The `kcp` administrator can configure:

- `<prefix>`, which defaults to `root:users`
- bucket depth, which defaults to 2
- bucket name length, in characters, which defaults to 2

The following outlines valid configuration options. With the default setup, ~5 users or ~700 sub-buckets will be in
any bucket.

{{% alert title="Warning" color="warning" %}}
DO NOT set the bucket size to be longer than 2, as this will adversely impact performance.
{{% /alert %}}

User-names have `(26 * [(26 + 10 + 2) * 61] * 36 = 2169648)` permutations, and buckets are made up of lowercase-alpha
chars.  Invalid configurations break the scale limit in sub-buckets or users. Valid configurations should target
having not more than ~1000 sub-buckets per bucket and at least 5 users per bucket.

### Valid Configurations

|length|depth|sub-buckets|users|
|------|-----|-----------|-----|
|1     |3    |26 * 1 = 26|2169648 / (26)^3 = 124 |
|1     |4    |26 * 1 = 26|2169648 / (26)^4 = 5 |
|2     |2    |26 * 26 = 676|2169648 / (26*26)^2 = 5 |

### Invalid Configurations

These are examples of invalid configurations and are for illustrative purposes only. In nearly all cases, the default values
will be sufficient.

|length|depth|sub-buckets|users|
|------|-----|-----------|-----|
|1     |1    |26 * 1 = 26|2169648 / (26) = 83448 |
|1     |2    |26 * 1 = 26|2169648 / (26)^2 = 3209 |
|2     |1    |26 * 26 = 676|2169648 / (26*26) = 3209 |
|2     |3    |26 * 26 = 676|2169648 / (26*26)^3 = .007 |
|3     |1    |26 *26* 26 = 17576|2169648 / (26*26*26) = 124 |
|3     |2    |26 *26* 26 = 17576|2169648 / (26*26*26)^2 = .007 |

## Organization Workspaces

Organization workspaces are ClusterWorkspaces of type `Organization`, defined in the
root workspace. Organization workspaces are accessible at `/clusters/root:<org-name>`.

{{% alert title="Note" color="primary" %}}
The organization WorkspaceType can only be created in the root workspace
verified through admission.
{{% /alert %}}

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
