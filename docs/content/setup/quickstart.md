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
Client Version: v1.31.1
Kustomize Version: v5.4.2
Server Version: v1.31.0+kcp-v0.26.0
```

## Next steps

Thanks for checking out our quickstart!

If you're interested in learning more about all the features kcp has to offer, please check out our additional
documentation:

- [Concepts](../concepts/index.md) - a high level overview of kcp concepts
- [Workspaces](../concepts/workspaces/index.md) - a more thorough introduction on kcp's workspaces
- [kubectl plugin](./kubectl-plugin.md)
- [Authorization](../concepts/authorization/index.md) - how kcp manages access control to workspaces and content
- [Virtual workspaces](../concepts/workspaces/virtual-workspaces.md) - details on kcp's mechanism for virtual views of workspace content
