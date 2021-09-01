# Build and run `kcp`

```
go run ./cmd/kcp start
```

This will build and run your kcp server, and generate a kubeconfig in `.kcp/data/admin.kubeconfig` you can use to connect to it:

```
export KUBECONFIG=.kcp/data/admin.kubeconfig
kubectl api-resources
```

# Build and run Cluster Controller

First, be sure to install the CRD types needed by the controller. These are:
- `Cluster`
- `APIResourceImport`
- `NegotiatedAPIResource`

```
kubectl apply -f config/
```

The Cluster Controller requires a `--syncer_image` to install on new clusters.
To build this image and pass it to the Cluster Controller, you can use [`ko`](https://github.com/google/ko):

```
go run ./cmd/cluster-controller \
    --syncer_image=$(ko publish ./cmd/syncer) \
    --kubeconfig=.kcp/data/admin.kubeconfig
```

`ko publish` requires the `KO_DOCKER_REPO` env var to be set to the container image registry to push the image to (e.g., `KO_DOCKER_REPO=quay.io/my-user`).

# Test the registration of a Physical Cluster

Registering a physical cluster can be done by simply creating a `cluster resource` that embeds a kubeconfig file.

For example, in order to register the default cluster of your default kubeconfig file, you can use the following command:

```bash
sed -e 's/^/    /' ${HOME}/.kube/config | cat contrib/examples/cluster.yaml - | kubectl apply -f -
```

# Using `kcp` as a library
Instead of running the kcp as a binary using `go run`, you can include the kcp api-server in your own projects. To create and start the api-server with the default options (including an embedded etcd server):

```go
if err := server.NewServer(server.DefaultConfig()).Run(ctx); err != nil {
    panic(err)
}
```

You may also configure post-start hooks which are useful if you need to start a some process that depends on a connection to the newly created api-server such as a controller manager.

```go
// Create a new api-server with default options
srv := server.NewServer(server.DefaultConfig())

// Register a post-start hook that connects to the api-server
srv.AddPostStartHook("connect-to-api", func(context genericapiserver.PostStartHookContext) error {
    // Create a new client using the client config from our newly created api-server
    client := clientset.NewForConfigOrDie(context.LoopbackClientConfig)
    _, err := client.Discovery().ServerGroups()
    if err != nil {
        return err
    }
    return nil
})

// Start the api-server
if err := srv.Run(ctx); err != nil {
	panic(err)
}
```

# Using vscode

## Workspace

A configured VSCode workspace is available in `contrib/kcp.code-workspace`
## Debug configuration

You can use the `Launch kcp` configuration to start the KCP lightweight API Server in debug mode inside VSCode.

If you're using [KinD](https://kind.sigs.k8s.io), as the physical cluster you want to register inside KCP,
then you can use the `Launch cluster controller` configuration to debug the cluster controller against a started KCP instance.
