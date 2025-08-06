---
linkTitle: "kcp-aware controllers"
weight: 1
description: >
  How to write a kcp-aware controller.
---

# Writing kcp-aware Controllers

While kcp follows the Kubernetes Resource Model (KRM) closely, its multi-tenancy capabilities via workspaces / logical clusters
make normal Kubernetes controllers somewhat limited in usefulness, since they generally expect a single Kubernetes API endpoint
(which would correspond to a single logical cluster). While kcp exposes something called [virtual workspaces](../../concepts/workspaces/virtual-workspaces.md),
their API semantics are also different from standard Kubernetes API endpoints and thus, controllers written for Kubernetes
cannot be used against them.

!!! info
    kcp has previously maintained a [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime) fork under [kcp-dev/controller-runtime](https://github.com/kcp-dev/controller-runtime).
    While it likely still works, we recommend migration to multicluster-runtime (see below).

## multicluster-runtime

[multicluster-runtime](https://github.com/kubernetes-sigs/multicluster-runtime) is an effort initiated by kcp developers in the upstream
community to provide a framework for building multi-cluster controllers. multicluster-runtime is **not** a fork of controller-runtime,
instead it is a sort of "addon" built on top of controller-runtime's excellent Go generic support. While the project is relatively young
and deemed experimental, we are confident in its ability to provide a great controller development experience for kcp.

The runtime itself is generic and needs a so-called provider that provides it with information about the fleet of clusters
it is supposed to reconcile over. A small ecosystem of providers exists already. The relevant provider for writing kcp-aware
controllers is described below.

For more information, also feel free to check out the [talk introducing multicluster-runtime at KubeCon + CloudNativeCon EU in London, 2025](https://www.youtube.com/watch?v=Tz8IcMSY7jw).

### Providers for kcp

kcp hosts a repository for kcp-specific provider implementations at [kcp-dev/multicluster-provider](https://github.com/kcp-dev/multicluster-provider).

Which provider to choose depends on the controller that is to be written, but there is a good chance you want to write a controller for resources
distributed via an [APIExport](../../concepts/apis/exporting-apis.md). In that case, you should choose the `apiexport` provider from the repository.

The provider repository also includes examples, including [one for the `apiexport` provider](https://github.com/kcp-dev/multicluster-provider/tree/main/examples/apiexport).
