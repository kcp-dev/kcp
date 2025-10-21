---
description: >
  Get started with kcp by running it locally and understanding its role as a platform-building framework.
---

# Quickstart

## What is kcp?

kcp is not a platform you can simply pick up and use immediately. Instead, it's a framework for building platforms. As Kelsey Hightower noted in 2017, Kubernetes itself is not a platform but rather a better way to start building one. kcp extends this philosophy further by providing a foundation for creating multi-tenant, workspace-based platforms.

Think of kcp as the infrastructure layer that provides core abstractions and services for building cloud-native application platforms. This quickstart will help you run kcp locally and explore its foundational capabilities, but building a complete platform on top of kcp requires additional design, development, and configuration tailored to your specific needs.

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
Client Version: v1.33.1
Kustomize Version: v5.6.0
Server Version: v1.32.3+kcp-v0.28.0
```

## Next steps

Congratulations on successfully running kcp! You've now experienced the basic mechanics of starting and connecting to kcp. However, remember that what you've run is the foundational layer for building platforms, not an end-user platform itself.

To understand how to build meaningful platforms with kcp, here are your next steps:

- [kcp Workshop](https://docs.kcp.io/contrib/learning/20250401-kubecon-london/workshop/) - a hands-on workshop that guides you through kcp's core concepts and features, including workspaces, syncers, and creating a provider-consumer architecture. We recommend using `github workspaces` for the best experience and following along with the exercises.
- [api-syncagent quick start](https://docs.kcp.io/api-syncagent/main/getting-started/) - learn how to use the api-syncagent to sync APIs from a kcp workspace to a downstream Kubernetes cluster. This is more advanced than workshop content and assumes familiarity with kcp concepts.


## Building your platform

Remember, kcp provides the building blocks, but you'll need to design and implement the higher-level abstractions that make sense for your users and use cases. To understand how to leverage kcp's capabilities effectively, explore our additional documentation:

- [Concepts](../concepts/index.md) - a high level overview of kcp concepts
- [Workspaces](../concepts/workspaces/index.md) - a more thorough introduction on kcp's workspaces
- [kubectl plugin](./kubectl-plugin.md)
- [Authorization](../concepts/authorization/index.md) - how kcp manages access control to workspaces and content
- [Virtual workspaces](../concepts/workspaces/virtual-workspaces.md) - details on kcp's mechanism for virtual views of workspace content
