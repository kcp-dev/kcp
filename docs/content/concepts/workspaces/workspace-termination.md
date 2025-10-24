# Workspace Termination

Workspace termination in kcp involves setting up optional cleanup logic when a Workspace is marked for deletion. This process is managed through `terminators`, which are enabled via `WorkspaceType` objects. In kcp, Terminators are the counterpart to [initializers](workspace-initialization.md). This document covers how to configure terminators, the necessary RBAC permissions, URL schemes, and the reasons for using terminators.

## Terminators

Terminators are used to customize the cleanup of workspaces and required resources. Terminators are defined in `WorkspaceType` objects and will be propagated to Workspaces and LogicalClusters. By doing this, users can create controllers processing `LogicalClusters` and running custom cleanup logic. Afterwards the controller has to remove its corresponding terminator. Once all terminators are removed, a logical cluster will be deleted. After a logical-cluster is deleted, its workspace is also ready for deletion.

Terminators are meant to be used specifically to customize deletion logic of LogicalClusters. For resources inside your workspace, you can use traditional Kubernetes finalizers.

### Defining Terminators in WorkspaceTypes

A `WorkspaceType` can specify having a terminator using the `terminator` field. Here is an example of a `WorkspaceType` with a terminator.

```yaml
apiVersion: tenancy.kcp.io/v1alpha1
kind: WorkspaceType
metadata:
  name: example
spec:
  terminator: true
  defaultChildWorkspaceType:
    name: universal
    path: root
```

Each terminator has a unique name, which gets automatically generated using  `<workspace-path-of-WorkspaceType>:<WorkspaceType-name>`. So for example, if you were to apply the aforementioned `WorkspaceType` in the `root` workspace, your terminator would be called `root:example`.

Since `WorkspaceType.spec.terminators` is a boolean field, each `WorkspaceType` comes with a single terminator by default. However each `WorkspaceType` inherits the terminators of its parent WorkspaceType. As a result, it is possible to have multiple terminators on a `WorkspaceType` using [WorkspaceType Extension](../../concepts/workspaces/workspace-types.md#workspace-type-extensions-and-constraints)

In the following example, `child` inherits the terminators of `parent`. As a result, child workspaces will have the `root:child` and `root:parent` terminators set.

```yaml
apiVersion: tenancy.kcp.io/v1alpha1
kind: WorkspaceType
metadata:
  name: child
spec:
  terminator: true
  extend:
    with:
    - name: parent
      path: root
```

### Enforcing Permissions for Terminators

The non-root user must have the `terminate` verb on the `WorkspaceType` that the terminator is for. This ensures that only authorized users can perform termination actions using the virtual workspace endpoint. Here is an example of the `ClusterRole`.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: terminate-example-workspacetype
rules:
  - apiGroups: ["tenancy.kcp.io"]
    resources: ["workspacetypes"]
    resourceNames: ["example"]
    verbs: ["terminate"]
```

You can then bind this role to a user or a group.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: terminate-example-workspacetype-binding
subjects:
  - kind: User
    name: user1
    apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: terminate-example-workspacetype
  apiGroup: rbac.authorization.k8s.io
```

## Writing Custom Termination Controllers

Custom Termination Controllers are responsible for handling termination logic for custom WorkspaceTypes. They interact with kcp by:

1. Watching for `LogicalClusters` (the backing object behind Workspaces) which are marked for deletion and have the corresponding terminator set
2. Running any custom cleanup logic
3. Removing the corresponding terminator from the `.status.terminators` list of the `LogicalCluster` after cleanup logic has successfully finished

In order to simplify these processes, kcp provides the `terminating` virtual workspace.

### The `terminating` Virtual Workspace

As a service provider, you can use the `terminatingworkspaces` virtual workspace to manage workspace resources in the terminating phase. This virtual workspace allows you to fetch `LogicalCluster` objects which have a DeletionTimeStamp and request termination by a specific controller.

You can retrieve the url for the TerminatingVirtualWorkspace directly from the `.status.virtualWorkspaces` field of the corresponding `WorkspaceType`. Returning to our previous example using a custom `WorkspaceType` called "example", you will receive the following output:

```sh
$ kubectl get workspacetype example -o yaml
...
status:
  virtualWorkspaces:
  - url: https://<shard-url>/services/terminatingworkspaces/root:example
```

You can use this url to construct a kubeconfig for your controller. To do so, use the url directly as the `cluster.server` in your kubeconfig and provide the subject with sufficient permissions (see [Enforcing Permissions for Terminators](#enforcing-permissions-for-terminators))

### Practical Tips

When writing a custom terminator controller, the following needs to be taken into account:

* We strongly recommend to use [multicluster-runtime](github.com/kcp-dev/multicluster-runtime) to build your controller in order to properly handle which `LogicalCluster` originates from which workspace
* You need to update `LogicalClusters` using patches; They cannot be updated using the update api
