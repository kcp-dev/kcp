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

First, be sure to define the Cluster CRD type:

```
kubectl apply -f config/cluster.example.dev_clusters.yaml
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

# Using vscode

## Workspace

A configured VSCode workspace is available in `contrib/kcp.code-workspace`
## Debug configuration

You can use the `Launch kcp` configuration to start the KCP lightweight API Server in debug mode inside VSCode.

If you're using [KinD](https://kind.sigs.k8s.io), as the physical cluster you want to register inside KCP,
then you can use the `Launch cluster controller` configuration to debug the cluster controller against a started KCP instance.
