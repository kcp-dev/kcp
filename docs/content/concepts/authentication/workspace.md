---
description: >
  How to admit users into workspaces by using custom JWT validators.
---

# Per-Workspace Authentication

kcp supports a range of authentication options, but all of them are global and applicable to every workspace in a kcp system. However when integrating with external partners and services, it can be beneficial to be able to admit users into a workspace that do not necessarily have access to kcp as a whole.

To enable this, kcp supports per-workspace authentication. In this model, a `WorkspaceType` configures a set of additional OIDC validators that are then used by kcp in addition to the global authentication mechanisms configured with CLI flags. Every workspace using these custom workspace types will then have these additional auth methods available.

This document describes how to enable and use this feature. Please refer to [OIDC Configuration](./oidc.md) for more information about the global OIDC configuration.

## Feature Gate

The feature is guarded by a feature gate called `WorkspaceAuthentication`, which is disabled by default. It can be independently enabled on any front-proxy and/or any kcp shard servers, though it is recommended and intended to enable it on front-proxies only. The shard support is mainly for testing and developing.

Add `--feature-gates=WorkspaceAuthentication=true` to the CLI flags on the front-proxy to enable the feature.

## Overview

Extra authentication for a workspace is configured using `WorkspaceAuthenticationConfiguration` (colloquially called "auth configs") objects, which can be thought of as CRD variants of the Kubernetes authentication configuration (as described in [OIDC Configuration](./oidc.md)). Each auth config contains a set of JWT validators that are capable of validating an incoming JWT bearer token.

Workspace types then reference a set of auth configs, and their configuration will apply to all their workspaces/logicalclusters. Compared to many other settings in a `WorkspaceType` that work only as a preset for _new_ workspaces, the configured auth configs will continue to affect workspaces, so when a `WorkspaceType` is changed, this will impact existing workspaces, too.

For every incoming HTTPS request, kcp will then resolve the logicalcluster, determine the used workspace type, assemble the list of auth configs and create an authenticator suitable for exactly the one logicalcluster targeted by the request. This workspace authenticator is an *alternative* to kcp's regular authentication (i.e. it forms a union with it).

This authentication can happen in the front-proxy or on each shard individually. However only the front-proxy has a global view across all shards and will be able to reliably resolve everything necessary. The shard-local per-workspace authentication really only works on a single
shard and requires that all of `Workspace`, `WorkspaceType`, auth configs and `LogicalClusters` are on the same local shard. Because of this, it's recommended to use the front-proxy to handle per-workspace authentication.

## OIDC

It is important to understand how the per-workspace authenticators interact with the global ones. Most importantly, how audiences are handled.

kcp has a `--api-audiences` flag that configures the global JWT audience claim that every single JWT needs to contain in order for it to be admitted. These global audiences are also required when using per-workspace authentication.

For example, suppose kcp is started with `--api-audiences=https://kcp.example.com` and there is a `WorkspaceAuthenticationConfiguration` that defines a JWT validator using the audience `https://corp.initech.com`. For a token to be admitted into a workspace that uses this auth config, the token will have to contain *both* audiences. This is to ensure the token is actually meant to be used in kcp, regardless of which audiences are then configured per workspace.

## Limitations

This feature has some small limitations that users should keep in mind:

* As mentioned above, the JWT validation for a workspace is not 100% independent from the global kcp authentication: tokens will need to contain kcp's global API audience (configured with `--api-audiences`) and any audience configured in the auth configs. You cannot have a token not contain kcp's global audience.
* `WorkspaceAuthenticationConfiguration` objects must reside in the same logicalcluster as the `WorkspaceType`.
* Workspace authenticators are started asynchronously and it will take a couple of seconds for them to be ready.
* The workspace authentication in the localproxy, as part of a single shard server, only knows about the data on the local shard and cannot handle cross-shard authentication. Users are advised to use the front-proxy instead.
* Even when the feature is disabled on all shards and all front-proxies, the API (CRDs) are always available in kcp. Admins might uses RBAC or webhooks to prevent creating `WorkspaceAuthenticationConfiguration` objects if needed.
* It is not possible to authenticate users with a username starting with with `system:` through per-workspace authentication.
* It is not possible to assign groups starting with `system:` to users authenticated via per-workspace authentication, e.g. via claim mappings.
* It is not possible to set keys containing `kcp.io` through the extra mappings in the authentication configuration.

## Example

In this example we want to create a workspace where users with tokens from our local OIDC provider are admitted to.

### Step 0: Enabling the Feature

Add `--feature-gates=WorkspaceAuthentication=true` to the CLI flags on the front-proxy to enable the feature. When developing or just testing, you can also add the feature gate to the kcp process like

```bash
kcp start --feature-gates=WorkspaceAuthentication=true
```

### Step 1: Auth Configs

First we need to create a `WorkspaceAuthenticationConfiguration` object in kcp:

```yaml
apiVersion: tenancy.kcp.io/v1alpha1
kind: WorkspaceAuthenticationConfiguration
metadata:
  name: my-auth-config
spec:
  jwt:
    - issuer:
        url: <url of the issuer>
        certificateAuthority: |
          <ca-file-content>
        audiences:
          - <client-id>
        audienceMatchPolicy: MatchAny
      claimMappings:
        groups:
          claim: <jwt-claim-name>
          prefix: ""
      claimValidationRules: []
      userValidationRules: []
```

Conveniently, this CRD has the exact same structure as Kubernetes' own `AuthenticationConfiguration`.

### Step 2: Workspace Type

Next we need to have a workspace type that uses our new auth config. You can edit an existing workspace type or create a new one. In this example we will create a new one:

```yaml
apiVersion: tenancy.kcp.io/v1alpha1
kind: WorkspaceType
metadata:
  name: with-auth
spec:
  authenticationConfigurations:
    - name: my-auth-config
```

Remember that changing a workspace type would affect all existing workspaces, too, not just newly created ones.

### Step 3: Workspaces

Now we're already create to create workspaces using the new type. This can be done on the command line:

```bash
kubectl create workspace my-workspace --type with-auth
```

### Step 4: Authorization

It's now time to configure permissions for your new users. Depending on the configuration and claims in the auth config, a suitable `ClusterRoleBinding` could look like this:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: make-externals-admins
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
  - apiGroup: rbac.authorization.k8s.io
    kind: Group
    name: oidc:admins
```

The CRB above would grant all users in the `admins` group cluster-admin permission inside the workspace.

Create the ClusterRoleBinding inside the new workspace:

```bash
kubectl ws :root:my-workspace
kubectl apply --filename clusterrolebinding.yaml
```

### Step 5: Testing

Your setup is now complete. You can take a token produced by your OIDC provider (remember that it needs to include both kcp's global audience and your own audience settings) and authenticate to your workspace.
