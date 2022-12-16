---
title: "Authorization"
linkTitle: "Authorization"
weight: 1
description: >
  How to authorize requests to kcp
---

Within workspaces, KCP implements the same RBAC-based authorization mechanism as Kubernetes.
Other authorization schemes (i.e. ABAC) are not supported.
Generally, the same (cluster) role and (cluster) role binding principles apply exactly as in Kubernetes.

In addition, additional RBAC semantics is implemented cross-workspaces, namely the following:

- **Top-Level Organization** access: the user must have this as pre-requisite to access any other workspace, or is
  even member and by that can create workspaces inside the organization workspace.
- **Workspace Content** access: the user needs access to a workspace or is even admin.
- for some resources, additional permission checks are performed, not represented by local or Kubernetes standard RBAC rules. E.g.
  - workspace creation checks for organization membership (see above).
  - workspace creation checks for `use` verb on the `ClusterWorkspaceType`.
  - API binding via APIBinding objects requires verb `bind` access to the corresponding `APIExport`.
- **System Workspaces** access: system workspaces are prefixed with `system:` and are not accessible by users.

The details are outlined below.

## Authorizers

The following authorizers are configured in kcp:

| Authorizer                             | Description                                                                       |
|----------------------------------------|-----------------------------------------------------------------------------------|
| Top-Level organization authorizer      | checks that the user is allowed to access the organization                        |
| Workspace content authorizer           | determines additional groups a user gets inside of a workspace                    |
| Maximal permission policy authorizer   | validates the maximal permission policy RBAC policy in the API exporter workspace |
| Local Policy authorizer                | validates the RBAC policy in the workspace that is accessed                       |
| Kubernetes Bootstrap Policy authorizer | validates the RBAC Kubernetes standard policy                                     |

They are related in the following way:

1. top-level organization authorizer must allow
2. workspace content authorizer must allow, and adds additional (virtual per-request) groups to the request user influencing the follow authorizers.
3. maximal permission policy authorizer must allow
4. one of the local authorizer or bootstrap policy authorizer must allow.

```
                                                                                 ┌──────────────┐
                                                                                 │              │
                                                                           ┌────►│ Local Policy ├──┐
          ┌──────────────┐     ┌──────────────┐    ┌───────────────────┐   │     │ authorizer   │  │
 request  │  Workspace   │     │  Required    │    │ Max. Permission   │   │     │              │  │
─────────►│  Content     ├────►│  Groups      ├────┤ Policy authorizer ├───┤     └──────────────┘  │
          │  Authorizer  │     │  Authorizer  │    │                   │   │                       ▼
          └──────────────┘     └──────────────┘    └───────────────────┘   │                       OR───►
                                                                           │     ┌──────────────┐  ▲
                                                                           │     │  Bootstrap   │  │
                                                                           └────►│  Policy      ├──┘
                                                                                 │  authorizer  │
                                                                                 │              │
                                                                                 └──────────────┘
```

