---
description: >
  How to authorize requests to kcp.
---

# Authorizers

In kcp, a request has four different ways of being admitted:

* It can be made to one of the preconfigured paths that do not require authorization, like `/healthz`.
* It can be performed by a user in one of the configured always-allow groups, by default `system:masters`.
* It can pass through the RBAC chain and match configured Roles and ClusterRoles.
* It can be permitted by an external HTTPS webhook backend.

They are related in the following way:

``` mermaid
graph TD
    start(Request):::state --> main_alt[/one of\]:::or
    main_alt --> aapa[Always Allow Paths Auth]
    main_alt --> aaga[Always Allow Groups Auth]
    main_alt --> wa[Webhook Auth]
    main_alt --> rga[Required Groups Auth]

    aapa --> decision(Decision):::state
    aaga --> decision
    wa --> decision

    subgraph "RBAC"
    rga --> wca[Workspace Content Auth]
    wca --> scrda[System CRD Auth]
    scrda --> mppa[Max. Permission Policy Auth]

    mppa --- mppa_alt[/one of\]:::or
    mppa_alt --> lpa[Local Policy Auth]
    mppa_alt --> gpa[Global Policy Auth]
    mppa_alt --> bpa[Bootstrap Policy Auth]
    end

    lpa --> decision
    gpa --> decision
    bpa --> decision

    classDef state color:#F77
    classDef or fill:none,stroke:none
```

