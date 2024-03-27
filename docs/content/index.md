# <img src="logo.png" style="vertical-align: middle;" /> kcp Documentation

## Overview

kcp is a Kubernetes-like control plane focusing on:

- A **control plane** for many independent, **isolated** “clusters” known as **workspaces**
- Enabling API service providers to **offer APIs centrally** using **multi-tenant operators**
- Easy **API consumption** for users in their workspaces
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

## Next steps

Thanks for checking out our quickstart!

If you're interested in learning more about all the features kcp has to offer, please check out our additional
documentation:

- [Concepts](concepts) - a high level overview of kcp concepts
- [Workspaces](concepts/workspaces.md) - a more thorough introduction on kcp's workspaces
- [kubectl plugin](concepts/kubectl-kcp-plugin.md)
- [Authorization](concepts/authorization.md) - how kcp manages access control to workspaces and content
- [Virtual workspaces](concepts/virtual-workspaces.md) - details on kcp's mechanism for virtual views of workspace content

## Contributing

We ❤️ our contributors! If you're interested in helping us out, please head over to our [Contributing](CONTRIBUTING.md)
guide.

## Getting in touch

There are several ways to communicate with us:

- The [`#kcp-dev` channel](https://app.slack.com/client/T09NY5SBT/C021U8WSAFK) in the [Kubernetes Slack workspace](https://slack.k8s.io).
- Our mailing lists:
    - [kcp-dev](https://groups.google.com/g/kcp-dev) for development discussions.
    - [kcp-users](https://groups.google.com/g/kcp-users) for discussions among users and potential users.
- By joining the kcp-dev mailing list, you should receive an invite to our bi-weekly community meetings.
- See recordings of past community meetings on [YouTube](https://www.youtube.com/channel/UCfP_yS5uYix0ppSbm2ltS5Q).
- The next community meeting dates are available via our [CNCF community group](https://community.cncf.io/kcp/).
- Check the [community meeting notes document](https://docs.google.com/document/d/1PrEhbmq1WfxFv1fTikDBZzXEIJkUWVHdqDFxaY1Ply4) for future and past meeting agendas.
- Browse the [shared Google Drive](https://drive.google.com/drive/folders/1FN7AZ_Q1CQor6eK0gpuKwdGFNwYI517M?usp=sharing) to share design docs, notes, etc.
    - Members of the kcp-dev mailing list can view this drive.

## Additional references

- [KubeCon EU 2021: Kubernetes as the Hybrid Cloud Control Plane Keynote - Clayton Coleman (video)](https://www.youtube.com/watch?v=oaPBYUfdFE8)
- [OpenShift Commons: Kubernetes as the Control Plane for the Hybrid Cloud - Clayton Coleman (video)](https://www.youtube.com/watch?v=Y3Y11Aj_01I)
- [TGI Kubernetes 157: Exploring kcp: apiserver without Kubernetes](https://youtu.be/FD_kY3Ey2pI)
- [K8s SIG Architecture meeting discussing kcp - June 29, 2021](https://www.youtube.com/watch?v=YrdAYoo-UQQ)
- [Let's Learn kcp - A minimal Kubernetes API server with Saiyam Pathak - July 7, 2021](https://www.youtube.com/watch?v=M4mn_LlCyzk)
