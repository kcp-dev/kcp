# Registering Kubernetes Clusters using syncer

In order to register a Kubernetes clusters with the kcp server,
users have to install a special component named [syncer](https://github.com/kcp-dev/kcp/tree/main/docs/architecture#syncer).

## Requirements

- kcp server
- [kcp kubectl plugin](./kubectl-kcp-plugin.md)
- kubernetes cluster

## Instructions

1. Create a workspace and immediately enter it:

```sh
$ kubectl kcp workspace create my-workspace --enter
Workspace "my-workspace" (type "Universal") created. Waiting for being ready.
Current workspace is "root:default:my-workspace".
```

1. Obtain the server URL used to connect to KCP from its kubeconfig file:

```sh
$ export KCP_SERVER_URL=$(grep server .kcp/admin.kubeconfig | grep -v clusters | awk '{print $2}')
```

1. Obtain the logical cluster name from `kubectl kcp workspace`:

```sh
$ export KCP_LOGICAL_CLUSTER_NAME=$(kubectl kcp workspace | awk '{print $4}' | sed 's/".$/"/' | sed 's/"//g')
```

1. Create a service account for the syncer on current workspace:

```sh
$ kubectl create serviceaccount syncer
serviceaccount/syncer created
```

1. Generate a kubeconfig from the service account:

```sh
$ export KCP_SYNCER_SECRET=$(kubectl get secret -o name | grep syncer)
```

Obtain the cluster certificate and the token associated to the service account:

```sh
$ export KCP_SYNCER_TOKEN=$(kubectl get ${KCP_SYNCER_SECRET} -o jsonpath='{.data.token}')
$ export KCP_SYNCER_CACRT=$(kubectl get configmap/kube-root-ca.crt -o jsonpath='{.data.ca\.crt}'| base64 -w 0)
```

1. Create a workload cluster:

```sh
$ kubectl create -f contrib/examples/cluster.yaml
workloadcluster.workload.kcp.dev/local created
```

1. Create a kind cluster to back the workload cluster

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

https://github.com/kubernetes-sigs/kind/blob/main/site/static/examples/kind-with-registry.sh

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

To use the image pushed to the local registry, update `spec.template.spec.containers[].image` in the syncer
manifest in the next step with the image tag output by `ko publish`

1. Complete the [syncer manifest](../manifest/syncer.yaml) with the information obtained in previous steps:

- KCP_SYNCER_CACRT
- KCP_SYNCER_TOKEN
- KCP_SERVER_URL
- KCP_LOGICAL_CLUSTER_NAME
- WORKLOAD_CLUSTER_NAME (defined by the user to identify the workload cluster)

1. Apply the manifest to the p-cluster

```sh
$ kubectl apply -f syncer.yaml
namespace/kcp-system created
serviceaccount/syncer created
clusterrole.rbac.authorization.k8s.io/syncer created
clusterrolebinding.rbac.authorization.k8s.io/syncer created
secret/syncer-kcp-sa created
configmap/syncer-kcp-config created
deployment.apps/syncer created
```

and it will create a deployment with the `syncer`:

```sh
$ kubectl -n kcp-system get deployments
NAME     READY   UP-TO-DATE   AVAILABLE   AGE
syncer   1/1     1            1           13m
```

1. Wait for the kcp workload cluster to go ready.

TODO(marun)
