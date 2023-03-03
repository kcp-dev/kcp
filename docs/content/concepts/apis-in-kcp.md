---
description: >
    What APIs come standard, how to share APIs with others, how to consume shared APIs.
---

# APIs in kcp

## Overview

kcp supports several built-in Kubernetes APIs, provides extensibility using CustomResourceDefinitions, and adds a new
way to export custom APIs for sharing with other workspaces.

## Built-in APIs

kcp includes some, but not all, of the APIs you are likely familiar with from Kubernetes:

### (core) v1
- Namespaces
- ConfigMaps
- Secrets
- Events
- LimitRanges
- ResourceQuotas
- ServiceAccounts

### admissionregistration.k8s.io/v1
- MutatingWebhookConfigurations
- ValidatingWebhookConfigurations

### apiextensions.k8s.io/v1
- CustomResourceDefinitions

### authentication.k8s.io/v1
- TokenReviews

### authorization.k8s.io/v1
- LocalSubjectAccessReviews
- SelfSubjectAccessReviews
- SelfSubjectRulesReviews
- SubjectAccessReviews

### certificates.k8s.io/v1
- CertificateSigningRequests

### coordination.k8s.io/v1
- Leases

### events.k8s.io/v1
- Events

### flowcontrol.apiserver.k8s.io/v1beta1 (temporarily removed)
- FlowSchemas
- PriorityLevelConfigurations

### rbac.authorization.k8s.io/v1
- ClusterRoleBindings
- ClusterRoles
- RoleBindings
- Roles

Notably, workload-related APIs (Pods, ReplicaSets, Deployments, Jobs, CronJobs, StatefulSets), cluster-related APIs (
Nodes), storage-related APIs (PersistentVolumes, PersistentVolumeClaims) are all missing - kcp does not include these,
and it instead relies on workload clusters to provide this functionality.

## CustomResourceDefinitions

kcp, like Kubernetes, allows developers to add new APIs using
[CustomResourceDefinitions](https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/)
(CRDs). Unlike Kubernetes, kcp allows multiple copies of a CRD (e.g. `widgets.example.com`) to be installed in multiple
workspaces at the same time. Each copy is entirely independent and isolated from all other copies. This means the API
versions and schemas can be entirely different. kcp makes this possible because each workspace is its own isolated
"cluster."

There are currently some limitations to be aware of with CRDs in kcp:

- Conversion webhooks are not supported
- `service`-based validating/mutating webhooks are not supported; you must use `url`-based  `clientConfigs` instead.

CRDs are a fantastic way to add new APIs to a workspace, but if you want to share a CRD with other workspaces, you have
to install it in each workspace separately. You also need a controller that can reconcile CRs in all the workspaces
where your CRD is installed, which typically means 1 distinct controller per workspace. CRDs are not "cheap" in the API
server (each one consumes memory), and kcp offers an improved workflow that significantly reduces overhead.

## Exporting APIs

If you're looking to provide APIs that can be consumed by multiple workspaces, this section is for you!

kcp adds new APIs that enable this new model of publishing and consuming APIs. The following diagram shows a workspace
that an API provider would use to export their `widgets` API. The provider uses kcp's new `APIResourceSchema` type to
define the schema for `widgets`, and the new `APIExport` type to export `widgets` to other workspaces.

2 separate consumer workspaces use kcp's new `APIBinding` type to add the `widgets` API to their workspaces. They can
then proceed to CRUD `widgets` in their workspaces, just like any other API type (e.g. `namespaces`, `configmaps`,
etc.).

