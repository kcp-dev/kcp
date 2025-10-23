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
