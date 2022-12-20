---
title: "Quickstart: Tenancy and APIs"
linkTitle: "Tenancy and APIs"
weight: 1
description: >
  How to create a new API and use it with tenancy
---

## Prerequisites

A running kcp server, as per [README.md](../README.md).

## Set your KUBECONFIG

To access a workspace, you need credentials and an Kubernetes API server URL for the workspace, both of which are stored
in a `kubeconfig` file.

The default context in the kcp-provided `admin.kubeconfig` gives access to the `root` workspace as the `kcp-admin` user.

```shell
$ export KUBECONFIG=.kcp/admin.kubeconfig
$ kubectl config get-contexts
CURRENT   NAME           CLUSTER   AUTHINFO      NAMESPACE
          base           base      kcp-admin
*         root           root      kcp-admin
          system:admin   base      shard-admin
```

You can use API discovery to see what resources are available in this `root` workspace. We're here to explore the
`tenancy.kcp.dev` and `apis.kcp.dev` resources.

```shell
$ kubectl api-resources
NAME                              SHORTNAMES   APIVERSION                             NAMESPACED   KIND
configmaps                        cm           v1                                     true         ConfigMap
events                            ev           v1                                     true         Event
...
apibindings                                    apis.kcp.dev/v1alpha1                  false        APIBinding
apiexports                                     apis.kcp.dev/v1alpha1                  false        APIExport
...
workspaces                        ws           tenancy.kcp.dev/v1beta1                false        Workspace
```

## Create and navigate some workspaces

The `ws` plugin for `kubectl` makes it easy to switch your `kubeconfig` between workspaces, and to create new ones:

```shell
$ kubectl ws .
Current workspace is "root".
$ kubectl ws create a --enter
Workspace "a" (type root:organization) created. Waiting for it to be ready...
Workspace "a" (type root:organization) is ready to use.
Current workspace is "root:a".
$ kubectl ws create b
Workspace "b" (type root:universal) created. Waiting for it to be ready...
Workspace "b" (type root:universal) is ready to use.
$ kubectl get workspaces
NAME   TYPE        PHASE   URL
b      universal   Ready   https://myhost:6443/clusters/root:a:b
$ kubectl ws b
Current workspace is "root:a:b".
$ kubectl ws ..
Current workspace is "root:a".
$ kubectl ws -
Current workspace is "root:a:b".
$ kubectl ws root
Current workspace is "root".
$ kubectl get workspaces
NAME    TYPE           PHASE   URL
a       organization   Ready   https://myhost:6443/clusters/root:a
```

Our `kubeconfig` now contains two additional contexts, one which represents the current workspace, and the other to keep
track of our most recently used workspace. This highlights that the `kubectl ws` plugin is primarily a convenience
wrapper for managing a `kubeconfig` that can be used for working within a workspace.

```shell
$ kubectl config get-contexts
CURRENT   NAME                         CLUSTER                      AUTHINFO      NAMESPACE
          base                         base                         kcp-admin
          root                         root                         kcp-admin
          system:admin                 base                         shard-admin
*         workspace.kcp.dev/current    workspace.kcp.dev/current    kcp-admin
          workspace.kcp.dev/previous   workspace.kcp.dev/previous   kcp-admin
```

## Understand workspace types

As we can see above, workspaces can contain sub-workspaces, and workspaces have different types. A workspace type
defines which sub-workspace types can be created under such workspaces. So, for example:

- A universal workspace is the base workspace type that most other workspace types inherit from - they may contain other
  universal workspaces, and they have a "default" namespace
- The root workspace primarily contains organization workspaces
- An organization workspace can contain universal workspaces, or can be further subdivided using team workspaces

The final type of workspace is "home workspaces". These are workspaces that are intended to be used privately by
individual users. They appear under the `root:users` workspace (type `homeroot`) and they are further organized into a
hierarchy of `homebucket` workspaces based on a hash of their name.

```shell
$ kubectl ws
Current workspace is "root:users:zu:yc:kcp-admin".
$ kubectl ws root
Current workspace is "root".
$ kubectl get workspaces
NAME    TYPE           PHASE   URL
a       organization   Ready   https://myhost:6443/clusters/root:a
users   homeroot       Ready   https://myhost:6443/clusters/root:users
```

