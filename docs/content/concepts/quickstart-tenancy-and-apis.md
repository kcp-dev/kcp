---
description: >
  How to create a new API and use it with tenancy.
---

# Quickstart: Tenancy and APIs

## Prerequisites

A running kcp server. The [quickstart](../setup/quickstart.md) is a good starting point.

## Set your KUBECONFIG

To access a workspace, you need credentials and an Kubernetes API server URL for the workspace, both of which are stored
in a `kubeconfig` file.

The default context in the kcp-provided `admin.kubeconfig` gives access to the `root` workspace as the `kcp-admin` user.

```sh
export KUBECONFIG=.kcp/admin.kubeconfig
kubectl config get-contexts
```

Output should look similar to below (depending on the kcp instance / method of kubeconfig generation):

```console
CURRENT   NAME           CLUSTER   AUTHINFO      NAMESPACE
          base           base      kcp-admin
*         root           root      kcp-admin
          system:admin   base      shard-admin
```

You can use API discovery to see what resources are available in this `root` workspace. We're here to explore the
`tenancy.kcp.io` and `apis.kcp.io` resources.

```sh
kubectl api-resources
```

Output should include some core resources and the kcp-specific ones for tenancy and API management:

```console
NAME                              SHORTNAMES   APIVERSION                            NAMESPACED   KIND
configmaps                        cm           v1                                    true         ConfigMap
events                            ev           v1                                    true         Event
...
apibindings                                    apis.kcp.io/v1alpha2                  false        APIBinding
apiexports                                     apis.kcp.io/v1alpha2                  false        APIExport
...
workspaces                        ws           tenancy.kcp.io/v1alpha1               false        Workspace
```

## Create and Navigate Workspaces

!!! tip
    Refer to the [plugins page](../setup/kubectl-plugin.md) to install the kubectl plugins for kcp.

The `ws` plugin for `kubectl` makes it easy to switch your `kubeconfig` between workspaces and use `create-workspace` plugin for `kubectl` to create new ones:


```sh
kubectl ws .
```

Output should show that you are currently in the `root` workspace:

```console
Current workspace is "root".
```

Next, let's create a new workspace in `root` and immediately switch to it (via `--enter`):

```sh
kubectl create-workspace a --enter
```

This will create the new workspace `a` and change the current workspace to it:

```console
Workspace "a" (type root:organization) created. Waiting for it to be ready...
Workspace "a" (type root:organization) is ready to use.
Current workspace is "root:a".
```

Let's create another workspace but not change into it:

```sh
kubectl create-workspace b
```

Output will look similar to the previous workspace creation:

```console
Workspace "b" (type root:universal) created. Waiting for it to be ready...
Workspace "b" (type root:universal) is ready to use.
```

Since there was no switch to the newly created workspace, you are still in `root:a` and can look at child workspaces here:

```sh
kubectl get workspaces
```

Output should include the new `b` workspace:

```console
NAME   TYPE        PHASE   URL
b      universal   Ready   https://myhost:6443/clusters/root:a:b
```

Here is a quick collection of commands showing the navigation between the workspaces you've just created. 
Note the usage of `..` to switch to the parent workspace and `-` to the previously selected workspace.

```console
$ kubectl ws b
Current workspace is "root:a:b".
$ kubectl ws ..
Current workspace is "root:a".
$ kubectl ws -
Current workspace is "root:a:b".
$ kubectl ws :root
Current workspace is "root".
```

!!! tip
    Workspace paths in kcp are separated by colons, not slashes. As such, absolute paths need to start with `:` (such as `:root` to navigate to the top-level root workspace as seen in the example above).

Our `kubeconfig` now contains two additional contexts, one which represents the current workspace, and the other to keep
track of our most recently used workspace. This highlights that the `kubectl ws` plugin is primarily a convenience
wrapper for managing a `kubeconfig` that can be used for working within a workspace.

```sh
kubectl config get-contexts
```

```console
CURRENT   NAME                         CLUSTER                      AUTHINFO      NAMESPACE
          base                         base                         kcp-admin
          root                         root                         kcp-admin
          system:admin                 base                         shard-admin
*         workspace.kcp.io/current    workspace.kcp.io/current    kcp-admin
          workspace.kcp.io/previous   workspace.kcp.io/previous   kcp-admin
```

## Understand Workspace Types

As we can see above, workspaces can contain sub-workspaces, and workspaces have different types. A workspace type
defines which sub-workspace types can be created under such workspaces. So, for example:

- A `universal` workspace is the base workspace type that most other workspace types inherit from - they may contain other
  universal workspaces, and they have a "default" namespace
- The `root` workspace primarily contains organization workspaces
- An `organization` workspace can contain universal workspaces, or can be further subdivided using team workspaces

Workspace types and their behaviors are defined using the `WorkspaceType` resource:

```shell
kubectl get workspacetypes
```

Output of this depends on the kcp instance, but by default the list should look like this:

```console
NAME           AGE
home           74m
organization   74m
root           74m
team           74m
universal      74m
```

Describing a `WorkspaceType` will yield some information about its configuration:

