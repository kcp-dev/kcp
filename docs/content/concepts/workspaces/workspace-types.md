---
description: >
  What are workspaces and how to use them.
---

# Workspace Types

Workspaces have a type. A type is defined by a `WorkspaceType`. A type
defines initializers. They are set on new Workspace objects and block the
workspace from leaving the initializing phase. Both system components and
3rd party components can use initializers to customize Workspaces on creation,
e.g. to bootstrap resources inside the workspace, or to set up permission in its parent.

kcp comes with a built-in set of workspace types, and the admin may create objects that
define additional types.

- **Root Workspace** is a singleton. It holds some data that applies
  to all workspaces, such as the set of defined workspace types
  (objects of type `WorkspaceType`).
- **Home Workspace** is a user's private workspace, created on first
  access. It holds user resources such as secrets, configmaps, etc.
  See [User Home Workspaces](#user-home-workspaces) below.
- **Universal Workspace** is a basic type of workspace with no
  particular nature. Has no restrictions on parent or child workspace
  types.

The following workspace types are created by kcp if the `workspace-types` battery
is enabled:

- **Organization Workspace** are workspaces holding organizational
  data, e.g. definitions of user workspaces, roles, policies,
  accounting data. Can only be a child of root.
- **Team Workspace** can only be a child of an Organization workspace.

A workspace of type `Universal` is a workspace without further initialization
or special properties by default, and it can be used without a corresponding
`WorkspaceType` object (though one can be added and its initializers will be
applied).

!!! note
    In order to create workspaces of a given type (including `Universal`)
    you must have `use` permissions against the `workspacetypes` resource with the
    lower-case name of the workspace type (e.g. `universal`). All `system:authenticated`
    users inherit this permission automatically for type `Universal`.

The different workspace types are discussed below.

## User Home Workspaces

User home workspaces are an optional feature of kcp, enabled with `--enable-home-workspaces`.
Each user gets a private workspace where they are cluster-admin.

There is a special virtual workspace called `~` in the root workspace. Accessing it
(e.g. `kubectl ws ~`) resolves to the current user's home workspace. The home workspace
is created on first access, so users only cost resources once they actually use it. Only
users in the configured creator groups (`--home-workspaces-home-creator-groups`, default
`system:authenticated`) get one.

Home workspaces are not part of a `root:users:...` path hierarchy. Each one is a
standalone logical cluster whose name is derived from the user name, reachable via the
path `user:<user-name>`.

!!! note
    Older kcp versions arranged home workspaces under a bucketed path such as
    `root:users:a8:f1:adam`. Those "bucket-style" home workspaces are still resolved if
    they exist, but new ones are no longer created that way.

## Organization Workspaces

Organization workspaces are workspaces of type `Organization`, defined in the
root workspace. Organization workspaces are accessible at `/clusters/root:<org-name>`.

!!! note
    The organization WorkspaceType can only be created in the root workspace
    verified through admission.

Organization workspaces have standard resources (on-top of `Universal` workspaces)
which include the `Workspace` API defined through an CRD deployed during
organization workspace initialization.

## Root Workspace

The default root workspace is a singleton in the system accessible under `/clusters/root`.
It is not represented by a `Workspace` anywhere, but shares the same properties.

Inside the root workspace at least the following resources are bootstrapped on
kcp startup:

- Workspace CRD
- WorkspaceType CRD
- Shard CRD
- Partition CRD
- PartitionSet CRD

The root workspace is the only one that holds `Shard` objects. Shards
are used to schedule a new Workspace to, i.e. to select in which etcd the
workspace content is to be persisted.

## System Workspaces

System workspaces are local to a shard and are named in the pattern `system:<system-workspace-name>`.
See the dedicated [System Workspaces](./system-workspaces.md) page for details.

## Workspace Type Extensions and Constraints

kcp offers extensions and constraints that enable you inherit functionality from other
workspace types and create custom workspace hierarchies for your organizational structure.

A `WorkspaceType` can extend one or more other `WorkspaceTypes` using the `spec.extend.with`
field.

**Example**

```yaml
apiVersion: tenancy.kcp.io/v1alpha1
kind: WorkspaceType
metadata:
  name: sample
spec:
  extend:
    with:
    - name: universal
    - name: custom
```

In this example, the `sample` workspace type:

* inherits [initializers](./workspace-initialization.md) from the extended types
* is considered as an extended type during type constraint evaluation

You can also extend `WorkspaceTypes` from other workspaces by specifying the path:

```yaml
apiVersion: tenancy.kcp.io/v1alpha1
kind: WorkspaceType
metadata:
  name: custom
spec:
  extend:
    with:
    - name: standard
      path: root:base
```

!!! note
    A type reference with a `path` points at a type in another workspace. To use it, you
    need `use` permission on that `workspacetypes` resource in the target workspace,
    not just in your own. The same applies when a `Workspace`'s `spec.type` references a
    type by path.

### Lifecycle Permissions

A `WorkspaceType` can declare the RBAC rules its initializer and terminator controllers are
allowed to use against the content of workspaces of this type:

* `spec.initializerPermissions` — `[]rbacv1.PolicyRule` evaluated by the initializing
  virtual workspace content proxy on every request, before forwarding to the shard.
* `spec.terminatorPermissions` — same, for the terminating virtual workspace.

When set, the VW forwards allowed requests with the controller's **own identity** plus a
synthetic group (`system:kcp:initializer:<name>` / `system:kcp:terminator:<name>`) that the
shard's workspace content authorizer trusts as a "pre-authorized by VW" marker. When unset,
the VW falls back to impersonating the workspace owner (`spec.createdBy` on the `LogicalCluster`).

```yaml
apiVersion: tenancy.kcp.io/v1alpha1
kind: WorkspaceType
metadata:
  name: tenant
spec:
  initializer: true
  terminator: true
  initializerPermissions:
    - apiGroups: [""]
      resources: ["configmaps", "namespaces"]
      verbs: ["get", "list", "create", "update"]
    - apiGroups: ["apis.kcp.io"]
      resources: ["apibindings"]
      verbs: ["get", "list", "create"]
  terminatorPermissions:
    - apiGroups: [""]
      resources: ["*"]
      verbs: ["get", "list", "delete"]
```

See [Workspace Initialization](./workspace-initialization.md#scoping-initializer-content-access)
and [Workspace Termination](./workspace-termination.md#scoping-terminator-content-access) for
the complete model, including how synthetic groups are protected from forgery and how
extended `WorkspaceType`s evaluate independently.

### Workspace Constraint Mechanisms

kcp provides two primary constraint mechanisms for workspace types:

* `limitAllowedChildren`: Controls which workspace types can be created as children.
* `limitAllowedParents`: Controls which workspace types can serve as parents.

```yaml
...
spec:
  limitAllowedParents:
    types:
    - name: sample
      path: root
  limitAllowedChildren:
    types:
    - name: custom
      path: root
```

You can also block all types from being used as children:

```yaml
apiVersion: tenancy.kcp.io/v1alpha1
kind: WorkspaceType
metadata:
  name: leaf-workspace
spec:
  limitAllowedChildren:
    none: true
```

This ensures that no other workspace type can be created as a child of `leaf-workspace`.
