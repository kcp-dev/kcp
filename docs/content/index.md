# <img src="logo.png" style="vertical-align: middle;" /> kcp Documentation

## Overview

kcp is a Kubernetes-like control plane focusing on:

- A **control plane** for many independent, **isolated** “clusters” known as **workspaces**
- Enabling API service providers to **offer APIs centrally** using **multi-tenant operators**
- Easy **API consumption** for users in their workspaces
- Flexible **scheduling** of workloads to physical clusters
- **Transparent movement** of workloads among compatible physical clusters
- **Advanced deployment strategies** for scenarios such as affinity/anti-affinity, geographic replication, cross-cloud
  replication, etc.

kcp can be a building block for SaaS service providers who need a **massively multi-tenant platform** to offer services
to a large number of fully isolated tenants using Kubernetes-native APIs. The goal is to be useful to cloud
providers as well as enterprise IT departments offering APIs within their company.

## Quickstart

### Prerequisites

- [kubectl](https://kubernetes.io/docs/tasks/tools/#kubectl)
- A Kubernetes cluster (for local testing, consider [kind](http://kind.sigs.k8s.io))

### Download kcp

Visit our [latest release page](https://github.com/kcp-dev/kcp/releases/latest) and download `kcp`
and `kubectl-kcp-plugin` that match your operating system and architecture.

Extract `kcp` and `kubectl-kcp-plugin` and place all the files in the `bin` directories somewhere in your `$PATH`.

### Start kcp

You can start kcp using this command:

```shell
kcp start
```

This launches kcp in the foreground. You can press `ctrl-c` to stop it.

To see a complete list of server options, run `kcp start options`.

### Set your KUBECONFIG

During its startup, kcp generates a kubeconfig in `.kcp/admin.kubeconfig`. Use this to connect to kcp and display the
version to confirm it's working:

```shell
$ export KUBECONFIG=.kcp/admin.kubeconfig
$ kubectl version
WARNING: This version information is deprecated and will be replaced with the output from kubectl version --short.  Use --output=yaml|json to get the full version.
Client Version: version.Info{Major:"1", Minor:"24", GitVersion:"v1.24.4", GitCommit:"95ee5ab382d64cfe6c28967f36b53970b8374491", GitTreeState:"clean", BuildDate:"2022-08-17T18:46:11Z", GoVersion:"go1.19", Compiler:"gc", Platform:"darwin/amd64"}
Kustomize Version: v4.5.4
Server Version: version.Info{Major:"1", Minor:"24", GitVersion:"v1.24.3+kcp-v0.8.0", GitCommit:"41863897", GitTreeState:"clean", BuildDate:"2022-09-02T18:10:37Z", GoVersion:"go1.18.5", Compiler:"gc", Platform:"darwin/amd64"}
```

### Configure kcp to sync to your cluster

kcp can't run pods by itself - it needs at least one physical cluster for that. For this example, we'll be using a
local `kind` cluster.

Run the following command to tell kcp about the `kind` cluster (replace the syncer image tag as needed):

```shell
$ kubectl kcp workload sync kind --syncer-image ghcr.io/kcp-dev/kcp/syncer:v0.8.0 -o syncer-kind-main.yaml
Creating synctarget "kind"
Creating service account "kcp-syncer-kind-25coemaz"
Creating cluster role "kcp-syncer-kind-25coemaz" to give service account "kcp-syncer-kind-25coemaz"

 1. write and sync access to the synctarget "kcp-syncer-kind-25coemaz"
 2. write access to apiresourceimports.

Creating or updating cluster role binding "kcp-syncer-kind-25coemaz" to bind service account "kcp-syncer-kind-25coemaz" to cluster role "kcp-syncer-kind-25coemaz".

Wrote physical cluster manifest to syncer-kind-main.yaml for namespace "kcp-syncer-kind-25coemaz". Use

  KUBECONFIG=<pcluster-config> kubectl apply -f "syncer-kind-main.yaml"

to apply it. Use

  KUBECONFIG=<pcluster-config> kubectl get deployment -n "kcp-syncer-kind-25coemaz" kcp-syncer-kind-25coemaz

to verify the syncer pod is running.
```

Next, we need to install the syncer pod on our `kind` cluster - this is what actually syncs content from kcp to the
physical cluster. Run the following command:

```shell
$ KUBECONFIG=</path/to/kind/kubeconfig> kubectl apply -f "syncer-kind-main.yaml"
namespace/kcp-syncer-kind-25coemaz created
serviceaccount/kcp-syncer-kind-25coemaz created
secret/kcp-syncer-kind-25coemaz-token created
clusterrole.rbac.authorization.k8s.io/kcp-syncer-kind-25coemaz created
clusterrolebinding.rbac.authorization.k8s.io/kcp-syncer-kind-25coemaz created
secret/kcp-syncer-kind-25coemaz created
deployment.apps/kcp-syncer-kind-25coemaz created
```

### Create a deployment in kcp

Let's create a deployment in our kcp workspace and see it get synced to our cluster:

```shell
$ kubectl create deployment --image=gcr.io/kuar-demo/kuard-amd64:blue --port=8080 kuard
deployment.apps/kuard created
```

Once your cluster has pulled the image and started the pod, you should be able to verify the deployment is running in
kcp:

```shell
$ kubectl get deployments
NAME    READY   UP-TO-DATE   AVAILABLE   AGE
kuard   1/1     1            1           3s
```

We are still working on adding support for `kubectl logs`, `kubectl exec`, and `kubectl port-forward` to kcp. For the
time being, you can check directly in your cluster.

kcp translates the names of namespaces in workspaces to unique names in a physical cluster. We first must get this
translated name; if you're following along, your translated name might be different.

```shell
$ KUBECONFIG=</path/to/kind/kubeconfig> kubectl get pods --all-namespaces --selector app=kuard
NAMESPACE          NAME                     READY   STATUS    RESTARTS   AGE
kcp-26zq2mc2yajx   kuard-7d49c786c5-wfpcc   1/1     Running   0          4m28s
```

Now we can e.g. check the pod logs:

```shell
$ KUBECONFIG=</path/to/kind/kubeconfig> kubectl --namespace kcp-26zq2mc2yajx logs deployment/kuard | head
2022/09/07 14:04:35 Starting kuard version: v0.10.0-blue
2022/09/07 14:04:35 **********************************************************************
2022/09/07 14:04:35 * WARNING: This server may expose sensitive
2022/09/07 14:04:35 * and secret information. Be careful.
2022/09/07 14:04:35 **********************************************************************
2022/09/07 14:04:35 Config:
{
  "address": ":8080",
  "debug": false,
  "debug-sitedata-dir": "./sitedata",
```

## Next steps

Thanks for checking out our quickstart!

If you're interested in learning more about all the features kcp has to offer, please check out our additional
documentation:

- [Concepts](concepts) - a high level overview of kcp concepts
- [Workspaces](concepts/workspaces.md) - a more thorough introduction on kcp's workspaces
- [Locations & scheduling](concepts/locations-and-scheduling.md) - details on kcp's primitives that abstract over clusters
- [Syncer](concepts/syncer.md) - information on running the kcp agent that syncs content between kcp and a physical cluster
- [kubectl plugin](concepts/kubectl-kcp-plugin.md)
- [Authorization](concepts/authorization.md) - how kcp manages access control to workspaces and content
- [Virtual workspaces](concepts/virtual-workspaces.md) - details on kcp's mechanism for virtual views of workspace content

## Contributing

We ❤️ our contributors! If you're interested in helping us out, please head over to our [Contributing](CONTRIBUTING.md)
and guide.

## Getting in touch

There are several ways to communicate with us:

- The [`#kcp-dev` channel](https://app.slack.com/client/T09NY5SBT/C021U8WSAFK) in the [Kubernetes Slack workspace](https://slack.k8s.io)
- Our mailing lists:
    - [kcp-dev](https://groups.google.com/g/kcp-dev) for development discussions
    - [kcp-users](https://groups.google.com/g/kcp-users) for discussions among users and potential users
- Subscribe to the [community calendar](https://calendar.google.com/calendar/embed?src=ujjomvk4fa9fgdaem32afgl7g0%40group.calendar.google.com) for community meetings and events
    - The kcp-dev mailing list is subscribed to this calendar
- See recordings of past community meetings on [YouTube](https://www.youtube.com/channel/UCfP_yS5uYix0ppSbm2ltS5Q)
- See [upcoming](https://github.com/kcp-dev/kcp/issues?q=is%3Aissue+is%3Aopen+label%3Acommunity-meeting) and [past](https://github.com/kcp-dev/kcp/issues?q=is%3Aissue+label%3Acommunity-meeting+is%3Aclosed) community meeting agendas and notes
- Browse the [shared Google Drive](https://drive.google.com/drive/folders/1FN7AZ_Q1CQor6eK0gpuKwdGFNwYI517M?usp=sharing) to share design docs, notes, etc.
    - Members of the kcp-dev mailing list can view this drive

## Additional references

- [KubeCon EU 2021: Kubernetes as the Hybrid Cloud Control Plane Keynote - Clayton Coleman (video)](https://www.youtube.com/watch?v=oaPBYUfdFE8)
- [OpenShift Commons: Kubernetes as the Control Plane for the Hybrid Cloud - Clayton Coleman (video)](https://www.youtube.com/watch?v=Y3Y11Aj_01I)
- [TGI Kubernetes 157: Exploring kcp: apiserver without Kubernetes](https://youtu.be/FD_kY3Ey2pI)
- [K8s SIG Architecture meeting discussing kcp - June 29, 2021](https://www.youtube.com/watch?v=YrdAYoo-UQQ)
- [Let's Learn kcp - A minimal Kubernetes API server with Saiyam Pathak - July 7, 2021](https://www.youtube.com/watch?v=M4mn_LlCyzk)
