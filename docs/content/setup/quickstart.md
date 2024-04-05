---
description: >
  Get started with kcp by running it locally.
---

# Quickstart

## Prerequisites

- [kubectl](https://kubernetes.io/docs/tasks/tools/#kubectl)

## Download kcp

Visit our [latest release page](https://github.com/kcp-dev/kcp/releases/latest) and download `kcp`
and `kubectl-kcp-plugin` that match your operating system and architecture.

Extract `kcp` and `kubectl-kcp-plugin` and place all the files in the `bin` directories somewhere in your `$PATH`.

## Start kcp

You can start kcp using this command:

```shell
kcp start
```

This launches kcp in the foreground. You can press `ctrl-c` to stop it.

To see a complete list of server options, run `kcp start options`.

## Set your KUBECONFIG

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

- [Concepts](../concepts/index.md) - a high level overview of kcp concepts
- [Workspaces](../concepts/workspaces.md) - a more thorough introduction on kcp's workspaces
- [kubectl plugin](./kubectl-plugin.md)
- [Authorization](../concepts/authorization.md) - how kcp manages access control to workspaces and content
- [Virtual workspaces](../concepts/virtual-workspaces.md) - details on kcp's mechanism for virtual views of workspace content
