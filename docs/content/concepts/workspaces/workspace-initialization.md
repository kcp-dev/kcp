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

Since `WorkspaceType.spec.initializer` is a boolean field, each WorkspaceType comes with a single initializer by default. However each WorkspaceType inherits the initializers of its parent workspaces. As a result, it is possible to have multiple initializers on a WorkspaceType, but you will need to nest them.
Here is a example:

1. In `root` workspace, create a new WorkspaceType called `parent`. You will receive a `root:parent` initializer
2. In the newly created `parent` workspace, create a new WorkspaceType `child`. You will receive a `root:parent:child` initializer
3. Whenever a new workspace is created in the child workspace, it will receive both the `root:parent` as well as the `root:parent:child` initializer

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

### Responsibilities Of Custom Intitialization Controllers

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
  - url: https://<front-proxy-url>/services/initializingworkspaces/root:example
```

You can use this url to construct a kubeconfig for your controller. To do so, use the url directly as the `cluster.server` in your kubeconfig and provide a user with sufficient permissions (see [Enforcing Permissions for Initializers](#enforcing-permissions-for-initializers))

### Code Sample

* It is important to use the kcp-dev controller runtime fork, as regular controller runtime is not able to deal with all logical clusters being name "cluster"
* LogicalClusters cannot updated using update api, but must be updated using patch api

// TODO paste in sample once it is finished
