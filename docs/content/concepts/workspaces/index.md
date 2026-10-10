# Workspaces

Multi-tenancy is implemented through workspaces. A workspace is a Kubernetes-cluster-like
HTTPS endpoint, i.e. an endpoint usual Kubernetes client tooling (client-go, controller-runtime
and others) and user interfaces (kubectl, helm, web console, ...) can talk to like to a
Kubernetes cluster. Workspaces become available under
`/clusters/<parent-workspace-name>:<cluster-workspace-name>`.

Workspaces are backed by logical clusters, which means they are persisted in etcd on a shard
with disjoint etcd prefix ranges, i.e. they have independent behaviour and no workspace
sees objects from other workspaces. In contrast to namespace in Kubernetes, this includes
non-namespaced objects, e.g. like CRDs where each workspace can have its own set of CRDs installed.

!!! note
    For workspaces not backed by storage, check out [virtual workspaces](./virtual-workspaces.md)
    that transform other APIs e.g. by projections or by applying visibility filters
    (e.g. showing all workspaces or all namespaces the current user has access to).
    Virtual workspaces are not part of the `/clusters/` path structure.

Workspaces are represented to the user via the `Workspace` kind, e.g.

```yaml
kind: Workspace
apiVersion: tenancy.kcp.io/v1alpha1
spec:
  type:
    name: universal
    path: root
  URL: https://kcp.example.com/clusters/myapp
```

The `type` is a reference to a `WorkspaceType`, by lower-cased `name`, and
optionally the `path` of the workspace that owns it.

There are different [types of workspaces](./workspace-types.md), and workspaces are arranged
in a tree.  Each type of workspace may restrict the types of its
children and may restrict the types it may be a child of; a
parent-child relationship is allowed if and only if the parent allows
the child and the child allows the parent.

## Pages

### [Workspace Types](workspace-types.md)
What are workspaces and how to use them.

### [WorkspaceType Best Practices](workspace-types-best-practices.md)
Guidance on how to design, deploy, and consume WorkspaceTypes safely.

### [System Workspaces](system-workspaces.md)
System workspaces are shard-local logical clusters with special meaning to kcp internals.

### [Virtual Workspaces](virtual-workspaces.md)
What are virtual workspaces and how do they work?

### [Workspace Initialization](workspace-initialization.md)

### [Workspace Termination](workspace-termination.md)

### [Workspace Mounts](mounts.md)
What are workspace mounts and how do they work?

### [Total Object Count Limit](object-count-limit.md)
Enforce a hard limit on the total number of objects in a workspace.

