---
description: >
  How to add a new resource for replication by the cache server.
---

# Replicating new resources in the cache server

As of today adding a new resource for replication is a manual process that consists of the following steps:

1. You need to register a new CRD in the cache server. 
   Registration is required otherwise the cache server won’t be able to serve the new resource.
   It boils down to adding a new entry into [an array](https://github.com/kcp-dev/kcp/blob/53fdaf580d46686686871f77e4a629bc3c234051/pkg/cache/server/bootstrap/bootstrap.go#L46).
   If you don’t have a CRD definition file for your type, you can use [the crdpuller](https://github.com/kcp-dev/kcp/tree/53fdaf580d46686686871f77e4a629bc3c234051/cmd/crd-puller) against any kube-apiserver to create the required manifest.

2. Next, you need to register the new type in the replication controller.
   For that you need to add a new entry into the following [array](https://github.com/kcp-dev/kcp/blob/53fdaf580d46686686871f77e4a629bc3c234051/pkg/reconciler/cache/replication/replication_controller.go#L73)

   In general there are two types of resources.
   The ones that are replicated automatically (i.e. APIExports)
   and the ones that are subject to additional filtering, for example require some additional annotation (i.e. ClusterRoles).

   For optional resources we usually create [a separate controller](https://github.com/kcp-dev/kcp/blob/53fdaf580d46686686871f77e4a629bc3c234051/pkg/reconciler/tenancy/replicateclusterrole/replicateclusterrole_controller.go) which simply annotates objects that need to be replicated.
   Then during registration we provide a filtering function that checks for existence of the annotation (i.e. [filtering function for CR](https://github.com/kcp-dev/kcp/blob/53fdaf580d46686686871f77e4a629bc3c234051/pkg/reconciler/cache/replication/replication_controller.go#L130))
