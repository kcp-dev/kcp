# Registering Kubernetes Clusters using syncer

In order to register a Kubernetes clusters with the kcp server,
users have to install a special component named [syncer](https://github.com/kcp-dev/kcp/tree/main/docs/architecture#syncer).

## Requirements

- kcp server
- [kcp kubectl plugin](./kubectl-kcp-plugin.md)
- kubernetes cluster

## Instructions

1. Create a workspace

```sh
$ kubectl kcp workspace create my-workspace --use
Workspace "my-workspace" created.
Current workspace is "my-workspace".
```

Check that it has been created correctly and is ready:

```sh
$ kubectl kcp workspace list
NAME           TYPE        PHASE   URL
my-workspace   Universal   Ready   https://192.168.1.150:6443/clusters/default:my-workspace--1
```

Obtain the KCP server URL and the logical cluster name:

- KCP_SERVER_URL=https://192.168.1.150:6443
- KCP_LOGICAL_CLUSTER_NAME=default:my-workspace--1

2. Create a service account for the syncer on current workspace:

```sh
$ kubectl create serviceaccount syncer
serviceaccount/syncer created
```

3. Generate a kubeconfig from the service account:

```sh
$ kubectl get secret
NAME                  TYPE                                  DATA   AGE
default-token-nkpxj   kubernetes.io/service-account-token   3      29m
syncer-token-2mtgf    kubernetes.io/service-account-token   3      29m

```

Obtain the cluster certificate and the token associated to the service account:

```sh
$ KCP_SYNCER_TOKEN=$(kubectl get secret/syncer-token-2mtgf -o jsonpath='{.data.token}')
$ KCP_SYNCER_CACRT=$(kubectl get configmap/kube-root-ca.crt -o jsonpath='{.data.ca\.crt}'| base64 -w 0)
```


4. Complete the [syncer manifest](../manifest/syncer.yaml) with the information obtained in previous steps:

- KCP_SYNCER_CACRT
- KCP_SYNCER_TOKEN
- KCP_SERVER_URL
- KCP_LOGICAL_CLUSTER_NAME
- WORKLOAD_CLUSTER_NAME (defined by the user to identify the workload cluster)

5.  Apply the manifest to the p-cluster

```sh
$ kubect --kubeconfig pcluster.kubeconfig apply -f syncer.yaml
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