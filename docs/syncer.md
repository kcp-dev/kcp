# Registering Kubernetes Clusters using syncer

In order to register a Kubernetes clusters with the kcp server,
users have to install a special component named [syncer](https://github.com/kcp-dev/kcp/tree/main/docs/architecture#syncer).

## Requirements

- kcp server
- [kcp kubectl plugin](./kubectl-kcp-plugin.md)
- kubernetes cluster

## Instructions

1. Create an organisation and universal workspace and immediately enter it:

```sh
$ kubectl kcp workspace create my-org --enter
Workspace "my-org" (type root:organization) created. Waiting for it to be ready...
Workspace "my-org" (type root:organization) is ready to use.
Current workspace is "root:my-org" (type "root:organization").
```

1. Enable the syncer for a new cluster

```sh
$ kubectl kcp workload sync <mycluster> --syncer-image <image name> -o syncer.yaml
```

1. Create a kind cluster to back the sync target

```sh
$ kind create cluster
Creating cluster "kind" ...
<snip>
Set kubectl context to "kind-kind"
You can now use your cluster with:

kubectl cluster-info --context kind-kind
```

## For syncer development

Alternately, create a `kind` cluster with a local registry to simplify syncer development by executing the
following script:

https://raw.githubusercontent.com/kubernetes-sigs/kind/main/site/static/examples/kind-with-registry.sh

### Building the syncer image

Install `ko`:

```sh
go install github.com/google/ko@latest
```

Build image and push to the local registry integrated with `kind`:

```sh
KO_DOCKER_REPO=localhost:5001 ko publish ./cmd/syncer -t <your tag>
```

By default `ko` will build for `amd64`. To build for `arm64` (e.g. apple silicon), specify
`--platform=linux/arm64`.

To use the image pushed to the local registry, supply `--image=<image tag>` to the
`enable-syncer` plugin command, where `<image tag>` is from the output of `ko publish`.

1. Apply the manifest to the p-cluster

```sh
$ kubectl --kubeconfig $KIND_KUBECONFIG apply -f syncer.yaml
namespace/kcpsync25e6e3ce5be10b16411448aec95b6b6d695a1daa5120732019531d8d created
serviceaccount/kcp-syncer created
clusterrole.rbac.authorization.k8s.io/kcpsync25e6e3ce5be10b16411448aec95b6b6d695a1daa5120732019531d8d created
clusterrolebinding.rbac.authorization.k8s.io/kcpsync25e6e3ce5be10b16411448aec95b6b6d695a1daa5120732019531d8d created
secret/kcp-syncer-config created
deployment.apps/kcp-syncer created
```

and it will create a `kcp-syncer` deployment:

```sh
$ kubectl -n kcpsync25e6e3ce5be10b16411448aec95b6b6d695a1daa5120732019531d8d get deployments
NAME     READY   UP-TO-DATE   AVAILABLE   AGE
kcp-syncer   1/1     1            1           13m
```

1. Wait for the kcp sync target to go ready.

```sh
$ kubectl wait --for=condition=Ready synctarget/<mycluster>
```

1. Create a deployment

```sh
$ kubectl create deployment kuard --image gcr.io/kuar-demo/kuard-amd64:blue
```

Note: replace "gcr.io/kuar-demo/kuard-amd64:blue" with "gcr.io/kuar-demo/kuard-arm64:blue" in case you're running
an Apple M1 based virtual machine.

Verify the deployment on the local workspace:

```sh
$ kubectl rollout status deployment/kuard
Waiting for deployment "kuard" rollout to finish: 0 of 1 updated replicas are available...
deployment "kuard" successfully rolled out
```
