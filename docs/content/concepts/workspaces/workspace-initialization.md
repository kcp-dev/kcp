# Workspace Initialization

Workspace initialization in kcp involves setting up initial configurations and resources for a workspace when it is created. This process is managed through `initializers`, which are enabled via `WorkspaceType` objects. This concept is the opposite of Kubernetes [finalizers](https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers/). This document covers how to configure initializers, the necessary RBAC permissions, URL schemes, and the reasons for using initializers.

## Initializers

Initializers are used to customize workspaces and bootstrap required resources upon creation. Initializers are defined in WorkspaceType objects. This way, a user can define a controller that will process the Workspace and remove the initializer, moving it from the Initializing phase to the Ready phase.

### Defining Initializers in WorkspaceTypes

A `WorkspaceType` can specify an initializer using the `initializer` field. Here is an example of a `WorkspaceType` with an initializer.

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

## initializingworkspaces Virtual Workspace

As a service provider, you can use initializingworkspaces virtual workspace to manage workspace resources in the initializing phase.

### Endpoint URL path

`initializingworkspaces` Virtual Workspace provide a virtual api-server to access workspaces that are initializing with the specific initializer. These URLs are published in the status of WorkspaceType object.


```yaml
  virtualWorkspaces:
  - url: https://<front-proxy-ip>:6443/services/initializingworkspaces/<initializer>
```

This is an example URL path for accessing logical cluster apis for a specific initializer in a `initializingworkspaces` virtual workspace.

```yaml
/services/initializingworkspaces/<initializer>/clusters/<logical-cluster>/apis/example.com/v1alpha1/exampleapis
```

### Example workflow

* Add your custom WorkspaceType to the platform with an initializer.

* Create a workspace with the necessary warrants and scopes. The workspace will stay in the initializing state as the initializer is present.

* Use a controller to watch your initializing workspaces, you can interact with the workspace through the virtual workspace endpoint:

```yaml
/services/initializingworkspaces/foo/clusters/*/apis/core.kcp.io/v1alpha1/logicalclusters
```
* Once you get the object, you need to initialize the workspace with its related resources, using the same endpoint

* Once the initialization is complete, use the same endpoint to remove the initializer from the workspace.