[ASCIIFlow document](https://asciiflow.com/#/share/eJyrVspLzE1VslLydg5QcCwtycgvyqxKLVLSUcpJrATSVkrVMUoVMUpWhgYGBjoxSpVAppGlGZBVklpRAuTEKClQGzya0vNoSgPRaEJMTB4N3NCEIUBde9B9OW0XyE6f%2FOTEHIWA%2FJzM5EqgkjnYPfloyh6SENmaSNWDaQQsIEF0Ijx9wSSgoVqUWliaWlwCtk9BITy%2FKLu4IDE5VQEqAKODgMoyi1JTFBASIMo3sUJPISC1KDezuDgzPw8uiWw1ZuRCrMbnflCMAM1xzs8rSc0rwRqGUCXuRfmlBcW44gYWnUjeB0vAI38JVOMUUpL9DMw0CXaLI1Igo4QeFgmYPJbgwQw1hPy0PUM9NWIC%2FyDkrEjtrA5LiKQVbKCg3kQrpwBpp%2Fz8kuKSosQCBZQ8QVXrUNM0pJCDZQioEnghN4NmJXkiStqnsieR7EEToI09pBUTMUq1SrUA%2FWv8Mg%3D%3D))


### Workspace Content authorizer

The workspace content authorizer checks whether the user is granted access to the workspace. 
Access is granted access through `verb=access` non-resource permission to `/` inside of the workspace.

The ClusterRole `system:kcp:workspace:access` is pre-defined which makes it easy
to give a user access through a ClusterRoleBinding inside of the workspace.

For example, to give a user `user1` access, create the following ClusterRoleBinding:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: example-access
subjects:
- kind: User
  name: user1
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: system:kcp:workspace:access
```

To give a user `user1` admin access, create the following ClusterRoleBinding:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: example-admin
subjects:
- kind: User
  name: user1
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
```

A service-account defined in a workspace implicitly is granted access to it.

A service-account defined in a differant workspace is NOT given access to it.

### Required Groups Authorizer

A `authorization.kcp.dev/required-groups` annotation can be added to a LogicalCluster 
to specify additional groups that are required to access a workspace for a user to be member of. 
The syntax is a disjunction (separator `,`) of conjunctions (separator `;`).

For example, `<group1>;<group2>,<group3>` means that a user must be member of `<group1>` AND `<group2>`, OR of `<group3>`.

The annotation is copied onto sub-workspaces during scheduling.

#### Initializing Workspaces

By default, workspaces are only accessible to a user if they are in `Ready` phase. Workspaces that are initializing
can be access only by users that are granted `admin` verb on the `workspaces/content` resource in the
parent workspace.

Service accounts declared within a workspace don't have access to initializing workspaces.

### Maximal permission policy authorizer

If the requested resource type is part of an API binding, then this authorizer verifies that
the request is not exceeding the maximum permission policy of the related API export.
Currently, the "local policy" maximum permission policy type is supported.

#### Local policy

The local maximum permission policy delegates the decision to the RBAC of the related API export.
To distinguish between local RBAC role bindings in that workspace and those for this these maximum permission policy,
every name and group is prefixed with `apis.kcp.dev:binding:`.

Example:

Given an API binding for type `foo` declared in workspace `consumer` that refers to an API export declared in workspace `provider`
and a user `user-1` having the group `group-1` requesting a `create` of `foo` in the `default` namespace in the `consumer` workspace,
this authorizer verifies that `user-1` is allowed to execute this request by delegating to `provider`'s RBAC using prefixed attributes.

Here, this authorizer prepends the `apis.kcp.dev:binding:` prefix to the username and all groups the user belongs to.
Using prefixed attributes prevents RBAC collisions i.e. if `user-1` is granted to execute requests within the `provider` workspace directly.

For the given example RBAC request looks as follows:

- Username: `apis.kcp.dev:binding:user-1`
- Group: `apis.kcp.dev:binding:group-1`
- Resource: `foo`
- Namespace: `default`
- Workspace: `provider`
- Verb: `create`

The following role and role binding declared within the `provider` workspace will grant access to the request:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: foo-creator
  clusterName: provider
rules:
- apiGroups:
  - foo.api
  resources:
  - foos
  verbs:
  - create
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: user-1-foo-creator
  namespace: default
  clusterName: provider
subjects:
- kind: User
  name: apis.kcp.dev:binding:user-1
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: foo-creator
```

{{% alert title="Note" color="primary" %}}
The same authorization scheme is enforced when executing the request of a claimed resource via the virtual API Export API server,
i.e. a claimed resource is bound to the same maximal permission policy. Only the actual owner of that resources can go beyond that policy.
{{% /alert %}}

TBD: Example

### Kubernetes Bootstrap Policy authorizer

The bootstrap policy authorizer works just like the local authorizer but references RBAC rules
defined in the `system:admin` system workspace.

### Local Policy authorizer

Once the top-level organization authorizer and the workspace content authorizer granted access to a
workspace, RBAC rules contained in the workspace derived from the request context are evaluated.

This authorizer ensures that RBAC rules contained within a workspace are being applied
and work just like in a regular Kubernetes cluster.

{{% alert title="Note" color="primary" %}}
Groups added by the workspace content authorizer can be used for role bindings in that workspace.
{{% /alert %}}

It is possible to bind to roles and cluster roles in the bootstrap policy from a local policy `RoleBinding` or `ClusterRoleBinding`.

### Service Accounts

Kubernetes service accounts are granted access to the workspaces they are defined in and that are ready.

E.g. a service account "default" in `root:org:ws:ws` is granted access to `root:org:ws:ws`, and through the
workspace content authorizer it gains the `system:kcp:clusterworkspace:access` group membership.
