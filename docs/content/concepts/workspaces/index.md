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
  type: Universal
status:
  url: https://kcp.example.com/clusters/myapp
```

There are different [types of workspaces](./workspace-types.md), and workspaces are arranged
in a tree.  Each type of workspace may restrict the types of its
children and may restrict the types it may be a child of; a
parent-child relationship is allowed if and only if the parent allows
the child and the child allows the parent.

## Pages

{% include "partials/section-overview.html" %}
