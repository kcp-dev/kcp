---
description: >
    Information on the different types of URLs that kcp serves.
---

# REST access patterns

This describes the various REST access patterns the kcp apiserver supports.

## Typical requests for resources in a workspace

These requests are all prefixed with `/clusters/<workspace path | logical cluster name>`. Here are some example URLs:

- `GET /clusters/root/apis/tenancy.kcp.io/v1alpha1/workspaces` - lists all kcp Workspaces in the 
  `root` workspace.
- `GET /clusters/root:compute/api/v1/namespaces/test` - gets the namespace `test` from the `root:compute` workspace
- `GET /clusters/yqzkjxmzl9turgsf/api/v1/namespaces/test` - same as above, using the logical cluster name for 
  `root:compute`

## Typical requests for resources through the APIExport virtual workspace

An APIExport provides a view into workspaces that contain APIBindings that are bound to the APIExport. This allows 
the service provider - the owner of the APIExport - to access data in its consumers' workspaces. Here is an example 
APIExport virtual workspace URL:

```
/services/apiexport/root:my-ws/my-service/clusters/root:consumer-1/apis/example.kcp.io/v1/widgets
```

Let's break down the segments in the URL path:

| Segment                        | Description                                  |
|--------------------------------|----------------------------------------------|
| /services/apiexport            | Prefix for every APIExport virtual workspace |
| root:my-ws                     | Workspace where the APIExport lives          |
| my-service                     | Name of the APIExport                        |
| clusters/root:consumer-1       | Consumer workspace path                      |
| apis/example.kcp.io/v1/widgets | Normal API path                              |

## Setting up shared informers for a virtual workspace

A virtual workspace typically allows the service provider to set up shared informers that can list and watch 
resources across all the consumer workspaces bound to or supported by the virtual workspace. For example, the 
APIExport virtual workspace lets you inform across all workspaces that have an APIBinding to your APIExport. The 
syncer virtual workspace lets a syncer inform across all workspaces that have a Placement on the syncer's associated 
SyncTarget.

To set up shared informers to span multiple workspaces, you use a special cluster called the **wildcard cluster**, 
denoted by `*`. An example URL you would use when constructing a shared informer in this manner might be:

```
/services/apiexport/root:my-ws/my-service/clusters/*/api/v1/configmaps
```

or, to set up an entire shared informer factory, you give it the wildcard base:

```
/services/apiexport/root:my-ws/my-service/clusters/*
```
