# Build and run `kcp`

```
go run ./cmd/kcp start
```

This will build and run your kcp server, and generate a kubeconfig in `.kcp/data/admin.kubeconfig` you can use to connect to it:

```
KUBECONFIG=.kcp/data/admin.kubeconfig
kubectl api-resources
```

The kubeconfig configures two contexts, `user` (the default) and `admin`.
Switch the default context to `admin` with the following command:

```
kubectl config use-context admin
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
If you're using [KinD](https://kind.sigs.k8s.io), you can set `KO_DOCKER_REPO=kind.local` to publish to your local KinD cluster.