```
                                     ┌───────────────────────┐
                                     │  Consumer Workspace   │
                                     ├───────────────────────┤
                                     │                       │
                                ┌────┼─ Widgets APIBinding   |
                                │    │                       │
                                │    │  Widget A             │
┌───────────────────────────┐   │    │  Widget B             │
│  API Provider Workspace   │   │    │  Widget C             │
├───────────────────────────┤   │    └───────────────────────┘
│                           │   │
│     Widgets APIExport ◄───┼───┤
│             │             │   │
│             ▼             │   │
│ Widgets APIResourceSchema │   │    ┌───────────────────────┐
└───────────────────────────┘   │    │  Consumer Workspace   │
                                │    ├───────────────────────┤
                                │    │                       │
                                └────┼─ Widgets APIBinding   │
                                     │                       │
                                     │  Widget A             │
                                     │  Widget B             │
                                     │  Widget C             │
                                     └───────────────────────┘
```

**Diagram: 1 APIExport consumed by 2 different workspaces** ([source][diagram1])

Above we talked about needing 1 controller instance per workspace, as workspace is roughly synonymous with cluster, and
most/all controllers out there currently only support a single cluster. But because multiple workspaces coexist in kcp,
we can be much more efficient and have 1 controller handle `widgets` in multiple workspaces!

We achieve this using a specific URL just for your controller for your `APIExport`.

You'll need to do a few things:

1. Define 1 or more `APIResourceSchema` objects, 1 per API resource (just like you'd do with CRDs).
2. Define an `APIExport` object that references all the `APIResourceSchema` instances you want to export.
3. Write controllers that are multi-workspace aware.

Let's look at each of these in more detail.

### Define APIResourceSchemas

An `APIResourceSchema` defines a single custom API type. It is almost identical to a CRD, but creating
an `APIResourceSchema` instance does not add a usable API to the server. By intentionally decoupling the schema
definition from serving, API owners can be more explicit about API evolution. In the future, we could envision the
possibility of a new `APIDeployment` resource that coordinates rolling out API updates.

Here is an example for a `widgets` resource:

```yaml
apiVersion: apis.kcp.dev/v1alpha1
kind: APIResourceSchema
metadata:
  name: v220801.widgets.example.kcp.dev # (1)
spec:
  group: example.kcp.dev
  names:
    categories:
    - kcp
    kind: Widget
    listKind: WidgetList
    plural: widgets
    singular: widget
  scope: Cluster
  versions:
  - additionalPrinterColumns:
    - description: The current phase
      jsonPath: .status.phase
      name: Phase
      type: string
    - jsonPath: .metadata.creationTimestamp
      name: Age
      type: date
    name: v1alpha1
    schema:
      description: 'NOTE: full schema omitted for brevity'
      type: object
    served: true
    storage: true
    subresources:
      status: {}
```

1. `name` must be of the format `<some prefix>.<plural resource name>.<group name>`. For this example:
       - `<some prefix>` is `v220801`
       - `<plural resource name>` is `widgets`
       - `<group name>` is `example.kcp.dev`

An `APIResourceSchema`'s `spec` is immutable; if you need to make changes to your API schema, you create a new instance.

Once you've created at least one `APIResourceSchema`, you can proceed with creating your `APIExport`.

### Define your APIExport

An `APIExport` is the way an API provider makes one or more APIs (coming from `APIResourceSchemas`) available to
workspaces.

Here is an example `APIExport` called `example.kcp.dev` that exports 1 resource: `widgets`.

```yaml
apiVersion: apis.kcp.dev/v1alpha1
kind: APIExport
metadata:
  name: example.kcp.dev
spec:
  latestResourceSchemas:
  - v220801.widgets.example.kcp.dev
```

At a minimum, you specify the names of the `APIResourceSchema`s you want to export in the `spec.latestResourceSchemas`
field. The `APIResourceSchemas` must be in the same workspace as the `APIExport` (and therefore no workspace name or
path is required here).

You can optionally configure the following additional aspects of an `APIExport`:

- its identity
- what permissions consumers of your exported APIs are granted
- API resources from other sources (built-in types and/or from other `APIExport`s) that your controllers need to access
  for your service to function correctly

We'll talk about each of these next.

#### APIExport Identity

Each API resource type is defined by an API group name and a resource name. Each API resource type can further be
distinguished by an API version. For example, the `roles.rbac.authorization.k8s.io` resource that we frequently interact
with is in the `rbac.authorization.k8s.io` API group and its resource name is `roles`.

In a Kubernetes cluster, it is impossible to define the same API `<group>,<resource>,<version>` multiple times; i.e., it
does not support competing definitions for `rbac.authorization.k8s.io,roles,v1` or any other API resource type.

In kcp, however, it is possible for multiple API providers to each define the same API `<group>,<resource>,<version>`,
_without interference_! This is where `APIExport` "identity" becomes critical.

When you create an `APIExport` and you don't specify its identity (as is the case in the `example.kcp.dev` example
above), kcp automatically generates one for you. The identity is always stored in a secret in the same workspace as
the `APIExport.` By default, it is created in the `kcp-system` namespace in a secret whose name is the name of
the `APIExport`.

