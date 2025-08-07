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

* We strongly recommend to use the kcp [initializingworkspace multicluster-provider](github.com/kcp-dev/multicluster-provider) to build your custom initializer
* You need to update LogicalClusters using patches; They cannot be updated using the update api

Keeping this in mind, you can use the following example as a starting point for your intitialization controller

=== "reconcile.go"

    ```Go
    package main

    import (
      "context"
      "slices"
     
      "github.com/go-logr/logr"
      kcpcorev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
      "github.com/kcp-dev/kcp/sdk/apis/tenancy/initialization"
      ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
      "sigs.k8s.io/controller-runtime/pkg/cluster"
      "sigs.k8s.io/controller-runtime/pkg/reconcile"
      mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
      mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
      mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
     )
     
     type Reconciler struct {
      Log             logr.Logger
      InitializerName kcpcorev1alpha1.LogicalClusterInitializer
      ClusterGetter   func(context.Context, string) (cluster.Cluster, error)
     }
     
     func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (reconcile.Result, error) {
      log := r.Log.WithValues("clustername", req.ClusterName)
      log.Info("Reconciling")
     
      // create a client scoped to the logical cluster the request came from
      cluster, err := r.ClusterGetter(ctx, req.ClusterName)
      if err != nil {
       return reconcile.Result{}, err
      }
      client := cluster.GetClient()
     
      lc := &kcpcorev1alpha1.LogicalCluster{}
      if err := client.Get(ctx, req.NamespacedName, lc); err != nil {
       return reconcile.Result{}, err
      }
     
      // check if your initializer is still set on the logicalcluster
      if slices.Contains(lc.Status.Initializers, r.InitializerName) {
     
       // your logic to initialize a Workspace goes here
       log.Info("Starting to initialize cluster")
     
       // after your initialization is done, don't forget to remove your initializer.
       // You will need to use patch, to update the LogicalCluster
       patch := ctrlclient.MergeFrom(lc.DeepCopy())
       lc.Status.Initializers = initialization.EnsureInitializerAbsent(r.InitializerName, lc.Status.Initializers)
       if err := client.Status().Patch(ctx, lc, patch); err != nil {
        return reconcile.Result{}, err
       }
      }
     
      return reconcile.Result{}, nil
     }
     
     func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
      return mcbuilder.ControllerManagedBy(mgr).
       For(&kcpcorev1alpha1.LogicalCluster{}).
       Complete(r)
     }
    ```

=== "main.go"

    ```Go
    package main

    import (
      "context"
      "fmt"
      "log/slog"
      "os"
      "strings"
     
      "github.com/go-logr/logr"
      kcpcorev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
      "github.com/kcp-dev/multicluster-provider/initializingworkspaces"
      "golang.org/x/sync/errgroup"
      "k8s.io/client-go/kubernetes/scheme"
      "k8s.io/client-go/tools/clientcmd"
      ctrl "sigs.k8s.io/controller-runtime"
      "sigs.k8s.io/controller-runtime/pkg/manager"
      mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
     )
     
     // glue and setup code
     func main() {
      if err := execute(); err != nil {
       fmt.Println(err)
       os.Exit(1)
      }
     }
     func execute() error {
      // your kubeconfig here
      kubeconfigpath := "<your-kubeconfig>"
     
      config, err := clientcmd.BuildConfigFromFlags("", kubeconfigpath)
      if err != nil {
       return err
      }
     
      // since the initializers name is is the last part of the hostname, we can take it from there
      initializerName := config.Host[strings.LastIndex(config.Host, "/")+1:]
     
      provider, err := initializingworkspaces.New(config, initializingworkspaces.Options{InitializerName: initializerName})
      if err != nil {
       return err
      }
     
      logger := logr.FromSlogHandler(slog.NewTextHandler(os.Stderr, nil))
      ctrl.SetLogger(logger)
     
      mgr, err := mcmanager.New(config, provider, manager.Options{Logger: logger})
      if err != nil {
       return err
      }
     
      // add the logicalcluster scheme
      if err := kcpcorev1alpha1.AddToScheme(scheme.Scheme); err != nil {
       return err
      }
     
      r := Reconciler{
       Log:             mgr.GetLogger().WithName("initializer-controller"),
       InitializerName: kcpcorev1alpha1.LogicalClusterInitializer(initializerName),
       ClusterGetter:   mgr.GetCluster,
      }
     
      if err := r.SetupWithManager(mgr); err != nil {
       return err
      }
      mgr.GetLogger().Info("Setup complete")
     
      // start the provider and manager
      g, ctx := errgroup.WithContext(context.Background())
      g.Go(func() error { return provider.Run(ctx, mgr) })
      g.Go(func() error { return mgr.Start(ctx) })
     
      return g.Wait()
     }
    ```