Workspace types and their behaviors are defined using the `WorkspaceType` resource:

```shell
$ kubectl get workspacetypes
NAME           AGE
home           74m
homebucket     74m
homeroot       74m
organization   74m
root           74m
team           74m
universal      74m
$ kubectl describe workspacetype/team
Name:         team
...
API Version:  tenancy.kcp.dev/v1alpha1
Kind:         WorkspaceType
...
Spec:
  Default Child Workspace Type:
    Name:  universal
    Path:  root
  Extend:
    With:
      Name:  universal
      Path:  root
  Limit Allowed Parents:
    Types:
      Name:  organization
      Path:  root
      Name:  team
      Path:  root
Status:
  Conditions:
    Status:                True
    Type:                  Ready
...
```

## Workspaces FAQs

Q: Why do we have both `ClusterWorkspaces` and `Workspaces`?

A: `Workspaces` are intended to be the user-facing resource, whereas `ClusterWorkspaces` is a low-level resource for kcp
platform admins.

`Workspaces` are actually a "projected resource", there is no such resource stored in etcd, instead it is served as a
transformation of the `ClusterWorkspace` resource. `ClusterWorkspaces` contain details like shard assignment, which are
low-level fields that users should not see.

We are working on a change of this system behind the scenes. That will probably promote Workspaces to a normal,
non-projected resource, and ClusterWorkspaces will change in its role.

## Publish some APIs as a service provider

kcp offers `APIExport` and `APIBinding` resources which allow a service provider operating in one workspace to offer its
capabilities to service consumers in other workspaces.

First we'll create an organization workspace, and then within that create a service provider workspace.

```shell
$ kubectl ws create wildwest --enter
Workspace "wildwest" (type root:organization) created. Waiting for it to be ready...
Workspace "wildwest" (type root:organization) is ready to use.
Current workspace is "root:wildwest".
$ kubectl ws create cowboys-service --enter
Workspace "cowboys-service" (type root:universal) created. Waiting for it to be ready...
Workspace "cowboys-service" (type root:universal) is ready to use.
Current workspace is "root:wildwest:cowboys-service".
```

Then we'll use a CRD to generate an `APIResourceSchema` and `APIExport` and apply these within the service provider
workspace.

