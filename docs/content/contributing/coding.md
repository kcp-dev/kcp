---
description: >
    Coding guidelines for kcp projects.
---

# Coding Guidelines & Conventions

- Always be clear about what clients or client configs target. Never use an unqualified `client`. Instead, always qualify. For example:
    - `rootClient`
    - `orgClient`
    - `pclusterClient`
    - `rootKcpClient`
    - `orgKubeClient`
- Configs intended for `NewForConfig` (i.e. today often called "admin workspace config") should uniformly be called `clusterConfig`
    - Note: with org workspaces, `kcp` will no longer default clients to the "root" ("admin") logical cluster
    - Note 2: sometimes we use clients for same purpose, but this can be harder to read
- Cluster-aware clients should follow similar naming conventions:
    - `crdClusterClient`
    - `kcpClusterClient`
    - `kubeClusterClient`
- `clusterName` is a kcp term. It is **NOT** a name of a physical cluster. If we mean the latter, use `pclusterName` or similar.
- Qualify "namespace"s in code that handle up- and downstream, e.g. `upstreamNamespace`, `downstreamNamespace`, and also `upstreamObj`, `downstreamObj`.
- Logging:
  - Use the `fmt.Sprintf("%s|%s/%s", clusterName, namespace, name` syntax.
  - Default log-level is 2.
  - Controllers should generally log (a) **one** line (not more) non-error progress per item with `klog.V(2)` (b) actions like create/update/delete via `klog.V(3)` and (c) skipped actions, i.e. what was not done for reasons via `klog.V(4)`.
- When orgs land: `clusterName` or `fooClusterName` is always the fully qualified value that you can stick into obj.ObjectMeta.ClusterName. It's not necessarily the `(Cluster)Workspace.Name` from the object. For the latter, use `workspaceName` or `orgName`.
- Generally do `klog.Errorf` or `return err`, but not both together. If you need to make it clear where an error came from, you can wrap it.
- New features start under a feature-gate (`--feature-gate GateName=true`). (At some point in the future), new feature-gates are off by default *at least* until the APIs are promoted to beta (we are not there before we have reached MVP).
- Feature-gated code can be incomplete. Also their e2e coverage can be incomplete. **We do not compromise on unit tests**. Every feature-gated code needs full unit tests as every other code-path.
- Go Proverbs are good guidelines for style: https://go-proverbs.github.io/ – watch https://www.youtube.com/watch?v=PAAkCSZUG1c.
- We use Testify's [require](https://pkg.go.dev/github.com/stretchr/testify/require) a
  lot in tests, and avoid
  [assert](https://pkg.go.dev/github.com/stretchr/testify/assert).

  Note this subtle distinction of nested `require` statements:
  ```Golang
  require.Eventually(t, func() bool {
    foos, err := client.List(...)
    require.NoError(err) // fail fast, including failing require.Eventually immediately
    return someCondition(foos)
  }, ...)
  ```
  and
  ```Golang
  require.Eventually(t, func() bool {
    foos, err := client.List(...)
    if err != nil {
       return false // keep trying
    }
    return someCondition(foos)
  }, ...)
  ```
  The first fails fast on every client error. The second ignores client errors and keeps trying. Either
  has its place, depending on whether the client error is to be expected (e.g. because of asynchronicity making the resource available),
  or signals a real test problem.

### Using Kubebuilder CRD Validation Annotations

All of the built-in types for `kcp` are `CustomResourceDefinitions`, and we generate YAML spec for them from our Go types using [kubebuilder](https://github.com/kubernetes-sigs/kubebuilder).

When adding a field that requires validation, custom annotations are used to translate this logic into the generated OpenAPI spec. [This doc](https://book.kubebuilder.io/reference/markers/crd-validation.html) gives an overview of possible validations. These annotations map directly to concepts in the [OpenAPI Spec](https://swagger.io/specification/#data-type-format) so, for instance, the `format` of strings is defined there, not in kubebuilder. Furthermore, Kubernetes has forked the OpenAPI project [here](https://github.com/kubernetes/kube-openapi/tree/master/pkg/validation) and extends more formats in the extensions-apiserver [here](https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1/types_jsonschema.go#L27).


### Replicated Data Types

Some objects are replicated and cached amongst shards when `kcp` is run in a sharded configuration. When writing code to list or get these objects, be sure to reference both shard-local and cache informers. To make this more convenient, wrap the look up in a function pointer.

For example:

```Golang

func NewController(ctx,
  localAPIExportInformer, cacheAPIExportInformer apisinformers.APIExportClusterInformer
) (*controller, error) {
  ...
  return &controller{
  listAPIExports: func(clusterName logicalcluster.Name) ([]apisv1apha1.APIExport, error) {
    exports, err := localAPIExportInformer.Cluster(clusterName).Lister().List(labels.Everything())
    if err != nil {
      return cacheAPIExportInformer.Cluster(clusterName).Lister().List(labels.Everything())
    }
    return exports, nil
  ...
  }
}
```

A full list of replicated resources is currently outlined in the [replication controller](https://github.com/kcp-dev/kcp/blob/main/pkg/reconciler/cache/replication/replication_controller.go).