```console
$ kubectl describe workspacetype/team
Name:         team
...
API Version:  tenancy.kcp.io/v1alpha1
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

## Publish Some APIs as a Service Provider

kcp offers `APIExport` and `APIBinding` resources which allow a service provider operating in one workspace to offer its
capabilities to service consumers in other workspaces.

First we'll create an organization workspace in `root`, and then within that create a service provider workspace.

```sh
kubectl create-workspace wildwest --enter
```

```console
Workspace "wildwest" (type root:organization) created. Waiting for it to be ready...
Workspace "wildwest" (type root:organization) is ready to use.
Current workspace is 'root:wildwest' (type root:organization).
```

```sh
kubectl create-workspace cowboys-service --enter
```

```console
Workspace "cowboys-service" (type root:universal) created. Waiting for it to be ready...
Workspace "cowboys-service" (type root:universal) is ready to use.
Current workspace is 'root:wildwest:cowboys-service' (type root:universal).
```

!!! tip
    The `apigen` tool used below can be found on the [release page](https://github.com/kcp-dev/kcp/releases/latest)

Then we'll use a CRD to generate an `APIResourceSchema` and `APIExport` and apply these within the service provider
workspace. For this example, we are using a CRD from kcp's own end-to-end testing. You can find it [here](https://github.com/kcp-dev/kcp/tree/main/test/e2e/customresourcedefinition).
One of the options to follow along is to clone the kcp repository.


```sh
mkdir wildwest-schemas/
./bin/apigen --input-dir test/e2e/customresourcedefinition/ --output-dir wildwest-schemas/
ls -1 wildwest-schemas/
```

`apigen` should have generated two files, one containing an `APIResourceSchema` and the other
containing an `APIExport`:

```console
apiexport-wildwest.dev.yaml
apiresourceschema-cowboys.wildwest.dev.yaml
```

We can now apply both of them:

```sh
kubectl apply -f wildwest-schemas/
```

```console
apiresourceschema.apis.kcp.io/v220920-6039d110.cowboys.wildwest.dev created
apiexport.apis.kcp.io/wildwest.dev created
```

You can think of an `APIResourceSchema` as being equivalent to a CRD, and an `APIExport` makes a set of schemas
available to consumers.

## Use Those APIs as a Service Consumer

Now we can adopt the service consumer persona and create a workspace from which we will use this new `APIExport`.
Let's start by creating a new workspace under `root` and switch to it:

```sh
kubectl ws :root
```

```console
Current workspace is "root".
```

```sh
kubectl create-workspace test-consumer --enter
```

```console
Workspace "test-consumer" (type root:organization) created. Waiting for it to be ready...
Workspace "test-consumer" (type root:organization) is ready to use.
Current workspace is 'root:test-consumer' (type root:organization).
```

Now create an `APIBinding` that references the previously created `APIExport`:

```sh
kubectl apply -f - <<EOF
apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: cowboys
spec:
  reference:
    export:
      name: wildwest.dev
      path: root:wildwest:cowboys-service
EOF
```

```console
apibinding.apis.kcp.io/cowboys created
```

Let's verify that the resource provided by the `APIExport` is now available:

```sh
kubectl api-resources | grep wildwest
```

The `cowboys` resource should show up:

```console
cowboys      wildwest.dev/v1alpha1    true    Cowboy
```

Now this resource type is available for use within our workspace, so
let's create an instance!

```sh
kubectl apply -f - <<EOF
apiVersion: wildwest.dev/v1alpha1
kind: Cowboy
metadata:
  name: one
spec:
  intent: one
EOF
```

```console
cowboy.wildwest.dev/one created
```

## Managing Permissions

Besides publishing APIs and reconciliating the related resources service providers' controllers may need access to core resources or resources exported by other services in the user workspaces as part of their duties. This access needs for security reason to get authorized. `permissionClaims` address this need.

A service provider wanting to access `ConfigMaps` needs to specify such a claim in the `APIExport`. They also need to specify what operations they need/request via `verbs`.

```yaml
spec:
...
  permissionClaims:
    - group: ""
      resource: "configmaps"
      verbs: ["*"]
```
Users can then authorize access to this resource type in their workspace by accepting the claim in the `APIBinding`, including the permitted verbs and a way to scope down object access by a selector of choice:

```yaml
spec:
...
  permissionClaims:
    - group: ""
      resource: "configmaps"
      verbs: ["*"]
      state: Accepted
      selector:
        matchAll: true
```

Operations allowed on the resources for which permission claim is accepted is defined as the intersection of the verbs in the `APIBinding` and the verbs in the `APIExport`. Verbs in this case are matching the verbs used by the [Kubernetes API](https://kubernetes.io/docs/reference/using-api/api-concepts/#api-verbs). There is the possibility to further limit the access claim to single resources.

PermissionClaims allows for additional selectors, for more details, check out the [APIBindings documentation](./apis/exporting-apis.md#apibinding).

## Dig Deeper into APIExports

Switching back to the service provider persona:

```sh
kubectl ws root:wildwest:cowboys-service
```

```console
Current workspace is "root:wildwest:cowboys-service".
```

```sh
kubectl get apiexport/wildwest.dev -o yaml
```

```yaml
apiVersion: apis.kcp.io/v1alpha2
kind: APIExport
metadata:
  name: wildwest.dev
  ...
