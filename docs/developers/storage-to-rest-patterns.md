### workspaces

kcp promises to support 1 mln of workspaces. 
A workspace is like a Kubernetes endpoint, i.e. an endpoint usual Kubernetes client tooling (client-go, controller-runtime and others) 
and user interfaces (kubectl, helm, web console, ...) can talk to, just like to a Kubernetes cluster.
Thus creating a workspace must be efficient, both in terms of storage and compute.
It also must provide isolation, just like regular clusters.

### etcd

etcd is the primary datastore used by kcp. 
It stores data in a key-value store. 
The store’s logical view is a flat binary key space. 
The key space has a lexically sorted index on byte string keys.

In order to create a logical hierarchy, keys are usually mixed with `/` e.g.  `/company/branch/location`

To make it more concrete let’s create a company acme with two branches, marketing, and sales in some locations.

```
etcdctl put /acme/marketing/london foo
etcdctl put /acme/marketing/gdansk foo2
etcdctl put /acme/sales/warszawa foo3
```

In order to get all branches of acme company, all we have to do is to ask the database to return all keys matching `/acme` prefix
```
etcdctl get --prefix /acme
/acme/marketing/gdansk
foo2
/acme/marketing/london
foo
/acme/sales/warszawa
foo3
```

In order to see all locations of marketing teams, we as for records matching /acme/marketing prefix
```
etcdctl get --prefix /acme/marketing
/acme/marketing/gdansk
foo2
/acme/marketing/london
foo
```

Note that those queries are based on byte comparisons. 
We didn't create an `/acme` key. 
Everything matching the prefix will be returned when using the --prefix parameter.

This is the key idea behind workspaces in kcp. 
Creating a workspace on the storage layer is very efficient because it boils down to concatenating a string.
It also provides isolation on the lowest possible level.
Data is filtered by the database engine.

### the generic registry

kcp is based on the generic apiserver library provided by Kubernetes. 
The central type provided by the library that interacts with a storage layer is called the generic registry. 
It connects an API endpoint (REST) with a database layer. 
Almost all API types make use of it.

The generic apiserver library keeps an in-memory representation of the store for each resource in an API group. 
For example, `secrets` resources in the `core` API group gets their own storage.

From the perspective of this document, we can assume that the most important feature of generic storage 
is to compute the key that is passed to the database to find the data the user wants.

![](registry.png)

When the server starts it precomputes the `ResourcePrefix` with a group and a resource name. 
Everything else, like a workspace name, is added dynamically. 
For example, a request with a URL of `/clusters/acme/core/secrets` finds a storage responsible 
for `core/secrets` resources and adds `acme` to the `ResourcePrefix`. 
The new string becomes a key that is passed to etcd to find only resources for `acme` cluster.
