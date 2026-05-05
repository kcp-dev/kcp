# Workspace Initialization

Workspace initialization in kcp involves setting up initial configurations and resources for a workspace when it is created. This process is managed through `initializers`, which are enabled via `WorkspaceType` objects. This concept is the opposite of Kubernetes [finalizers](https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers/). This document covers how to configure initializers, the necessary RBAC permissions, URL schemes, and the reasons for using initializers.

## Initializers

Initializers are used to customize workspaces and bootstrap required resources upon creation. Initializers are defined in WorkspaceType objects. This way, a user can define a controller that will process the Workspace and remove the initializer, moving it from the Initializing phase to the Ready phase.

### Defining Initializers in WorkspaceTypes

A `WorkspaceType` can specify having an initializer using the `initializer` field. Here is an example of a `WorkspaceType` with an initializer.

```yaml
apiVersion: tenancy.kcp.io/v1alpha1
kind: WorkspaceType
metadata:
  name: example
spec:
  initializer: true
  defaultChildWorkspaceType:
    name: universal
    path: root
```

Each initializer has a unique name, which gets automatically generated using  `<workspace-path-of-WorkspaceType>:<WorkspaceType-name>`. So for example, if you were to apply the aforementioned WorkspaceType on the root workspace, your initializer would be called `root:example`.

