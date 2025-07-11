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

When writing a custom initializer, the following needs to be taken into account:

* You need to use the kcp-dev controller-runtime fork, as regular controller-runtime is not able to work as under the hood all LogicalClusters have the sam name
* You need to update LogicalClusters using patches; They cannot be updated using the update api

Keeping this in mind, you can use the following example as a starting point for your intitialization controller

=== "main.go"

    ```Go
    package main

    import (
      "context"
      "fmt"
      "log/slog"
      "os"
      "slices"
      "strings"

      "github.com/go-logr/logr"
      kcpcorev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
      "github.com/kcp-dev/kcp/sdk/apis/tenancy/initialization"
      "k8s.io/client-go/tools/clientcmd"
      ctrl "sigs.k8s.io/controller-runtime"
      "sigs.k8s.io/controller-runtime/pkg/client"
      "sigs.k8s.io/controller-runtime/pkg/kcp"
      "sigs.k8s.io/controller-runtime/pkg/manager"
      "sigs.k8s.io/controller-runtime/pkg/reconcile"
    )

    type Reconciler struct {
      Client          client.Client
      Log             logr.Logger
      InitializerName kcpcorev1alpha1.LogicalClusterInitializer
    }

    func main() {
      if err := execute(); err != nil {
        fmt.Println(err)
        os.Exit(1)
      }
    }

    func execute() error {
      kubeconfigpath := "<path-to-kubeconfig>"

      config, err := clientcmd.BuildConfigFromFlags("", kubeconfigpath)
      if err != nil {
        return err
      }

      logger := logr.FromSlogHandler(slog.NewTextHandler(os.Stderr, nil))
      ctrl.SetLogger(logger)

      mgr, err := kcp.NewClusterAwareManager(config, manager.Options{
        Logger: logger,
      })
      if err != nil {
        return err
      }
      if err := kcpcorev1alpha1.AddToScheme(mgr.GetScheme()); err != nil {
        return err
      }

      // since the initializers name is is the last part of the hostname, we can take it from there
      initializerName := config.Host[strings.LastIndex(config.Host, "/")+1:]

      r := Reconciler{
        Client:          mgr.GetClient(),
        Log:             mgr.GetLogger().WithName("initializer-controller"),
        InitializerName: kcpcorev1alpha1.LogicalClusterInitializer(initializerName),
      }

      if err := r.SetupWithManager(mgr); err != nil {
        return err
      }
      mgr.GetLogger().Info("Setup complete")

      if err := mgr.Start(context.Background()); err != nil {
        return err
      }

      return nil
    }

    func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
      return ctrl.NewControllerManagedBy(mgr).
        For(&kcpcorev1alpha1.LogicalCluster{}).
        // we need to use kcp.WithClusterInContext here to target the correct logical clusters during reconciliation
        Complete(kcp.WithClusterInContext(r))
    }

    func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
      log := r.Log.WithValues("clustername", req.ClusterName)
      log.Info("Reconciling")

      lc := &kcpcorev1alpha1.LogicalCluster{}
      if err := r.Client.Get(ctx, req.NamespacedName, lc); err != nil {
        return reconcile.Result{}, err
      }

      // check if your initializer is still set on the logicalcluster
      if slices.Contains(lc.Status.Initializers, r.InitializerName) {

        log.Info("Starting to initialize cluster")
        // your logic here to initialize a Workspace

        // after your initialization is done, don't forget to remove your initializer
        // Since LogicalCluster objects cannot be directly updated, we need to create a patch.
        patch := client.MergeFrom(lc.DeepCopy())
        lc.Status.Initializers = initialization.EnsureInitializerAbsent(r.InitializerName, lc.Status.Initializers)
        if err := r.Client.Status().Patch(ctx, lc, patch); err != nil {
          return reconcile.Result{}, err
        }
      }

      return reconcile.Result{}, nil
    }
    ```

=== "kubeconfig"

    ```yaml
    apiVersion: v1
    clusters:
    - cluster:
        certificate-authority-data: <your-certificate-authority>
        # obtain the server url from the status of your WorkspaceType
        server: "<initializing-workspace-url>"
      name: finalizer
    contexts:
    - context:
        cluster: finalizer
        user: <user-with-sufficient-permissions>
      name: finalizer
    current-context: finalizer
    kind: Config
    preferences: {}
    users:
    - name: <user-with-sufficient-permissions>
      user:
        token: <user-token>
    ```

=== "go.mod"

    ```Go
    ...
    // replace upstream controller-runtime with kcp cluster aware fork
    replace sigs.k8s.io/controller-runtime v0.19.7 => github.com/kcp-dev/controller-runtime v0.19.0-kcp.1
    ...
    ```
