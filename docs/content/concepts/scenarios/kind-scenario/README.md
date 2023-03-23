# List KCP Workspace objects (Kind cluster scenario)

Table of content:

- [List KCP Workspace objects (Kind cluster scenario)](#list-kcp-workspace-objects-kind-cluster-scenario)
  - [1. Start KCP](#1-start-kcp)
  - [2. Check objects in the new `kind` KCP Workspace at the center (#1)](#2-check-objects-in-the-new-kind-kcp-workspace-at-the-center-1)
  - [3. Create a syncer at the center](#3-create-a-syncer-at-the-center)
  - [4. Check objects that were created in the `kind` KCP Workspace after running `kcp workload sync` at the center (#2)](#4-check-objects-that-were-created-in-the-kind-kcp-workspace-after-running-kcp-workload-sync-at-the-center-2)
  - [5. Apply the syncer on the pcluster at the edge (Kind cluster)](#5-apply-the-syncer-on-the-pcluster-at-the-edge-kind-cluster)
  - [6. Check objects that were created in the `kind` KCP Workspace at the center after running the syncer in the pcluster at the edge (#3)](#6-check-objects-that-were-created-in-the-kind-kcp-workspace-at-the-center-after-running-the-syncer-in-the-pcluster-at-the-edge-3)
  - [7. Bind to `compute` at the center](#7-bind-to-compute-at-the-center)
  - [8. Check objects that were created in the `kind` KCP Workspace after binding to `compute` at the center (#4)](#8-check-objects-that-were-created-in-the-kind-kcp-workspace-after-binding-to-compute-at-the-center-4)

## 1. Start KCP

Given KCP 0.11:

```bash
$ k version --short
Client Version: v1.25.3
Kustomize Version: v4.5.7
Server Version: v1.24.3+kcp-v0.11.0
```

Start KCP with an external IP address binding:

```bash
$ kcp start --bind-address $(ifconfig | grep -A 1 'enp0s8' | tail -1 | awk '{print $2}')
```

Create a new KCP Workspace called `kind`:

```bash
$ k ws create kind --enter
Workspace "kind" (type root:organization) created. Waiting for it to be ready...
Workspace "kind" (type root:organization) is ready to use.
Current workspace is "root:kind" (type root:organization).

$ k ws tree
.
└── root
    ├── compute
    ├── jn
    └── kind
```

## 2. Check objects in the new `kind` KCP Workspace at the center (#1)

Given:

```bash
$ echo $KUBECONFIG
~/.kcp/admin.kubeconfig

$ k ws .
Current workspace is "root:kind".
```

List key objects:

```bash
$ k get apiexports
No resources found

$ k get apibindings
NAME                       AGE
apiresource.kcp.io-c8wp6   49m
scheduling.kcp.io-7rr1j    49m
tenancy.kcp.io-5qqol       49m
topology.kcp.io-2eqkm      49m
workload.kcp.io-dil2i      49m

$ k get synctargets
No resources found

$ k get locations
No resources found

$ k get placements
No resources found

$ k get APIResourceImports
No resources found

$ k get NegotiatedAPIResources
No resources found

$ k get APIConversions
No resources found
```

## 3. Create a syncer at the center

Given:

```bash
$ echo $KUBECONFIG
~/.kcp/admin.kubeconfig

$ k ws .
Current workspace is "root:kind".
```

Create the [syncer YAML](yamls/syncer-kind.yaml):

```bash
$ k kcp workload sync kind --syncer-image ghcr.io/kcp-dev/kcp/syncer:v0.11.0 -o syncer-kind.yaml
Creating synctarget "kind"
Creating service account "kcp-syncer-kind-37zv5s0u"
Creating cluster role "kcp-syncer-kind-37zv5s0u" to give service account "kcp-syncer-kind-37zv5s0u"
```

Note that in this scenario, the `kind` `SyncTarget` contains a `supportedAPIExports` with `path` pointing to the [`kuberntes` `APIExport` object in the `root:compute` Workspace](yamls/root-compute-apiexport-kubernetes.yaml).

```yaml
supportedAPIExports:
  - export: kubernetes
    path: root:compute
```

 So in this case KCP will be using the schema specified by the [`kuberntes` `APIExport` object in the `root:compute` Workspace](yamls/root-compute-apiexport-kubernetes.yaml):

 ```yaml
 latestResourceSchemas:
  - v124.ingresses.networking.k8s.io
  - v124.services.core
  - v124.deployments.apps
  - v124.pods.core
  ```

## 4. Check objects that were created in the `kind` KCP Workspace after running `kcp workload sync` at the center (#2)

Given:

```bash
$ echo $KUBECONFIG
~/.kcp/admin.kubeconfig

$ k ws .
Current workspace is "root:kind".
```

List key objects:

```bash
$ k get apiexports
NAME         AGE
kubernetes   35s

$ k get apibindings
NAME                       AGE
apiresource.kcp.io-c8wp6   53m
kubernetes                 42s
scheduling.kcp.io-7rr1j    53m
tenancy.kcp.io-5qqol       53m
topology.kcp.io-2eqkm      53m
workload.kcp.io-dil2i      53m

$ k get synctargets
NAME   AGE
kind   47s

$ k get locations
NAME      RESOURCE      AVAILABLE   INSTANCES   LABELS   AGE
default   synctargets   0           1                    54s

$ k get placements
No resources found

$ k get APIResourceImports
No resources found

$ k get NegotiatedAPIResources
No resources found

$ k get APIConversions
No resources found
```

Objects yamls:
- [APIExport `kubernetes`](yamls/2-apiexport-kubernetes.yaml) from `k get apiexport kubernetes -o yaml > 2-apiexport-kubernetes.yaml`
- [APIBinding `kubernetes`](yamls/2-apibinding-kubernetes.yaml) from `k get apibinding kubernetes -o yaml > 2-apibinding-kubernetes.yaml`
- [SyncTarget `kind`](yamls/2-synctarget-kind.yaml) from `k get synctarget kind -o yaml > 2-synctarget-kind.yaml`
- [Location `default`](yamls/2-location-default.yaml) from `k get location default -o yaml > 2-location-default.yaml`

## 5. Apply the syncer on the pcluster at the edge (Kind cluster)

Create a kind cluster:

```bash
$ kind create cluster
Creating cluster "kind" ...
 ✓ Ensuring node image (kindest/node:v1.25.3) 🖼
 ✓ Preparing nodes 📦
 ✓ Writing configuration 📜
 ✓ Starting control-plane 🕹️
 ✓ Installing CNI 🔌
 ✓ Installing StorageClass 💾
Set kubectl context to "kind-kind"

$ echo $KUBECONFIG
~/.kube/config

$ k get pods -A
NAMESPACE            NAME                                         READY   STATUS    RESTARTS   AGE
kube-system          coredns-565d847f94-9nhw2                     1/1     Running   0          6m37s
kube-system          coredns-565d847f94-pvfc7                     1/1     Running   0          6m37s
kube-system          etcd-kind-control-plane                      1/1     Running   0          6m53s
kube-system          kindnet-8b7sd                                1/1     Running   0          6m37s
kube-system          kube-apiserver-kind-control-plane            1/1     Running   0          6m49s
kube-system          kube-controller-manager-kind-control-plane   1/1     Running   0          6m59s
kube-system          kube-proxy-c9nzm                             1/1     Running   0          6m37s
kube-system          kube-scheduler-kind-control-plane            1/1     Running   0          6m55s
local-path-storage   local-path-provisioner-684f458cdd-ctpsx      1/1     Running   0          6m37s
```

Apply the syncer:

```bash
$ k apply -f syncer-kind.yaml
namespace/kcp-syncer-kind-37zv5s0u created
serviceaccount/kcp-syncer-kind-37zv5s0u created
secret/kcp-syncer-kind-37zv5s0u-token created
clusterrole.rbac.authorization.k8s.io/kcp-syncer-kind-37zv5s0u created
clusterrolebinding.rbac.authorization.k8s.io/kcp-syncer-kind-37zv5s0u created
role.rbac.authorization.k8s.io/kcp-dns-kind-37zv5s0u created
rolebinding.rbac.authorization.k8s.io/kcp-dns-kind-37zv5s0u created
secret/kcp-syncer-kind-37zv5s0u created
deployment.apps/kcp-syncer-kind-37zv5s0u created

$ $ k get pods -A
NAMESPACE                  NAME                                         READY   STATUS    RESTARTS   AGE
kcp-syncer-kind-37zv5s0u   kcp-syncer-kind-37zv5s0u-6949dd87f8-zdsxm    1/1     Running   0          17s
```

## 6. Check objects that were created in the `kind` KCP Workspace at the center after running the syncer in the pcluster at the edge (#3)

Given:

```bash
$ echo $KUBECONFIG
~/.kcp/admin.kubeconfig

$ k ws .
Current workspace is "root:kind".
```

The `synctarget default` is not `available`:

```bash
$ k get apiexports
NAME         AGE
kubernetes   5m21s

$ k get apibindings
NAME                       AGE
apiresource.kcp.io-c8wp6   58m
kubernetes                 5m26s
scheduling.kcp.io-7rr1j    58m
tenancy.kcp.io-5qqol       58m
topology.kcp.io-2eqkm      58m
workload.kcp.io-dil2i      58m

$ k get synctargets
NAME   AGE
kind   5m35s

$ k get locations
NAME      RESOURCE      AVAILABLE   INSTANCES   LABELS   AGE
default   synctargets   1           1                    5m40s

$ k get placements
No resources found

$ k get APIResourceImports
NAME
deployments.kind2.v1.apps
ingresses.kind2.v1.networking.k8s.io
pods.kind2.v1.core
services.kind2.v1.core

$ k get NegotiatedAPIResources
NAME
deployments.v1.apps
ingresses.v1.networking.k8s.io
pods.v1.core
services.v1.core

$ k get APIConversions
No resources found

$ k get APIResourceImports
NAME
deployments.kind.v1.apps
ingresses.kind.v1.networking.k8s.io
pods.kind.v1.core
services.kind.v1.core

$ k get NegotiatedAPIResources
NAME
deployments.v1.apps
ingresses.v1.networking.k8s.io
pods.v1.core
services.v1.core

$ k get APIConversions
No resources found
```

Objects yamls:
- [APIExport `kubernetes`](yamls/3-apiexport-kubernetes.yaml) from `k get apiexport kubernetes -o yaml > 3-apiexport-kubernetes.yaml`

    No difference. Compared to the MicroShift case (NVIDIA Jetson Nano) there is no `latestResourceSchemas` in the `spec`.

- [APIBinding `kubernetes`](yamls/3-apibinding-kubernetes.yaml) from `k get apibinding kubernetes -o yaml > 3-apibinding-kubernetes.yaml`

    No difference.

- [SyncTarget `kind`](yamls/3-synctarget-kind.yaml) from `k get synctarget kind -o yaml > 3-synctarget-kind.yaml`

    State changes:

    ```bash
    $ diff 2-synctarget-kind.yaml 3-synctarget-kind.yaml
    11c11
    <   resourceVersion: "9104"
    ---
    >   resourceVersion: "9266"
    20,24c20,21
    <   - lastTransitionTime: "2023-03-21T17:05:37Z"
    <     message: No heartbeat yet seen
    <     reason: ErrorHeartbeat
    <     severity: Warning
    <     status: "False"
    ---
    >   - lastTransitionTime: "2023-03-21T17:10:07Z"
    >     status: "True"
    26,30c23,24
    <   - lastTransitionTime: "2023-03-21T17:05:37Z"
    <     message: No heartbeat yet seen
    <     reason: ErrorHeartbeat
    <     severity: Warning
    <     status: "False"
    ---
    >   - lastTransitionTime: "2023-03-21T17:10:07Z"
    >     status: "True"
    31a26,29
    >   - lastTransitionTime: "2023-03-21T17:10:07Z"
    >     status: "True"
    >     type: SyncerAuthorized
    >   lastSyncerHeartbeatTime: "2023-03-21T17:19:07Z"
    35c33
    <     state: Incompatible
    ---
    >     state: Accepted
    40c38
    <     state: Incompatible
    ---
    >     state: Accepted
    46c44
    <     state: Incompatible
    ---
    >     state: Accepted
    52c50
    <     state: Incompatible
    ---
    >     state: Accepted
    ```

- [Location `default`](yamls/3-location-default.yaml) from `k get location default -o yaml > 3-location-default.yaml`

    Syncer instance is now available:

    ```bash
    $ diff 2-location-default.yaml 3-location-default.yaml
    10c10
    <   resourceVersion: "9078"
    ---
    >   resourceVersion: "9136"
    19c19
    <   availableInstances: 0
    ---
    >   availableInstances: 1
    ```

- [APIResourceImport deployments.kind.v1.apps`](yamls/3-APIResourceImport-deployments.kind.v1.apps.yaml) from `k get APIResourceImport deployments.kind.v1.apps -o yaml > 3-APIResourceImport-deployments.kind.v1.apps.yaml`

- [APIResourceImport ingresses.kind.v1.networking.k8s.io`](yamls/3-APIResourceImport-ingresses.kind.v1.networking.k8s.io.yaml) from `k get APIResourceImport ingresses.kind.v1.networking.k8s.io -o yaml > 3-APIResourceImport-ingresses.kind.v1.networking.k8s.io.yaml`

- [APIResourceImport pods.kind.v1.core`](yamls/3-APIResourceImport-pods.kind.v1.core.yaml) from `k get APIResourceImport pods.kind.v1.core -o yaml > 3-APIResourceImport-pods.kind.v1.core.yaml`

- [APIResourceImport services.kind.v1.core`](yamls/3-APIResourceImport-services.kind.v1.core.yaml) from `k get APIResourceImport services.kind.v1.core -o yaml > 3-APIResourceImport-services.kind.v1.core.yaml`

- [NegotiatedAPIResource deployments.v1.apps`](yamls/3-NegotiatedAPIResource-deployments.v1.apps.yaml) from `k get NegotiatedAPIResource deployments.v1.apps -o yaml > 3-NegotiatedAPIResource-deployments.v1.apps.yaml`

- [NegotiatedAPIResource ingresses.v1.networking.k8s.io`](yamls/3-NegotiatedAPIResource-ingresses.v1.networking.k8s.io.yaml) from `k get NegotiatedAPIResource ingresses.v1.networking.k8s.io -o yaml > 3-NegotiatedAPIResource-ingresses.v1.networking.k8s.io.yaml`

- [NegotiatedAPIResource pods.v1.core`](yamls/3-NegotiatedAPIResource-pods.v1.core.yaml) from `k get NegotiatedAPIResource pods.v1.core -o yaml > 3-NegotiatedAPIResource-pods.v1.core.yaml`

- [NegotiatedAPIResource services.v1.core`](yamls/3-NegotiatedAPIResource-services.v1.core.yaml) from `k get NegotiatedAPIResource services.v1.core -o yaml > 3-NegotiatedAPIResource-services.v1.core.yaml`

## 7. Bind to `compute` at the center

Given:

```bash
$ echo $KUBECONFIG
~/.kcp/admin.kubeconfig

$ k ws .
Current workspace is "root:kind".
```

Run the bind command at the center:

```bash
$ k kcp bind compute root:kind
placement placement-2iddvmcj created.
Placement "placement-2iddvmcj" is ready.
```

Note that kcp DNS pod is also started at the edge:

```bash
$ echo $KUBECONFIG
~/.kube/config

$ k get pods -A
NAMESPACE                  NAME                                              READY   STATUS    RESTARTS   AGE
kcp-syncer-kind-37zv5s0u   kcp-dns-kind-37zv5s0u-2ruw4p1u-6f476948b4-nsqlh   1/1     Running   0          53s
kcp-syncer-kind-37zv5s0u   kcp-syncer-kind-37zv5s0u-6949dd87f8-zdsxm         1/1     Running   0          14m
```

## 8. Check objects that were created in the `kind` KCP Workspace after binding to `compute` at the center (#4)

A `placement` object is created.
Also, differently from the MicroShift case, a new `APIBinding` is also created.

```bash
$ echo $KUBECONFIG
~/.kube/config

$ k ws .
Current workspace is "root:kind".

$ k get apiexports
NAME         AGE
kubernetes   19m

$ k get apibindings
NAME                       AGE
apiresource.kcp.io-c8wp6   72m
kubernetes                 19m
kubernetes-1pre20xf        3m36s
scheduling.kcp.io-7rr1j    72m
tenancy.kcp.io-5qqol       72m
topology.kcp.io-2eqkm      72m
workload.kcp.io-dil2i      72m

$ k get synctargets
NAME   AGE
kind   20m

$ k get locations
NAME      RESOURCE      AVAILABLE   INSTANCES   LABELS   AGE
default   synctargets   1           1                    20m

$ k get placements
NAME                 AGE
placement-2iddvmcj   2m7s

$ k get APIResourceImports
NAME
deployments.kind.v1.apps
ingresses.kind.v1.networking.k8s.io
pods.kind.v1.core
services.kind.v1.core

$ k get NegotiatedAPIResources
NAME
deployments.v1.apps
ingresses.v1.networking.k8s.io
pods.v1.core
services.v1.core

$ k get APIConversions
No resources found
```

Objects yamls:
- [APIExport `kubernetes`](yamls/4-apiexport-kubernetes.yaml) from `k get apiexport kubernetes -o yaml > 4-apiexport-kubernetes.yaml`

    No difference.

- [APIBinding `kubernetes`](yamls/4-apibinding-kubernetes.yaml) from `k get apibinding kubernetes -o yaml > 4-apibinding-kubernetes.yaml`

    No difference.

- [APIBinding `kubernetes-1pre20xf`](yamls/4-apibinding-kubernetes-1pre20xf.yaml) from `k get apibinding kubernetes-1pre20xf -o yaml > 4-apibinding-kubernetes-1pre20xf.yaml`

    Compared to the plainly named `kubernetes` `APIBinding`, this file contains the schemas of the exported resources:

    ```bash
    $ diff 4-apibinding-kubernetes-1pre20xf.yaml 4-apibinding-kubernetes.yaml
    7c7
    <   creationTimestamp: "2023-03-21T17:21:48Z"
    ---
    >   creationTimestamp: "2023-03-21T17:05:37Z"
    12,16c12,16
    <     claimed.internal.apis.kcp.io/77zi8i256rosNE0ykJyOOB4OwxAMrho27rckj3: LstvmbbzVDDOn90ZbhzSQO5U3DMCf88h1pZ
    <     internal.apis.kcp.io/export: 77zi8i256rosNE0ykJyOOB4OwxAMrho27rckj3
    <   name: kubernetes-1pre20xf
    <   resourceVersion: "9320"
    <   uid: 35bd2e6c-ebc4-411b-ab95-f706841c9441
    ---
    >     claimed.internal.apis.kcp.io/2rwzR3UC0Z0lSFUGD5EuXbE1OSTXPKljY8O67v: LstvmbbzVDDOn90ZbhzSQO5U3DMCf88h1pZ
    >     internal.apis.kcp.io/export: 2rwzR3UC0Z0lSFUGD5EuXbE1OSTXPKljY8O67v
    >   name: kubernetes
    >   resourceVersion: "9101"
    >   uid: ba26edd8-fb9c-4b85-a5e1-e662e882a8ca
    21d20
    <       path: root:compute
    23,56c22
    <   apiExportClusterName: 2ei017lw8ljpxp5x
    <   boundResources:
    <   - group: networking.k8s.io
    <     resource: ingresses
    <     schema:
    <       UID: ac065681-9ce8-4798-956b-347dfe2610f2
    <       identityHash: <id-hash>
    <       name: v124.ingresses.networking.k8s.io
    <     storageVersions:
    <     - v1
    <   - group: ""
    <     resource: services
    <     schema:
    <       UID: 06b1ff23-56af-42bd-b3f9-d016dbbf6000
    <       identityHash: <id-hash>
    <       name: v124.services.core
    <     storageVersions:
    <     - v1
    <   - group: apps
    <     resource: deployments
    <     schema:
    <       UID: 8eb70a7d-6856-4b52-8a88-7adcf40b6c49
    <       identityHash: <id-hash>
    <       name: v124.deployments.apps
    <     storageVersions:
    <     - v1
    <   - group: ""
    <     resource: pods
    <     schema:
    <       UID: ad4c06b1-3d92-46d4-8ca7-78ee41548b40
    <       identityHash: <id-hash>
    <       name: v124.pods.core
    <     storageVersions:
    <     - v1
    ---
    >   apiExportClusterName: br3qimk2t1o3jb70
    58c24
    <   - lastTransitionTime: "2023-03-21T17:21:48Z"
    ---
    >   - lastTransitionTime: "2023-03-21T17:05:37Z"
    61c27
    <   - lastTransitionTime: "2023-03-21T17:21:48Z"
    ---
    >   - lastTransitionTime: "2023-03-21T17:05:37Z"
    64c30
    <   - lastTransitionTime: "2023-03-21T17:21:48Z"
    ---
    >   - lastTransitionTime: "2023-03-21T17:05:37Z"
    67c33
    <   - lastTransitionTime: "2023-03-21T17:21:48Z"
    ---
    >   - lastTransitionTime: "2023-03-21T17:05:37Z"
    70c36
    <   - lastTransitionTime: "2023-03-21T17:21:48Z"
    ---
    >   - lastTransitionTime: "2023-03-21T17:05:37Z"
    73c39
    <   - lastTransitionTime: "2023-03-21T17:21:48Z"
    ---
    >   - lastTransitionTime: "2023-03-21T17:05:37Z"
    ```

- [SyncTarget `kind`](yamls/4-synctarget-kind.yaml) from `k get synctarget kind -o yaml > 4-synctarget-kind.yaml`

    Resource version change.

- [Location `default`](yamls/4-location-default.yaml) from `k get location default -o yaml > 4-location-default.yaml`

    No difference.

- [Placement `placement-2iddvmcj`](yamls/4-placement.yaml) from `k get placement placement-2iddvmcj -o yaml > 4-placement.yaml`

    New object.

- [APIResourceImport deployments.kind.v1.apps`](yamls/4-APIResourceImport-deployments.kind.v1.apps.yaml) from `k get APIResourceImport deployments.kind.v1.apps -o yaml > 4-APIResourceImport-deployments.kind.v1.apps.yaml`

- [APIResourceImport ingresses.kind.v1.networking.k8s.io`](yamls/4-APIResourceImport-ingresses.kind.v1.networking.k8s.io.yaml) from `k get APIResourceImport ingresses.kind.v1.networking.k8s.io -o yaml > 4-APIResourceImport-ingresses.kind.v1.networking.k8s.io.yaml`

- [APIResourceImport pods.kind.v1.core`](yamls/4-APIResourceImport-pods.kind.v1.core.yaml) from `k get APIResourceImport pods.kind.v1.core -o yaml > 4-APIResourceImport-pods.kind.v1.core.yaml`

- [APIResourceImport services.kind.v1.core`](yamls/4-APIResourceImport-services.kind.v1.core.yaml) from `k get APIResourceImport services.kind.v1.core -o yaml > 4-APIResourceImport-services.kind.v1.core.yaml`

- [NegotiatedAPIResource deployments.v1.apps`](yamls/4-NegotiatedAPIResource-deployments.v1.apps.yaml) from `k get NegotiatedAPIResource deployments.v1.apps -o yaml > 4-NegotiatedAPIResource-deployments.v1.apps.yaml`

- [NegotiatedAPIResource ingresses.v1.networking.k8s.io`](yamls/4-NegotiatedAPIResource-ingresses.v1.networking.k8s.io.yaml) from `k get NegotiatedAPIResource ingresses.v1.networking.k8s.io -o yaml > 4-NegotiatedAPIResource-ingresses.v1.networking.k8s.io.yaml`

- [NegotiatedAPIResource pods.v1.core`](yamls/4-NegotiatedAPIResource-pods.v1.core.yaml) from `k get NegotiatedAPIResource pods.v1.core -o yaml > 4-NegotiatedAPIResource-pods.v1.core.yaml`

- [NegotiatedAPIResource services.v1.core`](yamls/4-NegotiatedAPIResource-services.v1.core.yaml) from `k get NegotiatedAPIResource services.v1.core -o yaml > 4-NegotiatedAPIResource-services.v1.core.yaml`