status:
  ...
  identityHash: a6a0cc778bec8c4b844e6326965fbb740b6a9590963578afe07276e6a0d41e20
```

We can see that our `APIExport` has a key attribute in its status - its identity hash.
This identity can be used in permissionClaims for referring to non-core resources.

## APIExportEndpointSlice

`APIExportEndpointSlices` allow service provider to retrieve the URL of service endpoints, acting as a sink for them. You can think of this endpoint as behaving just like a workspace or cluster, except it searches across all workspaces for instances of the resource types provided by the `APIExport`.
An `APIExportEndpointSlice` is created by a service provider, references a single `APIExport` and optionally a `Partition`.
`Partitions` are a mechanism for filtering service endpoints.

Within a multi-sharded kcp, each shard will offer its own service endpoint URL for an `APIExport`. Service provider may decide to have multiple instances of their controller reconciliating, for instance, resources of shards in the same region. For that they may create an `APIExportEndpointSlice` in the same workspace where a controller instance is deployed. This `APIExportEndpointSlice` will then reference a specific `Partition` by its name in the same workspace filtering the service endpoints for a subset of shards.

If an `APIExportEndpointSlice` does not reference a `Partition` all the available endpoints are populated in its `status`. More on `Partitions` [here](./sharding/partitions.md).

By default, kcp creates a non-partioned `APIExportEndpointSlice` for every `APIExport`.

```sh
kubectl get apiexportendpointslice/wildwest.dev -o yaml
```

```yaml
kind: APIExportEndpointSlice
apiVersion: apis.kcp.io/v1alpha1
metadata:
    name: wildwest.dev
...
status:
    endpoints
        - url: https://host1:6443/services/apiexport/ubjrrg1rhptt4f09/wildwest.dev
...
```

We can use API discovery to see what resource types are available via the endpoint URL:

```sh
kubectl --server='https://host1:6443/services/apiexport/ubjrrg1rhptt4f09/wildwest.dev/clusters/*/' api-resources
```

```console
NAME          SHORTNAMES   APIVERSION              NAMESPACED   KIND
apibindings                apis.kcp.io/v1alpha2    false        APIBinding
cowboys                    wildwest.dev/v1alpha1   true         Cowboy
error: unable to retrieve the complete list of server APIs: v1: received empty response for: v1
```

Every service provider sees the `APIBinding` resource so they can access the "contract" between API consumer and provider. `kubectl` gets a little confused about `v1` APIs missing, but the endpoint itself is fully functional.

The question is, can we see the particular instance created by the consumer persona?

```sh
kubectl --server='https://host1:6443/services/apiexport/ubjrrg1rhptt4f09/wildwest.dev/clusters/*/' get -A cowboys -o custom-columns='LOGICAL CLUSTER:.metadata.annotations.kcp\.io/cluster,NAME:.metadata.name'
```

```console
LOGICAL CLUSTER    NAME
18hjcbxmhbq0k9y1   one
```

Yay! We have access to the object (thus, we can reconcile it) and we see the source [logical cluster](./terminology.md#logical-cluster).

This completes the basic tour of kcp's tenancy and API management capabilities. One of the next things you likely want to do is [develop your own kcp-aware controller](../developers/controllers/writing-kcp-aware-controllers.md) to automatically reconcile the API we just created (or any other, for that matter).

## APIs FAQ

Q: Why is there a new `APIResourceSchema` resource type that appears to be very similar to `CustomResourceDefinition`?

A: An APIResourceSchema defines a single custom API type. It is almost identical to a CRD, but creating an APIResourceSchema instance does not add a usable API to the server. By intentionally decoupling the schema definition from serving, API owners can be more explicit about API evolution.

---

Q: Why do I have to append `/clusters/*/` to the `APIExport` service endpoint URL?

A: The URL represents the base path of a virtual kcp API server. With a standard kcp API server, workspaces live under the `/clusters/` path, so `/clusters/*/` represents a wildcard search across all workspaces via this virtual API server.

---

Q: How should we understand an `APIExport` `identityHash`?

A: Unlike with CRDs, a kcp instance might have many `APIResourceSchemas` of the same Group/Version/Kind, and users need
some way of securely distinguishing them.

Each `APIExport` is allocated a randomized private secret - this is currently just a large random number - and a public
identity - just a SHA256 hash of the private secret - which securely identifies this `APIExport` from others.

This is important because an `APIExport` makes service endpoints available to interact with all instances of a particular `APIResourceShema`, and we want to make sure that users are clear on which service provider `APIExports` they are trusting and only the owners of those `APIExport` have access to their resources via the service endpoints.

---

Q: Why do you have to use `--all-namespaces` with the `APIExport` service endpoint?

A: Think of this endpoint as representing a wildcard listing across all workspaces. It doesn't make sense to look at a specific namespace across all workspaces, so you have to list across all namespaces too.