> The `apigen` tool used below can be found
> [here](https://github.com/kcp-dev/kcp/tree/main/cmd/apigen). Builds for the
> tool are not currently published as part of the kcp release process.

```shell
$ mkdir wildwest-schemas/
$ ./bin/apigen --input-dir test/e2e/customresourcedefinition/ --output-dir wildwest-schemas/
$ ls -1 wildwest-schemas/
apiexport-wildwest.dev.yaml
apiresourceschema-cowboys.wildwest.dev.yaml

$ kubectl apply -f wildwest-schemas/apiresourceschema-cowboys.wildwest.dev.yaml
apiresourceschema.apis.kcp.dev/v220920-6039d110.cowboys.wildwest.dev created
$ kubectl apply -f wildwest-schemas/apiexport-wildwest.dev.yaml
apiexport.apis.kcp.dev/wildwest.dev created
```

You can think of an `APIResourceSchema` as being equivalent to a CRD, and an `APIExport` makes a set of schemas
available to consumers.

## Use those APIs as a service consumer

Now we can adopt the service consumer persona and create a workspace from which we will use this new `APIExport`:

```shell
$ kubectl ws
Current workspace is "root:users:zu:yc:kcp-admin".
$ kubectl ws create --enter test-consumer
Workspace "test-consumer" (type root:universal) created. Waiting for it to be ready...
Workspace "test-consumer" (type root:universal) is ready to use.
Current workspace is "root:users:zu:yc:kcp-admin:test-consumer".

$ kubectl apply -f - <<EOF
apiVersion: apis.kcp.dev/v1alpha1
kind: APIBinding
metadata:
  name: cowboys
spec:
  reference:
    workspace:
      path: root:wildwest:cowboys-service
      exportName: wildwest.dev
EOF
apibinding.apis.kcp.dev/cowboys created
```

Now this resource type is available for use within our workspace, so
let's create an instance!

```shell
$ kubectl api-resources | grep wildwest
cowboys      wildwest.dev/v1alpha1    true    Cowboy

$ kubectl apply -f - <<EOF
apiVersion: wildwest.dev/v1alpha1
kind: Cowboy
metadata:
  name: one
spec:
  intent: one
EOF
cowboy.wildwest.dev/one created
```

## Dig deeper into `APIExports`

Switching back to the service provider persona:

```shell
$ kubectl ws root:wildwest:cowboys-service
Current workspace is "root:wildwest:cowboys-service".
$ kubectl get apiexport/wildwest.dev -o yaml
apiVersion: apis.kcp.dev/v1alpha1
kind: APIExport
metadata:
  name: wildwest.dev
  ...
status:
  ...
  identityHash: a6a0cc778bec8c4b844e6326965fbb740b6a9590963578afe07276e6a0d41e20
  virtualWorkspaces:
  - url: https://myhost:6443/services/apiexport/root:wildwest:cowboys-service/wildwest.dev
```

We can see that our `APIExport` has two key attributes in its status - its identity (more on this below) and a "virtual
workspace" URL. You can think of this virtual workspace as behaving just like a workspace or cluster, except it searches
across all true workspaces for instances of the resource types provided by the `APIExport`. We can use API discovery to
see what resource types are available via this virtual workspace:

```shell
$ kubectl --server='https://myhost:6443/services/apiexport/root:wildwest:cowboys-service/wildwest.dev/clusters/*/' api-resources
NAME      SHORTNAMES   APIVERSION              NAMESPACED   KIND
cowboys                wildwest.dev/v1alpha1   true         Cowboy
```

The question is ... can we see the instance created by the consumer?

```shell
$ kubectl --server='https://myhost:6443/services/apiexport/root:wildwest:cowboys-service/wildwest.dev/clusters/*/' get -A cowboys \
    -o custom-columns='WORKSPACE:.metadata.annotations.kcp\.dev/cluster,NAME:.metadata.name'
WORKSPACE                                  NAME
root:users:zu:yc:kcp-admin:test-consumer   one
```

Yay!

## APIs FAQ

Q: Why is there a new `APIResourceSchema` resource type that appears to be very similar to `CustomResourceDefinition`?

A: FIXME

Q: Why do I have to append `/clusters/*/` to the `APIExport` virtual workspace URL?

A: The URL represents the base path of a virtual kcp API server. With a standard kcp API server, workspaces live under
the `/clusters/` path, so `/clusters/*/` represents a wildcard search across all workspaces via this virtual API
server.

Q: How should we understand an `APIExport` `identityHash`?

A: Unlike with CRDs, a kcp instance might have many `APIResourceSchemas` of the same Group/Version/Kind, and users need
some way of securely distinguishing them.

Each `APIExport` is allocated a randomized private secret - this is currently just a large random number - and a public
identity - just a SHA256 hash of the private secret - which securely identifies this `APIExport` from others.

This is important because an `APIExport` makes a virtual workspace available to interact with all instances of a
particular `APIResourceShema`, and we want to make sure that users are clear on which service provider `APIExports` they
are trusting and only the owners of those `APIExport` have access to their resources via virtual workspaces.

Q: Why do you have to use `--all-namespaces` with the apiexport virtual workspace?

A: Think of this virtual workspace as representing a wildcard listing across all workspaces. It doesn't make sense to
look at a specific namespace across all workspaces, so you have to list across all namespaces too.

Q: If I attempt to use an `APIExport` virtual workspace before there are any `APIBindings` I get the "Error from server
(NotFound): Unable to list ...: the server could not find the requested resource". Is this a bug?

A: It is a bug. See <https://github.com/kcp-dev/kcp/issues/1183>

When fixed, we expect the `APIExport` behavior will change such that there will be no virtual workspace URLs until an
`APIBinding` is created.
