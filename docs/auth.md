# Introduction

kcp implements the same RBAC-based authorization scheme as Kubernetes.
Other authorization schemes (i.e. ABAC) are currently not supported.
Generally, the same role and role binding priniciples apply the same way as in Kubernetes.

However RBAC rules are always declared within the context of a concrete workspace.
Rules created in a workspace apply to requests against this workspace independently from the workspace type.
This is what resemembles Kubernetes authorization model.
In addition, rules can play a role in authorization to requests to other workspaces. The details will be described below.
This is true for all non-system workspace types, including root, organizational and enduser.
The exceptions are system namespaces. These can only be accessed by shard-local admins.

# Authorizers

The following authorizers are configured in kcp:

| Authorizer                             | Description                                                                    |
|----------------------------------------|--------------------------------------------------------------------------------|
| Top-Level organization authorizer      | checks that the user is allowed to access the organization (access and member) |
| Workspace content authorizer           | determines additional groups a user gets inside of a workspace                 |
| Local Policy authorizer                | validates the RBAC policy in the workspace that is accessed                    |
| Kubernetes Bootstrap Policy authorizer | validates the RBAC Kubernetes standard policy                                  |

They are related in the following way:

1. top-level organization authorizer must allow
2. workspace content authorizer must allow, and adds additional (virtual per-request) groups to the request user influencing the follow authorizers.
3. one of the local authorizer or bootstrap policy authorizer must allow.

```
                                                           ┌──────────────────┐
                                                           │                  │
                                                           │ Local Policy     │
         ┌──────────────┐    ┌────────────────────┐   ┌───►│ authorizer       ├─┐
         │              │    │                    │   │    │                  │ │
 request │ Top-level    │    │ Workspace  ┌───────┴─┐ │    └──────────────────┘ ▼
────────►│ Organization ├───►│ Content    │+ groups ├─┤                         OR──►
         │ authorizer   │    │ authorizer └───────┬─┘ │    ┌──────────────────┐ ▲
         │              │    │                    │   │    │                  │ │
         └──────────────┘    └────────────────────┘   └───►│ Bootstrap Policy ├─┘
                                                           │ authorizer       │
                                                           │                  │
                                                           └──────────────────┘
```

## Top-Level Organization authorizer

An top-level organization is a workspace directly under root. When a user accesses a top-level organization or
a sub-workspace like `root:org:ws:ws`, this authorizer will check in `root` whether the user has permission
to the top-level org workspace represented by the `ClusterWorkspace` named `org` in `root` with the following verbs:

| Verb     | Resource                   | Semantics                                                  |
|----------|----------------------------|------------------------------------------------------------|
| `access` | `clusterworkspace/content` | the user can access the organization `root:org`            |
| `member` | `clusterworkspace/content` | like access, but the user can additional create workspaces |

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
  - clusterworkspaces/content
  resourceNames:
  - org
  verbs:
  - access
  - member
```

## Workspace Content authorizer

The workspace content authorizer checks whether the user is granted `admin` or `access` verbs in 
the parent workspace against the `clusterworkspaces/content` resource with the `resourceNames` of 
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
against the `clusterworkspaces/content` resource for the `resourceNames: ["ws"]` in the workspace `root:org:ws`.

To give a user called "adam" admin access to a workspace `root:org:ws:ws`, beyond having org access using the previous top-level organization authorizer,
a `ClusterRole` must be created in `root:org:ws` with the following shape:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: 
  clusterName: workspace-admin
rules:
- apiGroups:
  - tenancy.kcp.dev
  resources:
  - clusterworkspaces/content
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
  clusterName: adam-admin
subjects:
- kind: User
  name: adam
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: workspace-admin
```

## Kubernetes Bootstrap Policy authorizer

The bootstrap policy authorizer works just like the local authorizer but references RBAC rules
defined in the `system:admin` system workspace.

## Local Policy authorizer

Once the top-level organization authorizer and the workspace content authorizer granted access to a
workspace, RBAC rules contained in the workspace derived from the request context are evaluated.

This authorizer ensures that RBAC rules contained within a workspace are being applied
and work just like in a regular Kubernetes cluster.

Note: groups added by the workspace content authorizer can be used for role bindings in that workspace.

It is possible to bind to roles and cluster roles in the bootstrap policy from a local policy `RoleBinding` or `ClusterRoleBinding`.
