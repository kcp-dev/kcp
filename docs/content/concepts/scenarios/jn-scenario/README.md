# List KCP Workspace objects (NVIDIA Jetson Nano MicroShift scenario)

Table of content:

- [List KCP Workspace objects (NVIDIA Jetson Nano MicroShift scenario)](#list-kcp-workspace-objects-nvidia-jetson-nano-microshift-scenario)
  - [1. Start KCP](#1-start-kcp)
  - [2. Check objects in the new `jn` KCP Workspace at the center (#1)](#2-check-objects-in-the-new-jn-kcp-workspace-at-the-center-1)
  - [3. Create a syncer at the center](#3-create-a-syncer-at-the-center)
  - [4. Check objects that were created in the `jn` KCP Workspace after running `kcp workload sync` at the center (#2)](#4-check-objects-that-were-created-in-the-jn-kcp-workspace-after-running-kcp-workload-sync-at-the-center-2)
  - [5. Apply the syncer on the pcluster at the edge (NVIDIA Jetson Nano)](#5-apply-the-syncer-on-the-pcluster-at-the-edge-nvidia-jetson-nano)
  - [6. Check objects that were created in the `jn` KCP Workspace at the center after running the syncer in the pcluster at the edge (#3)](#6-check-objects-that-were-created-in-the-jn-kcp-workspace-at-the-center-after-running-the-syncer-in-the-pcluster-at-the-edge-3)
  - [7. Bind to `compute` at the center](#7-bind-to-compute-at-the-center)
  - [8. Check objects that were created in the `jn` KCP Workspace after binding to `compute` at the center (#4)](#8-check-objects-that-were-created-in-the-jn-kcp-workspace-after-binding-to-compute-at-the-center-4)

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

Create a new KCP Workspace called `jn`:

```bash
$ k ws create jn --enter
Workspace "jn" (type root:organization) created. Waiting for it to be ready...
Workspace "jn" (type root:organization) is ready to use.
Current workspace is "root:jn" (type root:organization).

$ k ws tree
.
└── root
    ├── compute
    └── jn
```

## 2. Check objects in the new `jn` KCP Workspace at the center (#1)

Given:

```bash
$ echo $KUBECONFIG
~/.kcp/admin.kubeconfig

$ k ws .
Current workspace is "root:jn".
```

List key objects:

```bash
$ k get apiexports
No resources found

$ k get apibindings
NAME                       AGE
apiresource.kcp.io-9xqib   3m3s
scheduling.kcp.io-1vgbc    3m3s
tenancy.kcp.io-gn443       3m3s
topology.kcp.io-1smay      3m2s
workload.kcp.io-ak35l      3m3s

$ k get synctargets
No resources found

$ k get locations
No resources found

$ k get placements
No resources found
```

Objects yamls:

- [APIBinding `apiresource.kcp.io-9xqib`](yamls/1-apibinding-apiresource.kcp.io-9xqib.yaml) from `k get APIBinding apiresource.kcp.io-9xqib -o yaml > 1-apibinding-apiresource.kcp.io-9xqib.yaml`

- [APIBinding `scheduling.kcp.io-1vgbc`](yamls/1-apibinding-scheduling.kcp.io-1vgbc.yaml) from `k get APIBinding scheduling.kcp.io-1vgbc -o yaml > 1-apibinding-scheduling.kcp.io-1vgbc.yaml`

- [APIBinding `tenancy.kcp.io-gn443`](yamls/1-apibinding-tenancy.kcp.io-gn443.yaml) from `k get APIBinding tenancy.kcp.io-gn443 -o yaml > 1-apibinding-tenancy.kcp.io-gn443.yaml`

- [APIBinding `topology.kcp.io-1smay`](yamls/1-apibinding-topology.kcp.io-1smay.yaml) from `k get APIBinding topology.kcp.io-1smay -o yaml > 1-apibinding-topology.kcp.io-1smay.yaml`

- [APIBinding `workload.kcp.io-ak35l`](yamls/1-apibinding-workload.kcp.io-ak35l.yaml) from `k get APIBinding workload.kcp.io-ak35l -o yaml > 1-apibinding-workload.kcp.io-ak35l.yaml`

## 3. Create a syncer at the center

Given:

```bash
$ echo $KUBECONFIG
~/.kcp/admin.kubeconfig

$ k ws .
Current workspace is "root:jn".
```

Create the [syncer YAML](yamls/syncer-jn.yaml) and pull the schemas from the pcluster:

```bash
$ k kcp workload sync jn \
    --syncer-image ghcr.io/kcp-dev/kcp/syncer:v0.11.0 \
    --apiexports="" \
    --resources=pods \
    --resources=deployments.apps \
    --resources=services \
    --resources=ingresses.networking.k8s.io \
    -o syncer-jn.yaml
Creating synctarget "jn"
Creating service account "kcp-syncer-jn-11hmg5wf"
Creating cluster role "kcp-syncer-jn-11hmg5wf" to give service account "kcp-syncer-jn-11hmg5wf"
```

## 4. Check objects that were created in the `jn` KCP Workspace after running `kcp workload sync` at the center (#2)

Given:

```bash
$ echo $KUBECONFIG
~/.kcp/admin.kubeconfig

$ k ws .
Current workspace is "root:jn".
```

List key objects:

```bash
$ k get apiexports
NAME         AGE
kubernetes   5m42s

$ k get apibindings
NAME                       AGE
apiresource.kcp.io-9xqib   22m
kubernetes                 5m55s
scheduling.kcp.io-1vgbc    22m
tenancy.kcp.io-gn443       22m
topology.kcp.io-1smay      22m
workload.kcp.io-ak35l      22m

$ k get synctargets
NAME   AGE
jn     6m15s

$ k get locations
NAME      RESOURCE      AVAILABLE   INSTANCES   LABELS   AGE
default   synctargets   0           1                    6m26s

$ k get placements
No resources found
```

Objects yamls:

- [APIExport `kubernetes`](yamls/2-apiexport-kubernetes.yaml) from `k get apiexport kubernetes -o yaml > 2-apiexport-kubernetes.yaml`
- [APIBinding `kubernetes`](yamls/2-apibinding-kubernetes.yaml) from `k get apibinding kubernetes -o yaml > 2-apibinding-kubernetes.yaml`
- [SyncTarget `jn`](yamls/2-synctarget-jn.yaml) from `k get synctarget jn -o yaml > 2-synctarget-jn.yaml`
- [Location `default`](yamls/2-location-default.yaml) from `k get location default -o yaml > 2-location-default.yaml`

## 5. Apply the syncer on the pcluster at the edge (NVIDIA Jetson Nano)

```bash
$ oc apply -f syncer-jn.yaml
namespace/kcp-syncer-jn-11hmg5wf created
serviceaccount/kcp-syncer-jn-11hmg5wf created
secret/kcp-syncer-jn-11hmg5wf-token created
clusterrole.rbac.authorization.k8s.io/kcp-syncer-jn-11hmg5wf created
clusterrolebinding.rbac.authorization.k8s.io/kcp-syncer-jn-11hmg5wf created
role.rbac.authorization.k8s.io/kcp-dns-jn-11hmg5wf created
rolebinding.rbac.authorization.k8s.io/kcp-dns-jn-11hmg5wf created
secret/kcp-syncer-jn-11hmg5wf created
deployment.apps/kcp-syncer-jn-11hmg5wf created

$ oc get pods -A
NAMESPACE                       NAME                                      READY   STATUS    RESTARTS   AGE
kcp-syncer-jn-11hmg5wf          kcp-syncer-jn-11hmg5wf-7c459c5b58-svl9l   1/1     Running   0          80s
```

## 6. Check objects that were created in the `jn` KCP Workspace at the center after running the syncer in the pcluster at the edge (#3)

The `synctarget default` is not `available`.

After running the syncer, [`kubernetes` `APIExport`](yamls/3-apiexport-kubernetes.yaml) contains the list of schemas pulled from the pcluster at the edge, running MicroShift 4.8:

```yaml
latestResourceSchemas:
  - rev-788.deployments.apps
  - rev-789.ingresses.networking.k8s.io
  - rev-774.pods.core
  - rev-770.services.core
```

Given:

```bash
$ echo $KUBECONFIG
~/.kcp/admin.kubeconfig

$ k ws .
Current workspace is "root:jn".
```

List key objects:

```bash
$ k get apiexports
NAME         AGE
kubernetes   34m

$ k get apibindings
NAME                       AGE
apiresource.kcp.io-9xqib   51m
kubernetes                 34m
scheduling.kcp.io-1vgbc    51m
tenancy.kcp.io-gn443       51m
topology.kcp.io-1smay      51m
workload.kcp.io-ak35l      51m

$ k get synctargets
NAME   AGE
jn     34m

$ k get locations
NAME      RESOURCE      AVAILABLE   INSTANCES   LABELS   AGE
default   synctargets   1           1                    34m

$ k get placements
No resources found
```

Objects yamls:

- [APIExport `kubernetes`](yamls/3-apiexport-kubernetes.yaml) from `k get apiexport kubernetes -o yaml > 3-apiexport-kubernetes.yaml`

    Resource schemas added to the `spec`:

    ```bash
    $ diff 2-apiexport-kubernetes.yaml 3-apiexport-kubernetes.yaml
    10c10
    <   generation: 2
    ---
    >   generation: 5
    12c12
    <   resourceVersion: "748"
    ---
    >   resourceVersion: "801"
    18a19,23
    >   latestResourceSchemas:
    >   - rev-788.deployments.apps
    >   - rev-789.ingresses.networking.k8s.io
    >   - rev-774.pods.core
    >   - rev-770.services.core
    ```

- [APIBinding `kubernetes`](yamls/3-apibinding-kubernetes.yaml) from `k get apibinding kubernetes -o yaml > 3-apibinding-kubernetes.yaml`

    Resources added to the list of `boundResources`:

    ```bash
    $ diff 2-apibinding-kubernetes.yaml 3-apibinding-kubernetes.yaml
    15c15
    <   resourceVersion: "751"
    ---
    >   resourceVersion: "819"
    22a23,55
    >   boundResources:
    >   - group: ""
    >     resource: services
    >     schema:
    >       UID: ddee1955-e64e-478b-9310-f934fac54910
    >       identityHash: <id-hash>
    >       name: rev-770.services.core
    >     storageVersions:
    >     - v1
    >   - group: ""
    >     resource: pods
    >     schema:
    >       UID: a5fe7c2d-adb9-4109-a4ee-8d703b27f2a9
    >       identityHash: <id-hash>
    >       name: rev-774.pods.core
    >     storageVersions:
    >     - v1
    >   - group: apps
    >     resource: deployments
    >     schema:
    >       UID: 309b6c26-5aa4-41d5-8afa-ce718ac1ea67
    >       identityHash: <id-hash>
    >       name: rev-788.deployments.apps
    >     storageVersions:
    >     - v1
    >   - group: networking.k8s.io
    >     resource: ingresses
    >     schema:
    >       UID: 6ab13f61-194b-4e0d-9816-b574cae3f875
    >       identityHash: <id-hash>
    >       name: rev-789.ingresses.networking.k8s.io
    >     storageVersions:
    >     - v1
    30c63
    <   - lastTransitionTime: "2023-03-20T18:37:20Z"
    ---
    >   - lastTransitionTime: "2023-03-20T19:08:23Z"
    ```

- [SyncTarget `jn`](yamls/3-synctarget-jn.yaml) from `k get synctarget jn -o yaml > 3-synctarget-jn.yaml`

    Resources added to the list of `syncedResources`:

    ```bash
    $ diff 2-synctarget-jn.yaml 3-synctarget-jn.yaml
    11c11
    <   resourceVersion: "728"
    ---
    >   resourceVersion: "855"
    19,23c19,20
    <   - lastTransitionTime: "2023-03-20T18:37:20Z"
    <     message: No heartbeat yet seen
    <     reason: ErrorHeartbeat
    <     severity: Warning
    <     status: "False"
    ---
    >   - lastTransitionTime: "2023-03-20T19:08:14Z"
    >     status: "True"
    25,29c22,23
    <   - lastTransitionTime: "2023-03-20T18:37:20Z"
    <     message: No heartbeat yet seen
    <     reason: ErrorHeartbeat
    <     severity: Warning
    <     status: "False"
    ---
    >   - lastTransitionTime: "2023-03-20T19:08:14Z"
    >     status: "True"
    30a25,51
    >   - lastTransitionTime: "2023-03-20T19:08:16Z"
    >     status: "True"
    >     type: SyncerAuthorized
    >   lastSyncerHeartbeatTime: "2023-03-20T19:14:14Z"
    >   syncedResources:
    >   - identityHash: <id-hash>
    >     resource: services
    >     state: Accepted
    >     versions:
    >     - v1
    >   - identityHash: <id-hash>
    >     resource: pods
    >     state: Accepted
    >     versions:
    >     - v1
    >   - group: networking.k8s.io
    >     identityHash: <id-hash>
    >     resource: ingresses
    >     state: Accepted
    >     versions:
    >     - v1
    >   - group: apps
    >     identityHash: <id-hash>
    >     resource: deployments
    >     state: Accepted
    >     versions:
    >     - v1
    ```

- [Location `default`](yamls/3-location-default.yaml) from `k get location default -o yaml > 3-location-default.yaml`

    Syncer instance is now available:

    ```bash
    $ diff 2-location-default.yaml 3-location-default.yaml
    10c10
    <   resourceVersion: "725"
    ---
    >   resourceVersion: "763"
    19c19
    <   availableInstances: 0
    ---
    >   availableInstances: 1
    ```

## 7. Bind to `compute` at the center

Given:

```bash
$ echo $KUBECONFIG
~/.kcp/admin.kubeconfig

$ k ws .
Current workspace is "root:jn".
```

```bash
$ k kcp bind compute root:jn --apiexports=root:jn:kubernetes
placement placement-268us406 created.
Placement "placement-268us406" is ready.
```

Note that kcp DNS pod is also started at the edge:

```bash
$ oc get pods -A
NAMESPACE                       NAME                                            READY   STATUS    RESTARTS   AGE
kcp-syncer-jn-11hmg5wf          kcp-dns-jn-11hmg5wf-2oyxxkaf-6dc9cf88b7-2njwg   1/1     Running   0          26s
kcp-syncer-jn-11hmg5wf          kcp-syncer-jn-11hmg5wf-7c459c5b58-svl9l         1/1     Running   0          35m
```

## 8. Check objects that were created in the `jn` KCP Workspace after binding to `compute` at the center (#4)

A `placement` object is created:

Given:

```bash
$ echo $KUBECONFIG
~/.kcp/admin.kubeconfig

$ k ws .
Current workspace is "root:jn".
```

List key objects:

```bash
$ k get apiexports
NAME         AGE
kubernetes   69m

$ k get apibindings
NAME                       AGE
apiresource.kcp.io-9xqib   86m
kubernetes                 69m
scheduling.kcp.io-1vgbc    86m
tenancy.kcp.io-gn443       86m
topology.kcp.io-1smay      86m
workload.kcp.io-ak35l      86m

$ k get synctargets
NAME   AGE
jn     69m

$ k get locations
NAME      RESOURCE      AVAILABLE   INSTANCES   LABELS   AGE
default   synctargets   1           1                    69m

$ k get placements
NAME                 AGE
placement-268us406   3m22s

$ k get APIResourceImports
NAME
deployments.jn.v1.apps
ingresses.jn.v1.networking.k8s.io
pods.jn.v1.core
services.jn.v1.core

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

- [SyncTarget `jn`](yamls/4-synctarget-jn.yaml) from `k get synctarget jn -o yaml > 4-synctarget-jn.yaml`

    No difference.

- [Location `default`](yamls/4-location-default.yaml) from `k get location default -o yaml > 4-location-default.yaml`

    No difference.

- [Placement `placement-268us406`](yamls/4-placement.yaml) from `k get placement placement-268us406 -o yaml > 4-placement.yaml`

    New object.

- [APIResourceImport deployments.jn.v1.apps`](yamls/4-APIResourceImport-deployments.jn.v1.apps.yaml) from `k get APIResourceImport deployments.jn.v1.apps -o yaml > 4-APIResourceImport-deployments.jn.v1.apps.yaml`

- [APIResourceImport ingresses.jn.v1.networking.k8s.io`](yamls/4-APIResourceImport-ingresses.jn.v1.networking.k8s.io.yaml) from `k get APIResourceImport ingresses.jn.v1.networking.k8s.io -o yaml > 4-APIResourceImport-ingresses.jn.v1.networking.k8s.io.yaml`

- [APIResourceImport pods.jn.v1.core`](yamls/4-APIResourceImport-pods.jn.v1.core.yaml) from `k get APIResourceImport pods.jn.v1.core -o yaml > 4-APIResourceImport-pods.jn.v1.core.yaml`

- [APIResourceImport services.jn.v1.core`](yamls/4-APIResourceImport-services.jn.v1.core.yaml) from `k get APIResourceImport services.jn.v1.core -o yaml > 4-APIResourceImport-services.jn.v1.core.yaml`

- [NegotiatedAPIResource deployments.v1.apps`](yamls/4-NegotiatedAPIResource-deployments.v1.apps.yaml) from `k get NegotiatedAPIResource deployments.v1.apps -o yaml > 4-NegotiatedAPIResource-deployments.v1.apps.yaml`

- [NegotiatedAPIResource ingresses.v1.networking.k8s.io`](yamls/4-NegotiatedAPIResource-ingresses.v1.networking.k8s.io.yaml) from `k get NegotiatedAPIResource ingresses.v1.networking.k8s.io -o yaml > 4-NegotiatedAPIResource-ingresses.v1.networking.k8s.io.yaml`

- [NegotiatedAPIResource pods.v1.core`](yamls/4-NegotiatedAPIResource-pods.v1.core.yaml) from `k get NegotiatedAPIResource pods.v1.core -o yaml > 4-NegotiatedAPIResource-pods.v1.core.yaml`

- [NegotiatedAPIResource services.v1.core`](yamls/4-NegotiatedAPIResource-services.v1.core.yaml) from `k get NegotiatedAPIResource services.v1.core -o yaml > 4-NegotiatedAPIResource-services.v1.core.yaml`