[View graph on Kroki](https://kroki.io/mermaid/svg/eNqFkkFrwzAMhe_7Faa7dLBux0IOg7ZhvWxQusEOWRmKoyambuTZDln__RQnG02bUp-E3mfx9OzcginEe3wj-DgP1o_X-F2h83dRFHHDo5hMnsQeVPkF2iePVKKg7eeGZbLh2p8WQAADyUzXcHBipjXVYgW-4LryxWYIz0_wpaXKXORrSD4wLYh2lwjLA5sVlMWsPyywjb_AZSiVU1SO4674X7jj8j4XuvVJr42tSvMQ42g9ny1GoWe727Vkw2R3zoBEsaDSY-mPrLMeOCdtBsnbwXnci8U6PkKC1D6C4Wxf4edBrNDulWssiBVpJQ_HKzYY85NQXHy0TguDNc99IQm6P-2My5lbakqvgimDcyLvPAdzzmKZtVa1GQg5H2qmZih6qcG5GLei_amSNNno9nk67atkxVZpHZWcwz17oh2G-he4ue-L)

### Always Allow Paths Authorizer

Like in vanilla Kubernetes, this authorizer always grants access to the configured URL paths. This is
used for the health and liveness checks of kcp.

### Always Allow Groups Authorizer

This authorizer always permits access if the user is in one of the configured groups. By default this
only includes the `system:masters` group.

### RBAC Chain

The primary authorization flow is handled by a sequence of RBAC-based authorizers that a request must
satisfy all in order to be granted access.

The following authorizers work together to implement RBAC in kcp:

| Authorizer                             | Description                                                                                |
|----------------------------------------|--------------------------------------------------------------------------------------------|
| Workspace content authorizer           | validates that the user has `access` permission to the workspace                           |
| Required groups authorizer             | validates that the user is in the annotation-based list of groups required for a workspace |
| System CRD authorizer             | prevents undesired updates to certain core resources, like the status subresource on APIBindings |
| Maximal permission policy authorizer   | validates the maximal permission policy RBAC policy in the API exporter workspace          |
| Local Policy authorizer                | validates the RBAC policy in the workspace that is accessed                                |
| Global Policy authorizer               | validates the RBAC policy in the workspace that is accessed across shards                  |
| Kubernetes Bootstrap Policy authorizer | validates the RBAC Kubernetes standard policy                                              |

#### Required Groups Authorizer

A `authorization.kcp.io/required-groups` annotation can be added to a LogicalCluster
to specify additional groups that are required to access a workspace for a user to be member of.
The syntax is a disjunction (separator `,`) of conjunctions (separator `;`).

For example, `<group1>;<group2>,<group3>` means that a user must be member of `<group1>` AND `<group2>`, OR of `<group3>`.

The annotation is copied onto sub-workspaces during workspace creation, but is then not updated
automatically if it's changed.

#### Workspace Content Authorizer

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

A service-account defined in a different workspace is NOT given access to it.

!!! note
    By default, workspaces are only accessible to a user if they are in `Ready` phase. Workspaces that are initializing
    can be accessed only by users that are granted `admin` verb on the `workspaces/content` resource in the
    parent workspace.

    Service accounts declared within a workspace don't have access to initializing workspaces.

#### System CRD Authorizer

This small authorizer simply prevents updates to the `status` subresource on APIExports or APIBindings. Note that this authorizer does not validate changes to the CustomResourceDefitions themselves, but to objects from those CRDs instead.

#### Maximal Permission Policy Authorizer

If the requested resource type is part of an API binding, then this authorizer verifies that
the request is not exceeding the maximum permission policy of the related API export.
Currently, the "local policy" maximum permission policy type is supported.

##### Local Policy

The local maximum permission policy delegates the decision to the RBAC of the related API export.
To distinguish between local RBAC role bindings in that workspace and those for this these maximum permission policy,
every name and group is prefixed with `apis.kcp.io:binding:`.

**Example:** Given an APIBinding for type `foo` declared in workspace `consumer` that refers to an APIExport declared in workspace `provider`
and a user `user-1` having the group `group-1` requesting a `create` of `foo` in the `default` namespace in the `consumer` workspace,
this authorizer verifies that `user-1` is allowed to execute this request by delegating to `provider`'s RBAC using prefixed attributes.

Here, this authorizer prepends the `apis.kcp.io:binding:` prefix to the username and all groups the user belongs to.
Using prefixed attributes prevents RBAC collisions i.e. if `user-1` is granted to execute requests within the `provider` workspace directly.

For the given example RBAC request looks as follows:

- Username: `apis.kcp.io:binding:user-1`
- Groups: [`apis.kcp.io:binding:group-1`]
- Resource: `foo`
- Namespace: `default`
- Workspace: `provider`
- Verb: `create`

The following Role and RoleBinding declared within the `provider` workspace will grant access to the request:

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
  name: apis.kcp.io:binding:user-1
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: foo-creator
```

!!! note
    The same authorization scheme is enforced when executing the request of a claimed resource via the virtual APIExport API server,
    i.e. a claimed resource is bound to the same maximal permission policy. Only the actual owner of that resources can go beyond that policy.

TBD: Example

#### Local Policy Authorizer

This authorizer ensures that RBAC rules contained within a workspace are being applied
and work just like in a regular Kubernetes cluster.

It is possible to bind to Roles and ClusterRoles in the bootstrap policy from a local policy's
`RoleBinding` or `ClusterRoleBinding`, for example the `system:kcp:workspace:access` ClusterRole exists in the
`system:admin` logical cluster, but can still be bound from without any other logical cluster.

#### Global Policy Authorizer

This authorizer works identically to the Local Policy Authorizer, just with the difference
that it uses a global (i.e. across shards) getter for Roles and RoleBindings.

#### Bootstrap Policy Authorizer

The bootstrap policy authorizer works just like the local authorizer but references RBAC rules
defined in the `system:admin` system workspace. This workspace is where the classic Kubernetes
RBAC like the `cluster-admin` ClusterRole is being defined and the policy defined in this workspace
applies to every workspace in a kcp shard.

### Webhook Authorizer

This authorizer can be enabled by providing the `--authorization-webhook-config-file` flag to the kcp process
and works identically to [how it works in vanilla Kubernetes](https://kubernetes.io/docs/reference/access-authn-authz/webhook/).

The given configuration file must be of the kubeconfg format and point to an HTTPS server, potentially including certificate information as needed:

```yaml
apiVersion: v1
kind: Config
clusters:
  - name: webhook
    cluster:
      server: https://localhost:8080/
current-context: webhook
contexts:
  - name: webhook
    context:
      cluster: webhook
```

The webhook will receive every authorization request made in kcp, including internal ones. This means if the webhook
is badly configured, it can even prevent kcp from starting up successfully, but on the other hand this allows
a lot of influence over the authorization in kcp.

The webhook will receive JSON-marshalled `SubjectAccessReview` objects, that (compared to vanilla Kubernetes) include the name of target logical cluster as an `extra` field, like so:

```json
{
  "apiVersion": "authorization.k8s.io/v1beta1",
  "kind": "SubjectAccessReview",
  "spec": {
    "resourceAttributes": {
      "namespace": "kittensandponies",
      "verb": "get",
      "group": "unicorn.example.org",
      "resource": "pods"
    },
    "user": "jane",
    "group": [
      "group1",
      "group2"
    ],
    "extra": {
      "authorization.kubernetes.io/cluster-name": ["root"]
    }
  }
}
```


    The extra field will contain the logical cluster _name_ (e.g. o43u2gh528rtfg721rg92), not the human-readable path. Webhooks need to resolve the name to a path themselves if necessary.

### Scopes

Scopes are a way to limit the access of a user to a specific logical cluster. 
Scopes are (optionally) attached to the user identity by setting the 
`authentication.kcp.io/scopes: cluster:<logical-cluster>,...` extra field. The
scope is then checked by the authorizers. For example:

```yaml
user: user1
groups: ["group1"]
extra:
  authentication.kcp.io/scopes: 
  - cluster:logical-cluster-1
```
This user will only be allowed to access resources in `logical-cluster-1`, 
falling back to be considered as user `system:anonymous` with group 
`system:authenticated` in all other logical clusters.

Each extra field can contain multiple scopes, separated by a comma:
```yaml
user: user1
groups: ["group1"]
extra:
  authentication.kcp.io/scopes: 
  - cluster:logical-cluster-1,cluster:logical-cluster-2
```
This user is allowed to operate in both `logical-cluster-1` and 
`logical-cluster-2`, falling back to be considered as user `system:anonymous` 
with group `system:authenticated` in all other logical clusters.

If multiple `authentication.kcp.io/scopes` values are set, the intersection is 
taken:
```yaml
user: user1
groups: ["group1"]
extra:
  authentication.kcp.io/scopes: 
  - cluster:logical-cluster-1,cluster:logical-cluster-2
  - cluster:logical-cluster-2,cluster:logical-cluster-3
```
This user is only allowed to operate in `logical-cluster-2`, falling back to be
considered as user `system:anonymous` with group `system:authenticated` in all 
other logical clusters.

The intersection can be empty, in which case it falls back in every logical 
cluster.

When impersonating a user in a logical cluster, the resulting user identity is 
scoped to the logical cluster the impersonation is happening in.

A scope mismatch does not invalidate the warrants (see next section) of a user. 

### Warrants

Warrants are a way to grant extra access to a user. It can be limited by the scope of a logical cluster.
A warrant is attached by adding a `authorization.kcp.io/warrant` extra field to the user identity
with a JSON-encoded user info, and the limiting logical cluster set as `authentication.kcp.io/scopes: cluster:<logical-cluster>`.
in the embedded user info's extra. The warrant is then checked by the authorizers in the chain of every step
if the primary users is not allowed. For example:

```yaml
user: user1
groups: ["group1"]
extra:
  authorization.kcp.io/warrant: |
    {
      "user": "user2",
      "groups": ["group2"],
      "extra": {
        "authentication.kcp.io/scopes": "cluster:logical-cluster-1"
      }
    }
```

This warrant allows `user1` to act under the permissions of `user2` in 
`logical-cluster-1` if `user1` is not allowed to act as `user2` in the first place.

Note that a warrant only allow to act under the permissions of the warrant user, 
but not to act as the warrant user itself. E.g. in auditing or admission control,
the primary user is still the one that is acting.

Warrants can be nested, i.e. a warrant can contain another warrant.
