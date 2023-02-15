---
description: >
  How to authorize requests to kcp
---

# Authorization

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
           ┌─────────────────┐     ┌───────────────┐        ┌───────────────────┐   ┌────►│ Local Policy ├──┐
           │ Top-level       │     │               │        │                   │   │     │ authorizer   │  │
 request   │ Organization    │     │ Workspace ┌───┴───┐    │ Max. Permission   │   │     │              │  │
──────────►│ authorizer      ├────►│ Content   │+groups├───►│ Policy authorizer ├───┤     └──────────────┘  │
           │                 │     │ authorizer└───┬───┘    │                   │   │                       ▼
           │                 │     │               │        └───────────────────┘   │                       OR───►
           └─────────────────┘     └───────────────┘                                │     ┌──────────────┐  ▲
                                                                                    │     │  Bootstrap   │  │
                                                                                    └────►│  Policy      ├──┘
                                                                                          │  authorizer  │
                                                                                          │              │
                                                                                          └──────────────┘

```

[ASCIIFlow document](https://asciiflow.com/#/share/eJy1VUFLwzAU%2FislV6egB6G96Y4qKyJ4ySWUMItdU9NM2o2BePbQQxk97OjRk3iS%2FZr9Ets125omxWaQEmj68vLe974vL52DEE0wcMDN0LWupuyJUH%2BGKRiAAKXl2wFzCBIIHNs%2BH0CQlrML%2B7KcMZyw8gMCy9izyT82%2BVvvkUEYmgTzLhnEhJpwD7iP3J3pJV53jEwRYflblXtLPBRYLgl8Ly1dVh1EV64PJDoN8CsOWmxJrDUt8uLB2gyA9sdyt8C5p%2FhlimPGrSM6RqE%2FQ8wnYRvDI6HPcYQ83Cr1p81n5XyHkjPLxXTix3EdS8YkQ64x%2FaNCTaxQ0Hb%2FSuE1JCHDIa%2FuZEzJNIoFz9qN69OIKYb75ClynbNVKM%2B3LJdaJjHZlxi3n%2B6K9eVaD496pR8XXc3CS%2BhGObpv6tMGrKWBSNoRu4sOjAq29C7aSoxvQ7etoOA1ISxmFEWW0GRm8or01s216y7usu%2Brwvy%2FpnlJmCu7kbBlMJxQ7zqCYAEWf%2BVCNyA%3D))

### Top-Level Organization authorizer

A top-level organization is a workspace directly under root. When a user accesses a top-level organization or
a sub-workspace like `root:org:ws:ws`, this authorizer will check in `root` whether the user has permission
to the top-level org workspace represented by the `ClusterWorkspace` named `org` in `root` with the following verbs:

| Verb     | Resource                   | Semantics                                                  |
|----------|----------------------------|------------------------------------------------------------|
| `access` | `clusterworkspace/content` | the user can access the organization `root:org`            |

E.g. the user is bound via a `ClusterRoleBinding` in `root` to a `ClusterRole` of the following shape:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  clusterName: root
rules:
- apiGroups:
  - tenancy.kcp.dev
  resources:
  - workspaces/content
  resourceNames:
  - org
  verbs:
  - access
```

### Workspace Content authorizer

The workspace content authorizer checks whether the user is granted `admin` or `access` verbs in
the parent workspace against the `workspaces/content` resource with the `resourceNames` of
the workspace being accessed.

If any of the verbs is granted, the associated group is added to the user's attributes
and will be evaluated in the subsequent authorizer chain.

| Verb     | Groups                                                         | Bootstrap cluster rolebinding       |
| -------- |----------------------------------------------------------------|-------------------------------------|
| `admin`  | `system:kcp:workspace:admin` and `system:kcp:workspace:access` | `system:kcp:clusterworkspace:admin` |
| `access` | `system:kcp:workspace:access`                                  | N/A                                 |

kcp's bootstrap policy provides default bindings:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  clusterName: system:kcp:clusterworkspace:admin
subjects:
- kind: Group
  name: system:kcp:clusterworkspace:admin
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
```

The access verb will not grant any cluster role but only associates with the `system:kcp:clusterworkspace:access` group
and executes the subsequent authorizer chain.

Example:

Given the user accesses `root:org:ws:ws`, the verbs `admin` and `access` are asserted
against the `workspaces/content` resource for the `resourceNames: ["ws"]` in the workspace `root:org:ws`.

To give a user called "adam" admin access to a workspace `root:org:ws:ws`, beyond having org access using the previous top-level organization authorizer,
a `ClusterRole` must be created in `root:org:ws` with the following shape:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: workspace-admin
  clusterName: root:org:ws
rules:
- apiGroups:
  - tenancy.kcp.dev
  resources:
  - workspaces/content
  resourceNames:
  - ws
  verbs:
  - admin
```

and the user must be bound to it via a `ClusterRoleBinding` in `root:org:ws` like the following:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: adam-admin
  clusterName: root:org:ws
subjects:
- kind: User
  name: adam
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: workspace-admin
```

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

!!! note
    The same authorization scheme is enforced when executing the request of a claimed resource via the virtual API Export API server,
    i.e. a claimed resource is bound to the same maximal permission policy. Only the actual owner of that resources can go beyond that policy.

TBD: Example

### Kubernetes Bootstrap Policy authorizer

The bootstrap policy authorizer works just like the local authorizer but references RBAC rules
defined in the `system:admin` system workspace.

### Local Policy authorizer

Once the top-level organization authorizer and the workspace content authorizer granted access to a
workspace, RBAC rules contained in the workspace derived from the request context are evaluated.

This authorizer ensures that RBAC rules contained within a workspace are being applied
and work just like in a regular Kubernetes cluster.

!!! note
  Groups added by the workspace content authorizer can be used for role bindings in that workspace.

It is possible to bind to roles and cluster roles in the bootstrap policy from a local policy `RoleBinding` or `ClusterRoleBinding`.

### Service Accounts

Kubernetes service accounts are granted access to the workspaces they are defined in and that are ready.

E.g. a service account "default" in `root:org:ws:ws` is granted access to `root:org:ws:ws`, and through the
workspace content authorizer it gains the `system:kcp:clusterworkspace:access` group membership.
