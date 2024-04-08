---
description: Changes kcp has made to etcd storage paths.
---

# etcd structure

kcp has made some changes to etcd storage paths to support logical clusters and APIExport identities. Please see 
below for details.

## Built-in APIs

Built-in APIs, such as `namespaces`, `configmaps`, `clusterroles`, etc., use the following storage path structure:

```
/etcd prefix/API group name/API resource name/logical cluster name/[namespace, if applicable]/name
```

Let's break down the segments in the etcd path for this example ConfigMap instance:

```
/registry/core/configmaps/root/default/kube-root-ca.crt
```

| Segment name     | Description          |
|------------------|----------------------|
| registry         | etcd prefix          |
| core             | API group name       |
| configmaps       | API resource name    |
| root             | logical cluster name |
| default          | namespace            |
| kube-root-ca.crt | ConfigMap name       |

## Standard custom resource instances

Custom resource instances for a CustomResourceDefinition in a workspace use the following storage path structure:

```
/etcd prefix/API group name/API resource name/"customresources"/logical cluster name/[namespace, if applicable]/name
```

Let's break down the segments in the etcd path for this example APIBinding instance:

```
/registry/apis.kcp.io/apibindings/customresources/2cynbfy2m0wtjqcs/scheduling.kcp.io-1mc2w
```

| Segment name            | Description                                           |
|-------------------------|-------------------------------------------------------|
| registry                | etcd prefix                                           |
| apis.kcp.io             | API group name                                        |
| apibindings             | API resource name                                     |
| customresources         | hard-coded (as opposed to an APIExport identity hash) |
| 2cynbfy2m0wtjqcs        | logical cluster name                                  |
| scheduling.kcp.io-1mc2w | APIBinding name                                       |

## "Bound" custom resource instances

Custom resource instances for an API provided by an APIExport, bound by an APIBinding, use the following storage 
path structure:

```
/etcd prefix/API group name/API resource name/APIExport identity hash/logical cluster name/[namespace, if applicable]
/name
```

Let's break down the segments in the etcd path for this example Workspace instance:

```
/registry/tenancy.kcp.io/workspaces/cbf732f90c9b7eac67e3d8f8e4c22cf8de0e36acefab6bbab738a85535c0d4cf/root/compute
```

| Segment name                                                     | Description             |
|------------------------------------------------------------------|-------------------------|
| registry                                                         | etcd prefix             |
| tenancy.kcp.io                                                   | API group name          |
| workspaces                                                       | API resource name       |
| cbf732f90c9b7eac67e3d8f8e4c22cf8de0e36acefab6bbab738a85535c0d4cf | APIExport identity hash |
| root                                                             | logical cluster name    |
| compute                                                          | Workspace name          |