An `APIExport`'s identity is similar to a private key; you should never share it with anyone.

The identity's **hash** is similar to a public key; it is not private and there are times when other consumers of your
exported APIs need to know and reference it.

Given 2 Workspaces, each with its own `APIExport` that exports `widgets.example.kcp.dev`, kcp uses the identity hash (in
a mostly transparent manner) to ensure the correct instances associated with the appropriate `APIResourceSchema` are
served to clients. See [Run Your Controller](#Run-Your-Controller) for more information.

#### Permission Claims

When a consumer creates an `APIBinding` that binds to an `APIExport`, the API provider who owns the `APIExport`
implicitly has access to instances of the exported APIs in the consuming workspace. There are also times when the API
provider needs to access additional resource data in a consuming workspace. These resources might come from other
APIExports the consumer has created `APIBindings` for, or from APIs that are built in to kcp. The API provider 
requests access to these additional resources by adding `PermissionClaims` for the desired API's group, resource, and 
identity hash to their `APIExport`. Let's take the example `APIExport` from above and add permission claims for 
`ConfigMaps` and `Things`:

```yaml
apiVersion: apis.kcp.dev/v1alpha1
kind: APIExport
metadata:
  name: example.kcp.dev
spec:
  latestResourceSchemas:
  - v220801.widgets.example.kcp.dev
  permissionClaims:
  - group: "" # (1)
    resource: configmaps
    resourceSelector: # (2)
    - namespace: example-system
      name: my-setup
  - group: somegroup.kcp.io
    resource: things
    identityHash: 5fdf7c7aaf407fd1594566869803f565bb84d22156cef5c445d2ee13ac2cfca6 # (3)
    all: true # (4)
```

1. This is how you specify the core API group
2. You can claim access to one or more resource instances by namespace and/or name
3. To claim another exported API, you must include its `identityHash`
4. If you aren't claiming access to individual instances, you must specify `all` instead

This is essentially a request from the APIProvider, asking each consumer to grant permission for the claimed 
resources. If the consumer does not accept a permission claim, the API Provider is not allowed to access the claimed
resources. Consumer acceptance of permission claims is part of the `APIBinding` spec. For more details, see the 
section on [APIBindings](#apibinding).

#### Maximal Permission Policy

If you want to set an upper bound on what is allowed for a consumer of your exported APIs. you can set a "maximal
permission policy" using standard RBAC resources. This is optional; if the policy is not set, no upper bound is applied,
and a consuming user is authorized based on the RBAC configuration in the consuming workspace.

The maximal permission policy consists of RBAC `(Cluster)Roles` and `(Cluster)RoleBindings`. Incoming requests to a 
workspace that binds to an APIExport are additionally checked against
these rules, with the username and groups prefixed with `apis.kcp.io:binding:`.

For example: we have an `APIExport` in the `root` workspace called `tenancy.kcp.io` that provides APIs
for `workspaces` and `workspacetypes`:

```yaml
apiVersion: apis.kcp.io/v1alpha1
kind: APIExport
metadata:
  name: tenancy.kcp.io
spec:
  latestResourceSchemas:
  - v230110-89146c99.workspacetypes.tenancy.kcp.io
  - v230116-832a4a55d.workspaces.tenancy.kcp.io
  maximalPermissionPolicy:
    local: {} # (1)
```

1. `local` is the only supported option at the moment. "Local" means the RBAC policy is defined in the same 
   workspace as the `APIExport`.

We don't want users to be able to mutate the `status` subresource, so we set up
a maximal permission policy to limit what users can do:

```yaml title="Tenancy APIExport Maximal Permission Policy ClusterRole"
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: system:kcp:apiexport:tenancy:maximal-permission-policy
rules:
- apiGroups: ["tenancy.kcp.io"]
  verbs: ["*"] # (1)
  resources:
  - workspaces
  - workspacetypes
- apiGroups: ["tenancy.kcp.io"]
  verbs: ["list","watch","get"] # (2)
  resources:
  - workspaces/status
  - workspacetypes/status
```

1. Users can perform any/all actions on the main resources
2. Users can only get/list/watch the status subresources

```yaml title="Tenancy APIExport Maximal Permission Policy ClusterRoleBinding"
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: system:kcp:authenticated:apiexport:tenancy:maximal-permission-policy
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: system:kcp:apiexport:tenancy:maximal-permission-policy
subjects:
- apiGroup: rbac.authorization.k8s.io
  kind: Group
  name: apis.kcp.io:binding:system:authenticated # (1)
```

1. Note the `apis.kcp.io:binding:` prefix; this identifies this `ClusterRoleBinding` as part of the maximal 
   permission policy. It applies to `system:authenticated`.

Now imagine a user named `unicorn` with group `system:authenticated`. They create a workspace named 
`magic` and bind to the `tenancy.kcp.io` APIExport from the workspace `root`. What actions `unicorn` is allowed to 
perform in the `magic` workspace must be granted by **both**:

1. the standard RBAC authorization flow in the `magic` workspace; i.e., `(Cluster)Roles` and `(Cluster)RoleBindings` 
   in the `magic` workspace itself, **and**
2. the maximal permission policy RBAC settings configured in the `root` workspace for the `tenancy` APIExport


### Run Your Controller

TODO
- virtual workspace URLs
- As a controller, I need to be granted permissions on the APIExport content sub-resource

### APIResourceSchema Evolution & Maintenance

TODO
- conversions
- doc when it's ok to delete "old"/no longer used APIResourceSchemas

## Binding to Exported APIs

### APIBinding
TODO
- "imports" all the `APIResourceSchemas` defined in an `APIExport` into its workspace
- Also provides access to the group/resources defined in an `APIExport`'s `PermissionClaims` slice
- APIs are "imported" by accepting `PermissionClaims` within the `APIBinding` for each of the `APIExport`'s group/resources
- The consumer of the `APIExport` must be granted permission to the `bind` verb on that `APIExport` in order to create an `APIBinding`
- An `APIBinding` is bound to a specific `APIExport` and associated `APIResourceSchema`s via the `APIBinding.Status.BoundResources` field, which will hold the identity information to precisely identify relevant objects.
- how do I correctly reference an APIExport?

[diagram1]: https://asciiflow.com/#/share/eJyrVspLzE1VssorzcnRUcpJrEwtUrJSqo5RqohRsrI0NdGJUaoEsozMzYCsktSKEiAnRkmBGPBoyh5qoZiYPGKtVFBwzs8rLs1NLVIIzy%2FKLi5ITE6FyJBgyIC4G5cMEYZgtVwhPDMlPbWkWMExwNMpMy8lMy%2BdFAOp5C44BXGNgiMWY6gY4igBgNUBTtgdAGQDw0khoCi%2FLDMFNfHgNMp5gPxCxeSJO4YR8YeqEilVuVYU5BeVKDya3kKCDdj5ONROw68WyS1BqcX5pUXJqcHJGam5iehx1vNoSgM10AT6xHATzlKsiZRcN4dKvl5C1xIDS9DgKMmICQyoqU24ZUgyBEcpRpYh6CURWYagl0EkGDKFSsljRoxSrVItAH%2FrdL4%3D
