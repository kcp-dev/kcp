---
title: "Virtual Workspaces"
linkTitle: "Virtual Workspaces"
weight: 1
description: >
  What are virtual workspaces and how do they work?
---

Virtual workspaces are proxy-like apiservers under a custom URL that provide some computed view of real workspaces.

## Examples

1. when the user does `kubectl get workspaces` only workspaces are shown that the user has access to. That resource is implemented through a virtual workspace under `/services/workspaces/<org>/personal/apis/tenancy.kcp.dev/v1beta1/workspaces`.
2. controllers should not be able to directly access customer workspaces. They should only be able to access the objects that are connected to their provided APIs. In [April 19's community call this virtual workspace was showcased](https://www.youtube.com/watch?v=Ca3vh3lS6YI&t=1280s), developed during v0.4 phase.
3. if we keep the initializer model with `WorkspaceType`, there must be a virtual workspace for the "workspace type owner" that gives access to initializing workspaces.
4. the syncer will get a virtual workspace view of the workspaces it syncs to physical clusters. That view will have transformed objects potentially, especially deployment-splitter-like transformations will be implemented within a virtual workspace, transparently applied from the point of view of the syncer.

## FAQ

- **Can we use go clients to watch resources on a virtual workspace?** Absolutely. From the point of view of the controllers it is just a normal (client) URL. So one can use client-go informers (or controller-runtime) to watch the objects in a virtual workspace.

{{% alert title="Note" color="primary" %}}
A normal service account lives in just ONE workspace and can only access its own workspace. So in order to use a service account for accessing cross-workspace data (and that's what is necessary in example 2 and 3 at least), we need a virtual workspace to add the necessary authz.
{{% /alert %}}

- **Are virtual workspaces read-only?** No, they are not necessarily. Some are, some are not. The controller view virtual workspace will be writable, as well as the syncer virtual workspace.
- **Do service teams have to write their own virtual workspace?** Not for the standard cases as described above. There might be cases in the future where service teams provide their own virtual workspace for some very special purpose access patterns. But we are not there yet.
- **Where does the developer get the URL from of the virtual workspace?** The URLs will be "published" in some object status. E.g. APIExport.status will have a list of URLs that controllers have to connect to (example 2). Similarly, SyncTarget.status will have URLs for the syncer virtual workspaces, etc. We might do the same in WorkspaceType.status (example 3).
- **Will there be multiple virtual workspace URLs my controller has to watch?** Yes, as soon as we add sharding, it will become a list. So it might be that 1000 tenants are accessible under one URL, the next 1000 under another one, and so on. The controllers have to watch the mentioned URL lists in status of objects and start new instances (either with their own controller sharding eventually, or just in process with another go routine).
- **Show me the code.** The stock kcp virtual workspaces are in [`pkg/virtual`](../pkg/virtual).
- **Who runs the virtual workspaces?** The stock kcp virtual workspaces will be run through `kcp start` in-process. The personal workspace one (example 1) can also be run as its own process and the kcp apiserver will forward traffic to the external address. There might be reasons in the future like scalability that the later model is preferred. For the clients of virtual workspaces that has no impact. They are supposed to "blindly" use the URLs published in the API objects' status. Those URLs might point to in-process instances or external addresses depending on deployment topology.