Since `WorkspaceType.spec.initializers` is a boolean field, each WorkspaceType comes with a single initializer by default. However each WorkspaceType inherits the initializers of its parent WorkspaceType. As a result, it is possible to have multiple initializers on a WorkspaceType using [WorkspaceType Extension](../../concepts/workspaces/workspace-types.md#workspace-type-extensions-and-constraints)

In the following example, `child` inherits the initializers of `parent`. As a result, child workspaces will have the `root:child` and `root:parent` initializers set.

```yaml
apiVersion: tenancy.kcp.io/v1alpha1
kind: WorkspaceType
metadata:
  name: child
spec:
  initializer: true
  extend:
    with:
    - name: parent
      path: root
```

### Enforcing Permissions for Initializers

The non-root user must have the `verb=initialize` on the `WorkspaceType` that the initializer is for. This ensures that only authorized users can perform initialization actions using virtual workspace endpoint. Here is an example of the `ClusterRole`.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: initialize-example-workspacetype
rules:
  - apiGroups: ["tenancy.kcp.io"]
    resources: ["workspacetypes"]
    resourceNames: ["example"]
    verbs: ["initialize"]
```

You can then bind this role to a user or a group.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: initialize-example-workspacetype-binding
subjects:
  - kind: User
    name: user1
    apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: initialize-example-workspacetype
  apiGroup: rbac.authorization.k8s.io
```

### Scoping Initializer Content Access

The `initialize` verb above only gates **access to the initializing virtual workspace** (the URL where controllers
list/watch `LogicalCluster` objects and modify `status.initializers`). The verb says **nothing** about what the
controller is allowed to do **inside** the workspace it's initializing. There are two modes for that:

#### Mode 1 — Declarative scoped permissions (recommended)

Set `spec.initializerPermissions` on the `WorkspaceType` to a list of standard RBAC `PolicyRule`s. The VW content
proxy evaluates each incoming request against these rules in-process before forwarding to the shard. Allowed
requests are forwarded with the **controller's own identity**; denied requests are rejected with `403` immediately.

```yaml
apiVersion: tenancy.kcp.io/v1alpha1
kind: WorkspaceType
metadata:
  name: example
spec:
  initializer: true
  initializerPermissions:
    - apiGroups: [""]
      resources: ["configmaps", "secrets", "namespaces"]
      verbs: ["get", "list", "create", "update", "delete"]
    - apiGroups: ["apis.kcp.io"]
      resources: ["apibindings"]
      verbs: ["get", "list", "create", "update", "delete"]
```

Why this is the recommended mode:

- **Least privilege.** A controller that only needs ConfigMaps gets only ConfigMaps — not full cluster-admin.
- **Clear audit attribution.** Audit logs show the controller's actual identity, not the workspace owner being
  impersonated. With multiple initializers on a single workspace each one is distinguishable.
- **Avoids cross-cluster impersonation problems.** Workspace owners (especially ServiceAccounts) are often foreign
  to the workspace they own, which previously caused initialization to fail with `User ... cannot create resource ...`
  (see [kcp#4038](https://github.com/kcp-dev/kcp/issues/4038)). With this mode the owner is never impersonated.
- **No materialized state.** No `ClusterRole`/`ClusterRoleBinding` objects are created inside the workspace; the
  rules live on the `WorkspaceType` and are evaluated on the fly. Edits take effect immediately for all workspaces
  of that type.

##### How the trust model works

When a request is allowed, the VW forwards it with the controller's identity **plus a synthetic group** of the form:

```
system:kcp:initializer:<initializer-name>
```

For example, an initializer derived from a `WorkspaceType` named `example` in the `root` workspace produces the
group `system:kcp:initializer:root:example`.

The shard's workspace content authorizer treats the presence of this group as a "pre-authorized by VW" marker and
allows the request without re-evaluating the permissions. This is safe because clients cannot self-assert these
groups: the front-proxy's `--authentication-drop-groups` defaults strip `system:kcp:initializer:*` and
`system:kcp:terminator:*` from every incoming request before any routing happens.

##### Extending types

When a `WorkspaceType` `gamma` extends `alpha` and `beta`, each initializer is evaluated independently against
**its own** `WorkspaceType`'s `initializerPermissions` — there is no merging. A workspace of type `gamma` accessed
through the `alpha` initializer is gated by `alpha`'s permissions only.

#### Mode 2 — Owner impersonation (default, backwards compatible)

If `initializerPermissions` is unset or empty, the VW falls back to impersonating the workspace owner recorded in
`LogicalCluster.spec.createdBy`. Because the owner is bound to the `cluster-admin` `ClusterRole` via the
`workspace-admin` `ClusterRoleBinding`, the controller effectively gets cluster-admin in the workspace.

This preserves historical behavior. It works well when the workspace creator is a regular user who lives in the
same logical cluster as the workspace, but can fail when the creator is foreign to the new workspace (e.g. a
`ServiceAccount` from another cluster). For new `WorkspaceType`s, prefer Mode 1.

## Writing Custom Initialization Controllers

### Responsibilities Of Custom Initialization Controllers

Custom Initialization Controllers are responsible for handling initialization logic for custom WorkspaceTypes. They interact with kcp by:

1. Watching for the creation of new LogicalClusters (the backing object behind Workspaces) with the corresponding initializer on them
2. Running any custom initialization logic
3. Removing the corresponding initializer from the `.status.initializers` list of the LogicalCluster after initialization logic has successfully finished

In order to simplify these processes, kcp provides the `initializingworkspaces` virtual workspace.

### The `initializingworkspaces` Virtual Workspace

As a service provider, you can use the `initializingworkspaces` virtual workspace to manage workspace resources in the initializing phase. This virtual workspace allows you to fetch `LogicalCluster` objects that are in the initializing phase and request initialization by a specific controller.

You can retrieve the url of a Virtual Workspace directly from the `.status.virtualWorkspaces` field of the corresponding WorkspaceType. Returning to our previous example using a custom WorkspaceType called "example", you will receive the following output:

```sh
$ kubectl get workspacetype example -o yaml

...
status:
  virtualWorkspaces:
  - url: https://<shard-url>/services/initializingworkspaces/root:example
```

You can use this url to construct a kubeconfig for your controller. To do so, use the url directly as the `cluster.server` in your kubeconfig and provide the subject with sufficient permissions (see [Enforcing Permissions for Initializers](#enforcing-permissions-for-initializers))

### Code Sample

When writing a custom initializer, the following needs to be taken into account:

* We strongly recommend to use the kcp [initializingworkspace multicluster-provider](https://github.com/kcp-dev/multicluster-provider) to build your custom initializer
* You need to update LogicalClusters using patches; They cannot be updated using the update api

Keeping this in mind, you can use the [multicluster-provider initializingworkspaces example](https://github.com/kcp-dev/multicluster-provider/tree/main/examples/initializingworkspaces) as a starting point for your initialization controller
