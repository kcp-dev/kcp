---
description: >
  How to register Kubernetes clusters using syncer.
---

# Registering Kubernetes Clusters using syncer

To register a Kubernetes cluster with kcp, you have to install a special component named syncer.

## Requirements

- kcp server
- [kcp kubectl plugin](./kubectl-kcp-plugin.md)
- kubernetes cluster

## Instructions

1. (Optional) Skip this step, if you already have a physical cluster.
   Create a kind cluster to back the sync target:

    ```sh
    $ kind create cluster
    Creating cluster "kind" ...
    <snip>
    Set kubectl context to "kind-kind"
    You can now use your cluster with:

    kubectl cluster-info --context kind-kind
    ```

!!! note
    This step sets current context to the new kind cluster. Make sure to use a KCP kubeconfig for the next steps unless told otherwise.

1. Create an organization and immediately enter it:

    ```sh
    $ kubectl kcp workspace create my-org --enter
    Workspace "my-org" (type root:organization) created. Waiting for it to be ready...
    Workspace "my-org" (type root:organization) is ready to use.
    Current workspace is "root:my-org" (type "root:organization").
    ```

1. Enable the syncer for a physical cluster:

    ```sh
    kubectl kcp workload sync <synctarget name> --syncer-image <image name> -o syncer.yaml
    ```

    Where `<image name>` [one of the syncer images](https://github.com/kcp-dev/kcp/pkgs/container/kcp%2Fsyncer) for your corresponding KCP release (e.g. `ghcr.io/kcp-dev/kcp/syncer:v0.7.5`).

1. Apply the manifest to the physical cluster:

    ```sh
    $ KUBECONFIG=<pcluster-config> kubectl apply -f syncer.yaml
    namespace/kcp-syncer-kind-1owee1ci created
    serviceaccount/kcp-syncer-kind-1owee1ci created
    secret/kcp-syncer-kind-1owee1ci-token created
    clusterrole.rbac.authorization.k8s.io/kcp-syncer-kind-1owee1ci created
    clusterrolebinding.rbac.authorization.k8s.io/kcp-syncer-kind-1owee1ci created
    secret/kcp-syncer-kind-1owee1ci created
    deployment.apps/kcp-syncer-kind-1owee1ci created
    ```

    and it will create a `kcp-syncer-<synctarget name>-xx` deployment:

    ```sh
    $ KUBECONFIG=<pcluster-config> kubectl -n kcp-syncer-kind-1owee1ci get deployments
    NAME     READY                    UP-TO-DATE   AVAILABLE   AGE
    kcp-syncer-<synctarget name>-xx   1/1     1            1           13m
    ```

1. Wait for the kcp sync target to go ready:

    ```bash
    kubectl wait --for=condition=Ready synctarget/<mycluster>
    ```

### Select resources to synchronize

Syncer will by default use the `kubernetes` APIExport in `root:compute` workspace, synchronize `deployments/services/ingresses` resources to the physical cluster and up-synchronized `pods` from the physical cluster. The related API schemas of the physical cluster should be compatible with kubernetes 1.24. User can
select to synchronize other resources in physical clusters or from other APIExports on the kcp server.

To synchronize resources that the KCP server does not have an APIExport to support yet, run:

```sh
kubectl kcp workload sync <mycluster> --syncer-image <image name> --resources foo.bar -o syncer.yaml
```

Make sure to have the resource name in the form of `resourcename.<gvr_of_the_resource>` to be able to
synchronize successfully to the physical cluster.

For example to synchronize resource `routes` to physical cluster run the command below:

```sh
kubectl kcp workload sync <mycluster> --syncer-image <image name> --resources routes.route.openshift.io -o syncer.yaml
```

And apply the generated manifests to the physical cluster. The syncer will then import the API schema of `foo.bar`
to the workspace of the SyncTarget, followed by an auto generated kubernetes APIExport in the same workspace.
The auto generated APIExport name is `imported-apis`, and you can then create an APIBinding to bind this APIExport.

To synchronize resource from another existing APIExport in the KCP server, run:

```sh
kubectl kcp workload sync <mycluster> --syncer-image <image name> --apiexports another-workspace:another-apiexport -o syncer.yaml
```

Syncer will start synchronizing the resources in this `APIExport` as long as the `SyncTarget` has compatible API schemas. The auto generated
APIExport `imported-apis` is a reserved APIExport name and should not be set in the `--apiexports`

To see if a certain resource is synchronized by the syncer, you can check the `state` of the `syncedResources` in `SyncTarget` status:

- `Pending` state indicates the syncer has not report compatibility of the resource.
- `Accepted` state indicates  the resource schema is compatible and can be synced by syncer.
- `Incompatible` state indicates the resource schema is incompatible with the physical cluster schema.

### Bind workspaces to the Location Workspace

After the `SyncTarget` is ready, switch to any workspace containing some workloads that you want to sync to this `SyncTarget`, and run:

```sh
kubectl kcp bind compute <workspace of synctarget>
```

This command will create a `Placement` in the workspace. By default, it will also create `APIBinding`s for global kubernetes `APIExport`.

You can also bind to the auto generated `imported-apis` APIExport by running

```sh
kubectl kcp bind compute <workspace of synctarget> --apiexports <workspace of synctarget>:imported-apis
```

Alternatively, if you would like to bind other `APIExport`s which are supported by the `SyncerTarget`, run:

```
kubectl kcp bind compute <workspace of synctarget> --apiexports <apiexport workspace>:<apiexport name>
```

In addition, you can specify the certain location or namespace to create placement. e.g.

```
kubectl kcp bind compute <workspace of synctarget> --location-selectors=env=test --namespace-selector=purpose=workload
```

this command will create a `Placement` selecting a `Location` with label `env=test` and bind the selected `Location` to namespaces with
label `purpose=workload`. See more details of placement and location [here](placement-locations-and-scheduling.md)

### Running a workload

1. Create a deployment:

    ```sh
    kubectl create deployment kuard --image gcr.io/kuar-demo/kuard-amd64:blue
    ```

!!! note
    Replace "gcr.io/kuar-demo/kuard-amd64:blue" with "gcr.io/kuar-demo/kuard-arm64:blue" in case you're running
    an Apple M1 based virtual machine.

1. Verify the deployment on the local workspace:

    ```sh
    $ kubectl rollout status deployment/kuard
    Waiting for deployment "kuard" rollout to finish: 0 of 1 updated replicas are available...
    deployment "kuard" successfully rolled out
    ```

## For syncer development

### Building components

The syncer, kcp and kubectl plugins should come from the same build, so they are compatible with each other.

To build, make the root kcp folder your current working directory and run:

```bash
make build-all install build-kind-images
```

If your go version is not 1.19, which is the expected version, you need to run

```bash
IGNORE_GO_VERSION=1 make build-all install build-kind-images
```

Make a note of the syncer image that is produced by this build, which is in the output near the end. It should be something like `kind.local/syncer-c2e3073d5026a8f7f2c47a50c16bdbec:8287441974cf604dd93da5e6d010a78d38ae49733ea3a5031048a516101dd8a2`. This will be used by the `kubectl kcp workload sync ...` command, below.

The kubectl kcp plugin binaries should be first in your path so kubectl picks them up.

```bash
which kubectl-kcp
```
should point to the kubectl-kcp you have just built in your `$GOPATH/bin`, and not any other installed version.

### Start kcp on another terminal

```bash
kcp start
```

### Running in a kind cluster with a local registry

You can run the syncer in a kind cluster for development.

1. Create a `kind` cluster with a local registry to simplify syncer development
   by executing the following script:

   ```bash
   /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/kubernetes-sigs/kind/main/site/static/examples/kind-with-registry.sh)"
   ```

1. From the kcp root directory:
    ```bash
    export KUBECONFIG=.kcp/admin.kubeconfig
    ```

1. Create a location workspace and immediately enter it:

    ```sh
    $ kubectl kcp workspace create my-locations --enter
    Workspace "my-locations" (type root:organization) created. Waiting for it to be ready...
    Workspace "my-locations" (type root:organization) is ready to use.
    Current workspace is "root:my-locations" (type "root:organization").
    ```

1. To create the synctarget and use the image pushed to the local registry, supply `<image name>` to the
   `kcp workload sync` plugin command, where `<image name>` was captured in the build steps, above.

    ```sh
    kubectl kcp workload sync kind --syncer-image <image name> -o syncer.yaml
    ```

1. Create a second workspace for your workloads and immediately enter it:

    ```sh
    $ kubectl kcp workspace ..
    $ kubectl kcp workspace create my-workloads --enter
    Workspace "my-workloads" (type root:organization) created. Waiting for it to be ready...
    Workspace "my-workloads" (type root:organization) is ready to use.
    Current workspace is "root:my-workloads" (type "root:organization").
    ```

1. Bind it to the `my-locations` workspace with the synctarget:

    ```bash
    kubectl kcp bind compute "root:my-locations"
    ```

1. Apply the manifest to the p-cluster:

    ```sh
    $ KUBECONFIG=<pcluster-config> kubectl apply -f syncer.yaml
    namespace/kcp-syncer-kind-1owee1ci created
    serviceaccount/kcp-syncer-kind-1owee1ci created
    secret/kcp-syncer-kind-1owee1ci-token created
    clusterrole.rbac.authorization.k8s.io/kcp-syncer-kind-1owee1ci created
    clusterrolebinding.rbac.authorization.k8s.io/kcp-syncer-kind-1owee1ci created
    secret/kcp-syncer-kind-1owee1ci created
    deployment.apps/kcp-syncer-kind-1owee1ci created
    ```

    and it will create a `kcp-syncer` deployment:

    ```sh
    $ KUBECONFIG=<pcluster-config> kubectl -n kcp-syncer-kind-1owee1ci get deployments
    NAME     READY   UP-TO-DATE   AVAILABLE   AGE
    kcp-syncer   1/1     1            1           13m
    ```

1. Wait for the kcp sync target to go ready:

    ```bash
    kubectl wait --for=condition=Ready synctarget/<mycluster>
    ```

1. Add a deployment to the my-workloads workspace and check the p-cluster to see if the workload has been created there:

    ```bash
    kubectl create deployment --image=gcr.io/kuar-demo/kuard-amd64:blue --port=8080 kuard
    ```

### Running locally

TODO(m1kola): we need a less hacky way to run locally: needs to be more close
to what we have when running inside the kind with own kubeconfig.

This assumes that KCP is also being run locally.

1. Create a kind cluster to back the sync target:

    ```sh
    $ kind create cluster
    Creating cluster "kind" ...
    <snip>
    Set kubectl context to "kind-kind"
    You can now use your cluster with:

    kubectl cluster-info --context kind-kind
    ```

1. Make sure to use kubeconfig for your local KCP:

    ```bash
    export KUBECONFIG=.kcp/admin.kubeconfig
    ```

1. Create an organisation and immediately enter it:

    ```sh
    $ kubectl kcp workspace create my-org --enter
    Workspace "my-org" (type root:organization) created. Waiting for it to be ready...
    Workspace "my-org" (type root:organization) is ready to use.
    Current workspace is "root:my-org" (type "root:organization").
    ```

1. Enable the syncer for a p-cluster:

    ```sh
    kubectl kcp workload sync <mycluster> --syncer-image <image name> -o syncer.yaml
    ```

    `<image name>` can be anything here as it will only be used to generate `syncer.yaml` which we are not going to apply.

1. Gather data required for the syncer:

    ```bash
    syncTargetName=<mycluster>
    syncTargetUID=$(kubectl get synctarget $syncTargetName -o jsonpath="{.metadata.uid}")
    fromCluster=$(kubectl ws current --short)
    ```

1. Run the following snippet:

    ```bash
    go run ./cmd/syncer \
      --from-kubeconfig=.kcp/admin.kubeconfig \
      --from-context=base \
      --to-kubeconfig=$HOME/.kube/config \
      --sync-target-name=$syncTargetName \
      --sync-target-uid=$syncTargetUID \
      --from-cluster=$fromCluster \
      --resources=configmaps \
      --resources=deployments.apps \
      --resources=secrets \
      --resources=serviceaccounts \
      --qps=30 \
      --burst=20
    ```

1. Wait for the kcp sync target to go ready:

    ```bash
    kubectl wait --for=condition=Ready synctarget/<mycluster>
    ```
